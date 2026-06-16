package service

// validate.go — DTD validity checking, the type-level constraint pass that runs
// after well-formedness. Where checkWellFormed answers "is this syntactically a
// document?", validate answers "does this document obey the content models and
// attribute declarations in its DTD?" — the W3C Validity Constraints.
//
// Scope: the internal DTD subset only. A document with no DOCTYPE, an external
// subset (declarations we cannot see), or parameter entities (which we do not
// expand) is not validated — it is reported as merely well-formed, never
// invalid, so we never guess "invalid" from declarations we cannot read.
//
// As everywhere else, no grammar lives here: the content models and attribute
// lists are walked off the DTD CST (lang/dtd.ebnf node kinds), reusing the same
// helpers and the content-model parser the schema compiler uses.

import (
	"fmt"
	"sort"
	"strings"

	pb "github.com/accretional/gluon/v2/pb"

	"github.com/accretional/xmile/lex"
	xmlpb "github.com/accretional/xmile/proto/pb/xml"
)

// ValidityError reports a well-formed document that violates a constraint in
// its DTD. It is distinct from WFError: it maps to the W3C "invalid" category
// and, over gRPC, to FAILED_PRECONDITION rather than INVALID_ARGUMENT.
type ValidityError struct {
	Msg string
}

func (e *ValidityError) Error() string { return "invalid: " + e.Msg }

func invalidf(format string, args ...any) *ValidityError {
	return &ValidityError{Msg: fmt.Sprintf(format, args...)}
}

// --- the validation model, built from the DTD CST ---

type cmKind int

const (
	cmEmpty cmKind = iota
	cmAny
	cmMixed
	cmElement
)

type elemDecl struct {
	kind  cmKind
	mixed map[string]bool // cmMixed: the allowed child element names
	model *particle       // cmElement: the content model tree
}

type attrDecl struct {
	typ     string   // CDATA ID IDREF IDREFS ENTITY ENTITIES NMTOKEN NMTOKENS NOTATION ENUM
	values  []string // NOTATION / ENUM: the allowed tokens
	defKind string   // REQUIRED IMPLIED FIXED DEFAULT
	defVal  string   // FIXED / DEFAULT: the declared value
}

type dtdModel struct {
	rootName  string
	elems     map[string]*elemDecl
	attrs     map[string]map[string]*attrDecl
	notations map[string]bool
	info      *dtdInfo       // declared entities (for ENTITY/ENTITIES)
	declErr   *ValidityError // a validity error in the DTD itself (e.g. duplicate element decl)
}

// buildModel walks a DOCTYPE CST into a validation model. The root name is the
// doctype's element name; element and attribute declarations are read off the
// markup declarations, first-declaration-binding as XML requires.
func buildModel(dtdRoot *pb.ASTNode, info *dtdInfo) *dtdModel {
	m := &dtdModel{
		rootName:  "",
		elems:     map[string]*elemDecl{},
		attrs:     map[string]map[string]*attrDecl{},
		notations: map[string]bool{},
		info:      info,
	}
	if n := directChild(dtdRoot, "Name"); n != nil {
		m.rootName = n.GetValue()
	}
	for _, nd := range descendants(dtdRoot, "notationDecl") {
		if n := firstName(nd); n != "" {
			m.notations[n] = true
		}
	}

	for _, decl := range descendants(dtdRoot, "elementDecl") {
		name := firstName(decl)
		if name == "" {
			continue
		}
		// VC: Unique Element Type Declaration.
		if _, dup := m.elems[name]; dup {
			m.setDeclErr(invalidf("element type %s is declared more than once", name))
			continue
		}
		m.elems[name] = parseElemDecl(firstDescendant(decl, "contentspec"))
	}

	for _, decl := range descendants(dtdRoot, "attlistDecl") {
		elem := firstName(decl)
		if elem == "" {
			continue
		}
		list := m.attrs[elem]
		if list == nil {
			list = map[string]*attrDecl{}
			m.attrs[elem] = list
		}
		for _, ad := range descendants(decl, "attDef") {
			an := firstName(ad)
			if an == "" || list[an] != nil {
				continue // first declaration of an attribute binds
			}
			ty, vals := parseAttType(firstDescendant(ad, "attType"))
			dk, dv := parseDefault(firstDescendant(ad, "defaultDecl"))
			list[an] = &attrDecl{typ: ty, values: vals, defKind: dk, defVal: dv}
		}
		// VC: One ID per Element Type, and ID Attribute Default.
		m.checkIDDecls(elem, list)
	}
	m.checkDeclConstraints(dtdRoot)
	return m
}

