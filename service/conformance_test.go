package service

import (
	"bytes"
	"encoding/xml"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// conformance_test.go gates the parser against the W3C XML Conformance Test
// Suite (xmlconf), filtered to what a 5th-edition, DTD-aware,
// namespace-agnostic, no-external-entity parser is responsible for:
//
//   - every TYPE=valid / invalid document MUST parse (it is well-formed),
//   - TYPE=not-wf documents MUST be rejected, at or above minRejectRate.
//
// Skipped: XML 1.1 / namespace tests, EDITION sets excluding 5, and not-wf
// cases needing external-entity resolution.

const suiteRoot = "../testing/conformance_test/xmlconf"

// minRejectRate ratchets not-wf rejection over the applicable subset. Raise
// it as the grammar improves; never lower without cause.
const minRejectRate = 0.95

var manifests = []string{
	"xmltest/xmltest.xml",
	"sun/sun-valid.xml", "sun/sun-invalid.xml", "sun/sun-not-wf.xml",
	"oasis/oasis.xml",
	"ibm/ibm_oasis_valid.xml", "ibm/ibm_oasis_invalid.xml", "ibm/ibm_oasis_not-wf.xml",
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

type resolved struct {
	t    tcTest
	path string
	coll string
}

func collect(g *tcGroup, base, coll string, out *[]resolved) {
	b := base
	if g.Base != "" {
		b = filepath.Join(base, g.Base)
	}
	for _, t := range g.Tests {
		*out = append(*out, resolved{t: t, path: filepath.Join(b, t.URI), coll: coll})
	}
	for i := range g.Groups {
		collect(&g.Groups[i], b, coll, out)
	}
}

// classify returns (expectParse, skip).
func classify(t tcTest) (expect, skip bool) {
	if t.Version == "1.1" || strings.HasPrefix(t.Rec, "XML1.1") || strings.HasPrefix(t.Rec, "NS") {
		return false, true
	}
	if t.Namespace == "no" {
		return false, true
	}
	if t.Edition != "" && !strings.Contains(" "+t.Edition+" ", " 5 ") {
		return false, true
	}
	switch t.Type {
	case "valid", "invalid":
		return true, false
	case "not-wf":
		if t.Entities != "" && t.Entities != "none" {
			return false, true
		}
		return false, false
	default:
		return false, true
	}
}

func loadSuite(t *testing.T) []resolved {
	var all []resolved
	for _, m := range manifests {
		mp := filepath.Join(suiteRoot, m)
		data, err := os.ReadFile(mp)
		if err != nil {
			continue
		}
		var root tcGroup
		dec := xml.NewDecoder(bytes.NewReader(data))
		dec.Strict = false
		if dec.Decode(&root) != nil {
			continue
		}
		collect(&root, filepath.Dir(mp), strings.SplitN(m, "/", 2)[0], &all)
	}
	if len(all) == 0 {
		t.Skipf("conformance suite not vendored under %s", suiteRoot)
	}
	return all
}

func TestW3CConformance(t *testing.T) {
	p, err := Default()
	if err != nil {
		t.Fatalf("parser init: %v", err)
	}
	all := loadSuite(t)

	var acceptFails, rejectTotal, rejectPass int
	for _, rt := range all {
		expect, skip := classify(rt.t)
		if skip {
			continue
		}
		data, err := os.ReadFile(rt.path)
		if err != nil {
			continue
		}
		_, perr := p.Parse(string(data))
		got := perr == nil
		if expect {
			if !got {
				acceptFails++
				if acceptFails <= 20 {
					t.Errorf("valid/invalid document %s wrongly rejected: %v", rt.t.ID, perr)
				}
			}
			continue
		}
		rejectTotal++
		if !got {
			rejectPass++
		}
	}
	if acceptFails > 0 {
		t.Errorf("%d valid/invalid documents wrongly rejected (must be 0)", acceptFails)
	}
	rate := float64(rejectPass) / float64(rejectTotal)
	t.Logf("not-wf rejection: %d/%d (%.2f%%)", rejectPass, rejectTotal, 100*rate)
	if rate < minRejectRate {
		t.Errorf("not-wf rejection %.2f%% < required %.2f%%", 100*rate, 100*minRejectRate)
	}
}

// TestLocalCorpus checks that every document filed as well-formed in the
// local corpora parses.
func TestLocalCorpus(t *testing.T) {
	p, err := Default()
	if err != nil {
		t.Fatalf("parser init: %v", err)
	}
	dirs := []struct {
		path string
		gate bool // hard-fail on any rejection (curated W3C-derived corpora)
	}{
		{"../testing/xml/valid", true},
		{"../testing/xml/invalid", true},
		{"../testing/rss/valid", false}, // real-world feeds; may be mis-filed
	}
	for _, d := range dirs {
		var files []string
		for _, ext := range []string{"*.xml", "*.opml", "*.rss"} {
			m, _ := filepath.Glob(filepath.Join(d.path, ext))
			files = append(files, m...)
		}
		sort.Strings(files)
		var fails int
		for _, f := range files {
			data, _ := os.ReadFile(f)
			if _, perr := p.Parse(string(data)); perr != nil {
				fails++
				if d.gate && fails <= 10 {
					t.Errorf("%s wrongly rejected: %v", filepath.Base(f), perr)
				}
			}
		}
		t.Logf("%s: %d files, %d rejected", d.path, len(files), fails)
	}
}
