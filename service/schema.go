package service

// schema.go — the DTD-as-schema compiler (the type-level companion to the
// instance-level Parse) and the projection of a parsed document into a
// generated schema.
//
// Parse answers "is THIS document well-formed?"; CompileDTD answers "what is
// the SHAPE of the family of documents this DTD describes?" — it lowers a DTD
// into a google.protobuf.FileDescriptorProto, one proto message per
// <!ELEMENT>, so a generic feed gains a unique typed AST (an rss.Rss) rather
// than the homogeneous Tag. See ADR 0004.
//
// The compile/lowering half (the schema-language front-ends) lives in package
// service/language, which carries no grammar knowledge and never imports this
// package. The Compile* functions here are thin wrappers that inject this
// package's grammar-driven parsers and delegate; the projection half (filling a
// generated message from a parsed Tag) stays here, next to the engine it uses.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"

	pb "github.com/accretional/gluon/v2/pb"

	"github.com/accretional/xmile/service/language"

	xmlpb "github.com/accretional/xmile/proto/pb/xml"
)

// SchemaOptions configure the Compile* functions' FileDescriptorProto output.
// It is an alias to the language package's type, so service.SchemaOptions and
// language.SchemaOptions are interchangeable.
type SchemaOptions = language.SchemaOptions

// CompileDTD validates DTD bytes and lowers them into a FileDescriptorProto.
// See language.CompileDTD; this wrapper injects parseExternalSubset, the
// grammar-driven external-subset DTD parser.
func CompileDTD(dtd []byte, opts SchemaOptions) (*descriptorpb.FileDescriptorProto, error) {
	return language.CompileDTD(dtd, opts, parseExternalSubset)
}

// CompileGrammar lowers an EBNF schema grammar into a FileDescriptorProto.
// See language.CompileGrammar (no parser injection: it uses gluon directly).
func CompileGrammar(ebnf []byte, opts SchemaOptions) (*descriptorpb.FileDescriptorProto, error) {
	return language.CompileGrammar(ebnf, opts)
}

// XSDResolver resolves an xs:import / xs:include schemaLocation to the bytes of
// the referenced schema. It is an alias to the language package's type, so the
// two are interchangeable. See language.XSDResolver.
type XSDResolver = language.XSDResolver

// CompileXSD lowers a single self-contained XSD into a FileDescriptorProto. See
// language.CompileXSD; this wrapper injects parseXMLForSchema (the grammar-driven
// XML parser) and the default resolver (which resolves nothing, so a stray
// xs:import / xs:include of an unreachable location is skipped gracefully). Use
// CompileXSDWithResolver to supply a resolver for a multi-file schema set.
func CompileXSD(xsd []byte, opts SchemaOptions) (*descriptorpb.FileDescriptorProto, error) {
	return language.CompileXSD(xsd, opts, parseXMLForSchema, defaultXSDResolver)
}

// CompileXSDWithResolver lowers an XSD into a FileDescriptorProto, resolving its
// xs:import / xs:include targets through resolve (e.g. fileXSDResolver for a
// schema set on disk). A nil resolver resolves nothing.
func CompileXSDWithResolver(xsd []byte, opts SchemaOptions, resolve XSDResolver) (*descriptorpb.FileDescriptorProto, error) {
	return language.CompileXSD(xsd, opts, parseXMLForSchema, resolve)
}

// CompileSource lowers a metagrammar into a FileDescriptorProto, dispatching on
// the schema language: DTD, an EBNF element vocabulary (the default), or XSD.
// It is the single entry the Schemas.Compile RPC and the Documents.Process
// compile-then-use path share. See language.CompileSource; this wrapper injects
// both grammar-driven parsers and the default (no-op) XSD resolver.
func CompileSource(src []byte, lang xmlpb.SchemaLanguage, opts SchemaOptions) (*descriptorpb.FileDescriptorProto, error) {
	return language.CompileSource(src, lang, opts, parseExternalSubset, parseXMLForSchema, defaultXSDResolver)
}

// defaultXSDResolver resolves nothing: every xs:import / xs:include is reported
// unresolved, so CompileXSD skips it gracefully. This is the right default for a
// single self-contained schema (and the RPC/CLI path, where no base directory is
// known) — a schema that imports the XML namespace or an unreachable URL still
// compiles on its own declarations. A caller with a schema set on disk passes
// its own file-based resolver to CompileXSDWithResolver (e.g. one that reads a
// schemaLocation relative to a base directory); the testing harness's
// flatSiblingResolver is one such, resolving against the flattened corpus.
func defaultXSDResolver(location string) ([]byte, error) {
	return nil, fmt.Errorf("xsd import/include %q unresolved (no resolver configured)", location)
}

// FileXSDResolver returns a resolver that reads an xs:import / xs:include
// schemaLocation as a file relative to baseDir, for compiling a schema set on
// disk via CompileXSDWithResolver. A non-local location (a URL) or an unreadable
// file is reported unresolved, so CompileXSD skips it gracefully.
func FileXSDResolver(baseDir string) XSDResolver {
	return func(location string) ([]byte, error) {
		if location == "" || strings.Contains(location, "://") {
			return nil, fmt.Errorf("not a local schema location: %q", location)
		}
		return os.ReadFile(filepath.Join(baseDir, filepath.FromSlash(location)))
	}
}

