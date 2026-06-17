// Command rss-parse exercises service.ParseRSS over the real-world RSS 2.0
// corpus. Every feed is parsed as generic XML and walked against the rss.proto
// AST compiled from lang/rss.ebnf, with namespace-qualified extensions
// tolerated and unprefixed out-of-vocabulary markup rejected.
//
// The corpus is split into the valid set (testing/corpus/rss2.0/*.xml) and a
// curated invalid set (testing/corpus/rss2.0/invalid/*.xml) of genuine spec
// violations found in the wild — wrong-position core elements (item>image,
// channel>author), miscased names (isPermalink, pubdate), unprefixed foreign
// markup, and XML/namespace well-formedness errors (undeclared media: prefix,
// duplicate xmlns, content after </rss>).
//
//	go run ./testing/rss-parse              # report: valid pass rate + invalid reject rate
//	go run ./testing/rss-parse -all         # also list every valid-set failure
//	go run ./testing/rss-parse -classify    # move ParseRSS failures from valid -> invalid/ (run once after fetch)
//
// It reports rather than gates: the real-world long tail is for hardening, not
// a 100% gate (cf. the docx/xlsx/rss corpora). The deterministic correctness
// gate is service/rss_test.go.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"

	"github.com/accretional/xmile/service"
	"github.com/accretional/xmile/testing/progress"
)

const (
	validDir   = "testing/corpus/rss2.0"
	invalidDir = "testing/corpus/rss2.0/invalid"
	fallback   = "testing/corpus/rss/valid" // used when the rss2.0 set is absent (offline)
)

var (
	verbose  = flag.Bool("v", false, "print every feed, not just failures")
	allFails = flag.Bool("all", false, "print every valid-set failure (no sample cap)")
	classify = flag.Bool("classify", false, "move feeds that fail ParseRSS from the valid set into invalid/")
	rss2Sig  = regexp.MustCompile(`(?s)<rss\b[^>]*\bversion\s*=\s*["']2\.0["']`)
)

func main() {
	flag.Parse()
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "rss-parse:", err)
		os.Exit(1)
	}
}

func run() error {
	parser, err := service.Default()
	if err != nil {
		return fmt.Errorf("parser: %w", err)
	}
	if *classify {
		return classifyCorpus(parser)
	}
	return report(parser)
}

// result is one feed's ParseRSS outcome.
type result struct {
	base   string
	ok     bool
	reason string // classify() bucket, when !ok
	detail string // full error, when !ok
	items  int
}

// scan runs ParseRSS over files (skipping non-RSS-2.0 ones) and returns a
// result per in-scope feed.
func scan(parser *service.Parser, files []string, label string) []result {
	sort.Strings(files)
	bar := progress.New(label, len(files))
	var out []result
	for _, fp := range files {
		bar.Inc()
		data, err := os.ReadFile(fp)
		if err != nil || !rss2Sig.Match(data) {
			continue
		}
		r := result{base: filepath.Base(fp)}
		msg, perr := service.ParseRSS(parser, string(data))
		if perr != nil {
			r.reason, r.detail = bucket(perr), perr.Error()
		} else {
			r.ok, r.items = true, service.RSSItemCount(msg)
		}
		out = append(out, r)
	}
	bar.Finish()
	return out
}

// report prints the valid-set pass rate and the invalid-set reject rate.
func report(parser *service.Parser) error {
	vfiles, _ := filepath.Glob(filepath.Join(validDir, "*.xml"))
	dir := validDir
	if len(vfiles) == 0 {
		vfiles, _ = filepath.Glob(filepath.Join(fallback, "*.xml"))
		dir = fallback
	}
	if len(vfiles) == 0 {
		fmt.Println("rss-parse: WARNING no corpus — run: go run ./testing rss2.0")
		return nil
	}
	fmt.Printf("rss-parse: valid set = %d files under %s\n", len(vfiles), dir)
	valid := scan(parser, vfiles, "valid")

	pass, items := 0, 0
	reasons := map[string]int{}
	var samples []string
	for _, r := range valid {
		if r.ok {
			pass++
			items += r.items
			if *verbose {
				samples = append(samples, fmt.Sprintf("[pass] %s: %d items", r.base, r.items))
			}
			continue
		}
		reasons[r.reason]++
		if *allFails || len(samples) < 25 {
			samples = append(samples, fmt.Sprintf("[fail] %s: %s", r.base, r.detail))
		}
	}
	for _, s := range samples {
		fmt.Println(s)
	}
	if len(reasons) > 0 {
		fmt.Println("\nrss-parse: valid-set failure breakdown (unclassified — run -classify):")
		for _, k := range sortedKeys(reasons) {
			fmt.Printf("  %4d  %s\n", reasons[k], k)
		}
	}
	rate := 100.0
	if len(valid) > 0 {
		rate = 100 * float64(pass) / float64(len(valid))
	}
	fmt.Printf("\nrss-parse: %d/%d valid feeds projected cleanly (%.1f%%), %d items total\n", pass, len(valid), rate, items)

	// Negative set: every curated invalid feed must be rejected.
	if ifiles, _ := filepath.Glob(filepath.Join(invalidDir, "*.xml")); len(ifiles) > 0 {
		inv := scan(parser, ifiles, "invalid")
		rejected, wrong := 0, 0
		ibreak := map[string]int{}
		for _, r := range inv {
			if r.ok {
				wrong++
				fmt.Printf("  [LEAK] %s: invalid feed now accepted\n", r.base)
			} else {
				rejected++
				ibreak[r.reason]++
			}
		}
		fmt.Printf("rss-parse: invalid set — %d/%d correctly rejected", rejected, len(inv))
		if wrong > 0 {
			fmt.Printf(" (%d wrongly accepted!)", wrong)
		}
		fmt.Println()
		for _, k := range sortedKeys(ibreak) {
			fmt.Printf("  %4d  %s\n", ibreak[k], k)
		}
	}
	fmt.Println("rss-parse: real-world corpus — reported, not gating (correctness gate: go test ./service)")
	return nil
}

// classifyCorpus moves every valid-set feed that ParseRSS rejects into
// invalid/, so the corpus stays split into conformant and violating sets.
func classifyCorpus(parser *service.Parser) error {
	files, _ := filepath.Glob(filepath.Join(validDir, "*.xml"))
	if len(files) == 0 {
		fmt.Println("rss-parse: no rss2.0 corpus to classify")
		return nil
	}
	if err := os.MkdirAll(invalidDir, 0o755); err != nil {
		return err
	}
	moved := 0
	for _, r := range scan(parser, files, "classify") {
		if r.ok {
			continue
		}
		if os.Rename(filepath.Join(validDir, r.base), filepath.Join(invalidDir, r.base)) == nil {
			moved++
		}
	}
	fmt.Printf("rss-parse: classified — moved %d violating feed(s) to %s/\n", moved, invalidDir)
	return nil
}

// bucket reduces a parse error to a short reason.
func bucket(err error) string {
	switch err.(type) {
	case *service.WFError:
		return "not well-formed XML"
	case *service.ValidityError:
		return "not valid RSS 2.0"
	case *service.CannotValidateError:
		return "cannot validate"
	default:
		return "other"
	}
}

func sortedKeys(m map[string]int) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}