// checkDeclConstraints enforces the validity constraints that live in the DTD
// itself, independent of any document instance: No Duplicate Types (a name may
// not repeat in a Mixed declaration), Notation Attributes (every notation named
// in a NOTATION attribute must be declared), Attribute Default Legal (a default
// value must be valid for its type), and Notation Declared (an unparsed
// entity's notation must be declared).
func (m *dtdModel) checkDeclConstraints(dtdRoot *pb.ASTNode) {
	for _, decl := range descendants(dtdRoot, "elementDecl") {
		mx := firstDescendant(decl, "mixed")
		if mx == nil {
			continue
		}
		seen := map[string]bool{}
		for _, nm := range descendants(mx, "Name") {
			n := nm.GetValue()
			if seen[n] {
				m.setDeclErr(invalidf("element %s appears more than once in a mixed content declaration", n))
			}
			seen[n] = true
		}
	}

	for _, en := range sortedKeys(m.attrs) {
		for _, an := range sortedKeys(m.attrs[en]) {
			d := m.attrs[en][an]
			if d.typ == "NOTATION" {
				for _, v := range d.values {
					if !m.notations[v] {
						m.setDeclErr(invalidf("notation %s named in attribute %s of %s is not declared", v, an, en))
					}
				}
			}
			if (d.defKind == "DEFAULT" || d.defKind == "FIXED") && !defaultLegal(d) {
				m.setDeclErr(invalidf("default value %q for attribute %s of %s is not valid for its type", d.defVal, an, en))
			}
		}
	}

	for _, ed := range descendants(dtdRoot, "entityDecl") {
		ev := firstDescendant(ed, "entityValue")
		if ev == nil || !hasTerminal(ev, "NDATA") {
			continue
		}
		if nm := firstDescendant(ev, "Name"); nm != nil && !m.notations[nm.GetValue()] {
			m.setDeclErr(invalidf("notation %s of unparsed entity is not declared", nm.GetValue()))
		}
	}
}

// defaultLegal reports whether an attribute's #FIXED/default value satisfies
// its declared type (VC: Attribute Default Legal).
func defaultLegal(d *attrDecl) bool {
	switch d.typ {
	case "CDATA":
		return true
	case "ID", "IDREF", "ENTITY":
		return isXMLName(collapseSpaces(d.defVal))
	case "IDREFS", "ENTITIES":
		toks := splitSpaces(d.defVal)
		if len(toks) == 0 {
			return false
		}
		for _, t := range toks {
			if !isXMLName(t) {
				return false
			}
		}
		return true
	case "NMTOKEN":
		return isXMLNmtoken(collapseSpaces(d.defVal))
	case "NMTOKENS":
		toks := splitSpaces(d.defVal)
		if len(toks) == 0 {
			return false
		}
		for _, t := range toks {
			if !isXMLNmtoken(t) {
				return false
			}
		}
		return true
	case "NOTATION", "ENUM":
		return contains(d.values, collapseSpaces(d.defVal))
	}
	return true
}

func sortedKeys[V any](m map[string]V) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

func (m *dtdModel) setDeclErr(e *ValidityError) {
	if m.declErr == nil {
		m.declErr = e
	}
}

