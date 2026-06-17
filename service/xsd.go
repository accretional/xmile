package service

// xsd.go — the XSD front-end (ADR 0008 Phase 4). An XSD is itself an XML
// document, so it is parsed by xmile's own parser and the resulting Tag tree is
// walked into the gluon schema-AST that compiler.Compile lowers — exactly like
// CompileDTD and CompileGrammar, one more front-end into the shared backend.
//
// Spec: W3C XML Schema Part 1 (Structures) and Part 2 (Datatypes), see
// docs/REFERENCES.md. The supported subset, sufficient for an element
// vocabulary (and the OOXML part schemas of Phase 5): xs:element (named/ref,
// minOccurs/maxOccurs), xs:complexType (named/inline), xs:sequence / xs:choice /
// xs:all, xs:attribute, xs:simpleContent and xs:complexContent extensions, and
// built-in / named simple types (lowered to a string `text` leaf — datatype
// facets are validation, deferred per ADR 0008). One message per declared
// element, the same shape CompileDTD produces.

import (
	"fmt"
	"sort"
	"strings"

	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/accretional/gluon/v2/compiler"
	pb "github.com/accretional/gluon/v2/pb"

	xmlpb "github.com/accretional/xmile/proto/pb/xml"
)

// xsdNS is the XML Schema namespace; XSD elements (xs:element, xs:complexType, …)
// are matched by local name within it.
const xsdNS = "http://www.w3.org/2001/XMLSchema"

// CompileXSD lowers an XSD into a FileDescriptorProto, one message per declared
// element. Returns an error if the bytes are not a well-formed XSD or declare
// no elements.
func CompileXSD(xsd []byte, opts SchemaOptions) (*descriptorpb.FileDescriptorProto, error) {
	p, err := Default()
	if err != nil {
		return nil, err
	}
	doc, err := p.Parse(string(xsd), false)
	if err != nil {
		return nil, fmt.Errorf("not a well-formed XSD: %w", err)
	}
	root := doc.GetRoot()
	if root == nil || xsdLocal(root) != "schema" {
		return nil, fmt.Errorf("XSD root element is not <xs:schema>")
	}
	pkg := opts.Package
	if pkg == "" {
		pkg = "lang"
	}
	ast, err := xsdSchemaAST(root, pkg)
	if err != nil {
		return nil, err
	}
	return compiler.Compile(ast, compiler.Options{
		Package:   pkg,
		GoPackage: opts.GoPackage,
		FileName:  opts.FileName,
	})
}

// xsdSchemaAST walks an <xs:schema> Tag tree into a gluon schema-AST (file →
// rule* → body): one rule per element declaration (top-level or nested, first
// binding), its body built from the element's type — an inline or named
// complexType, or a simple type lowered to a `text` leaf.
func xsdSchemaAST(schema *xmlpb.Tag, language string) (*pb.ASTDescriptor, error) {
	complexTypes := map[string]*xmlpb.Tag{}
	for _, ct := range xsdChildrenLocal(schema, "complexType") {
		if n := xsdAttr(ct, "name"); n != "" {
			complexTypes[n] = ct
		}
	}

	// Every element declaration, depth-first, first binding wins.
	elems := map[string]*xmlpb.Tag{}
	var names []string
	for _, el := range xsdDescendants(schema, "element") {
		n := xsdAttr(el, "name")
		if n == "" {
			continue // a ref= use, not a declaration
		}
		if _, dup := elems[n]; dup {
			continue
		}
		elems[n] = el
		names = append(names, n)
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("XSD declares no elements")
	}
	declared := func(n string) bool { _, ok := elems[n]; return ok }

	sort.Strings(names)
	file := &pb.ASTNode{Kind: compiler.KindFile}
	for _, name := range names {
		rule := &pb.ASTNode{Kind: compiler.KindRule, Value: name}
		if body := xsdElementBody(elems[name], complexTypes, declared); body != nil {
			rule.Children = []*pb.ASTNode{body}
		}
		file.Children = append(file.Children, rule)
	}
	return &pb.ASTDescriptor{Language: language, Root: file}, nil
}

