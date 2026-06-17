package service

import (
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// rssParser returns the shared XML parser, failing the test on init error.
func rssParser(t *testing.T) *Parser {
	t.Helper()
	p, err := Default()
	if err != nil {
		t.Fatalf("parser init: %v", err)
	}
	return p
}

const minimalRSS = `<?xml version="1.0"?>
<rss version="2.0">
  <channel>
    <title>Example</title>
    <link>http://example.com/</link>
    <description>An example feed</description>
    <item>
      <title>First</title>
      <guid isPermaLink="true">http://example.com/1</guid>
    </item>
    <item>
      <description>Second, no title</description>
    </item>
  </channel>
</rss>`

func TestParseRSSCore(t *testing.T) {
	p := rssParser(t)
	msg, err := ParseRSS(p, minimalRSS)
	if err != nil {
		t.Fatalf("ParseRSS: %v", err)
	}
	if got := stringField(msg.ProtoReflect(), "version"); got != "2.0" {
		t.Errorf("version = %q, want 2.0", got)
	}
	if n := RSSItemCount(msg); n != 2 {
		t.Errorf("item count = %d, want 2", n)
	}
	if got := channelLeafText(t, msg, "title"); got != "Example" {
		t.Errorf("channel title = %q, want Example", got)
	}
	if _, err := proto.Marshal(msg); err != nil {
		t.Errorf("marshal projected message: %v", err)
	}
}

func TestParseRSSNamespaceExtensionsTolerated(t *testing.T) {
	const feed = `<rss version="2.0" xmlns:atom="http://www.w3.org/2005/Atom" xmlns:dc="http://purl.org/dc/elements/1.1/">
  <channel>
    <title>T</title><link>http://x/</link><description>D</description>
    <atom:link href="http://x/feed" rel="self"/>
    <item>
      <title>I</title>
      <dc:creator>someone</dc:creator>
      <atom:updated>2026-01-01</atom:updated>
    </item>
  </channel>
</rss>`
	if _, err := ParseRSS(rssParser(t), feed); err != nil {
		t.Fatalf("namespaced extensions should be tolerated, got: %v", err)
	}
}

func TestParseRSSRejectsUnprefixedUnknown(t *testing.T) {
	// An unprefixed element outside the core vocabulary violates RSS 2.0's
	// rule that extensions must be namespaced.
	const feed = `<rss version="2.0"><channel>
    <title>T</title><link>http://x/</link><description>D</description>
    <bogus>not allowed unprefixed</bogus>
  </channel></rss>`
	_, err := ParseRSS(rssParser(t), feed)
	if _, ok := err.(*ValidityError); !ok {
		t.Fatalf("want *ValidityError for unprefixed unknown element, got %T (%v)", err, err)
	}
}

func TestParseRSSStructuralVerdicts(t *testing.T) {
	cases := []struct{ name, feed string }{
		{"wrong root", `<feed xmlns="http://www.w3.org/2005/Atom"><title>x</title></feed>`},
		{"wrong version", `<rss version="1.0"><channel><title>t</title><link>l</link><description>d</description></channel></rss>`},
		{"no channel", `<rss version="2.0"></rss>`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := ParseRSS(rssParser(t), c.feed)
			if _, ok := err.(*ValidityError); !ok {
				t.Fatalf("want *ValidityError, got %T (%v)", err, err)
			}
		})
	}
}

func TestParseRSSLeafFoldsInlineMarkup(t *testing.T) {
	// RSS leaves are #PCDATA that may carry inline (here raw) HTML; the markup
	// is the element's text, not RSS vocabulary — so this must not be rejected,
	// and the title's value is the inline text.
	const feed = `<rss version="2.0"><channel>
    <title><a href="/x" hreflang="en">Headline</a></title>
    <link>http://x/</link><description>D</description>
  </channel></rss>`
	msg, err := ParseRSS(rssParser(t), feed)
	if err != nil {
		t.Fatalf("inline markup in a leaf should fold to text, got: %v", err)
	}
	if got := channelLeafText(t, msg, "title"); got != "Headline" {
		t.Errorf("channel title = %q, want Headline", got)
	}
}

func TestRSSConformanceWarnings(t *testing.T) {
	p := rssParser(t)
	// An item with neither title nor description is a soft conformance warning,
	// not a parse failure (real feeds do this — see corpus feed_015).
	const bent = `<rss version="2.0"><channel>
    <title>T</title><link>l</link><description>d</description>
    <item><guid>http://x/1</guid><link>http://x/1</link></item>
  </channel></rss>`
	doc, err := p.Parse(bent, false)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if _, err := ParseRSS(p, bent); err != nil {
		t.Fatalf("a contentless item is a warning, not a parse failure, got: %v", err)
	}
	warn := RSSConformance(doc)
	if len(warn) != 1 {
		t.Fatalf("want 1 conformance warning, got %d: %v", len(warn), warn)
	}

	const clean = `<rss version="2.0"><channel>
    <title>T</title><link>l</link><description>d</description>
    <item><title>only title is fine</title></item>
  </channel></rss>`
	cleanDoc, _ := p.Parse(clean, false)
	if warn := RSSConformance(cleanDoc); len(warn) != 0 {
		t.Errorf("conformant feed should have no warnings, got: %v", warn)
	}
}

// --- reflection helpers over the projected (dynamic) rss.Rss message ---

func stringField(m protoreflect.Message, name string) string {
	f := m.Descriptor().Fields().ByName(protoreflect.Name(name))
	if f == nil {
		return ""
	}
	return m.Get(f).String()
}

// channelLeafText returns the text of the first channel child whose oneof
// variant matches name (e.g. "title").
func channelLeafText(t *testing.T, m proto.Message, name string) string {
	t.Helper()
	rss := m.ProtoReflect()
	ch := rss.Get(rss.Descriptor().Fields().ByName("channel")).Message()
	wf := ch.Descriptor().Fields().ByName("alt1")
	entries := ch.Get(wf).List()
	oo := wf.Message().Oneofs().ByName("value")
	for i := 0; i < entries.Len(); i++ {
		e := entries.Get(i).Message()
		if fd := e.WhichOneof(oo); fd != nil && string(fd.Name()) == name {
			return stringField(e.Get(fd).Message(), "text")
		}
	}
	return ""
}
