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
//   - every valid/ document MUST parse with no error (well-formed and valid),
//   - every invalid/ document MUST be rejected with a *ValidityError
//     (well-formed but in breach of its DTD),
//   - not-wf/ documents MUST be rejected, at or above minRejectRate.
//
// The corpus is already the applicable subset (XML 1.0 5th edition + 1.1; no
// namespaces, external entities, or — for invalid/ — documents with no DTD to
// validate against), filtered at fetch time, so every file is a real test.

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

	// valid/ documents must parse with no error.
	var acceptFail int
	for _, f := range xmlFiles(filepath.Join(corpusXML, "valid")) {
		data, _ := os.ReadFile(f)
		if _, perr := p.Parse(string(data)); perr != nil {
			acceptFail++
			if acceptFail <= 25 {
				t.Errorf("valid/%s wrongly rejected: %v", filepath.Base(f), perr)
			}
		}
	}

	// invalid/ documents must be rejected specifically as DTD-invalid (a
	// *ValidityError), not merely not-well-formed.
	invalid := xmlFiles(filepath.Join(corpusXML, "invalid"))
	var invalidRejected, miscategorized int
	for _, f := range invalid {
		data, _ := os.ReadFile(f)
		_, perr := p.Parse(string(data))
		switch perr.(type) {
		case *ValidityError:
			invalidRejected++
		case nil:
			if miscategorized < 25 {
				t.Errorf("invalid/%s wrongly accepted as valid", filepath.Base(f))
			}
			miscategorized++
		default: // *WFError
			if miscategorized < 25 {
				t.Errorf("invalid/%s rejected as not-wf, want invalid: %v", filepath.Base(f), perr)
			}
			miscategorized++
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
	t.Logf("valid accept-failures=%d; invalid rejected %d/%d (miscategorized %d); not-wf rejected %d/%d (%.2f%%)",
		acceptFail, invalidRejected, len(invalid), miscategorized, rejected, len(notwf), 100*rate)
	if acceptFail > 0 {
		t.Errorf("%d valid documents wrongly rejected (must be 0)", acceptFail)
	}
	if miscategorized > 0 {
		t.Errorf("%d invalid documents not rejected as DTD-invalid (must be 0)", miscategorized)
	}
	if rate < minRejectRate {
		t.Errorf("not-wf rejection %.2f%% < required %.2f%%", 100*rate, 100*minRejectRate)
	}
}
