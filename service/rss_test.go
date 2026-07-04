package service

import (
	"strings"
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
    <title>T</title><link>http://x/</link><description>d</description>
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
    <title>T</title><link>http://x/</link><description>d</description>
    <item><title>only title is fine</title></item>
  </channel></rss>`
	cleanDoc, _ := p.Parse(clean, false)
	if warn := RSSConformance(cleanDoc); len(warn) != 0 {
		t.Errorf("conformant feed should have no warnings, got: %v", warn)
	}
}

// The value-level soft rules: link/url schemes, RFC-822 dates, image dimensions.
func TestRSSConformanceValueRules(t *testing.T) {
	p := rssParser(t)
	const bad = `<rss version="2.0"><channel>
    <title>T</title><link>javascript:alert(1)</link><description>d</description>
    <pubDate>not-a-date</pubDate>
    <image><url>http://x/i.png</url><title>t</title><link>http://x/</link><width>200</width><height>500</height></image>
    <item><title>i</title><link>ftp://x/1</link><pubDate>Sat, 07 Sep 2002 09:42:31 GMT</pubDate></item>
  </channel></rss>`
	doc, err := p.Parse(bad, false)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	got := strings.Join(RSSConformance(doc), "\n")
	for _, want := range []string{
		`<channel> <link> "javascript:alert(1)" is not an http(s) URL`,
		`<channel> <pubDate> "not-a-date" is not an RFC-822 date`,
		`<image> <width> "200" is not an integer in [1, 144]`,
		`<image> <height> "500" is not an integer in [1, 400]`,
		`<item> #1 <link> "ftp://x/1" is not an http(s) URL`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing warning %q; got:\n%s", want, got)
		}
	}

	const clean = `<rss version="2.0"><channel>
    <title>T</title><link>https://x/</link><description>d</description>
    <lastBuildDate>Sat, 07 Sep 2002 09:42:31 -0700</lastBuildDate>
    <image><url>http://x/i.png</url><title>t</title><link>http://x/</link><width>88</width><height>31</height></image>
    <item><title>i</title><link>https://x/1</link><pubDate>Mon, 02 Jan 2006 15:04:05 GMT</pubDate></item>
  </channel></rss>`
	cleanDoc, _ := p.Parse(clean, false)
	if w := RSSConformance(cleanDoc); len(w) != 0 {
		t.Errorf("valid value feed should have no warnings, got: %v", w)
	}
}

// A feed round-trips through Generate at the canonical infoset, the RSS
// counterpart to proto-sitemap's round-trip gate (fixed-point detector).
func TestRSSRoundTrip(t *testing.T) {
	p := rssParser(t)
	const feed = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:content="http://purl.org/rss/1.0/modules/content/">
  <channel>
    <title>Example &amp; Co</title>
    <link>https://example.com/</link>
    <description>News</description>
    <item>
      <title>Post</title>
      <guid isPermaLink="true">https://example.com/1</guid>
      <content:encoded><![CDATA[<p>hi</p>]]></content:encoded>
    </item>
  </channel>
</rss>`
	x1, err := p.Parse(feed, false)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	g1, err := Generate(x1)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	x2, err := p.Parse(string(g1), false)
	if err != nil {
		t.Fatalf("reparse: %v", err)
	}
	g2, _ := Generate(x2)
	if string(g1) != string(g2) {
		t.Errorf("RSS feed did not round-trip:\n--g1--\n%s\n--g2--\n%s", g1, g2)
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
