package service

// generate.go — the inverse of the parse pipeline: serialize the generic XML
// AST (xml.proto's Xml/Tag tree) back to a document. It is the lossless
// direction Process can't be — a *format's* typed projection is a read-only
// view that may drop unmodeled markup, but the generic AST keeps every element,
// attribute, namespace, and character datum in order, so generating from it is
// faithful: parse(Generate(parse(b))) == parse(b).
//
// No schema and no reflection are needed for the element tree (Tag/Attribute/
// ContentItem are concrete) — it is a plain recursive walk plus XML escaping.
//
// The DOCTYPE is preserved: the projector stores the verbatim DOCTYPE body in
// Xml.Doctype (project.go), and Generate re-emits "<!DOCTYPE" + body + ">", so a
// document round-trips with its DOCTYPE intact. The body is currently carried
// whole in Doctype.Name (a preservation shim); unparseCST walks it reflectively,
// so this same path already serves a fully-structured dtd.Doctype CST proto if
// the projector is ever taught to build one.

import (
	"fmt"
	"strconv"
	"strings"

	"google.golang.org/protobuf/reflect/protoreflect"

	dtdpb "github.com/accretional/xmile/proto/pb/dtd"
	xmlpb "github.com/accretional/xmile/proto/pb/xml"
)

// Generate serializes a parsed XML document back to bytes. It is the inverse of
// Parse on the AST it produces: the result re-parses to an equal tree (an
// infoset-equivalent round-trip, not necessarily byte-identical — entity
// spelling, quote style, and insignificant whitespace are not recorded by the
// parser and so are not reproduced).
func Generate(x *xmlpb.Xml) ([]byte, error) {
	if x == nil {
		return nil, fmt.Errorf("generate: nil document")
	}
	root := x.GetRoot()
	if root == nil {
		return nil, fmt.Errorf("generate: document has no root element")
	}

	var b strings.Builder
	if d := x.GetXmlDecl(); d != nil {
		writeXMLDecl(&b, d)
	}
	for _, m := range x.GetPrologMisc() {
		writeMisc(&b, m)
	}
	// Re-emit the DOCTYPE. The "<!DOCTYPE" open and ">" close come from
	// lang/xml.ebnf's doctypedecl (not the dtd grammar), so they are not in the
	// dtd proto's MessagePrefix and must be written here; unparseCST emits the
	// body (Doctype.Name, the verbatim DOCTYPE body) in between.
	if dt := x.GetDoctype(); dt != nil {
		b.WriteString("<!DOCTYPE")
		unparseCST(dt.ProtoReflect(), &b)
		b.WriteByte('>')
	}
	writeTag(&b, root)
	for _, m := range x.GetEpilogMisc() {
		writeMisc(&b, m)
	}
	return []byte(b.String()), nil
}

func writeXMLDecl(b *strings.Builder, d *xmlpb.XmlDecl) {
	b.WriteString(`<?xml version="`)
	b.WriteString(d.GetVersion())
	b.WriteByte('"')
	// The parser decodes the source to a UTF-8 string and Generate emits UTF-8,
	// so the declaration must say UTF-8 regardless of the source's original
	// encoding. Emitted only when the source declared an encoding at all.
	if d.GetEncoding() != "" {
		b.WriteString(` encoding="UTF-8"`)
	}
	if s := d.GetStandalone(); s != "" {
		b.WriteString(` standalone="`)
		b.WriteString(s)
		b.WriteByte('"')
	}
	b.WriteString("?>")
}

func writeMisc(b *strings.Builder, m *xmlpb.Misc) {
	switch it := m.GetItem().(type) {
	case *xmlpb.Misc_Comment:
		b.WriteString("<!--")
		b.WriteString(it.Comment)
		b.WriteString("-->")
	case *xmlpb.Misc_Pi:
		writePI(b, it.Pi)
	}
}

func writePI(b *strings.Builder, pi *xmlpb.PI) {
	b.WriteString("<?")
	b.WriteString(pi.GetTarget())
	if d := pi.GetData(); d != "" {
		b.WriteByte(' ')
		b.WriteString(d)
	}
	b.WriteString("?>")
}

