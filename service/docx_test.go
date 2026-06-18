package service

// docx_test.go — the self-contained gate for the docx/xlsx vocabularies (the
// level-2 OOXML formats): build a minimal docx-style package in-memory, project
// its main part against Format("docx"), and assert the WordprocessingML core
// (document/body/p/r/t) comes through typed — and that an unmodeled element in
// the same part is tolerated, not rejected, because the schema is open.

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"
)

// docxCT is a minimal [Content_Types].xml routing word/document.xml to the
// WordprocessingML main-document type.
const docxCT = `<?xml version="1.0"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
  <Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
  <Default Extension="xml" ContentType="application/xml"/>
  <Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/>
</Types>`

const docxDotRels = `<?xml version="1.0"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/>
</Relationships>`

// docxDoc is a small WordprocessingML body: a styled paragraph with a bold run
// of text, plus a deliberately unmodeled element (<w:proofErr/>) the open schema
// must tolerate.
const docxDoc = `<?xml version="1.0"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
  <w:body>
    <w:p>
      <w:pPr><w:pStyle w:val="Heading1"/></w:pPr>
      <w:proofErr w:type="spellStart"/>
      <w:r><w:rPr><w:b/></w:rPr><w:t>Hello World</w:t></w:r>
    </w:p>
    <w:sectPr><w:pgSz w:w="12240" w:h="15840"/></w:sectPr>
  </w:body>
</w:document>`

func TestDocxProjection(t *testing.T) {
	schema, err := Format("docx")
	if err != nil {
		t.Fatalf("Format(docx): %v", err)
	}
	if !schema.Open {
		t.Fatal("docx schema must be open")
	}

	data := buildPackage(t, map[string]string{
		"[Content_Types].xml": docxCT,
		"_rels/.rels":         docxDotRels,
		"word/document.xml":   docxDoc,
	})
	pkg, err := ProcessPackage(data)
	if err != nil {
		t.Fatalf("ProcessPackage: %v", err)
	}

	var doc *Part
	for _, p := range pkg.Parts {
		if p.Name == "/word/document.xml" {
			doc = p
		}
	}
	if doc == nil {
		t.Fatal("word/document.xml not among parts")
	}

	// Open mode: the unmodeled <w:proofErr/> must NOT make projection fail.
	msg, root, perr := schema.Project(doc.Document)
	if perr != nil {
		t.Fatalf("project (open mode should tolerate unmodeled markup): %v", perr)
	}
	if root != "w:document" {
		t.Errorf("root = %q, want w:document", root)
	}

	// document -> body -> p -> r -> t.text == "Hello World". typedChild descends
	// through the choice wrappers (Body.entry, R.entry) the XSD choices lower to.
	rm := msg.ProtoReflect()
	body := typedChild(t, rm, "Body")
	p := typedChild(t, body, "P")
	r := typedChild(t, p, "R")
	tt := typedChild(t, r, "T")
	if got := stringField(tt, "text"); strings.TrimSpace(got) != "Hello World" {
		t.Errorf("run text = %q, want %q", got, "Hello World")
	}

	// The typed structure carries the modeled properties through.
	pStyle := typedChild(t, typedChild(t, p, "PPr"), "PStyle")
	if got := stringField(pStyle, "val"); got != "Heading1" {
		t.Errorf("pStyle val = %q, want Heading1", got)
	}
	if typedChild(t, typedChild(t, r, "RPr"), "B") == nil {
		t.Error("run properties missing <w:b/>")
	}
	pgSz := typedChild(t, typedChild(t, body, "SectPr"), "PgSz")
	if got := stringField(pgSz, "w"); got != "12240" {
		t.Errorf("pgSz w = %q, want 12240", got)
	}
}

// typedChild returns the first child message of msg typed typeName, descending
// transparently through any choice-wrapper field (the repeated Entry messages
// the XSD <xs:choice> lowering produces, whose oneof holds the real variant). It
// is the test mirror of the projector's placeChild. Fails the test if absent.
func typedChild(t *testing.T, msg protoreflect.Message, typeName string) protoreflect.Message {
	t.Helper()
	if msg == nil {
		t.Fatalf("nil parent when looking for %s", typeName)
	}
	if m := directOrWrapped(msg, typeName); m != nil {
		return m
	}
	t.Fatalf("%s has no %s child", msg.Descriptor().Name(), typeName)
	return nil
}

// directOrWrapped finds a child message typed typeName as a direct field of msg,
// or inside one of msg's wrapper fields (a oneof variant). Returns nil if absent.
func directOrWrapped(msg protoreflect.Message, typeName string) protoreflect.Message {
	fs := msg.Descriptor().Fields()
	// A direct message field of the wanted type (singular or first repeated).
	for i := 0; i < fs.Len(); i++ {
		f := fs.Get(i)
		if f.Kind() != protoreflect.MessageKind || string(f.Message().Name()) != typeName {
			continue
		}
		if f.IsList() {
			if l := msg.Get(f).List(); l.Len() > 0 {
				return l.Get(0).Message()
			}
			return nil
		}
		if msg.Has(f) {
			return msg.Get(f).Message()
		}
	}
	// Otherwise look inside wrapper fields (each holds a oneof of variants).
	for i := 0; i < fs.Len(); i++ {
		f := fs.Get(i)
		if f.Kind() != protoreflect.MessageKind {
			continue
		}
		if f.IsList() {
			l := msg.Get(f).List()
			for j := 0; j < l.Len(); j++ {
				if m := variantOf(l.Get(j).Message(), typeName); m != nil {
					return m
				}
			}
			continue
		}
		if msg.Has(f) {
			if m := variantOf(msg.Get(f).Message(), typeName); m != nil {
				return m
			}
		}
	}
	return nil
}

// variantOf returns the message held by a wrapper's set oneof variant if it is
// typed typeName.
func variantOf(wrap protoreflect.Message, typeName string) protoreflect.Message {
	fs := wrap.Descriptor().Fields()
	for i := 0; i < fs.Len(); i++ {
		f := fs.Get(i)
		if f.Kind() == protoreflect.MessageKind && string(f.Message().Name()) == typeName && wrap.Has(f) {
			return wrap.Get(f).Message()
		}
	}
	return nil
}