// checkIDDecls enforces the two ID declaration constraints for one element's
// attribute list: at most one ID-typed attribute, and an ID attribute's
// default must be #IMPLIED or #REQUIRED.
func (m *dtdModel) checkIDDecls(elem string, list map[string]*attrDecl) {
	ids := 0
	names := make([]string, 0, len(list))
	for an := range list {
		names = append(names, an)
	}
	sort.Strings(names)
	for _, an := range names {
		d := list[an]
		if d.typ != "ID" {
			continue
		}
		ids++
		if ids > 1 {
			m.setDeclErr(invalidf("element %s has more than one ID attribute", elem))
		}
		if d.defKind != "IMPLIED" && d.defKind != "REQUIRED" {
			m.setDeclErr(invalidf("ID attribute %s on %s must be #IMPLIED or #REQUIRED", an, elem))
		}
	}
}

// parseElemDecl reads a contentspec node into an elemDecl. In a children/mixed
// model an element name is a Name node, never a terminal, so the EMPTY/ANY
// keyword checks cannot be fooled by an element happening to be named EMPTY.
func parseElemDecl(cs *pb.ASTNode) *elemDecl {
	switch {
	case cs == nil:
		return &elemDecl{kind: cmAny}
	case hasTerminal(cs, "EMPTY"):
		return &elemDecl{kind: cmEmpty}
	case hasTerminal(cs, "ANY"):
		return &elemDecl{kind: cmAny}
	case firstDescendant(cs, "mixed") != nil:
		allowed := map[string]bool{}
		for _, nm := range descendants(firstDescendant(cs, "mixed"), "Name") {
			allowed[nm.GetValue()] = true
		}
		return &elemDecl{kind: cmMixed, mixed: allowed}
	case firstDescendant(cs, "children") != nil:
		return &elemDecl{kind: cmElement, model: parseContentModel(leafText(firstDescendant(cs, "children")))}
	}
	return &elemDecl{kind: cmAny}
}

// parseAttType reads an attType node into a type tag and (for enumerations and
// notations) the set of permitted tokens.
func parseAttType(at *pb.ASTNode) (string, []string) {
	if at == nil {
		return "CDATA", nil
	}
	if nt := firstDescendant(at, "notationType"); nt != nil {
		var vals []string
		for _, nm := range descendants(nt, "Name") {
			vals = append(vals, nm.GetValue())
		}
		return "NOTATION", vals
	}
	if en := firstDescendant(at, "enumeration"); en != nil {
		var vals []string
		for _, nm := range descendants(en, "Nmtoken") {
			vals = append(vals, nm.GetValue())
		}
		return "ENUM", vals
	}
	for _, t := range []string{"IDREFS", "IDREF", "ID", "ENTITIES", "ENTITY", "NMTOKENS", "NMTOKEN", "CDATA"} {
		if hasTerminal(at, t) {
			return t, nil
		}
	}
	return "CDATA", nil
}

// parseDefault reads a defaultDecl node into a default kind and value.
func parseDefault(dd *pb.ASTNode) (string, string) {
	if dd == nil {
		return "IMPLIED", ""
	}
	if hasTerminal(dd, "#REQUIRED") {
		return "REQUIRED", ""
	}
	if hasTerminal(dd, "#IMPLIED") {
		return "IMPLIED", ""
	}
	val := litValue(firstDescendant(dd, "fixedDefault"))
	if hasTerminal(dd, "#FIXED") {
		return "FIXED", val
	}
	return "DEFAULT", val
}

// --- validating a document against the model ---

type validator struct {
	m      *dtdModel
	is11   bool
	ids    map[string]bool // ID values seen (uniqueness)
	idrefs []string        // IDREF/IDREFS values to resolve at the end
}