// writeTag serializes an element and its content. An element with no content is
// written as an empty-element tag (<a/>); the parser reads <a/> and <a></a> to
// the same Tag, so the choice is immaterial to the round-trip.
func writeTag(b *strings.Builder, t *xmlpb.Tag) {
	name := t.GetName()
	b.WriteByte('<')
	b.WriteString(name)
	for _, a := range t.GetAttrs() {
		b.WriteByte(' ')
		b.WriteString(a.GetName())
		b.WriteString(`="`)
		writeAttrValue(b, a.GetValue())
		b.WriteByte('"')
	}
	contents := t.GetContents()
	if len(contents) == 0 {
		b.WriteString("/>")
		return
	}
	b.WriteByte('>')
	for _, ci := range contents {
		switch it := ci.GetItem().(type) {
		case *xmlpb.ContentItem_Text:
			writeText(b, it.Text)
		case *xmlpb.ContentItem_Cdata:
			// A CDATA section's text cannot contain "]]>", so it needs no escaping.
			b.WriteString("<![CDATA[")
			b.WriteString(it.Cdata)
			b.WriteString("]]>")
		case *xmlpb.ContentItem_Comment:
			b.WriteString("<!--")
			b.WriteString(it.Comment)
			b.WriteString("-->")
		case *xmlpb.ContentItem_Pi:
			writePI(b, it.Pi)
		case *xmlpb.ContentItem_Child:
			writeTag(b, it.Child)
		}
	}
	b.WriteString("</")
	b.WriteString(name)
	b.WriteByte('>')
}

// writeText escapes character data: the markup delimiters & < >, every C0
// control other than tab and newline, and the restricted characters as numeric
// references. A control such as form-feed is legal in content only via a
// reference (the parser resolved one to the raw character); CR, NEL and the line
// separator must be references so the parser's line-end normalization (which
// folds them to a newline) does not alter them on re-parse.
func writeText(b *strings.Builder, s string) {
	for _, r := range s {
		switch {
		case r == '&':
			b.WriteString("&amp;")
		case r == '<':
			b.WriteString("&lt;")
		case r == '>':
			b.WriteString("&gt;")
		case r == '\t' || r == '\n':
			b.WriteRune(r) // legal raw in character data, not line-end-normalized
		case r < 0x20 || isRestricted(r):
			writeCharRef(b, r)
		default:
			b.WriteRune(r)
		}
	}
}

// writeAttrValue escapes an attribute value for a double-quoted literal: &, <,
// the delimiter ", every C0 control (tab/newline/CR included), and the restricted
// characters as numeric references. The parser normalizes literal whitespace in
// an attribute value to spaces, so a value that held a tab/newline/CR round-trips
// unchanged only when emitted as a reference (which normalization leaves alone).
func writeAttrValue(b *strings.Builder, s string) {
	for _, r := range s {
		switch {
		case r == '&':
			b.WriteString("&amp;")
		case r == '<':
			b.WriteString("&lt;")
		case r == '"':
			b.WriteString("&quot;")
		case r < 0x20 || isRestricted(r):
			writeCharRef(b, r)
		default:
			b.WriteRune(r)
		}
	}
}

// isRestricted reports whether a character must be written as a numeric
// reference regardless of context: DEL and the C1 controls (forbidden raw in
// XML 1.1, where the parser resolved a reference to one) and the line separator
// U+2028 (a line-end the parser folds to a newline if it appears literally).
func isRestricted(r rune) bool {
	return (r >= 0x7F && r <= 0x9F) || r == 0x2028
}

func writeCharRef(b *strings.Builder, r rune) {
	b.WriteString("&#")
	b.WriteString(strconv.Itoa(int(r)))
	b.WriteByte(';')
}

// unparseCST re-emits a DTD concrete-syntax node: the terminal tokens
// genproto's StripKeywords pulled into dtdpb.MessagePrefix (a message's leading
// keywords, e.g. "<!ELEMENT", ">"), then each set field in field order — string
// leaves verbatim (whitespace lives in OptS/S.s) and message fields recursively.
// Because the tree preserves every token and inter-token whitespace, this
// would reconstruct the internal subset exactly once Xml.Doctype is populated.
// It is currently unreachable (Doctype is always nil — see the package comment).
func unparseCST(m protoreflect.Message, b *strings.Builder) {
	for _, tok := range dtdpb.MessagePrefix["."+string(m.Descriptor().FullName())] {
		b.WriteString(tok)
	}
	fields := m.Descriptor().Fields()
	for i := 0; i < fields.Len(); i++ {
		f := fields.Get(i)
		if !m.Has(f) {
			continue
		}
		if f.IsList() {
			l := m.Get(f).List()
			for j := 0; j < l.Len(); j++ {
				writeCSTValue(f, l.Get(j), b)
			}
			continue
		}
		writeCSTValue(f, m.Get(f), b)
	}
}

func writeCSTValue(f protoreflect.FieldDescriptor, v protoreflect.Value, b *strings.Builder) {
	switch f.Kind() {
	case protoreflect.StringKind:
		b.WriteString(v.String())
	case protoreflect.MessageKind:
		unparseCST(v.Message(), b)
	}
}
