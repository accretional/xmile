package service

import (
	"strconv"
	"strings"

	pb "github.com/accretional/gluon/v2/pb"

	xmlpb "github.com/accretional/xmile/proto/pb/xml"
)

// projector projects a CST into the AST, resolving declared general entities:
// text-only ones to their replacement text (in attribute values), and ones
// whose replacement contains markup to the element/content they expand to (in
// element content).
type projector struct {
	p          *Parser
	info       *dtdInfo
	is11       bool
	ents       map[string]string                // entity name -> resolved text (attr values)
	entContent map[string][]*xmlpb.ContentItem  // entity name -> expanded content items (memo)
	expanding  map[string]bool                  // recursion guard for entity expansion
}

// projectDocument projects a well-formed CST into the xml.proto AST. It
// references grammar rule names (the CST node kinds) but encodes no grammar
// rules — those live in lang/xml.ebnf.
func projectDocument(p *Parser, root *pb.ASTNode, info *dtdInfo, is11 bool) *xmlpb.Document {
	pr := &projector{
		p:          p,
		info:       info,
		is11:       is11,
		ents:       resolvedTextEntities(info),
		entContent: map[string][]*xmlpb.ContentItem{},
		expanding:  map[string]bool{},
	}
	doc := &xmlpb.Document{}
	if prolog := directChild(root, "prolog"); prolog != nil {
		doc.XmlDecl = projectXMLDecl(prolog)
		doc.PrologMisc = projectMiscs(prolog)
		// TODO(dtd): parse the dtd_text span into doc.Doctype (dtd.Doctype).
	}
	if el := directChild(root, "element"); el != nil {
		doc.Root = pr.tag(el)
	}
	if m := directChild(root, "miscs"); m != nil {
		doc.EpilogMisc = projectMiscs(m)
	}
	return doc
}

// resolvedTextEntities returns each declared internal general entity whose
// replacement is character data only (no element markup), resolved to text.
func resolvedTextEntities(info *dtdInfo) map[string]string {
	out := map[string]string{}
	if info == nil {
		return out
	}
	for name, ent := range info.general {
		if ent.external {
			continue
		}
		expanded, err := info.expandValue(name, map[string]bool{})
		if err != nil {
			continue
		}
		if text, ok := entityText(expanded); ok {
			out[name] = text
		}
	}
	return out
}

