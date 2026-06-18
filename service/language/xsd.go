package language

// xsd.go — the XSD front-end (ADR 0008 Phase 4). An XSD is itself an XML
// document, so it is parsed by xmile's own parser and the resulting Tag tree is
// walked into the gluon schema-AST that compiler.Compile lowers — exactly like
// CompileDTD and CompileGrammar, one more front-end into the shared backend.
//
// Spec: W3C XML Schema Part 1 (Structures) and Part 2 (Datatypes), see
// docs/REFERENCES.md. The supported subset is the structural shape of a schema,
// sufficient for an element vocabulary (and the OOXML part schemas of Phase 5):
//
//   - xs:element (named / ref, minOccurs / maxOccurs);
//   - xs:complexType (named / inline);
//   - xs:sequence / xs:choice / xs:all content models;
//   - xs:attribute (string fields);
//   - xs:simpleContent / xs:complexContent extension AND restriction (the
//     derived particle + attributes are the type's content; for restriction the
//     base content is replaced, for extension it is the derivation's own — we
//     model the locally-declared shape either way);
//   - xs:group / xs:attributeGroup references (inlined transitively, cycle-guarded);
//   - xs:any / xs:anyAttribute wildcards (no constraint → no field; an `open`
//     schema tolerates the matching instance markup);
//   - substitution groups (a content reference to a head element expands to a
//     choice over the head and its substitutes);
//   - xs:simpleType derivations — xs:restriction (an enum/pattern over a base),
//     xs:union, xs:list — and named simple types referenced by type=, all lowered
//     to a string field (datatype facets are validation, deferred per ADR 0008);
//   - xs:import / xs:include — a resolver pulls in other schema documents and
//     merges their top-level declarations into the scope (we match by local name,
//     so a namespace difference does not block a reference).
//
// One message per declared element, the same shape CompileDTD produces. This is
// construct/compilation support (the structural shape), not datatype-facet or
// identity-constraint validation, which stay out of scope.

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

// XSDResolver resolves an xs:import / xs:include schemaLocation to the bytes of
// the referenced schema document. It returns an error when the location cannot
// be resolved; CompileXSD then skips that import/include gracefully rather than
// failing the whole compile (many real schemas reference the XML namespace or
// unreachable URLs). A nil resolver resolves nothing — every import/include is
// skipped, which is the right default for a single self-contained schema.
type XSDResolver func(location string) ([]byte, error)

// CompileXSD lowers an XSD into a FileDescriptorProto, one message per declared
// element. Returns an error if the bytes are not a well-formed XSD or declare
// no elements.
//
// parseXML parses an XSD-as-XML into the generic AST; the service package
// supplies it (its grammar-driven parser) so this package never imports the
// parser directly. resolve, when non-nil, pulls in xs:import / xs:include
// targets (see XSDResolver); pass nil for a single self-contained schema.
func CompileXSD(xsd []byte, opts SchemaOptions, parseXML func(string) (*xmlpb.Xml, error), resolve XSDResolver) (*descriptorpb.FileDescriptorProto, error) {
	root, err := parseXSDRoot(xsd, parseXML)
	if err != nil {
		return nil, err
	}
	pkg := opts.Package
	if pkg == "" {
		pkg = "lang"
	}
	ast, err := xsdSchemaAST(root, pkg, parseXML, resolve)
	if err != nil {
		return nil, err
	}
	return compiler.Compile(ast, compiler.Options{
		Package:   pkg,
		GoPackage: opts.GoPackage,
		FileName:  opts.FileName,
	})
}

// parseXSDRoot parses XSD bytes and returns the <xs:schema> root, erroring if
// the bytes are not a well-formed XSD or the root is not a schema element.
func parseXSDRoot(xsd []byte, parseXML func(string) (*xmlpb.Xml, error)) (*xmlpb.Tag, error) {
	doc, err := parseXML(string(xsd))
	if err != nil {
		return nil, fmt.Errorf("not a well-formed XSD: %w", err)
	}
	root := doc.GetRoot()
	if root == nil || xsdLocal(root) != "schema" {
		return nil, fmt.Errorf("XSD root element is not <xs:schema>")
	}
	return root, nil
}

// xsdScope carries the top-level declarations and the element-declared predicate
// that the content-model walk needs to resolve type=, group ref=,
// attributeGroup ref= and substitutionGroup= references. Named groups and
// attributeGroups are inlined (Structures §3.7, §3.6) where referenced,
// transitively; named simpleTypes lower to a string field; a substitution-group
// head expands at a reference into a choice over its members.
type xsdScope struct {
	complexTypes    map[string]*xmlpb.Tag
	simpleTypes     map[string]*xmlpb.Tag   // name -> <xs:simpleType> definition
	groups          map[string]*xmlpb.Tag   // name -> <xs:group> definition
	attributeGroups map[string]*xmlpb.Tag   // name -> <xs:attributeGroup> definition
	substitutions   map[string][]string     // head element local name -> member local names
	declared        func(string) bool
}