// validate checks a projected document against its DTD model and returns the
// first validity violation, or nil if the document is valid.
func validate(doc *xmlpb.Document, m *dtdModel, is11 bool) error {
	if m.declErr != nil {
		return m.declErr
	}
	root := doc.GetRoot()
	if root == nil {
		return nil
	}
	// VC: Root Element Type.
	if root.GetName() != m.rootName {
		return invalidf("root element <%s> does not match the DOCTYPE name %q", root.GetName(), m.rootName)
	}
	v := &validator{m: m, is11: is11, ids: map[string]bool{}}
	if err := v.element(root); err != nil {
		return err
	}
	// VC: IDREF — every referenced ID must be defined somewhere in the document.
	for _, ref := range v.idrefs {
		if !v.ids[ref] {
			return invalidf("IDREF %q does not match any ID attribute value", ref)
		}
	}
	return nil
}

func (v *validator) element(tag *xmlpb.Tag) error {
	name := tag.GetName()
	ed := v.m.elems[name]
	if ed == nil {
		// VC: Element Valid (no matching element declaration).
		return invalidf("element <%s> is not declared", name)
	}
	if err := v.content(name, ed, tag); err != nil {
		return err
	}
	if err := v.attributes(name, tag); err != nil {
		return err
	}
	for _, ci := range tag.GetContents() {
		if ch, ok := ci.GetItem().(*xmlpb.ContentItem_Child); ok {
			if err := v.element(ch.Child); err != nil {
				return err
			}
		}
	}
	return nil
}

// content enforces the element's declared content model (VC: Element Valid).
func (v *validator) content(name string, ed *elemDecl, tag *xmlpb.Tag) error {
	var children []string
	anyCharData, nonSpaceCharData := false, false
	for _, ci := range tag.GetContents() {
		switch it := ci.GetItem().(type) {
		case *xmlpb.ContentItem_Child:
			children = append(children, it.Child.GetName())
		case *xmlpb.ContentItem_Text:
			anyCharData = true
			if !onlyXMLSpace(it.Text) {
				nonSpaceCharData = true
			}
		case *xmlpb.ContentItem_Cdata:
			// A CDATA section is character data even when it looks blank.
			anyCharData, nonSpaceCharData = true, true
		}
	}

	switch ed.kind {
	case cmEmpty:
		if len(children) > 0 || anyCharData {
			return invalidf("element <%s> is declared EMPTY but has content", name)
		}
	case cmAny:
		// Any declared element and any character data; child declarations are
		// checked when we recurse into them.
	case cmMixed:
		for _, cn := range children {
			if !ed.mixed[cn] {
				return invalidf("element <%s> may not appear in the mixed content of <%s>", cn, name)
			}
		}
	case cmElement:
		if nonSpaceCharData {
			return invalidf("character data is not allowed in the element content of <%s>", name)
		}
		if !matchContentModel(ed.model, children) {
			return invalidf("content of <%s> does not match its declared content model", name)
		}
	}
	return nil
}

// attributes enforces the attribute declarations for one element (VC:
// Attribute Value Type, Required Attribute, Fixed Attribute, and the
// per-type token constraints).
func (v *validator) attributes(name string, tag *xmlpb.Tag) error {
	decls := v.m.attrs[name]
	present := map[string]string{}
	for _, a := range tag.GetAttrs() {
		present[a.GetName()] = a.GetValue()
	}

	// Declared-ness and per-value checks, in document order.
	for _, a := range tag.GetAttrs() {
		an := a.GetName()
		var d *attrDecl
		if decls != nil {
			d = decls[an]
		}
		if d == nil {
			return invalidf("attribute %q on <%s> is not declared", an, name)
		}
		if err := v.attrValue(name, an, d, a.GetValue()); err != nil {
			return err
		}
	}

	// Required attributes present; fixed attributes match. Sorted for stable
	// diagnostics.
	names := make([]string, 0, len(decls))
	for an := range decls {
		names = append(names, an)
	}
	sort.Strings(names)
	for _, an := range names {
		d := decls[an]
		val, ok := present[an]
		if !ok {
			if d.defKind == "REQUIRED" {
				return invalidf("required attribute %q is missing on <%s>", an, name)
			}
			continue
		}
		if d.defKind == "FIXED" && normForType(d.typ, val) != normForType(d.typ, d.defVal) {
			return invalidf("attribute %q on <%s> must equal its #FIXED value %q", an, name, d.defVal)
		}
	}
	return nil
}