// entityText resolves predefined entities and character references in an
// already-expanded replacement to literal text, reporting ok=false if it
// contains element markup (a literal '<').
func entityText(s string) (string, bool) {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '<':
			return "", false
		case s[i] == '&':
			if i+1 < len(s) && s[i+1] == '#' {
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
			if j < len(s) && s[j] == ';' {
				switch s[i+1 : j] {
				case "amp":
					b.WriteByte('&')
				case "lt":
					b.WriteByte('<')
				case "gt":
					b.WriteByte('>')
				case "quot":
					b.WriteByte('"')
				case "apos":
					b.WriteByte('\'')
				default:
					b.WriteString(s[i : j+1])
				}
				i = j
				continue
			}
			b.WriteByte('&')
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String(), true
}

func projectXMLDecl(prolog *pb.ASTNode) *xmlpb.XmlDecl {
	decl := firstDescendant(prolog, "XMLDecl")
	if decl == nil {
		return nil
	}
	d := &xmlpb.XmlDecl{}
	if v := firstDescendant(decl, "VersionNum"); v != nil {
		d.Version = leafText(v)
	}
	if e := firstDescendant(decl, "EncName"); e != nil {
		d.Encoding = leafText(e)
	}
	if sd := firstDescendant(decl, "YesNo"); sd != nil {
		d.Standalone = leafText(sd)
	}
	return d
}

func projectMiscs(n *pb.ASTNode) []*xmlpb.Misc {
	var out []*xmlpb.Misc
	var rec func(x *pb.ASTNode)
	rec = func(x *pb.ASTNode) {
		switch x.GetKind() {
		case "Comment":
			out = append(out, &xmlpb.Misc{Item: &xmlpb.Misc_Comment{Comment: tokenText(x, "comment_text")}})
			return
		case "PI":
			out = append(out, &xmlpb.Misc{Item: &xmlpb.Misc_Pi{Pi: projectPI(x)}})
			return
		case "element":
			return // never descend into the root element
		}
		for _, c := range x.GetChildren() {
			rec(c)
		}
	}
	rec(n)
	return out
}

func (pr *projector) tag(el *pb.ASTNode) *xmlpb.Tag {
	if empty := directChild(el, "EmptyElemTag"); empty != nil {
		return &xmlpb.Tag{Name: childName(empty), Attrs: pr.attrs(empty)}
	}
	stag := directChild(el, "STag")
	tag := &xmlpb.Tag{Name: childName(stag), Attrs: pr.attrs(stag)}
	if content := directChild(el, "content"); content != nil {
		tag.Contents = pr.content(content)
	}
	return tag
}

func (pr *projector) attrs(tag *pb.ASTNode) []*xmlpb.Attribute {
	var out []*xmlpb.Attribute
	for _, a := range descendants(tag, "Attribute") {
		out = append(out, &xmlpb.Attribute{Name: childName(a), Value: pr.attrValue(a)})
	}
	return out
}

func (pr *projector) attrValue(a *pb.ASTNode) string {
	av := directChild(a, "AttValue")
	if av == nil {
		return ""
	}
	val := directChild(av, "dqValue")
	if val == nil {
		val = directChild(av, "sqValue")
	}
	if val == nil {
		return ""
	}
	var b strings.Builder
	var rec func(x *pb.ASTNode)
	rec = func(x *pb.ASTNode) {
		switch x.GetKind() {
		case "dqText", "sqText":
			b.WriteString(normalizeAttrWS(x.GetValue()))
			return
		case "Reference", "EntityRef", "CharRef":
			b.WriteString(pr.resolveRef(x))
			return
		}
		for _, c := range x.GetChildren() {
			rec(c)
		}
	}
	rec(val)
	return b.String()
}

// normalizeAttrWS applies CDATA attribute-value normalization: each literal
// whitespace character becomes a space.
func normalizeAttrWS(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\t' || r == '\n' || r == '\r' {
			return ' '
		}
		return r
	}, s)
}

func (pr *projector) content(content *pb.ASTNode) []*xmlpb.ContentItem {
	var out []*xmlpb.ContentItem
	var rec func(x *pb.ASTNode)
	rec = func(x *pb.ASTNode) {
		switch x.GetKind() {
		case "CharData":
			out = append(out, textItem(x.GetValue()))
			return
		case "Reference", "EntityRef", "CharRef":
			out = append(out, pr.refContent(x)...)
			return
		case "element":
			out = append(out, &xmlpb.ContentItem{Item: &xmlpb.ContentItem_Child{Child: pr.tag(x)}})
			return
		case "CDSect":
			out = append(out, &xmlpb.ContentItem{Item: &xmlpb.ContentItem_Cdata{Cdata: tokenText(x, "cdata_text")}})
			return
		case "Comment":
			out = append(out, &xmlpb.ContentItem{Item: &xmlpb.ContentItem_Comment{Comment: tokenText(x, "comment_text")}})
			return
		case "PI":
			out = append(out, &xmlpb.ContentItem{Item: &xmlpb.ContentItem_Pi{Pi: projectPI(x)}})
			return
		}
		for _, c := range x.GetChildren() {
			rec(c)
		}
	}
	for _, c := range content.GetChildren() {
		rec(c)
	}
	return out
}

func projectPI(pi *pb.ASTNode) *xmlpb.PI {
	p := &xmlpb.PI{}
	if n := firstDescendant(pi, "Name"); n != nil {
		p.Target = n.GetValue()
	}
	p.Data = strings.TrimLeft(tokenText(pi, "pi_text"), " \t\r\n")
	return p
}

