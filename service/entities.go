package service

import (
	"strconv"
	"strings"

	pb "github.com/accretional/gluon/v2/pb"

	xmlpb "github.com/accretional/xmile/proto/pb/xml"
)

// charRefText resolves a "&#...;" character reference to its literal text.
// An out-of-range or malformed value yields NUL, which the reparse rejects
// as an illegal character.
func charRefText(s string) string {
	t := strings.TrimSuffix(strings.TrimPrefix(s, "&#"), ";")
	if t == "" {
		return "\x00"
	}
	var v int64
	var err error
	if t[0] == 'x' || t[0] == 'X' {
		v, err = strconv.ParseInt(t[1:], 16, 64)
	} else {
		v, err = strconv.ParseInt(t, 10, 64)
	}
	if err != nil || v < 0 || v > 0x10FFFF {
		return "\x00"
	}
	return string(rune(v))
}

// builtinEntities are the five predefined entities, always available.
var builtinEntities = map[string]bool{"amp": true, "lt": true, "gt": true, "quot": true, "apos": true}

type entityInfo struct {
	external bool   // declared with SYSTEM/PUBLIC
	unparsed bool   // declared with NDATA
	value    string // internal replacement text (for expansion checks)
}

// dtdInfo summarizes the parts of a parsed DTD needed for the entity
// well-formedness constraints.
type dtdInfo struct {
	general     map[string]entityInfo
	hasPERef    bool // internal subset references a parameter entity
	hasExternal bool // DOCTYPE names an external subset
}

// buildDTDInfo extracts the entity declarations and structural flags from a
// parsed DTD CST (the doctype rule).
func buildDTDInfo(dtdRoot *pb.ASTNode) *dtdInfo {
	info := &dtdInfo{general: map[string]entityInfo{}}
	if dtdRoot == nil {
		return info
	}
	if ext := directChild(dtdRoot, "externalID"); ext != nil && firstDescendant(ext, "extID") != nil {
		info.hasExternal = true
	}
	if len(descendants(dtdRoot, "PEReference")) > 0 {
		info.hasPERef = true
	}
	for _, ed := range descendants(dtdRoot, "entityDecl") {
		ek := firstDescendant(ed, "entityKind")
		if ek == nil {
			continue
		}
		if firstLeaf(ek) == "%" { // parameter-entity declaration
			info.hasPERef = true
			continue
		}
		name := ""
		if n := firstDescendant(ek, "Name"); n != nil {
			name = n.GetValue()
		}
		if name == "" {
			continue
		}
		ent := entityInfo{}
		if ev := firstDescendant(ek, "entityValue"); ev != nil {
			ent.external = firstDescendant(ev, "extID") != nil
			ent.unparsed = hasTerminal(ev, "NDATA")
			if lit := firstDescendant(ev, "litDq"); lit != nil {
				ent.value = lit.GetValue()
			} else if lit := firstDescendant(ev, "litSq"); lit != nil {
				ent.value = lit.GetValue()
			}
		}
		// The first declaration of an entity is binding; ignore re-declarations.
		if _, exists := info.general[name]; !exists {
			info.general[name] = ent
		}
	}
	return info
}

// checkEntities enforces the entity well-formedness constraints over the
// document body: Entity Declared, Parsed Entity (no unparsed-entity
// reference), No External Entity References in attribute values, no
// recursion, and well-formed replacement text for entities used in content.
func (p *Parser) checkEntities(root *pb.ASTNode, info *dtdInfo) error {
	contentRefs := map[string]bool{}
	if err := walkEnt(root, info, false, contentRefs); err != nil {
		return err
	}
	// Replacement text of an internal entity referenced in content must
	// expand to well-formed content. Expand it (char references become
	// literal characters; predefined entities stay references) and reparse
	// it inside a synthetic wrapper element.
	for name := range contentRefs {
		if ent := info.general[name]; ent.external {
			continue
		}
		expanded, err := info.expandValue(name, map[string]bool{})
		if err != nil {
			return err
		}
		if _, perr := p.Parse("<xmilewrap>" + expanded + "</xmilewrap>"); perr != nil {
			return &WFError{Msg: "entity " + name + " replacement is not well-formed: " + perr.Error()}
		}
	}
	return nil
}