// attrValue validates one attribute value against its declared type and
// records ID/IDREF values for the document-wide resolution pass.
func (v *validator) attrValue(elem, attr string, d *attrDecl, raw string) error {
	switch d.typ {
	case "CDATA":
		return nil
	case "ID":
		val := collapseSpaces(raw)
		if !isXMLName(val) {
			return invalidf("ID attribute %q on <%s> is not a valid name: %q", attr, elem, val)
		}
		if v.ids[val] {
			return invalidf("duplicate ID value %q", val)
		}
		v.ids[val] = true
	case "IDREF":
		val := collapseSpaces(raw)
		if !isXMLName(val) {
			return invalidf("IDREF attribute %q on <%s> is not a valid name: %q", attr, elem, val)
		}
		v.idrefs = append(v.idrefs, val)
	case "IDREFS":
		toks := splitSpaces(raw)
		if len(toks) == 0 {
			return invalidf("IDREFS attribute %q on <%s> is empty", attr, elem)
		}
		for _, t := range toks {
			if !isXMLName(t) {
				return invalidf("IDREFS token %q on <%s> is not a valid name", t, elem)
			}
			v.idrefs = append(v.idrefs, t)
		}
	case "NMTOKEN":
		val := collapseSpaces(raw)
		if !isXMLNmtoken(val) {
			return invalidf("NMTOKEN attribute %q on <%s> is not a valid name token: %q", attr, elem, val)
		}
	case "NMTOKENS":
		toks := splitSpaces(raw)
		if len(toks) == 0 {
			return invalidf("NMTOKENS attribute %q on <%s> is empty", attr, elem)
		}
		for _, t := range toks {
			if !isXMLNmtoken(t) {
				return invalidf("NMTOKENS token %q on <%s> is not a valid name token", t, elem)
			}
		}
	case "ENTITY":
		if !v.unparsedEntity(collapseSpaces(raw)) {
			return invalidf("ENTITY attribute %q on <%s> does not name an unparsed entity: %q", attr, elem, raw)
		}
	case "ENTITIES":
		toks := splitSpaces(raw)
		if len(toks) == 0 {
			return invalidf("ENTITIES attribute %q on <%s> is empty", attr, elem)
		}
		for _, t := range toks {
			if !v.unparsedEntity(t) {
				return invalidf("ENTITIES token %q on <%s> does not name an unparsed entity", t, elem)
			}
		}
	case "NOTATION", "ENUM":
		val := collapseSpaces(raw)
		if !contains(d.values, val) {
			return invalidf("attribute %q on <%s> value %q is not one of its declared values", attr, elem, val)
		}
	}
	return nil
}

func (v *validator) unparsedEntity(name string) bool {
	ent, ok := v.m.info.general[name]
	return ok && ent.unparsed
}

// --- content-model matching (a regular expression over child element names) ---

// matchContentModel reports whether the sequence of child element names is
// accepted by the content model. It threads a set of reachable positions
// through the particle tree (a small Thompson-style NFA), so it handles
// arbitrary nesting, choices, sequences, and ?/*/+ occurrences.
func matchContentModel(p *particle, names []string) bool {
	ends := matchOcc(p, names, map[int]bool{0: true})
	return ends[len(names)]
}

// matchOcc applies a particle's occurrence indicator (?/*/+ or none).
func matchOcc(p *particle, names []string, starts map[int]bool) map[int]bool {
	switch p.occ {
	case '?':
		return union(starts, matchOnce(p, names, starts))
	case '*':
		return closure(p, names, starts, true)
	case '+':
		return closure(p, names, starts, false)
	default:
		return matchOnce(p, names, starts)
	}
}