// xsdElementBody builds a rule body from an element declaration: an inline
// complexType, a named complexType referenced by type=, or — for a built-in or
// named simple type, or no type — a `text` leaf (no body if the element is
// declared with neither type nor content).
func xsdElementBody(el *xmlpb.Tag, cts map[string]*xmlpb.Tag, declared func(string) bool) *pb.ASTNode {
	if ct := xsdChildLocal(el, "complexType"); ct != nil {
		return xsdComplexTypeBody(ct, declared)
	}
	ty := localName(xsdAttr(el, "type"))
	if ty == "" {
		return nil // empty element
	}
	if ct, ok := cts[ty]; ok {
		return xsdComplexTypeBody(ct, declared)
	}
	return scalarNode("text") // built-in / named simple type → character content
}

// xsdComplexTypeBody lowers a complexType into its fields: attributes (string
// fields), then either a `text` leaf (simpleContent) or the content-model group
// (sequence / choice / all). complexContent/simpleContent extensions are
// unwrapped to their container so derived attributes and particles are seen.
func xsdComplexTypeBody(ct *xmlpb.Tag, declared func(string) bool) *pb.ASTNode {
	container, simple := xsdContentContainer(ct)
	var fields []*pb.ASTNode
	for _, a := range xsdChildrenLocal(container, "attribute") {
		if n := xsdAttr(a, "name"); n != "" {
			fields = append(fields, scalarNode(n))
		}
	}
	if simple {
		fields = append(fields, scalarNode("text"))
		return seqOrSingle(fields)
	}
	if g := xsdGroup(container); g != nil {
		fields = append(fields, xsdGroupFields(g, declared)...)
	}
	return seqOrSingle(fields)
}

// xsdContentContainer returns the element whose direct children hold the content
// model and attributes — the complexType itself, or the extension/restriction
// inside a complexContent/simpleContent — and whether it is simple content
// (character data plus attributes).
func xsdContentContainer(ct *xmlpb.Tag) (container *xmlpb.Tag, simple bool) {
	if cc := xsdChildLocal(ct, "complexContent"); cc != nil {
		if ext := xsdChildLocal(cc, "extension"); ext != nil {
			return ext, false
		}
		if res := xsdChildLocal(cc, "restriction"); res != nil {
			return res, false
		}
	}
	if sc := xsdChildLocal(ct, "simpleContent"); sc != nil {
		if ext := xsdChildLocal(sc, "extension"); ext != nil {
			return ext, true
		}
		if res := xsdChildLocal(sc, "restriction"); res != nil {
			return res, true
		}
		return sc, true
	}
	return ct, false
}

// xsdGroupFields lowers a content-model group (sequence / choice / all) into the
// fields it contributes, mirroring the DTD content-model lowering: a choice is a
// oneof, a repeated group (maxOccurs > 1) a repeated wrapper, a sequence flat
// ordered fields.
func xsdGroupFields(g *xmlpb.Tag, declared func(string) bool) []*pb.ASTNode {
	repeated := xsdRepeated(g)
	if xsdLocal(g) == "choice" {
		alt := &pb.ASTNode{Kind: compiler.KindAlternation, Value: choiceWrapperName}
		for _, m := range xsdGroupMembers(g) {
			alt.Children = append(alt.Children, xsdMemberField(m, declared))
		}
		if repeated {
			return []*pb.ASTNode{repeatedOf(alt)}
		}
		return []*pb.ASTNode{alt}
	}
	// sequence / all
	var out []*pb.ASTNode
	for _, m := range xsdGroupMembers(g) {
		out = append(out, xsdMemberField(m, declared))
	}
	if repeated && len(out) > 1 {
		return []*pb.ASTNode{repeatedOf(seqOrSingle(out))}
	}
	if repeated {
		for i := range out {
			out[i] = repeatedOf(out[i])
		}
	}
	return out
}