// resolveRef resolves an entity or character reference to its text: the five
// predefined entities, then declared text-only general entities (resolved
// during projection). A general entity whose replacement is markup, or one
// that is undeclared, is left as its literal "&name;".
func (pr *projector) resolveRef(ref *pb.ASTNode) string {
	if er := firstDescendant(ref, "EntityRef"); er != nil {
		name := ""
		if n := firstDescendant(er, "Name"); n != nil {
			name = n.GetValue()
		}
		switch name {
		case "amp":
			return "&"
		case "lt":
			return "<"
		case "gt":
			return ">"
		case "quot":
			return "\""
		case "apos":
			return "'"
		}
		if text, ok := pr.ents[name]; ok {
			return text
		}
		return "&" + name + ";"
	}
	if cr := firstDescendant(ref, "CharRef"); cr != nil {
		t := strings.TrimSuffix(strings.TrimPrefix(leafText(cr), "&#"), ";")
		var n int64
		if strings.HasPrefix(t, "x") || strings.HasPrefix(t, "X") {
			n, _ = strconv.ParseInt(t[1:], 16, 32)
		} else {
			n, _ = strconv.ParseInt(t, 10, 32)
		}
		return string(rune(n))
	}
	return ""
}

// refContent projects an entity or character reference appearing in element
// content into the content items it stands for. A character reference and the
// five predefined entities become text; a declared internal general entity
// expands to the content of its replacement (which may be markup — elements,
// CDATA, nested references); an undeclared or external entity is left as its
// literal "&name;".
func (pr *projector) refContent(ref *pb.ASTNode) []*xmlpb.ContentItem {
	if er := firstDescendant(ref, "EntityRef"); er != nil {
		name := ""
		if n := firstDescendant(er, "Name"); n != nil {
			name = n.GetValue()
		}
		switch name {
		case "amp":
			return []*xmlpb.ContentItem{textItem("&")}
		case "lt":
			return []*xmlpb.ContentItem{textItem("<")}
		case "gt":
			return []*xmlpb.ContentItem{textItem(">")}
		case "quot":
			return []*xmlpb.ContentItem{textItem("\"")}
		case "apos":
			return []*xmlpb.ContentItem{textItem("'")}
		}
		if ent, ok := pr.info.general[name]; ok && !ent.external {
			return pr.entityContentItems(name)
		}
		return []*xmlpb.ContentItem{textItem("&" + name + ";")}
	}
	return []*xmlpb.ContentItem{textItem(pr.resolveRef(ref))}
}

// entityContentItems expands a declared internal general entity into the
// content items it contributes. The replacement text is fully expanded (the
// same expansion the entity well-formedness check validated), reparsed inside
// a synthetic wrapper element, and projected. Results are memoized per entity.
func (pr *projector) entityContentItems(name string) []*xmlpb.ContentItem {
	if items, ok := pr.entContent[name]; ok {
		return items
	}
	if pr.expanding[name] { // guarded; recursion is already a WF error
		return nil
	}
	expanded, err := pr.info.expandValue(name, map[string]bool{})
	if err != nil {
		return []*xmlpb.ContentItem{textItem("&" + name + ";")}
	}
	pr.expanding[name] = true
	defer delete(pr.expanding, name)

	cst, perr := pr.p.ParseCST("<xmilewrap>" + expanded + "</xmilewrap>")
	if perr != nil {
		return []*xmlpb.ContentItem{textItem("&" + name + ";")}
	}
	var items []*xmlpb.ContentItem
	if wrap := directChild(cst.GetRoot(), "element"); wrap != nil {
		if content := directChild(wrap, "content"); content != nil {
			items = pr.content(content)
		}
	}
	pr.entContent[name] = items
	return items
}

func textItem(s string) *xmlpb.ContentItem {
	return &xmlpb.ContentItem{Item: &xmlpb.ContentItem_Text{Text: s}}
}

func childName(n *pb.ASTNode) string {
	if c := directChild(n, "Name"); c != nil {
		return c.GetValue()
	}
	return ""
}

// tokenText returns the value of the first leaf of the given token kind in
// n's subtree (e.g. "comment_text", "cdata_text", "pi_text").
func tokenText(n *pb.ASTNode, kind string) string {
	if t := firstDescendant(n, kind); t != nil {
		return t.GetValue()
	}
	return ""
}