func walkEnt(n *pb.ASTNode, info *dtdInfo, inAttr bool, contentRefs map[string]bool) error {
	if n == nil {
		return nil
	}
	if n.GetKind() == "AttValue" {
		inAttr = true
	}
	if n.GetKind() == "EntityRef" {
		name := ""
		if nm := firstDescendant(n, "Name"); nm != nil {
			name = nm.GetValue()
		}
		if !builtinEntities[name] {
			ent, declared := info.general[name]
			switch {
			case !declared:
				// Undeclared is fatal only when no external subset and no
				// parameter-entity reference could supply the declaration.
				if !info.hasExternal && !info.hasPERef {
					return &WFError{Msg: "reference to undeclared entity " + name, Offset: n.GetLocation().GetOffset()}
				}
			case ent.unparsed:
				return &WFError{Msg: "reference to unparsed entity " + name, Offset: n.GetLocation().GetOffset()}
			case inAttr && info.externalInChain(name, map[string]bool{}):
				return &WFError{Msg: "external entity reference in attribute value", Offset: n.GetLocation().GetOffset()}
			case !ent.external:
				if !inAttr {
					contentRefs[name] = true
				}
				// Transitively validate the replacement text: no recursion,
				// no undeclared/unparsed sub-entity.
				if err := expandCheck(name, info, map[string]bool{}); err != nil {
					if wf, ok := err.(*WFError); ok && wf.Offset == 0 {
						wf.Offset = n.GetLocation().GetOffset()
					}
					return err
				}
			}
		}
	}
	for _, c := range n.GetChildren() {
		if err := walkEnt(c, info, inAttr, contentRefs); err != nil {
			return err
		}
	}
	return nil
}

// externalInChain reports whether name, or any entity reachable from its
// replacement text, is an external entity.
func (info *dtdInfo) externalInChain(name string, visiting map[string]bool) bool {
	if visiting[name] {
		return false
	}
	ent, ok := info.general[name]
	if !ok {
		return false
	}
	if ent.external {
		return true
	}
	visiting[name] = true
	defer delete(visiting, name)
	for _, r := range entityRefsIn(ent.value) {
		if !builtinEntities[r] && info.externalInChain(r, visiting) {
			return true
		}
	}
	return false
}

