package service

// rss.go — the RSS 2.0 semantics the vocabulary grammar does not carry: the
// structural pre-check (validateRSS, wired in as the "rss-2.0" format's
// PreValidate hook), the soft conformance rules (RSSConformance), and a
// typed-feed convenience (ParseRSS, RSSItemCount). RSS's *structure* is data —
// formats/rss-2.0.ebnf, compiled on demand by the format registry.
//
// These rules are NOT here because a context-free grammar is incapable of
// expressing them — they are all regular or context-free (version="2.0" is a
// fixed attribute literal; "exactly one <channel>" and "an item needs a title
// or description" are bounded cardinality/presence; width<=144 etc. are finite
// numeric bounds). They are out of the grammar because rss-2.0.ebnf is a
// projection schema over opaque attribute/leaf strings, and the projection is
// intentionally loose (it checks neither values nor cardinality). The only truly
// non-context-free constraints in the stack — start/end tag-name agreement and
// namespace scoping — are the engine's, enforced by xmile as tree walks, not
// RSS's. The namespace-extensibility rule is the registry's nsExtensible flag.

import (
	"fmt"

	"google.golang.org/protobuf/proto"

	xmlpb "github.com/accretional/xmile/proto/pb/xml"
)

// ParseRSS parses RSS 2.0 source and projects it into the typed RSS AST compiled
// on demand from formats/rss-2.0.ebnf — a thin convenience over Format("rss-2.0")
// + Process. The error is *WFError (not well-formed XML) or *ValidityError
// (well-formed but not valid RSS 2.0). The result is a dynamic proto message;
// read it by reflection (e.g. RSSItemCount).
func ParseRSS(p *Parser, src string) (proto.Message, error) {
	schema, err := Format("rss-2.0")
	if err != nil {
		return nil, err
	}
	res, err := p.Process(src, schema, true)
	if err != nil {
		return nil, err
	}
	return res.Document, nil
}

// validateRSS enforces the *structural* RSS-2.0 constraints the vocabulary
// grammar does not carry (regular/context-free, but out of a loose projection
// schema over opaque attributes), over the parsed Tag tree: the root is
// <rss version="2.0"> with exactly one <channel>. These determine whether the
// document is RSS 2.0 at all; a
// violation is a hard *ValidityError. Softer required-content rules (which
// real-world feeds routinely bend) are reported by RSSConformance, not enforced
// here — mirroring how the real-world corpora are reported, not gated.
func validateRSS(doc *xmlpb.Xml) error {
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
func RSSConformance(doc *xmlpb.Xml) []string {
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
