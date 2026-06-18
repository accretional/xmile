package service

// xsd_test.go — the self-contained gate for the XSD front-end (ADR 0008 Phase
// 4): compile a hand-authored XSD, then project conforming and non-conforming
// instances through Process. Spec-derived (W3C XML Schema), independent of any
// fetched corpus.

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"
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
	return xsdSchemaOpen(t, xsd, false)
}

// xsdSchemaOpen compiles an XSD and links it into a Schema, optionally open (so
// unmodeled markup — e.g. an xs:any wildcard's content — is tolerated rather
// than reported as out-of-vocabulary).
func xsdSchemaOpen(t *testing.T, xsd string, open bool) *Schema {
	t.Helper()
	fdp, err := CompileXSD([]byte(xsd), SchemaOptions{Package: "catalog"})
	if err != nil {
		t.Fatalf("CompileXSD: %v", err)
	}
	schema, err := schemaFromCompiled(fdp, false)
	if err != nil {
		t.Fatalf("link schema: %v", err)
	}
	schema.Open = open
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

// A vocabulary exercising named xs:group and xs:attributeGroup references, the
// two most-used OOXML constructs. PersonType pulls its content model from a
// named group (NameGroup, which itself references a nested group ContactGroup —
// transitive inlining) and its attributes from a named attributeGroup
// (IdentAttrs, which references another attributeGroup AuditAttrs). The fields
// contributed by the group and the attributeGroup must surface on the projected
// message exactly as if they had been written inline.
const groupsXSD = `<?xml version="1.0"?>
<xs:schema xmlns:xs="http://www.w3.org/2001/XMLSchema">
  <xs:element name="person" type="PersonType"/>
  <xs:complexType name="PersonType">
    <xs:sequence>
      <xs:group ref="NameGroup"/>
    </xs:sequence>
    <xs:attributeGroup ref="IdentAttrs"/>
  </xs:complexType>
  <xs:group name="NameGroup">
    <xs:sequence>
      <xs:element name="first" type="xs:string"/>
      <xs:element name="last" type="xs:string"/>
      <xs:group ref="ContactGroup"/>
    </xs:sequence>
  </xs:group>
  <xs:group name="ContactGroup">
    <xs:sequence>
      <xs:element name="email" type="xs:string" minOccurs="0"/>
    </xs:sequence>
  </xs:group>
  <xs:attributeGroup name="IdentAttrs">
    <xs:attribute name="id" type="xs:string"/>
    <xs:attributeGroup ref="AuditAttrs"/>
  </xs:attributeGroup>
  <xs:attributeGroup name="AuditAttrs">
    <xs:attribute name="version" type="xs:string"/>
  </xs:attributeGroup>
</xs:schema>`

const groupsXML = `<person id="p1" version="3">` +
	`<first>Ada</first><last>Lovelace</last><email>ada@example.com</email></person>`

func TestCompileXSDGroupAndAttributeGroup(t *testing.T) {
	schema := xsdSchema(t, groupsXSD)
	p, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	res, err := p.Process(groupsXML, schema, true)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if res.Document == nil {
		t.Fatal("expected a typed tree, got none")
	}
	msg := res.Document.ProtoReflect()
	if got := string(msg.Descriptor().Name()); got != "Person" {
		t.Fatalf("root message = %q, want Person", got)
	}
	fields := msg.Descriptor().Fields()
	// Element fields contributed by NameGroup (and the nested ContactGroup).
	for _, f := range []string{"first", "last", "email"} {
		if fields.ByName(protoreflect.Name(f)) == nil {
			t.Errorf("Person has no %q field (named group not inlined)", f)
		}
	}
	// Attribute fields contributed by IdentAttrs (and the nested AuditAttrs).
	for _, f := range []string{"id", "version"} {
		if fields.ByName(protoreflect.Name(f)) == nil {
			t.Errorf("Person has no %q field (named attributeGroup not inlined)", f)
		}
	}
	// The inlined fields are actually populated from the instance. The group's
	// "first" element is a message with a character-content "text" leaf; the
	// attributeGroup's "id"/"version" are direct string fields.
	first := msg.Get(fields.ByName("first")).Message()
	if got := first.Get(first.Descriptor().Fields().ByName("text")).String(); got != "Ada" {
		t.Errorf("first/text = %q, want Ada", got)
	}
	if got := msg.Get(fields.ByName("id")).String(); got != "p1" {
		t.Errorf("id = %q, want p1", got)
	}
	if got := msg.Get(fields.ByName("version")).String(); got != "3" {
		t.Errorf("version = %q, want 3", got)
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