// collectTopLevel folds one schema element's top-level declarations into the
// scope (first binding wins, so a local schema is not overridden by an included
// one), and appends every element declaration (top-level or nested) to elems /
// names. It is applied to the primary schema and, transitively, to every
// resolved xs:import / xs:include target.
func (sc *xsdScope) collectTopLevel(schema *xmlpb.Tag, elems map[string]*xmlpb.Tag, names *[]string) {
	bindFirst := func(m map[string]*xmlpb.Tag, t *xmlpb.Tag) {
		if n := xsdAttr(t, "name"); n != "" {
			if _, dup := m[n]; !dup {
				m[n] = t
			}
		}
	}
	for _, ct := range xsdChildrenLocal(schema, "complexType") {
		bindFirst(sc.complexTypes, ct)
	}
	for _, st := range xsdChildrenLocal(schema, "simpleType") {
		bindFirst(sc.simpleTypes, st)
	}
	for _, g := range xsdChildrenLocal(schema, "group") {
		bindFirst(sc.groups, g)
	}
	for _, ag := range xsdChildrenLocal(schema, "attributeGroup") {
		bindFirst(sc.attributeGroups, ag)
	}

	// Substitution groups: an element with substitutionGroup="head" may appear
	// wherever head is referenced (Structures §3.3.6). Record head -> members by
	// local name; the content-model walk expands a ref to head into a choice.
	for _, el := range xsdChildrenLocal(schema, "element") {
		head := localName(xsdAttr(el, "substitutionGroup"))
		member := xsdAttr(el, "name")
		if head == "" || member == "" {
			continue
		}
		if !contains(sc.substitutions[head], member) {
			sc.substitutions[head] = append(sc.substitutions[head], member)
		}
	}

	// Every element declaration, depth-first, first binding wins.
	for _, el := range xsdDescendants(schema, "element") {
		n := xsdAttr(el, "name")
		if n == "" {
			continue // a ref= use, not a declaration
		}
		if _, dup := elems[n]; dup {
			continue
		}
		elems[n] = el
		*names = append(*names, n)
	}
}