// parseExternalSubset parses a bare external-subset DTD into its CST.
//
// lang/dtd.ebnf's start rule (doctype) expects a DOCTYPE *body* — a root name
// then the bracketed internal subset. A standalone DTD file is just the
// declaration list, so we wrap it in a synthetic DOCTYPE body and reuse the
// runtime DTD parser unchanged. The synthetic root name is never read.
func parseExternalSubset(subset string) (*pb.ASTNode, error) {
	dp, err := dtdParser()
	if err != nil {
		return nil, err
	}
	cst, err := dp.ParseCST("_xmile_schema_root [" + subset + "]")
	if err != nil {
		return nil, fmt.Errorf("not a well-formed DTD: %w", err)
	}
	return cst.GetRoot(), nil
}

// parseXMLForSchema parses an XSD-as-XML into the generic AST, the parser
// CompileXSD injects into language.CompileXSD.
func parseXMLForSchema(s string) (*xmlpb.Xml, error) {
	p, err := Default()
	if err != nil {
		return nil, err
	}
	return p.Parse(s, false)
}

// --- projecting a parsed document into a generated schema ---

// ProjectTag fills msg (described by md, a message from CompileDTD output) from
// a parsed Tag. Attributes map to string fields by snake-cased name; the text
// of a leaf maps to its `text` field; each child element maps either to a
// direct message field named after it or, for a lowered choice, to a oneof
// variant inside a wrapper message (appended in document order for a repeated
// choice). It returns the names of elements / attributes (@-prefixed) with no
// matching field — empty for a document fully covered by the schema, non-empty
// for one carrying out-of-vocabulary markup.
func ProjectTag(tag *xmlpb.Tag, md protoreflect.MessageDescriptor, msg protoreflect.Message) []string {
	_, unknown := project(tag, md, msg, projectOptions{nsExtensible: false})
	return unknown
}

// placeChild finds where a child element belongs in msg (described by md) and
// returns the descriptor and mutable message to project it into. It handles a
// direct message field (a sequence/single element, or an inline oneof variant)
// and a message-with-oneof wrapper field (a lowered choice): for the wrapper it
// appends a new element to the repeated wrapper (or takes the singular one) and
// selects the matching oneof variant.
func placeChild(md protoreflect.MessageDescriptor, msg protoreflect.Message, element string) (protoreflect.MessageDescriptor, protoreflect.Message, bool) {
	if f := msgFieldByType(md, element); f != nil {
		return f.Message(), mutableMessage(msg, f), true
	}
	fs := md.Fields()
	for i := 0; i < fs.Len(); i++ {
		f := fs.Get(i)
		if f.Kind() != protoreflect.MessageKind {
			continue
		}
		if vf := msgFieldByType(f.Message(), element); vf != nil {
			wrap := mutableMessage(msg, f)
			return vf.Message(), mutableMessage(wrap, vf), true
		}
	}
	return nil, nil, false
}

// msgFieldByType returns the message field of md whose message type is named
// after element (PascalCase), matching how CompileDTD names element messages.
func msgFieldByType(md protoreflect.MessageDescriptor, element string) protoreflect.FieldDescriptor {
	want := pascalName(element)
	fs := md.Fields()
	for i := 0; i < fs.Len(); i++ {
		f := fs.Get(i)
		if f.Kind() == protoreflect.MessageKind && string(f.Message().Name()) == want {
			return f
		}
	}
	return nil
}

// mutableMessage returns a mutable message to fill for field f: a freshly
// appended element for a repeated field, or the field's message otherwise
// (which selects f's oneof variant when f belongs to a oneof).
func mutableMessage(msg protoreflect.Message, f protoreflect.FieldDescriptor) protoreflect.Message {
	if f.IsList() {
		return msg.Mutable(f).List().AppendMutable().Message()
	}
	return msg.Mutable(f).Message()
}

func scalarField(md protoreflect.MessageDescriptor, attr string) protoreflect.FieldDescriptor {
	f := md.Fields().ByName(protoreflect.Name(snakeName(attr)))
	if f != nil && f.Kind() == protoreflect.StringKind {
		return f
	}
	return nil
}

// --- name mangling, mirroring gluon/v2/compiler/names.go so projector lookups
// match the names compiler.Compile emits ---

func idParts(s string) []string {
	var parts []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			parts = append(parts, cur.String())
			cur.Reset()
		}
	}
	runes := []rune(s)
	for i, r := range runes {
		if r == '-' || r == '_' || r == ' ' {
			flush()
			continue
		}
		if i > 0 && unicode.IsUpper(r) && unicode.IsLower(runes[i-1]) {
			flush()
		}
		cur.WriteRune(r)
	}
	flush()
	return parts
}

func pascalName(s string) string {
	var out strings.Builder
	for _, p := range idParts(s) {
		if p == "" {
			continue
		}
		r := []rune(strings.ToLower(p))
		r[0] = unicode.ToUpper(r[0])
		out.WriteString(string(r))
	}
	return out.String()
}

func snakeName(s string) string {
	parts := idParts(s)
	for i, p := range parts {
		parts[i] = strings.ToLower(p)
	}
	return strings.Join(parts, "_")
}