// matchOnce matches exactly one instance of the particle (ignoring its own
// occurrence indicator, which matchOcc has already handled).
func matchOnce(p *particle, names []string, starts map[int]bool) map[int]bool {
	if p.name != "" {
		ends := map[int]bool{}
		for s := range starts {
			if s < len(names) && names[s] == p.name {
				ends[s+1] = true
			}
		}
		return ends
	}
	if p.choice {
		ends := map[int]bool{}
		for _, c := range p.children {
			for e := range matchOcc(c, names, starts) {
				ends[e] = true
			}
		}
		return ends
	}
	cur := starts
	for _, c := range p.children {
		cur = matchOcc(c, names, cur)
		if len(cur) == 0 {
			break
		}
	}
	return cur
}

// closure matches one-or-more (zeroOK=false) or zero-or-more (zeroOK=true)
// repetitions of the particle, accumulating every reachable position.
func closure(p *particle, names []string, starts map[int]bool, zeroOK bool) map[int]bool {
	reached := map[int]bool{}
	if zeroOK {
		for s := range starts {
			reached[s] = true
		}
	}
	frontier := starts
	for len(frontier) > 0 {
		next := matchOnce(p, names, frontier)
		newFront := map[int]bool{}
		for e := range next {
			if !reached[e] {
				reached[e] = true
				newFront[e] = true
			}
		}
		frontier = newFront
	}
	return reached
}

func union(a, b map[int]bool) map[int]bool {
	out := map[int]bool{}
	for k := range a {
		out[k] = true
	}
	for k := range b {
		out[k] = true
	}
	return out
}

// --- small lexical helpers (XML S, Name, Nmtoken) over the generated table ---

var nameMatcher = xmlpb.Lexical["Name"]

func isXMLName(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if i == 0 {
			if !inLexRanges(r, nameMatcher.Ranges) {
				return false
			}
			continue
		}
		if !isNameChar(r) {
			return false
		}
	}
	return true
}

func isXMLNmtoken(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !isNameChar(r) {
			return false
		}
	}
	return true
}

func isNameChar(r rune) bool {
	return inLexRanges(r, nameMatcher.Ranges) || inLexRanges(r, nameMatcher.Rest)
}

func inLexRanges(r rune, rs []lex.Range) bool {
	for _, x := range rs {
		if r >= x.Lo && r <= x.Hi {
			return true
		}
	}
	return false
}

// onlyXMLSpace reports whether s consists solely of XML white space (S):
// space, tab, CR, LF. Note this is stricter than Unicode whitespace — NEL
// (#x85) and LSEP (#x2028) are NOT XML space, so they count as character data.
func onlyXMLSpace(s string) bool {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case ' ', '\t', '\r', '\n':
		default:
			return false
		}
	}
	return true
}

// splitSpaces splits a tokenized attribute value into its tokens on XML white
// space (S: space, tab, CR, LF). Literal white space normalizes to a space
// before we see it, but a character-referenced separator (e.g. &#xD;) survives
// verbatim — and CR/LF/tab ARE valid token separators, so they must split here
// too. A non-S character like a referenced NEL (#x85) or LSEP (#x2028) stays
// inside its token, which is exactly what makes it fail an NMTOKEN check.
func splitSpaces(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\r' || r == '\n'
	})
}

func collapseSpaces(s string) string { return strings.Join(splitSpaces(s), " ") }

// normForType normalizes a value for comparison against a #FIXED default:
// tokenized types collapse white space, CDATA compares verbatim.
func normForType(typ, s string) string {
	if typ == "CDATA" {
		return s
	}
	return collapseSpaces(s)
}

func contains(set []string, s string) bool {
	for _, x := range set {
		if x == s {
			return true
		}
	}
	return false
}
