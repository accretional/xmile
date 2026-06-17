// Command testing fetches and organizes the xmile test corpus under
// testing/corpus/<format>/<verdict>/ from the vendored sources (the W3C
// conformance suite, OOXML reference files, and real-world RSS feeds).
//
// It has no dependency on the parser — classification uses an independent
// oracle (the manifests, and Go's encoding/xml for RSS). The parser
// conformance harness that runs over this corpus lives in
// testing/xml-parse.
//
// Usage:
//
//	go run ./testing        # (re)build testing/corpus/
package main

import (
	"fmt"
	"os"
)

// testingDir is the root of the organized corpus.
const testingDir = "testing/corpus"

// formats maps a corpus format to its extension and whether it is a ZIP
// container of XML parts.
var formats = []struct {
	name  string
	ext   string
	isZip bool
}{
	{"xml", ".xml", false},
	{"rss", ".opml", false},
	{"docx", ".docx", true},
	{"xlsx", ".xlsx", true},
}

// verdictDirs are the per-format subfolders; the folder name is the expected
// outcome for every file inside it.
var verdictDirs = []string{"valid", "invalid", "not-wf"}

func main() {
	// `go run ./testing rss0.91` / `rss2.0` fetch only that vocabulary's corpus
	// (used by the schema-compile and rss-parse harnesses) without re-running
	// the full corpus build.
	if len(os.Args) > 1 && os.Args[1] == rss091Dir {
		downloadRSS091()
		return
	}
	if len(os.Args) > 1 && os.Args[1] == rss2Dir {
		downloadRSS2()
		return
	}
	if err := fetchCorpus(); err != nil {
		fmt.Fprintln(os.Stderr, "fetch:", err)
		os.Exit(1)
	}
}
