package service

// xsd_structural_test.go — the self-contained gate for the remaining XSD
// STRUCTURAL constructs (the construct/compilation shape, not datatype-facet or
// identity-constraint validation): xs:any / xs:anyAttribute wildcards,
// substitution groups, xs:restriction-derived content, xs:union / xs:list simple
// types, and multi-file xs:import / xs:include. Each fixture compiles a
// hand-authored XSD and projects a conforming instance through Process, the same
// shape as xsd_test.go.

import (
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"
)

// --- xs:any / xs:anyAttribute wildcards ---

// A type bearing wildcards: the content model is a known <note> followed by an
// xs:any wildcard, and the type carries an xs:anyAttribute. The wildcards add no
// fields, so the modeled <note> projects and the wildcard markup is tolerated by
// an open schema.
const wildcardXSD = `<?xml version="1.0"?>
<xs:schema xmlns:xs="http://www.w3.org/2001/XMLSchema">
  <xs:element name="envelope" type="EnvelopeType"/>
  <xs:complexType name="EnvelopeType">
    <xs:sequence>
      <xs:element name="note" type="xs:string"/>
      <xs:any minOccurs="0" maxOccurs="unbounded" processContents="lax"/>
    </xs:sequence>
    <xs:anyAttribute processContents="lax"/>
  </xs:complexType>
</xs:schema>`

const wildcardXML = `<envelope foo="bar">` +
	`<note>hi</note>` +
	`<anything><nested>x</nested></anything>` +
	`</envelope>`

func TestCompileXSDWildcards(t *testing.T) {
	// Open, so the wildcard's content (<anything>) and the wildcard attribute
	// (foo) are tolerated rather than reported as out-of-vocabulary.
	schema := xsdSchemaOpen(t, wildcardXSD, true)
	p, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	res, err := p.Process(wildcardXML, schema, true)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if res.Document == nil {
		t.Fatal("expected a typed tree, got none")
	}
	msg := res.Document.ProtoReflect()
	if got := string(msg.Descriptor().Name()); got != "Envelope" {
		t.Fatalf("root message = %q, want Envelope", got)
	}
	// The wildcard contributes no field; only the modeled <note> is a field.
	note := msg.Descriptor().Fields().ByName("note")
	if note == nil {
		t.Fatal("Envelope has no note field")
	}
	if msg.Descriptor().Fields().ByName("anything") != nil {
		t.Fatal("xs:any wrongly produced a field")
	}
	noteMsg := msg.Get(note).Message()
	if got := noteMsg.Get(noteMsg.Descriptor().Fields().ByName("text")).String(); got != "hi" {
		t.Errorf("note/text = %q, want hi", got)
	}
}

// --- substitution groups ---

// A substitution group: the abstract head <publication> has two substitutes,
// <book> and <magazine>. The <library> content references the head; a conforming
// instance substitutes <book>/<magazine> where <publication> is expected. The
// reference must expand to a choice over {publication, book, magazine}.
const substXSD = `<?xml version="1.0"?>
<xs:schema xmlns:xs="http://www.w3.org/2001/XMLSchema">
  <xs:element name="library" type="LibraryType"/>
  <xs:element name="publication" type="xs:string"/>
  <xs:element name="book" type="xs:string" substitutionGroup="publication"/>
  <xs:element name="magazine" type="xs:string" substitutionGroup="publication"/>
  <xs:complexType name="LibraryType">
    <xs:sequence>
      <xs:element ref="publication" maxOccurs="unbounded"/>
    </xs:sequence>
  </xs:complexType>
</xs:schema>`

const substXML = `<library><book>SICP</book><magazine>Wired</magazine><publication>Misc</publication></library>`

func TestCompileXSDSubstitutionGroup(t *testing.T) {
	schema := xsdSchema(t, substXSD)
	p, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	res, err := p.Process(substXML, schema, true)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if res.Document == nil {
		t.Fatal("expected a typed tree, got none")
	}
	msg := res.Document.ProtoReflect()
	if got := string(msg.Descriptor().Name()); got != "Library" {
		t.Fatalf("root message = %q, want Library", got)
	}
	// The head reference lowered to a repeated choice; the substitutes (book,
	// magazine) and the head (publication) are all variants, so they all place.
	// Find the repeated wrapper field and check it received all three entries.
	fs := msg.Descriptor().Fields()
	var wrap protoreflect.FieldDescriptor
	for i := 0; i < fs.Len(); i++ {
		if f := fs.Get(i); f.IsList() && f.Kind() == protoreflect.MessageKind {
			wrap = f
			break
		}
	}
	if wrap == nil {
		t.Fatal("Library has no repeated choice-wrapper field for the substitution group")
	}
	if n := msg.Get(wrap).List().Len(); n != 3 {
		t.Fatalf("projected %d substitution-group entries, want 3 (book, magazine, publication)", n)
	}
}

