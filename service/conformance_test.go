package service

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// conformance_test.go gates the parser against the organized corpus under
// testing/corpus/xml/<verdict>/ (built by `go run ./testing`):
//
//   - every valid/ and invalid/ document MUST parse (it is well-formed),
//   - not-wf/ documents MUST be rejected, at or above minRejectRate.
//
// The corpus is already the applicable subset (XML 1.0 5th edition + 1.1; no
// namespaces or external entities), filtered at fetch time, so every file is
// a real test — nothing is skipped here.

const corpusXML = "../testing/corpus/xml"

// minRejectRate ratchets not-wf rejection. Raise it as the grammar improves.
const minRejectRate = 0.99

func xmlFiles(dir string) []string {
	m, _ := filepath.Glob(filepath.Join(dir, "*.xml"))
	sort.Strings(m)
	return m
}

func TestCorpusWellFormed(t *testing.T) {
	p, err := Default()
	if err != nil {
		t.Fatalf("parser init: %v", err)
	}
	notwf := xmlFiles(filepath.Join(corpusXML, "not-wf"))
	if len(notwf) == 0 {
		t.Skip("corpus not present — run: go run ./testing")
	}

	// valid + invalid documents are well-formed and must parse.
	var acceptFail int
	for _, sub := range []string{"valid", "invalid"} {
		for _, f := range xmlFiles(filepath.Join(corpusXML, sub)) {
			data, _ := os.ReadFile(f)
			if _, perr := p.Parse(string(data)); perr != nil {
				acceptFail++
				if acceptFail <= 25 {
					t.Errorf("%s/%s wrongly rejected: %v", sub, filepath.Base(f), perr)
				}
			}
		}
	}

	// not-wf documents must be rejected.
	var rejected int
	for _, f := range notwf {
		data, _ := os.ReadFile(f)
		if _, perr := p.Parse(string(data)); perr != nil {
			rejected++
		}
	}
	rate := float64(rejected) / float64(len(notwf))
	t.Logf("valid/invalid accept-failures=%d; not-wf rejected %d/%d (%.2f%%)",
		acceptFail, rejected, len(notwf), 100*rate)
	if acceptFail > 0 {
		t.Errorf("%d valid/invalid documents wrongly rejected (must be 0)", acceptFail)
	}
	if rate < minRejectRate {
		t.Errorf("not-wf rejection %.2f%% < required %.2f%%", 100*rate, 100*minRejectRate)
	}
}
