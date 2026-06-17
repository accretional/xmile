package service

// process.go — the unified instance-level entry point of ADR 0008. Process is
// the mode-aware classifier that subsumes Parse and ParseRSS: it parses bytes
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

	rsspb "github.com/accretional/xmile/proto/pb/rss"
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
	// descriptor can (e.g. RSS's "<rss version='2.0'> with exactly one
	// <channel>"). Optional; nil means none.
	PreValidate func(*xmlpb.Document) error
}

// Processed is the outcome of Process: the generic XML AST when no schema was
// given (the loosest projection), or the typed message when a schema was.
// Exactly one field is set.
type Processed struct {
	Document *xmlpb.Document
	Typed    proto.Message
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
	if schema.PreValidate != nil {
		if verr := schema.PreValidate(doc); verr != nil {
			return nil, verr
		}
	}
	root := doc.GetRoot()
	if root == nil {
		return nil, &ValidityError{Msg: "document has no root element"}
	}
	md := schema.Root
	if md == nil && schema.File != nil {
		md = schema.File.Messages().ByName(protoreflect.Name(pascalName(root.GetName())))
	}
	if md == nil {
		return nil, &ValidityError{Msg: fmt.Sprintf("schema has no type for root element <%s>", root.GetName())}
	}
	msg := dynamicpb.NewMessage(md)
	_, unknown := project(root, md, msg, projectOptions{nsExtensible: schema.NSExtensible})
	if len(unknown) > 0 {
		return nil, &ValidityError{Msg: fmt.Sprintf("out-of-vocabulary markup %v (extensions must be in a namespace)", dedupStrings(unknown))}
	}
	return &Processed{Typed: msg}, nil
}

// Format resolves a registered format name to its Schema. "" returns (nil, nil)
// — generic XML. Unknown names are an error. Registered formats are the typed
// vocabularies xmile ships with a committed proto (today: RSS 2.0).
func Format(name string) (*Schema, error) {
	switch name {
	case "":
		return nil, nil
	case "rss-2.0", "rss":
		md := (&rsspb.Rss{}).ProtoReflect().Descriptor()
		return &Schema{
			File:         md.ParentFile(),
			Root:         md,
			NSExtensible: true,
			PreValidate:  validateRSS,
		}, nil
	default:
		return nil, fmt.Errorf("unknown format %q", name)
	}
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
