package service

// rss.go — RSS 2.0 parsing as "generic XML AST + walk against RSS".
//
// A feed is parsed by the universal XML parser into the homogeneous Tag tree
// (Parser.Parse), then walked against the typed rss.proto AST compiled from
// lang/rss.ebnf (CompileGrammar). The walk is namespace-aware: RSS 2.0 core
// elements are unprefixed (the spec puts them in no namespace), so a
// namespace-qualified element/attribute is a tolerated extension and an
// unprefixed unknown one is invalid — the one place the namespace-extensibility
// rule is enforced, since a CFG cannot express it. The remaining
// context-sensitive constraints (version, required children, item title-or-
// description) live in validateRSS, also not the grammar.

import (
	"fmt"
	"strings"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	rsspb "github.com/accretional/xmile/proto/pb/rss"
	xmlpb "github.com/accretional/xmile/proto/pb/xml"
)

// ParseRSS parses RSS 2.0 source: it parses the bytes as XML (well-formedness
// and namespaces), enforces the RSS-2.0 constraints a CFG cannot, and walks the
// resulting Tag tree into the typed rss.Rss AST (proto/pb/rss, generated from
// lang/rss.ebnf). The error is *WFError when the bytes are not well-formed XML,
// or *ValidityError when they are well-formed but not valid RSS 2.0 (wrong
// root/version, missing required children, or an unprefixed out-of-vocabulary
// element). Namespace-qualified extensions are tolerated, never rejected.
func ParseRSS(p *Parser, src string) (*rsspb.Rss, error) {
	doc, err := p.Parse(src, false)
	if err != nil {
		return nil, err // *WFError
	}
	if verr := validateRSS(doc); verr != nil {
		return nil, verr // *ValidityError
	}
	out := &rsspb.Rss{}
	_, unknown := projectRSS(doc.GetRoot(), out.ProtoReflect().Descriptor(), out.ProtoReflect())
	if len(unknown) > 0 {
		return nil, &ValidityError{Msg: fmt.Sprintf("not RSS 2.0: out-of-vocabulary markup %v (extensions must be in a namespace)", dedupStrings(unknown))}
	}
	return out, nil
}

// projectRSS walks a parsed Tag tree against the rss.proto descriptor, filling
// msg. It returns the names of tolerated namespace-qualified extensions
// (foreign) and of unprefixed markup with no matching field (unknown — an RSS
// 2.0 validity error). Attributes fill string fields by name; a leaf's text
// fills its `text` field; child elements recurse into message fields (a lowered
// choice is a repeated wrapper-with-oneof, handled by placeChild).
func projectRSS(tag *xmlpb.Tag, md protoreflect.MessageDescriptor, msg protoreflect.Message) (foreign, unknown []string) {
	for _, a := range tag.GetAttrs() {
		name := a.GetName()
		switch {
		case name == "xmlns" || strings.HasPrefix(name, "xmlns:"):
			continue // a namespace declaration, not data
		case a.GetNamespace().GetNamespaceUri() != "":
			foreign = append(foreign, "@"+name) // namespaced extension attribute
		default:
			if f := scalarField(md, name); f != nil {
				msg.Set(f, protoreflect.ValueOfString(a.GetValue()))
			} else {
				unknown = append(unknown, tag.GetName()+"@"+name)
			}
		}
	}

	// A leaf element (a #PCDATA element such as <title>/<description>) holds
	// character content, which RSS allows to carry inline HTML — entity-encoded
	// or, as some feeds do, raw (<title><a href=…>…</a></title>). That markup is
	// the element's text, not RSS vocabulary, so a leaf's value is all of its
	// descendant character data and we do not recurse into it.
	if tf := textField(md); tf != nil && !hasMessageField(md) {
		if s := gatherText(tag); strings.TrimSpace(s) != "" {
			msg.Set(tf, protoreflect.ValueOfString(s))
		}
		return foreign, unknown
	}

	for _, ci := range tag.GetContents() {
		child := ci.GetChild()
		if child == nil {
			continue // text/cdata between child elements is insignificant here
		}
		if child.GetNamespace().GetNamespaceUri() != "" {
			foreign = append(foreign, child.GetName()) // namespaced extension element — tolerated
			continue
		}
		cd, cm, ok := placeChild(md, msg, child.GetName())
		if !ok {
			unknown = append(unknown, tag.GetName()+">"+child.GetName())
			continue
		}
		f, u := projectRSS(child, cd, cm)
		foreign = append(foreign, f...)
		unknown = append(unknown, u...)
	}
	return foreign, unknown
}

// textField returns md's string `text` field (a leaf's character content), or
// nil. hasMessageField reports whether md has any message-typed field (i.e. it
// nests child elements); a message with a text field and no message fields is a
// #PCDATA leaf.
func textField(md protoreflect.MessageDescriptor) protoreflect.FieldDescriptor {
	if f := md.Fields().ByName("text"); f != nil && f.Kind() == protoreflect.StringKind {
		return f
	}
	return nil
}