// checkDTDRefs validates references that occur inside the DTD itself:
// character references in entity values must be legal, and entity
// references in ATTLIST default values must resolve to declared, internal,
// non-recursive entities.
func checkDTDRefs(dtdRoot *pb.ASTNode, info *dtdInfo) error {
	if dtdRoot == nil {
		return nil
	}
	for _, ed := range descendants(dtdRoot, "entityDecl") {
		if err := checkValueCharRefs(litValue(ed)); err != nil {
			return err
		}
	}
	for _, dd := range descendants(dtdRoot, "defaultDecl") {
		val := litValue(dd)
		if err := checkValueCharRefs(val); err != nil {
			return err
		}
		for _, r := range entityRefsIn(val) {
			if builtinEntities[r] {
				continue
			}
			ent, ok := info.general[r]
			switch {
			case !ok:
				if !info.hasExternal && !info.hasPERef {
					return &WFError{Msg: "reference to undeclared entity " + r}
				}
			case ent.external:
				return &WFError{Msg: "external entity reference in attribute default"}
			default:
				if err := expandCheck(r, info, map[string]bool{}); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// litValue returns the literal text (litDq/litSq) within n.
func litValue(n *pb.ASTNode) string {
	if l := firstDescendant(n, "litDq"); l != nil {
		return l.GetValue()
	}
	if l := firstDescendant(n, "litSq"); l != nil {
		return l.GetValue()
	}
	return ""
}

// checkValueCharRefs validates every character reference embedded in literal
// text (character references in entity/attribute values are resolved at
// declaration, so an illegal value is fatal there).
func checkValueCharRefs(s string) error {
	for i := 0; i+1 < len(s); i++ {
		if s[i] != '&' || s[i+1] != '#' {
			continue
		}
		j := i + 2
		for j < len(s) && s[j] != ';' {
			j++
		}
		if j >= len(s) {
			break
		}
		for _, r := range charRefText(s[i : j+1]) {
			if !xmlpb.Lexical["Char"].Contains(r) {
				return &WFError{Msg: "character reference to an illegal character"}
			}
		}
		i = j
	}
	return nil
}

// expandValue fully expands an internal entity's replacement text for the
// well-formedness reparse: character references become literal characters,
// predefined entities are left as references (they denote data), and
// general-entity references are expanded recursively. Recursion is fatal.
func (info *dtdInfo) expandValue(name string, visiting map[string]bool) (string, error) {
	if visiting[name] {
		return "", &WFError{Msg: "recursive reference to entity " + name}
	}
	ent, ok := info.general[name]
	if !ok || ent.external {
		return "", nil
	}
	visiting[name] = true
	defer delete(visiting, name)
	return info.expandText(ent.value, visiting)
}

func (info *dtdInfo) expandText(s string, visiting map[string]bool) (string, error) {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if strings.HasPrefix(s[i:], "<![CDATA[") {
			if j := strings.Index(s[i+9:], "]]>"); j >= 0 {
				b.WriteString(s[i : i+9+j+3])
				i = i + 9 + j + 2
				continue
			}
			return "", &WFError{Msg: "unterminated CDATA in entity value"}
		}
		if strings.HasPrefix(s[i:], "<!--") {
			if j := strings.Index(s[i+4:], "-->"); j >= 0 {
				b.WriteString(s[i : i+4+j+3])
				i = i + 4 + j + 2
				continue
			}
			return "", &WFError{Msg: "unterminated comment in entity value"}
		}
		if s[i] != '&' {
			b.WriteByte(s[i])
			continue
		}
		if i+1 < len(s) && s[i+1] == '#' { // character reference -> literal char
			j := i + 2
			for j < len(s) && s[j] != ';' {
				j++
			}
			if j >= len(s) {
				b.WriteByte('&')
				continue
			}
			b.WriteString(charRefText(s[i : j+1]))
			i = j
			continue
		}
		j := i + 1
		for j < len(s) && isNameByte(s[j]) {
			j++
		}
		if j < len(s) && s[j] == ';' && j > i+1 {
			ref := s[i+1 : j]
			if builtinEntities[ref] {
				b.WriteString(s[i : j+1]) // predefined: keep as reference
			} else {
				sub, err := info.expandValue(ref, visiting)
				if err != nil {
					return "", err
				}
				b.WriteString(sub)
			}
			i = j
			continue
		}
		b.WriteByte('&')
	}
	return b.String(), nil
}

// expandCheck transitively validates an internal entity's replacement text:
// it detects recursion (a cycle of references) and references to undeclared
// or unparsed sub-entities.
func expandCheck(name string, info *dtdInfo, visiting map[string]bool) error {
	if visiting[name] {
		return &WFError{Msg: "recursive reference to entity " + name}
	}
	ent, ok := info.general[name]
	if !ok || ent.external {
		return nil
	}
	visiting[name] = true
	for _, sub := range entityRefsIn(ent.value) {
		if builtinEntities[sub] {
			continue
		}
		subEnt, declared := info.general[sub]
		switch {
		case !declared:
			if !info.hasExternal && !info.hasPERef {
				return &WFError{Msg: "reference to undeclared entity " + sub}
			}
		case subEnt.unparsed:
			return &WFError{Msg: "reference to unparsed entity " + sub}
		default:
			if err := expandCheck(sub, info, visiting); err != nil {
				return err
			}
		}
	}
	delete(visiting, name)
	return nil
}

// entityRefsIn returns the general-entity names referenced in replacement
// text (skipping character references).
func entityRefsIn(s string) []string {
	var out []string
	for i := 0; i < len(s); i++ {
		// References inside CDATA sections / comments are literal text.
		if strings.HasPrefix(s[i:], "<![CDATA[") {
			if j := strings.Index(s[i+9:], "]]>"); j >= 0 {
				i = i + 9 + j + 2
			} else {
				break
			}
			continue
		}
		if strings.HasPrefix(s[i:], "<!--") {
			if j := strings.Index(s[i+4:], "-->"); j >= 0 {
				i = i + 4 + j + 2
			} else {
				break
			}
			continue
		}
		if s[i] != '&' {
			continue
		}
		if i+1 < len(s) && s[i+1] == '#' { // character reference
			j := i + 2
			for j < len(s) && s[j] != ';' {
				j++
			}
			i = j
			continue
		}
		j := i + 1
		for j < len(s) && isNameByte(s[j]) {
			j++
		}
		if j < len(s) && s[j] == ';' && j > i+1 {
			out = append(out, s[i+1:j])
			i = j
		}
	}
	return out
}

func isNameByte(b byte) bool {
	switch {
	case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9':
		return true
	case b == '_' || b == '-' || b == '.' || b == ':' || b >= 0x80:
		return true
	}
	return false
}

// firstLeaf returns the value of the left-most leaf in n's subtree.
func firstLeaf(n *pb.ASTNode) string {
	if n == nil {
		return ""
	}
	if len(n.GetChildren()) == 0 {
		return n.GetValue()
	}
	for _, c := range n.GetChildren() {
		if s := firstLeaf(c); s != "" {
			return s
		}
	}
	return ""
}

// hasTerminal reports whether n's subtree contains a terminal with value val.
func hasTerminal(n *pb.ASTNode, val string) bool {
	if n == nil {
		return false
	}
	if n.GetKind() == "terminal" && n.GetValue() == val {
		return true
	}
	for _, c := range n.GetChildren() {
		if hasTerminal(c, val) {
			return true
		}
	}
	return false
}