// xsdSchemaAST walks an <xs:schema> Tag tree into a gluon schema-AST (file →
// rule* → body): one rule per element declaration (top-level or nested, first
// binding), its body built from the element's type — an inline or named
// complexType, or a simple type lowered to a `text` leaf. Resolved xs:import /
// xs:include targets contribute their top-level declarations and elements too.
func xsdSchemaAST(schema *xmlpb.Tag, language string, parseXML func(string) (*xmlpb.Xml, error), resolve XSDResolver) (*pb.ASTDescriptor, error) {
	sc := &xsdScope{
		complexTypes:    map[string]*xmlpb.Tag{},
		simpleTypes:     map[string]*xmlpb.Tag{},
		groups:          map[string]*xmlpb.Tag{},
		attributeGroups: map[string]*xmlpb.Tag{},
		substitutions:   map[string][]string{},
	}

	elems := map[string]*xmlpb.Tag{}
	var names []string

	// The primary schema, then every transitively imported/included schema. A
	// visited-set on the schemaLocation avoids re-including a shared header.
	visited := map[string]bool{}
	var fold func(s *xmlpb.Tag)
	fold = func(s *xmlpb.Tag) {
		sc.collectTopLevel(s, elems, &names)
		if resolve == nil {
			return
		}
		for _, ref := range append(xsdChildrenLocal(s, "include"), xsdChildrenLocal(s, "import")...) {
			loc := xsdAttr(ref, "schemaLocation")
			if loc == "" || visited[loc] {
				continue
			}
			visited[loc] = true
			data, err := resolve(loc)
			if err != nil {
				continue // unresolved (XML namespace, unreachable URL) — skip gracefully
			}
			sub, err := parseXSDRoot(data, parseXML)
			if err != nil {
				continue
			}
			fold(sub)
		}
	}
	fold(schema)

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
// complexType, a named complexType referenced by type=, or — for a built-in
// type, a named simpleType (or an inline simpleType), or no type — a `text`
// leaf (no body if the element is declared with neither type nor content).
func xsdElementBody(el *xmlpb.Tag, sc *xsdScope) *pb.ASTNode {
	if ct := xsdChildLocal(el, "complexType"); ct != nil {
		return xsdComplexTypeBody(ct, sc)
	}
	if xsdChildLocal(el, "simpleType") != nil {
		return scalarNode("text") // inline simple type → character content
	}
	ty := localName(xsdAttr(el, "type"))
	if ty == "" {
		return nil // empty element
	}
	if ct, ok := sc.complexTypes[ty]; ok {
		return xsdComplexTypeBody(ct, sc)
	}
	// A named simpleType, or a built-in (xs:string, xs:decimal, …) → character
	// content. (A named simpleType reference resolves to a string field; its
	// restriction/union/list derivation is a datatype facet, deferred.)
	return scalarNode("text")
}

// xsdComplexTypeBody lowers a complexType into its fields: attributes (string
// fields, including those pulled in by attributeGroup ref=), then either a
// `text` leaf (simpleContent) or the content-model group (sequence / choice /
// all). complexContent/simpleContent extension AND restriction are unwrapped to
// their container so the derivation's own attributes and particles are seen.
func xsdComplexTypeBody(ct *xmlpb.Tag, sc *xsdScope) *pb.ASTNode {
	container, simple := xsdContentContainer(ct)
	fields := xsdAttributeFields(container, sc, map[string]bool{})
	if simple {
		fields = append(fields, scalarNode("text"))
		return seqOrSingle(fields)
	}
	if g := xsdGroup(container); g != nil {
		fields = append(fields, xsdGroupFields(g, sc, map[string]bool{})...)
	} else if gref := xsdChildLocal(container, "group"); gref != nil {
		// A complexType whose whole content model is a named-group reference
		// (no enclosing sequence/choice/all), e.g. <complexType><group ref="G"/>.
		fields = append(fields, xsdMemberFields(gref, sc, map[string]bool{})...)
	}
	return seqOrSingle(fields)
}

// xsdAttributeFields collects the string fields a container contributes through
// its direct xs:attribute children and, transitively, the xs:attribute children
// of any xs:attributeGroup it references (Structures §3.6.2.2). An
// xs:anyAttribute wildcard adds no field — the instance side relies on an open
// schema to tolerate it. seen guards against attributeGroup reference cycles.
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
		case "anyAttribute":
			// A wildcard: no constraint, so no field (open schemas tolerate it).
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
// (character data plus attributes). For a restriction the base's content is
// replaced by the restriction's own declared particle + attributes; for an
// extension the derivation's own are added; either way the container's direct
// children are exactly the locally-declared shape, which is what we model.
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
		if len(alt.Children) == 0 {
			return nil
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
// sequence/choice/all group; a named-group reference (<xs:group ref="G"/>) whose
// members are inlined in place; or an xs:any wildcard, which contributes nothing.
func xsdMemberFields(m *xmlpb.Tag, sc *xsdScope, seen map[string]bool) []*pb.ASTNode {
	switch xsdLocal(m) {
	case "element":
		// A ref to a substitution-group head accepts the head OR any member, so
		// it expands to a choice; a plain element (or a ref to a non-head) is one
		// field. Either way the occurrence wraps the result.
		if ref := localName(xsdAttr(m, "ref")); ref != "" {
			if members := sc.substitutions[ref]; len(members) > 0 {
				return []*pb.ASTNode{xsdOccWrap(xsdSubstChoice(ref, members, sc), m)}
			}
			return []*pb.ASTNode{xsdOccWrap(childField(ref, sc.declared), m)}
		}
		name := xsdAttr(m, "name")
		return []*pb.ASTNode{xsdOccWrap(childField(name, sc.declared), m)}
	case "any":
		// A wildcard: no constraint, so no field (open schemas tolerate it).
		return nil
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

// xsdSubstChoice builds the oneof a substitution-group head reference expands
// to: a variant for the head itself and one for each substitute member (deduped,
// head first), all by their element field. With a single member (no real
// substitutes) it collapses to the head's plain field.
func xsdSubstChoice(head string, members []string, sc *xsdScope) *pb.ASTNode {
	variants := []string{head}
	for _, m := range members {
		if !contains(variants, m) {
			variants = append(variants, m)
		}
	}
	if len(variants) == 1 {
		return childField(head, sc.declared)
	}
	alt := &pb.ASTNode{Kind: compiler.KindAlternation, Value: choiceWrapperName}
	for _, v := range variants {
		alt.Children = append(alt.Children, childField(v, sc.declared))
	}
	return alt
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

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
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

// xsdGroupMembers returns a group's element, nested-group, named-group reference
// (<xs:group ref=…/>), and xs:any wildcard members in order.
func xsdGroupMembers(g *xmlpb.Tag) []*xmlpb.Tag {
	var out []*xmlpb.Tag
	for _, ci := range g.GetContents() {
		if c := ci.GetChild(); c != nil {
			switch xsdLocal(c) {
			case "element", "sequence", "choice", "all", "group", "any":
				out = append(out, c)
			}
		}
	}
	return out
}