// xsdMemberField lowers one member of a group: a child element (by name or ref)
// wrapped per its occurrence, or a nested group.
func xsdMemberField(m *xmlpb.Tag, declared func(string) bool) *pb.ASTNode {
	if xsdLocal(m) == "element" {
		name := xsdAttr(m, "name")
		if name == "" {
			name = localName(xsdAttr(m, "ref"))
		}
		return xsdOccWrap(childField(name, declared), m)
	}
	return xsdOccWrap(seqOrSingle(xsdGroupFields(m, declared)), m)
}

// xsdOccWrap applies a particle's occurrence: maxOccurs > 1 → repeated,
// minOccurs = 0 → optional.
func xsdOccWrap(n *pb.ASTNode, p *xmlpb.Tag) *pb.ASTNode {
	if n == nil {
		return nil
	}
	if xsdRepeated(p) {
		return repeatedOf(n)
	}
	if xsdAttr(p, "minOccurs") == "0" {
		return optionalOf(n)
	}
	return n
}

// xsdRepeated reports whether maxOccurs makes the particle repeatable.
func xsdRepeated(t *xmlpb.Tag) bool {
	switch mo := xsdAttr(t, "maxOccurs"); mo {
	case "", "0", "1":
		return false
	default:
		return true // "unbounded" or any count > 1
	}
}

// --- Tag-tree helpers (XSD elements matched by local name) ---

// xsdLocal returns an element's local name (the resolved local part, or the
// part after any prefix).
func xsdLocal(t *xmlpb.Tag) string {
	if ln := t.GetNamespace().GetLocalName(); ln != "" {
		return ln
	}
	return localName(t.GetName())
}

func localName(qname string) string {
	if i := strings.IndexByte(qname, ':'); i >= 0 {
		return qname[i+1:]
	}
	return qname
}

func xsdChildrenLocal(t *xmlpb.Tag, local string) []*xmlpb.Tag {
	var out []*xmlpb.Tag
	for _, ci := range t.GetContents() {
		if c := ci.GetChild(); c != nil && xsdLocal(c) == local {
			out = append(out, c)
		}
	}
	return out
}

func xsdChildLocal(t *xmlpb.Tag, local string) *xmlpb.Tag {
	if cs := xsdChildrenLocal(t, local); len(cs) > 0 {
		return cs[0]
	}
	return nil
}

func xsdDescendants(t *xmlpb.Tag, local string) []*xmlpb.Tag {
	var out []*xmlpb.Tag
	var walk func(*xmlpb.Tag)
	walk = func(n *xmlpb.Tag) {
		for _, ci := range n.GetContents() {
			if c := ci.GetChild(); c != nil {
				if xsdLocal(c) == local {
					out = append(out, c)
				}
				walk(c)
			}
		}
	}
	walk(t)
	return out
}

func xsdAttr(t *xmlpb.Tag, name string) string {
	for _, a := range t.GetAttrs() {
		if a.GetName() == name {
			return a.GetValue()
		}
	}
	return ""
}

// xsdGroup returns the first content-model group child of a container.
func xsdGroup(container *xmlpb.Tag) *xmlpb.Tag {
	for _, ci := range container.GetContents() {
		if c := ci.GetChild(); c != nil {
			switch xsdLocal(c) {
			case "sequence", "choice", "all":
				return c
			}
		}
	}
	return nil
}

// xsdGroupMembers returns a group's element and nested-group members in order.
func xsdGroupMembers(g *xmlpb.Tag) []*xmlpb.Tag {
	var out []*xmlpb.Tag
	for _, ci := range g.GetContents() {
		if c := ci.GetChild(); c != nil {
			switch xsdLocal(c) {
			case "element", "sequence", "choice", "all":
				out = append(out, c)
			}
		}
	}
	return out
}
