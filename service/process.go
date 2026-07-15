package service

// process.go — the unified instance-level entry point of ADR 0008. Process is
// the mode-aware classifier that subsumes the former per-format parse entry
// points (a plain Parse and the vocabulary parsers): it parses bytes
// into the generic XML AST and, given a schema, projects that AST into the
// schema's typed message. Generic XML is simply the loosest schema — Process
// with no schema returns the Tag tree, exactly the old Parse — and a format
// schema is a refinement on the same axis.

import (
	"fmt"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"

	xmlpb "github.com/accretional/xmile/proto/pb/xml"
)

// Schema is a compiled vocabulary a document projects into: the message types
// of the family, plus the vocabulary's namespace-extensibility rule and any
// structural pre-validation a descriptor cannot express. It is produced from a
// FileDescriptorProto (CompileDTD / CompileGrammar / CompileXSD) or taken from a
// committed generated type (Format).
type Schema struct {
	// File holds every message type of the vocabulary. Root, when set, is the
	// document's root type; otherwise Process resolves it from the root element's
	// name (pascal-cased), so one compiled descriptor serves any of its roots.
	File         protoreflect.FileDescriptor
	Root         protoreflect.MessageDescriptor
	NSExtensible bool
	// PreValidate enforces structural constraints neither the grammar nor the
	// descriptor can express (e.g. a required root attribute value, or a fixed
	// child-element cardinality). Optional; nil means none.
	PreValidate func(*xmlpb.Xml) error
	// Open makes the schema partial: unmodeled markup is tolerated, not rejected,
	// so a minimal schema for a large format (OOXML docx/xlsx) still accepts every
	// valid document. See projectOptions.open.
	Open bool
}

// Processed is the outcome of Process: the generic XML AST when no schema was
// given (the loosest projection), or the typed message when a schema was.
// Exactly one of Document / Typed is set; Root is the root element's name (the
// key under which the typed tree is presented).
type Processed struct {
	Document proto.Message
	Root     string
}

// Process parses src and, given a schema, projects it into that vocabulary's
// typed tree. With no schema it returns the generic XML AST (the former Parse);
// validating then selects well-formed-only vs DTD-valid. With a schema the bytes
// are parsed well-formed, pre-validated, and projected; out-of-vocabulary markup
// is a *ValidityError. The error is one of *WFError / *ValidityError /
// *CannotValidateError, exactly as Parse.
func (p *Parser) Process(src string, schema *Schema, validating bool) (*Processed, error) {
	if schema == nil {
		doc, err := p.Parse(src, validating)
		if err != nil {
			return nil, err
		}
		return &Processed{Document: doc}, nil
	}

	doc, err := p.Parse(src, false)
	if err != nil {
		return nil, err
	}
	msg, root, perr := schema.Project(doc)
	if perr != nil {
		return nil, perr
	}
	return &Processed{Document: msg, Root: root}, nil
}

// Project projects an already-parsed document into this schema's typed tree,
// the projection half of Process without re-parsing. It is the entry the OPC
// path uses: ProcessPackage has already parsed each part into the generic AST
// (*xmlpb.Xml), so a part is projected against its format's Schema directly. The
// root type is resolved by the root element's local name (as Process does);
// out-of-vocabulary markup is a *ValidityError unless the schema is open (the
// docx/xlsx case, where every valid part projects). It returns the typed message
// and the resolved root element name.
func (s *Schema) Project(x *xmlpb.Xml) (proto.Message, string, error) {
	if s == nil {
		return nil, "", &ValidityError{Msg: "nil schema"}
	}
	if s.PreValidate != nil {
		if verr := s.PreValidate(x); verr != nil {
			return nil, "", verr
		}
	}
	root := x.GetRoot()
	if root == nil {
		return nil, "", &ValidityError{Msg: "document has no root element"}
	}
	md := s.Root
	if md == nil && s.File != nil {
		md = s.File.Messages().ByName(protoreflect.Name(pascalName(localElemName(root))))
	}
	if md == nil {
		return nil, "", &ValidityError{Msg: fmt.Sprintf("schema has no type for root element <%s>", root.GetName())}
	}
	msg := dynamicpb.NewMessage(md)
	_, unknown := project(root, md, msg, projectOptions{nsExtensible: s.NSExtensible, open: s.Open})
	if len(unknown) > 0 {
		return nil, root.GetName(), &ValidityError{Msg: fmt.Sprintf("out-of-vocabulary markup %v (extensions must be in a namespace)", dedupStrings(unknown))}
	}
	return msg, root.GetName(), nil
}

// HasRoot reports whether the schema models a root element with the given local
// name — the cheap test the OPC runner uses to pick which parts a format
// projects (word/document.xml -> "document", a worksheet -> "worksheet").
func (s *Schema) HasRoot(localName string) bool {
	if s == nil || s.File == nil {
		return false
	}
	return s.File.Messages().ByName(protoreflect.Name(pascalName(localName))) != nil
}

// CompileSchema compiles a metagrammar (DTD / EBNF vocabulary / XSD) and links
// it into a Schema ready for Process — the compile-then-use path as a one-liner.
// The document root type is resolved per document (by element name).
func CompileSchema(src []byte, language xmlpb.SchemaLanguage, opts SchemaOptions, nsExtensible bool) (*Schema, error) {
	fdp, err := CompileSource(src, language, opts)
	if err != nil {
		return nil, err
	}
	return schemaFromCompiled(fdp, nsExtensible)
}

// schemaFromCompiled links a freshly compiled descriptor into a Schema whose
// root is resolved per document (by element name). Schema vocabularies have no
// external dependencies, so a nil resolver suffices.
func schemaFromCompiled(fdp *descriptorpb.FileDescriptorProto, nsExtensible bool) (*Schema, error) {
	fd, err := protodesc.NewFile(fdp, nil)
	if err != nil {
		return nil, fmt.Errorf("link schema descriptor: %w", err)
	}
	return &Schema{File: fd, NSExtensible: nsExtensible}, nil
}

// dedupStrings returns in with duplicates removed, preserving first-seen order —
// used to tidy the out-of-vocabulary markup list in the projection error above.
func dedupStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
