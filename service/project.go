package service

import (
	"strconv"
	"strings"

	pb "github.com/accretional/gluon/v2/pb"

	xmlpb "github.com/accretional/xmile/proto/pb/xml"
)

// projectDocument projects a well-formed CST into the xml.proto AST. It
// references grammar rule names (the CST node kinds) but encodes no grammar
// rules — those live in lang/xml.ebnf.
func projectDocument(root *pb.ASTNode) *xmlpb.Document {
	doc := &xmlpb.Document{}
	if prolog := directChild(root, "prolog"); prolog != nil {
		doc.XmlDecl = projectXMLDecl(prolog)
		doc.PrologMisc = projectMiscs(prolog)
		// TODO(dtd): parse the dtd_text span into doc.Doctype (dtd.Doctype).
	}
	if el := directChild(root, "element"); el != nil {
		doc.Root = projectTag(el)
	}
	if m := directChild(root, "miscs"); m != nil {
		doc.EpilogMisc = projectMiscs(m)
	}
	return doc
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

func projectTag(el *pb.ASTNode) *xmlpb.Tag {
	if empty := directChild(el, "EmptyElemTag"); empty != nil {
		return &xmlpb.Tag{Name: childName(empty), Attrs: projectAttrs(empty)}
	}
	stag := directChild(el, "STag")
	tag := &xmlpb.Tag{Name: childName(stag), Attrs: projectAttrs(stag)}
	if content := directChild(el, "content"); content != nil {
		tag.Contents = projectContent(content)
	}
	return tag
}

func projectAttrs(tag *pb.ASTNode) []*xmlpb.Attribute {
	var out []*xmlpb.Attribute
	for _, a := range descendants(tag, "Attribute") {
		out = append(out, &xmlpb.Attribute{Name: childName(a), Value: attrValue(a)})
	}
	return out
}

func attrValue(a *pb.ASTNode) string {
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
			b.WriteString(resolveRef(x))
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

func projectContent(content *pb.ASTNode) []*xmlpb.ContentItem {
	var out []*xmlpb.ContentItem
	var rec func(x *pb.ASTNode)
	rec = func(x *pb.ASTNode) {
		switch x.GetKind() {
		case "CharData":
			out = append(out, textItem(x.GetValue()))
			return
		case "Reference", "EntityRef", "CharRef":
			out = append(out, textItem(resolveRef(x)))
			return
		case "element":
			out = append(out, &xmlpb.ContentItem{Item: &xmlpb.ContentItem_Child{Child: projectTag(x)}})
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

// resolveRef resolves an entity or character reference to its text. Without
// a DTD, only the five predefined entities resolve; an unknown general
// entity is left as its literal "&name;" (it remains a structurally
// well-formed reference).
func resolveRef(ref *pb.ASTNode) string {
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
