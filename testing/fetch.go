package main

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// xmlManifests are the W3C suite leaf manifests with <TEST> entries, plus the
// XML 1.1 collection.
var xmlManifests = []string{
	"xmltest/xmltest.xml",
	"sun/sun-valid.xml", "sun/sun-invalid.xml", "sun/sun-not-wf.xml",
	"oasis/oasis.xml",
	"ibm/ibm_oasis_valid.xml", "ibm/ibm_oasis_invalid.xml", "ibm/ibm_oasis_not-wf.xml",
	"eduni/xml-1.1/xml11.xml",
}

type tcGroup struct {
	Base   string    `xml:"base,attr"`
	Groups []tcGroup `xml:"TESTCASES"`
	Tests  []tcTest  `xml:"TEST"`
}

type tcTest struct {
	ID        string `xml:"ID,attr"`
	Type      string `xml:"TYPE,attr"`
	Entities  string `xml:"ENTITIES,attr"`
	URI       string `xml:"URI,attr"`
	Version   string `xml:"VERSION,attr"`
	Namespace string `xml:"NAMESPACE,attr"`
	Rec       string `xml:"RECOMMENDATION,attr"`
	Edition   string `xml:"EDITION,attr"`
}

// fetchCorpus builds testing/corpus/<format>/<verdict>/ by downloading the
// W3C XML conformance suite (classified by its manifests into xml/<verdict>/),
// the OOXML reference files, and real-world RSS feeds — all organized by file
// type. Idempotent: a corpus already present is left in place.
func fetchCorpus() error {
	for _, f := range formats {
		for _, v := range verdictDirs {
			os.MkdirAll(filepath.Join(testingDir, f.name, v), 0o755)
		}
	}

	if xmlCorpusPresent() {
		fmt.Println("xml:  corpus already present")
	} else if root, err := downloadXMLConf(); err != nil {
		fmt.Println("xml: ", err)
	} else {
		n := classifyXML(root)
		os.RemoveAll(filepath.Dir(root))
		fmt.Printf("xml:  %d test files -> xml/<verdict>/\n", n)
	}

	downloadOOXML()
	fetchRSS()
	return nil
}

func xmlCorpusPresent() bool {
	e, _ := os.ReadDir(filepath.Join(testingDir, "xml", "not-wf"))
	return len(e) > 0
}

const rssRepo = "plenaryapp/awesome-rss-feeds"

// fetchRSS pulls the repo's OPML feed-list files (which are XML) into
// rss/valid/, and best-effort fetches a sample of the actual RSS/Atom feeds
// those OPMLs point to, so the corpus exercises real-world feed XML.
func fetchRSS() {
	dst := filepath.Join(testingDir, "rss", "valid")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return
	}
	paths, err := listRepoOPML(rssRepo)
	if err != nil {
		fmt.Printf("rss:  could not reach %s (%v) — skipping\n", rssRepo, err)
		return
	}

	_ = dst
	opml, feeds := 0, 0
	var feedURLs []string
	for _, p := range paths {
		body, err := httpGet(rawURL(rssRepo, p), 10*time.Second)
		if err != nil {
			continue
		}
		// Real-world OPML often has unescaped '&'; classify by a reference
		// parser (encoding/xml) so the folder reflects true well-formedness.
		if routeRSS(body, "opml_"+sanitizeName(p)) {
			opml++
		}
		if len(feedURLs) < 300 {
			feedURLs = append(feedURLs, extractXMLUrls(body)...)
		}
	}

	seen := map[string]bool{}
	for _, u := range feedURLs {
		if feeds >= 30 {
			break
		}
		if seen[u] {
			continue
		}
		seen[u] = true
		body, err := httpGet(u, 6*time.Second)
		if err != nil || !looksLikeXML(body) {
			continue
		}
		if routeRSS(body, fmt.Sprintf("feed_%03d.xml", feeds)) {
			feeds++
		}
	}
	fmt.Printf("rss:  %d OPML + %d live feeds (classified valid/not-wf by encoding/xml)\n", opml, feeds)
}

// routeRSS writes a fetched feed into rss/valid or rss/not-wf depending on
// whether Go's standard encoding/xml accepts it — an independent reference
// oracle. The corpus check then verifies our parser agrees.
func routeRSS(body []byte, name string) bool {
	verdict := "not-wf"
	if referenceWellFormed(body) {
		verdict = "valid"
	}
	return os.WriteFile(filepath.Join(testingDir, "rss", verdict, name), body, 0o644) == nil
}

// referenceWellFormed reports whether the standard library's XML decoder
// accepts the document as well-formed.
func referenceWellFormed(b []byte) bool {
	dec := xml.NewDecoder(bytes.NewReader(b))
	dec.Strict = true
	for {
		_, err := dec.Token()
		if err == io.EOF {
			return true
		}
		if err != nil {
			return false
		}
	}
}

// classifyXML copies each conformance test file from the suite at root into
// testing/corpus/xml/<verdict>/, filtered to the subset this parser targets
// (XML 1.0 5th edition + 1.1; no namespaces or external entities).
func classifyXML(root string) int {
	count := 0
	for _, m := range xmlManifests {
		mp := filepath.Join(root, m)
		data, err := os.ReadFile(mp)
		if err != nil {
			continue
		}
		var grp tcGroup
		dec := xml.NewDecoder(bytes.NewReader(data))
		dec.Strict = false
		if dec.Decode(&grp) != nil {
			continue
		}
		coll := strings.SplitN(m, "/", 2)[0]
		var tests []resolvedT
		collectT(&grp, filepath.Dir(mp), &tests)
		for _, rt := range tests {
			verdict, skip := classifyTest(rt.t)
			if skip {
				continue
			}
			b, err := os.ReadFile(rt.path)
			if err != nil {
				continue
			}
			id := strings.TrimSuffix(rt.t.ID, ".xml")
			dst := filepath.Join(testingDir, "xml", verdict, coll+"_"+id+".xml")
			if os.WriteFile(dst, b, 0o644) == nil {
				count++
			}
		}
	}
	return count
}

// classifyTest returns the verdict folder for a test, or skip=true for tests
// outside this parser's scope: namespaces, external entities, 4th-edition-only
// character tests, and "error"-type tests.
func classifyTest(t tcTest) (verdict string, skip bool) {
	if strings.HasPrefix(t.Rec, "NS") || t.Namespace == "no" {
		return "", true
	}
	if t.Edition != "" && !strings.Contains(" "+t.Edition+" ", " 5 ") {
		return "", true
	}
	if t.Entities != "" && t.Entities != "none" {
		return "", true
	}
	switch t.Type {
	case "valid", "invalid", "not-wf":
		return t.Type, false
	}
	return "", true
}

type resolvedT struct {
	t    tcTest
	path string
}

func collectT(g *tcGroup, base string, out *[]resolvedT) {
	b := base
	if g.Base != "" {
		b = filepath.Join(base, g.Base)
	}
	for _, t := range g.Tests {
		*out = append(*out, resolvedT{t, filepath.Join(b, t.URI)})
	}
	for i := range g.Groups {
		collectT(&g.Groups[i], b, out)
	}
}
