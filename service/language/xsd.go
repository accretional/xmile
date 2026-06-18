package language

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
//
// parseXML parses an XSD-as-XML into the generic AST; the service package
// supplies it (its grammar-driven parser) so this package never imports the
// parser directly.
func CompileXSD(xsd []byte, opts SchemaOptions, parseXML func(string) (*xmlpb.Xml, error)) (*descriptorpb.FileDescriptorProto, error) {
	doc, err := parseXML(string(xsd))
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

// xsdScope carries the top-level declarations and the element-declared predicate
// that the content-model walk needs to resolve type=, group ref= and
// attributeGroup ref= references. Named groups and attributeGroups are inlined
// (Structures §3.7, §3.6) where referenced, transitively.
type xsdScope struct {
	complexTypes    map[string]*xmlpb.Tag
	groups          map[string]*xmlpb.Tag // name -> <xs:group> definition
	attributeGroups map[string]*xmlpb.Tag // name -> <xs:attributeGroup> definition
	declared        func(string) bool
}

// xsdSchemaAST walks an <xs:schema> Tag tree into a gluon schema-AST (file →
// rule* → body): one rule per element declaration (top-level or nested, first
// binding), its body built from the element's type — an inline or named
// complexType, or a simple type lowered to a `text` leaf.
func xsdSchemaAST(schema *xmlpb.Tag, language string) (*pb.ASTDescriptor, error) {
	sc := &xsdScope{
		complexTypes:    map[string]*xmlpb.Tag{},
		groups:          map[string]*xmlpb.Tag{},
		attributeGroups: map[string]*xmlpb.Tag{},
	}
	for _, ct := range xsdChildrenLocal(schema, "complexType") {
		if n := xsdAttr(ct, "name"); n != "" {
			sc.complexTypes[n] = ct
		}
	}
	for _, g := range xsdChildrenLocal(schema, "group") {
		if n := xsdAttr(g, "name"); n != "" {
			sc.groups[n] = g
		}
	}
	for _, ag := range xsdChildrenLocal(schema, "attributeGroup") {
		if n := xsdAttr(ag, "name"); n != "" {
			sc.attributeGroups[n] = ag
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
	sc.declared = func(n string) bool { _, ok := elems[n]; return ok }

	sort.Strings(names)
	file := &pb.ASTNode{Kind: compiler.KindFile}
	for _, name := range names {
		rule := &pb.ASTNode{Kind: compiler.KindRule, Value: name}
		if body := xsdElementBody(elems[name], sc); body != nil {
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
func xsdElementBody(el *xmlpb.Tag, sc *xsdScope) *pb.ASTNode {
	if ct := xsdChildLocal(el, "complexType"); ct != nil {
		return xsdComplexTypeBody(ct, sc)
	}
	ty := localName(xsdAttr(el, "type"))
	if ty == "" {
		return nil // empty element
	}
	if ct, ok := sc.complexTypes[ty]; ok {
		return xsdComplexTypeBody(ct, sc)
	}
	return scalarNode("text") // built-in / named simple type → character content
}

// xsdComplexTypeBody lowers a complexType into its fields: attributes (string
// fields, including those pulled in by attributeGroup ref=), then either a
// `text` leaf (simpleContent) or the content-model group (sequence / choice /
// all). complexContent/simpleContent extensions are unwrapped to their
// container so derived attributes and particles are seen.
func xsdComplexTypeBody(ct *xmlpb.Tag, sc *xsdScope) *pb.ASTNode {
	container, simple := xsdContentContainer(ct)
	fields := xsdAttributeFields(container, sc, map[string]bool{})
	if simple {
		fields = append(fields, scalarNode("text"))
		return seqOrSingle(fields)
	}
	if g := xsdGroup(container); g != nil {
		fields = append(fields, xsdGroupFields(g, sc, map[string]bool{})...)
	}
	return seqOrSingle(fields)
}

// xsdAttributeFields collects the string fields a container contributes through
// its direct xs:attribute children and, transitively, the xs:attribute children
// of any xs:attributeGroup it references (Structures §3.6.2.2). seen guards
// against attributeGroup reference cycles.
func xsdAttributeFields(container *xmlpb.Tag, sc *xsdScope, seen map[string]bool) []*pb.ASTNode {
	var fields []*pb.ASTNode
	for _, ci := range container.GetContents() {
		c := ci.GetChild()
		if c == nil {
			continue
		}
		switch xsdLocal(c) {
		case "attribute":
			if n := xsdAttr(c, "name"); n != "" {
				fields = append(fields, scalarNode(n))
			}
		case "attributeGroup":
			ref := localName(xsdAttr(c, "ref"))
			if ref == "" || seen[ref] {
				continue // a definition (handled at top level) or a cycle
			}
			ag, ok := sc.attributeGroups[ref]
			if !ok {
				continue
			}
			seen[ref] = true
			fields = append(fields, xsdAttributeFields(ag, sc, seen)...)
			delete(seen, ref)
		}
	}
	return fields
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
// ordered fields. seen tracks the named groups currently being inlined so a
// group reference cycle terminates (Structures §3.7.3).
func xsdGroupFields(g *xmlpb.Tag, sc *xsdScope, seen map[string]bool) []*pb.ASTNode {
	repeated := xsdRepeated(g)
	if xsdLocal(g) == "choice" {
		alt := &pb.ASTNode{Kind: compiler.KindAlternation, Value: choiceWrapperName}
		for _, m := range xsdGroupMembers(g) {
			alt.Children = append(alt.Children, xsdMemberFields(m, sc, seen)...)
		}
		if repeated {
			return []*pb.ASTNode{repeatedOf(alt)}
		}
		return []*pb.ASTNode{alt}
	}
	// sequence / all
	var out []*pb.ASTNode
	for _, m := range xsdGroupMembers(g) {
		out = append(out, xsdMemberFields(m, sc, seen)...)
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

// xsdMemberFields lowers one member of a group into the field(s) it contributes:
// a child element (by name or ref) wrapped per its occurrence; a nested
// sequence/choice/all group; or a named-group reference (<xs:group ref="G"/>),
// whose members are inlined in place wrapped per the ref's occurrence.
func xsdMemberFields(m *xmlpb.Tag, sc *xsdScope, seen map[string]bool) []*pb.ASTNode {
	switch xsdLocal(m) {
	case "element":
		name := xsdAttr(m, "name")
		if name == "" {
			name = localName(xsdAttr(m, "ref"))
		}
		return []*pb.ASTNode{xsdOccWrap(childField(name, sc.declared), m)}
	case "group":
		// A named-group reference: inline its single content-model group,
		// guarding the cycle. At default occurrence (1..1) the members splice
		// flat into the enclosing group, exactly as if written inline; an
		// optional or repeated reference must wrap them so the occurrence
		// applies to the group as a whole.
		ref := localName(xsdAttr(m, "ref"))
		if ref == "" || seen[ref] {
			return nil
		}
		def, ok := sc.groups[ref]
		if !ok {
			return nil
		}
		inner := xsdGroup(def)
		if inner == nil {
			return nil
		}
		seen[ref] = true
		fields := xsdGroupFields(inner, sc, seen)
		delete(seen, ref)
		if !xsdRepeated(m) && xsdAttr(m, "minOccurs") != "0" {
			return fields // 1..1: splice members flat
		}
		if n := xsdOccWrap(seqOrSingle(fields), m); n != nil {
			return []*pb.ASTNode{n}
		}
		return nil
	default:
		// A nested anonymous sequence/choice/all.
		if n := xsdOccWrap(seqOrSingle(xsdGroupFields(m, sc, seen)), m); n != nil {
			return []*pb.ASTNode{n}
		}
		return nil
	}
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

// xsdGroupMembers returns a group's element, nested-group, and named-group
// reference (<xs:group ref=…/>) members in order.
func xsdGroupMembers(g *xmlpb.Tag) []*xmlpb.Tag {
	var out []*xmlpb.Tag
	for _, ci := range g.GetContents() {
		if c := ci.GetChild(); c != nil {
			switch xsdLocal(c) {
			case "element", "sequence", "choice", "all", "group":
				out = append(out, c)
			}
		}
	}
	return out
}