func hasMessageField(md protoreflect.MessageDescriptor) bool {
	fs := md.Fields()
	for i := 0; i < fs.Len(); i++ {
		if fs.Get(i).Kind() == protoreflect.MessageKind {
			return true
		}
	}
	return false
}

// gatherText concatenates all descendant character data (text and CDATA) of
// tag, in document order — the textual value of a #PCDATA element, including
// any inline markup folded back to its text.
func gatherText(tag *xmlpb.Tag) string {
	var b strings.Builder
	var walk func(*xmlpb.Tag)
	walk = func(t *xmlpb.Tag) {
		for _, ci := range t.GetContents() {
			switch it := ci.GetItem().(type) {
			case *xmlpb.ContentItem_Text:
				b.WriteString(it.Text)
			case *xmlpb.ContentItem_Cdata:
				b.WriteString(it.Cdata)
			case *xmlpb.ContentItem_Child:
				walk(it.Child)
			}
		}
	}
	walk(tag)
	return b.String()
}

// validateRSS enforces the *structural* RSS-2.0 constraints no CFG can express,
// over the parsed Tag tree: the root is <rss version="2.0"> with exactly one
// <channel>. These determine whether the document is RSS 2.0 at all; a
// violation is a hard *ValidityError. Softer required-content rules (which
// real-world feeds routinely bend) are reported by RSSConformance, not enforced
// here — mirroring how the real-world corpora are reported, not gated.
func validateRSS(doc *xmlpb.Document) error {
	root := doc.GetRoot()
	if root == nil {
		return &ValidityError{Msg: "not RSS: document has no root element"}
	}
	if root.GetName() != "rss" || root.GetNamespace().GetNamespaceUri() != "" {
		return &ValidityError{Msg: fmt.Sprintf("not RSS: root element is <%s>, want <rss>", root.GetName())}
	}
	version := ""
	for _, a := range root.GetAttrs() {
		if a.GetName() == "version" {
			version = a.GetValue()
		}
	}
	if version != "2.0" {
		return &ValidityError{Msg: fmt.Sprintf("not RSS 2.0: rss version=%q, want \"2.0\"", version)}
	}
	if channels := childElems(root, "channel"); len(channels) != 1 {
		return &ValidityError{Msg: fmt.Sprintf("RSS 2.0: <rss> must contain exactly one <channel>, found %d", len(channels))}
	}
	return nil
}

// RSSConformance returns the soft RSS-2.0 conformance warnings for a parsed
// feed — rules the spec states ("required" channel children; an item needs at
// least a title or description) but that real feeds commonly violate. They are
// warnings, not parse failures: a reader still uses the feed. Empty when the
// feed is fully conformant.
func RSSConformance(doc *xmlpb.Document) []string {
	var warn []string
	channels := childElems(doc.GetRoot(), "channel")
	if len(channels) != 1 {
		return warn
	}
	ch := channels[0]
	for _, req := range []string{"title", "link", "description"} {
		if len(childElems(ch, req)) == 0 {
			warn = append(warn, fmt.Sprintf("<channel> is missing the required <%s>", req))
		}
	}
	for i, item := range childElems(ch, "item") {
		if len(childElems(item, "title")) == 0 && len(childElems(item, "description")) == 0 {
			warn = append(warn, fmt.Sprintf("<item> #%d has neither <title> nor <description>", i+1))
		}
	}
	return warn
}

// childElems returns the unprefixed (no-namespace) child elements of tag with
// the given local name — used by validateRSS to test the core vocabulary,
// ignoring namespaced extensions.
func childElems(tag *xmlpb.Tag, name string) []*xmlpb.Tag {
	var out []*xmlpb.Tag
	for _, ci := range tag.GetContents() {
		if c := ci.GetChild(); c != nil && c.GetName() == name && c.GetNamespace().GetNamespaceUri() == "" {
			out = append(out, c)
		}
	}
	return out
}

// RSSItemCount reports the number of channel <item>s projected into a typed
// rss.Rss message: the channel's children are a repeated choice wrapper, so an
// item is a wrapper entry whose oneof selects the `item` variant.
func RSSItemCount(m proto.Message) int {
	rss := m.ProtoReflect()
	cf := rss.Descriptor().Fields().ByName("channel")
	if cf == nil || !rss.Has(cf) {
		return 0
	}
	ch := rss.Get(cf).Message()
	wf := ch.Descriptor().Fields().ByName("alt1")
	if wf == nil || !wf.IsList() {
		return 0
	}
	entries := ch.Get(wf).List()
	n := 0
	for i := 0; i < entries.Len(); i++ {
		e := entries.Get(i).Message()
		if iv := e.Descriptor().Fields().ByName("item"); iv != nil && e.Has(iv) {
			n++
		}
	}
	return n
}

func dedupStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
