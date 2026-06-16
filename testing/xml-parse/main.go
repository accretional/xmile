// Command xml-parse runs the xmile parser over the organized corpus under
// testing/corpus/<format>/<verdict>/ and checks that each file's verdict
// matches the folder it lives in. The corpus is built by the fetcher:
//
//	go run ./testing             # (re)build testing/corpus/
//
// Then:
//
//	go run ./testing/xml-parse [-v]      # run over the corpus + report
//	go run ./testing/xml-parse rss       # restrict to one format
//	go run ./testing/xml-parse -ast FILE # print the parsed AST of one file
//
// A file in valid/ or invalid/ must be well-formed; not-wf/ must be
// rejected. ZIP containers (docx/xlsx) are checked part-by-part.
package main

import (
	"archive/zip"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"google.golang.org/protobuf/encoding/prototext"

	"github.com/accretional/xmile/service"
	"github.com/accretional/xmile/testing/progress"
)

// testingDir is the root of the organized corpus (built by `go run ./testing`).
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

var (
	verbose = flag.Bool("v", false, "print every file, not just failures")
	astFlag = flag.Bool("ast", false, "print the parsed AST of each file argument")
)

func main() {
	flag.Parse()
	args := flag.Args()

	if *astFlag {
		for _, f := range args {
			data, err := os.ReadFile(f)
			if err != nil {
				fmt.Printf("# %s\n  (read error: %v)\n", f, err)
				continue
			}
			fmt.Printf("# %s\n", f)
			p, perr := service.Default()
			if perr != nil {
				fmt.Printf("  (init error: %v)\n", perr)
				continue
			}
			doc, derr := p.Parse(string(data))
			if derr != nil {
				fmt.Printf("  (not well-formed: %v)\n", derr)
				continue
			}
			fmt.Print(prototext.Format(doc))
		}
		return
	}

	filter := ""
	if len(args) > 0 {
		filter = args[0]
	}

	if !corpusPresent() {
		fmt.Fprintf(os.Stderr, "corpus not found under %s — run: go run ./testing\n", testingDir)
		os.Exit(1)
	}
	// Report-only: the Go gate (service tests) enforces the XML corpus;
	// docx/xlsx/rss are real-world and reported for visibility.
	runCorpus(filter)
}

// corpusPresent reports whether at least one verdict folder has files.
func corpusPresent() bool {
	for _, f := range formats {
		for _, v := range verdictDirs {
			entries, _ := os.ReadDir(filepath.Join(testingDir, f.name, v))
			if len(entries) > 0 {
				return true
			}
		}
	}
	return false
}

type tally struct{ pass, fail int }

type corpusTask struct {
	path, group, verdict string
	isZip                bool
}

type corpusResult struct {
	group, path, detail string
	pass                bool
}

// runCorpus walks every <format>/<verdict>/ folder and checks each file in
// parallel, then reports. It returns true if anything failed.
func runCorpus(filter string) bool {
	var tasks []corpusTask
	for _, f := range formats {
		if filter != "" && f.name != filter {
			continue
		}
		for _, v := range verdictDirs {
			dir := filepath.Join(testingDir, f.name, v)
			entries, err := os.ReadDir(dir)
			if err != nil {
				continue
			}
			for _, e := range entries {
				if e.IsDir() {
					continue
				}
				tasks = append(tasks, corpusTask{filepath.Join(dir, e.Name()), f.name + "/" + v, v, f.isZip})
			}
		}
	}

	results := make([]corpusResult, len(tasks))
	bar := progress.New("parse", len(tasks))
	idx := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < runtime.NumCPU(); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range idx {
				t := tasks[i]
				ok, detail := checkFile(t.path, t.verdict, t.isZip)
				results[i] = corpusResult{t.group, t.path, detail, ok}
				bar.Inc()
			}
		}()
	}
	for i := range tasks {
		idx <- i
	}
	close(idx)
	wg.Wait()
	bar.Finish()

	tallies := map[string]*tally{}
	failsShown := 0
	for _, r := range results {
		t := tallies[r.group]
		if t == nil {
			t = &tally{}
			tallies[r.group] = t
		}
		if r.pass {
			t.pass++
			if *verbose {
				fmt.Printf("[pass] %s\n", r.path)
			}
		} else {
			t.fail++
			if failsShown < 40 {
				fmt.Printf("[fail] %s: %s\n", r.path, r.detail)
				failsShown++
			}
		}
	}

	keys := make([]string, 0, len(tallies))
	for k := range tallies {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fmt.Println("\ncorpus results:")
	fmt.Println("group              pass  fail   rate")
	fmt.Println("-------------------------------------")
	var totPass, totFail int
	for _, k := range keys {
		t := tallies[k]
		totPass += t.pass
		totFail += t.fail
		fmt.Printf("%-16s  %5d %5d  %s\n", k, t.pass, t.fail, rate(t.pass, t.fail))
	}
	fmt.Println("-------------------------------------")
	fmt.Printf("%-16s  %5d %5d  %s\n", "TOTAL", totPass, totFail, rate(totPass, totFail))
	return totFail > 0
}

func rate(pass, fail int) string {
	tot := pass + fail
	if tot == 0 {
		return "  n/a"
	}
	return fmt.Sprintf("%5.1f%%", 100*float64(pass)/float64(tot))
}

// checkFile validates one corpus file (or every XML part of a ZIP) and
// reports whether the result matches the expected verdict for its folder.
func checkFile(path, want string, isZip bool) (bool, string) {
	if isZip {
		return checkZip(path, want)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false, "read: " + err.Error()
	}
	got, gerr := validate(string(data))
	return matchVerdict(want, got), describe(got, gerr)
}

// checkZip validates every XML part of a ZIP container against the folder's
// expectation.
func checkZip(path, want string) (bool, string) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return false, "open zip: " + err.Error()
	}
	defer zr.Close()
	for _, f := range zr.File {
		ext := strings.ToLower(filepath.Ext(f.Name))
		if ext != ".xml" && ext != ".rels" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return false, f.Name + ": " + err.Error()
		}
		b, _ := io.ReadAll(rc)
		rc.Close()
		got, gerr := validate(string(b))
		if !matchVerdict(want, got) {
			return false, f.Name + ": " + describe(got, gerr)
		}
	}
	return true, ""
}

// matchVerdict reports whether a verdict satisfies the folder's expectation.
// valid and invalid both require well-formedness; DTD validity (which would
// split them) is not yet checked, so an invalid document is accepted here.
func matchVerdict(want string, got verdict) bool {
	switch want {
	case "valid", "invalid":
		return got == wellFormed
	case "not-wf":
		return got == notWF
	}
	return false
}

func describe(got verdict, err error) string {
	if err != nil {
		return fmt.Sprintf("got %s (%v)", got, err)
	}
	return "got " + string(got)
}