// TestCompileXSDSubstituteOutOfVocabularyClosed confirms that a NON-member
// element is still rejected by a closed schema — substitution widens the choice
// to the members only, not to arbitrary markup.
func TestCompileXSDSubstituteRejectsNonMember(t *testing.T) {
	schema := xsdSchema(t, substXSD)
	p, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Process(`<library><pamphlet>x</pamphlet></library>`, schema, true); err == nil {
		t.Fatal("expected a closed-schema rejection of a non-member element")
	}
}

// --- xs:restriction-derived content ---

// A restriction over a complex base: BaseType has two elements; DerivedType
// restricts it to a narrower content model (just <a>). The restriction's own
// particle is the type's content (the base content is replaced), so DerivedType
// models <a> only. A simpleContent restriction (an enum over xs:string) is a
// string leaf.
const restrictionXSD = `<?xml version="1.0"?>
<xs:schema xmlns:xs="http://www.w3.org/2001/XMLSchema">
  <xs:element name="root" type="DerivedType"/>
  <xs:complexType name="BaseType">
    <xs:sequence>
      <xs:element name="a" type="xs:string"/>
      <xs:element name="b" type="xs:string"/>
    </xs:sequence>
  </xs:complexType>
  <xs:complexType name="DerivedType">
    <xs:complexContent>
      <xs:restriction base="BaseType">
        <xs:sequence>
          <xs:element name="a" type="StatusType"/>
        </xs:sequence>
      </xs:restriction>
    </xs:complexContent>
  </xs:complexType>
  <xs:simpleType name="StatusType">
    <xs:restriction base="xs:string">
      <xs:enumeration value="ok"/>
      <xs:enumeration value="bad"/>
    </xs:restriction>
  </xs:simpleType>
</xs:schema>`

const restrictionXML = `<root><a>ok</a></root>`

func TestCompileXSDRestriction(t *testing.T) {
	schema := xsdSchema(t, restrictionXSD)
	p, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	res, err := p.Process(restrictionXML, schema, true)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if res.Document == nil {
		t.Fatal("expected a typed tree, got none")
	}
	msg := res.Document.ProtoReflect()
	if got := string(msg.Descriptor().Name()); got != "Root" {
		t.Fatalf("root message = %q, want Root", got)
	}
	fs := msg.Descriptor().Fields()
	if fs.ByName("a") == nil {
		t.Fatal("Root has no <a> field (restriction particle not used)")
	}
	// The base's <b> was restricted away; it must NOT be a field.
	if fs.ByName("b") != nil {
		t.Fatal("restriction wrongly kept the base's <b> field")
	}
	// <a>'s type is a named simpleType (an enum over xs:string) → a string leaf.
	a := msg.Get(fs.ByName("a")).Message()
	if got := a.Get(a.Descriptor().Fields().ByName("text")).String(); got != "ok" {
		t.Errorf("a/text = %q, want ok", got)
	}
	// A non-member element is still rejected (closed schema).
	if _, err := p.Process(`<root><b>x</b></root>`, schema, true); err == nil {
		t.Fatal("expected rejection of the restricted-away <b>")
	}
}

// --- xs:union / xs:list simple types ---

// A union and a list simple type, each referenced by an element's type=. Both
// lower to a string leaf (datatype facets are validation, out of scope).
const unionListXSD = `<?xml version="1.0"?>
<xs:schema xmlns:xs="http://www.w3.org/2001/XMLSchema">
  <xs:element name="measure" type="MeasureType"/>
  <xs:complexType name="MeasureType">
    <xs:sequence>
      <xs:element name="amount" type="NumOrWord"/>
      <xs:element name="tags" type="TagList"/>
    </xs:sequence>
  </xs:complexType>
  <xs:simpleType name="NumOrWord">
    <xs:union memberTypes="xs:integer xs:string"/>
  </xs:simpleType>
  <xs:simpleType name="TagList">
    <xs:list itemType="xs:string"/>
  </xs:simpleType>
</xs:schema>`

const unionListXML = `<measure><amount>42</amount><tags>red green blue</tags></measure>`

func TestCompileXSDUnionAndList(t *testing.T) {
	schema := xsdSchema(t, unionListXSD)
	p, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	res, err := p.Process(unionListXML, schema, true)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if res.Document == nil {
		t.Fatal("expected a typed tree, got none")
	}
	msg := res.Document.ProtoReflect()
	fs := msg.Descriptor().Fields()
	for _, name := range []string{"amount", "tags"} {
		f := fs.ByName(protoreflect.Name(name))
		if f == nil {
			t.Fatalf("Measure has no %q field", name)
		}
	}
	amount := msg.Get(fs.ByName("amount")).Message()
	if got := amount.Get(amount.Descriptor().Fields().ByName("text")).String(); got != "42" {
		t.Errorf("amount/text = %q, want 42 (union → string leaf)", got)
	}
	tags := msg.Get(fs.ByName("tags")).Message()
	if got := tags.Get(tags.Descriptor().Fields().ByName("text")).String(); got != "red green blue" {
		t.Errorf("tags/text = %q, want \"red green blue\" (list → string leaf)", got)
	}
}

