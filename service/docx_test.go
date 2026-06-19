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

// TestDocxCompanionParts covers the parts a real .docx carries besides the main
// document — styles, fontTable, settings, numbering, headers/footers, notes, the
// DrawingML theme, and the docProps core/extended properties. Each must be a
// modeled root (so the OPC layer routes the part to it) and project its common
// markup typed, while open mode tolerates the rest.
func TestDocxCompanionParts(t *testing.T) {
	schema, err := Format("docx")
	if err != nil {
		t.Fatalf("Format(docx): %v", err)
	}
	p, err := Default()
	if err != nil {
		t.Fatalf("Default: %v", err)
	}

	// Every companion part root the survey found in a regular .docx is modeled.
	for _, root := range []string{
		"styles", "fonts", "settings", "webSettings", "numbering",
		"hdr", "ftr", "footnotes", "endnotes", "theme",
		"coreProperties", "Properties",
	} {
		if !schema.HasRoot(root) {
			t.Errorf("docx schema does not model companion root %q", root)
		}
	}

	// styles.xml: a paragraph style with a display name, plus doc defaults. The
	// trailing <w:unknownStyleBit/> is unmodeled and must be tolerated.
	styles := `<w:styles xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
	  <w:docDefaults><w:rPrDefault><w:rPr><w:sz w:val="22"/></w:rPr></w:rPrDefault></w:docDefaults>
	  <w:style w:type="paragraph" w:styleId="Heading1">
	    <w:name w:val="heading 1"/>
	    <w:basedOn w:val="Normal"/>
	    <w:rPr><w:b/></w:rPr>
	    <w:unknownStyleBit/>
	  </w:style>
	</w:styles>`
	sm := projectPart(t, p, schema, styles, "w:styles")
	style := typedChild(t, sm, "Style")
	if got := stringField(style, "type"); got != "paragraph" {
		t.Errorf("style type = %q, want paragraph", got)
	}
	if got := stringField(typedChild(t, style, "Name"), "val"); got != "heading 1" {
		t.Errorf("style name val = %q, want %q", got, "heading 1")
	}
	if typedChild(t, typedChild(t, style, "RPr"), "B") == nil {
		t.Error("style run properties missing <w:b/>")
	}

	// numbering.xml: an abstract definition (a level) and a concrete instance.
	numbering := `<w:numbering xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
	  <w:abstractNum w:abstractNumId="0">
	    <w:lvl w:ilvl="0"><w:numFmt w:val="decimal"/><w:lvlText w:val="%1."/></w:lvl>
	  </w:abstractNum>
	  <w:num w:numId="1"><w:abstractNumId w:val="0"/></w:num>
	</w:numbering>`
	nm := projectPart(t, p, schema, numbering, "w:numbering")
	lvl := typedChild(t, typedChild(t, nm, "AbstractNum"), "Lvl")
	if got := stringField(lvl, "ilvl"); got != "0" {
		t.Errorf("lvl ilvl = %q, want 0", got)
	}
	if got := stringField(typedChild(t, lvl, "NumFmt"), "val"); got != "decimal" {
		t.Errorf("numFmt val = %q, want decimal", got)
	}
	// <w:abstractNumId> is an element here (in <w:num>), distinct from the
	// attribute of the same name on <w:abstractNum>.
	if got := stringField(typedChild(t, typedChild(t, nm, "Num"), "AbstractNumId"), "val"); got != "0" {
		t.Errorf("num abstractNumId val = %q, want 0", got)
	}

	// docProps/core.xml: Dublin Core metadata, each child a text leaf.
	core := `<cp:coreProperties
	    xmlns:cp="http://schemas.openxmlformats.org/package/2006/metadata/core-properties"
	    xmlns:dc="http://purl.org/dc/elements/1.1/">
	  <dc:title>My Title</dc:title>
	  <dc:creator>Ada</dc:creator>
	</cp:coreProperties>`
	cm := projectPart(t, p, schema, core, "cp:coreProperties")
	if got := stringField(typedChild(t, cm, "Title"), "text"); got != "My Title" {
		t.Errorf("core title text = %q, want %q", got, "My Title")
	}
	if got := stringField(typedChild(t, cm, "Creator"), "text"); got != "Ada" {
		t.Errorf("core creator text = %q, want Ada", got)
	}
}

// projectPart parses a standalone part and projects it against the schema,
// asserting the root key matches and projection does not error (open mode).
func projectPart(t *testing.T, p *Parser, schema *Schema, src, wantRoot string) protoreflect.Message {
	t.Helper()
	x, err := p.Parse(src, false)
	if err != nil {
		t.Fatalf("parse %s: %v", wantRoot, err)
	}
	msg, root, perr := schema.Project(x)
	if perr != nil {
		t.Fatalf("project %s (open mode should tolerate unmodeled markup): %v", wantRoot, perr)
	}
	if root != wantRoot {
		t.Errorf("root = %q, want %q", root, wantRoot)
	}
	return msg.ProtoReflect()
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
