package service

// xsd_test.go — the self-contained gate for the XSD front-end (ADR 0008 Phase
// 4): compile a hand-authored XSD, then project conforming and non-conforming
// instances through Process. Spec-derived (W3C XML Schema), independent of any
// fetched corpus.

import (
	"strings"
	"testing"
)

// A small vocabulary exercising the supported XSD subset: a named complexType
// referenced by type=, a sequence with a repeated element, a repeated choice
// (maxOccurs="unbounded" → repeated oneof wrapper), optional elements
// (minOccurs="0"), attributes, and built-in simple types (→ text leaves).
const catalogXSD = `<?xml version="1.0"?>
<xs:schema xmlns:xs="http://www.w3.org/2001/XMLSchema">
  <xs:element name="catalog" type="CatalogType"/>
  <xs:complexType name="CatalogType">
    <xs:sequence>
      <xs:element name="book" type="BookType" maxOccurs="unbounded"/>
    </xs:sequence>
    <xs:attribute name="owner" type="xs:string"/>
  </xs:complexType>
  <xs:complexType name="BookType">
    <xs:choice maxOccurs="unbounded">
      <xs:element name="title" type="xs:string"/>
      <xs:element name="author" type="xs:string"/>
      <xs:element name="year" type="xs:string" minOccurs="0"/>
    </xs:choice>
    <xs:attribute name="id" type="xs:string"/>
  </xs:complexType>
</xs:schema>`

const catalogXML = `<catalog owner="me">
  <book id="b1"><title>The Go Programming Language</title><author>Donovan</author><year>2015</year></book>
  <book id="b2"><title>SICP</title></book>
</catalog>`

func xsdSchema(t *testing.T, xsd string) *Schema {
	t.Helper()
	fdp, err := CompileXSD([]byte(xsd), SchemaOptions{Package: "catalog"})
	if err != nil {
		t.Fatalf("CompileXSD: %v", err)
	}
	schema, err := schemaFromCompiled(fdp, false)
	if err != nil {
		t.Fatalf("link schema: %v", err)
	}
	return schema
}

func TestCompileXSDAndProject(t *testing.T) {
	schema := xsdSchema(t, catalogXSD)
	p, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	res, err := p.Process(catalogXML, schema, true)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if res.Document == nil {
		t.Fatal("expected a typed tree, got none")
	}
	// The root message is named after the root element; both books and their
	// fields must be present.
	msg := res.Document.ProtoReflect()
	if got := string(msg.Descriptor().Name()); got != "Catalog" {
		t.Fatalf("root message = %q, want Catalog", got)
	}
	if owner := msg.Descriptor().Fields().ByName("owner"); owner == nil {
		t.Fatal("Catalog has no owner attribute field")
	}
	books := msg.Descriptor().Fields().ByName("book")
	if books == nil || !books.IsList() {
		t.Fatal("Catalog.book is not a repeated message field")
	}
	if n := msg.Get(books).List().Len(); n != 2 {
		t.Fatalf("projected %d books, want 2", n)
	}
}

func TestCompileXSDRejectsOutOfVocabulary(t *testing.T) {
	schema := xsdSchema(t, catalogXSD)
	p, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Process(`<catalog><book><bogus>x</bogus></book></catalog>`, schema, true)
	if err == nil {
		t.Fatal("expected an out-of-vocabulary rejection")
	}
	if _, ok := err.(*ValidityError); !ok {
		t.Fatalf("error = %T (%v), want *ValidityError", err, err)
	}
	if !strings.Contains(err.Error(), "bogus") {
		t.Errorf("error %q does not name the offending element", err)
	}
}