// --- multi-file xs:import / xs:include ---

// The primary schema declares <order> with a content model that references
// types/elements declared in two other documents: a same-namespace include
// (items.xsd, contributing <item>) and an other-namespace import (addr.xsd,
// contributing <shipTo>). An in-memory resolver supplies the two targets; their
// top-level declarations merge into the scope so the references resolve.
const importMainXSD = `<?xml version="1.0"?>
<xs:schema xmlns:xs="http://www.w3.org/2001/XMLSchema">
  <xs:include schemaLocation="items.xsd"/>
  <xs:import namespace="http://example.com/addr" schemaLocation="addr.xsd"/>
  <xs:element name="order" type="OrderType"/>
  <xs:complexType name="OrderType">
    <xs:sequence>
      <xs:element ref="shipTo"/>
      <xs:element ref="item" maxOccurs="unbounded"/>
    </xs:sequence>
  </xs:complexType>
</xs:schema>`

const importItemsXSD = `<?xml version="1.0"?>
<xs:schema xmlns:xs="http://www.w3.org/2001/XMLSchema">
  <xs:element name="item" type="xs:string"/>
</xs:schema>`

const importAddrXSD = `<?xml version="1.0"?>
<xs:schema xmlns:xs="http://www.w3.org/2001/XMLSchema">
  <xs:element name="shipTo" type="xs:string"/>
</xs:schema>`

const importXML = `<order><shipTo>123 Main St</shipTo><item>Widget</item><item>Gadget</item></order>`

func TestCompileXSDImportInclude(t *testing.T) {
	resolve := func(location string) ([]byte, error) {
		switch location {
		case "items.xsd":
			return []byte(importItemsXSD), nil
		case "addr.xsd":
			return []byte(importAddrXSD), nil
		}
		return nil, errUnresolved(location)
	}
	fdp, err := CompileXSDWithResolver([]byte(importMainXSD), SchemaOptions{Package: "order"}, resolve)
	if err != nil {
		t.Fatalf("CompileXSDWithResolver: %v", err)
	}
	schema, err := schemaFromCompiled(fdp, false)
	if err != nil {
		t.Fatalf("link schema: %v", err)
	}
	p, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	res, err := p.Process(importXML, schema, true)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if res.Document == nil {
		t.Fatal("expected a typed tree, got none")
	}
	msg := res.Document.ProtoReflect()
	if got := string(msg.Descriptor().Name()); got != "Order" {
		t.Fatalf("root message = %q, want Order", got)
	}
	fs := msg.Descriptor().Fields()
	// shipTo came from the imported addr.xsd; item from the included items.xsd.
	if fs.ByName("ship_to") == nil {
		t.Fatal("Order has no ship_to field (import not merged)")
	}
	items := fs.ByName("item")
	if items == nil || !items.IsList() {
		t.Fatal("Order.item is not a repeated field (include not merged)")
	}
	if n := msg.Get(items).List().Len(); n != 2 {
		t.Fatalf("projected %d items, want 2", n)
	}
}

// TestCompileXSDFileResolver compiles the same multi-file set through the
// on-disk FileXSDResolver: the targets are written to a temp dir and resolved by
// schemaLocation relative to it.
func TestCompileXSDFileResolver(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"main.xsd":  importMainXSD,
		"items.xsd": importItemsXSD,
		"addr.xsd":  importAddrXSD,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	fdp, err := CompileXSDWithResolver([]byte(importMainXSD), SchemaOptions{Package: "order"}, FileXSDResolver(dir))
	if err != nil {
		t.Fatalf("CompileXSDWithResolver(FileXSDResolver): %v", err)
	}
	schema, err := schemaFromCompiled(fdp, false)
	if err != nil {
		t.Fatalf("link schema: %v", err)
	}
	p, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	res, err := p.Process(importXML, schema, true)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	fs := res.Document.ProtoReflect().Descriptor().Fields()
	if fs.ByName("ship_to") == nil || fs.ByName("item") == nil {
		t.Fatal("FileXSDResolver did not merge the imported/included declarations")
	}
}

// TestCompileXSDUnresolvedImportSkipped confirms an unresolvable import/include
// is skipped gracefully: the schema still compiles on its own declarations.
func TestCompileXSDUnresolvedImportSkipped(t *testing.T) {
	// The default CompileXSD resolver resolves nothing, so the include/import in
	// importMainXSD is skipped — but the schema has its own <order> declaration,
	// so it still compiles (the missing item/shipTo refs lower to string fields).
	if _, err := CompileXSD([]byte(importMainXSD), SchemaOptions{Package: "order"}); err != nil {
		t.Fatalf("unresolved import/include should be skipped, got: %v", err)
	}
}

func errUnresolved(location string) error {
	return &unresolvedErr{location}
}

type unresolvedErr struct{ loc string }

func (e *unresolvedErr) Error() string { return "unresolved: " + e.loc }
