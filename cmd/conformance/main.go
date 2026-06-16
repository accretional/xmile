// Command conformance runs the service parser over the W3C XML Conformance
// Test Suite (xmlconf) manifests and reports well-formedness classification
// per collection. It filters to what a 5th-edition, DTD-aware,
// namespace-agnostic, no-external-entity parser is responsible for:
//
//   - VERSION=1.1 / NS / namespace tests          skipped
//   - ENTITIES requiring external parsing          skipped
//   - EDITION that excludes "5"                     skipped (4e-only)
//   - TYPE=valid / invalid                          must parse (well-formed)
//   - TYPE=not-wf                                   must be rejected
//
// Pass `-v` to list sample failures, `-flat` to also scan testing/xml &
// testing/rss (lossy: those carry no edition metadata).
package main

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/accretional/xmile/service"
)

const suiteRoot = "testing/conformance_test/xmlconf"

var manifests = []string{
	"xmltest/xmltest.xml",
	"sun/sun-valid.xml", "sun/sun-invalid.xml", "sun/sun-not-wf.xml",
	"oasis/oasis.xml",
	"ibm/ibm_oasis_valid.xml", "ibm/ibm_oasis_invalid.xml", "ibm/ibm_oasis_not-wf.xml",
}

type tcGroup struct {
	Base   string     `xml:"base,attr"`
	Groups []tcGroup  `xml:"TESTCASES"`
	Tests  []tcTest   `xml:"TEST"`
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

// classify returns (expectParse, skip, reason).
func classify(t tcTest) (bool, bool, string) {
	if t.Version == "1.1" || strings.HasPrefix(t.Rec, "XML1.1") || strings.HasPrefix(t.Rec, "NS") {
		return false, true, "xml1.1/ns"
	}
	if t.Namespace == "no" {
		return false, true, "namespace"
	}
	if t.Edition != "" && !strings.Contains(" "+t.Edition+" ", " 5 ") {
		return false, true, "edition!=5"
	}
	switch t.Type {
	case "valid", "invalid":
		return true, false, ""
	case "not-wf":
		if t.Entities != "" && t.Entities != "none" {
			return false, true, "ext-entities"
		}
		return false, false, ""
	default:
		return false, true, "type=" + t.Type
	}
}

func main() {
	p, err := service.Default()
	if err != nil {
		fmt.Fprintln(os.Stderr, "init:", err)
		os.Exit(1)
	}
	verbose := false
	for _, a := range os.Args[1:] {
		if a == "-v" {
			verbose = true
		}
	}

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

	type stat struct{ pass, fail int; fails []string }
	buckets := map[string]*stat{}
	skips := map[string]int{}
	var totPass, totFail int

	for _, rt := range all {
		expect, skip, reason := classify(rt.t)
		if skip {
			skips[reason]++
			continue
		}
		data, err := os.ReadFile(rt.path)
		if err != nil {
			continue
		}
		_, perr := p.Parse(string(data))
		got := perr == nil
		cat := "accept"
		if !expect {
			cat = "reject"
		}
		key := rt.coll + "|" + cat
		b := buckets[key]
		if b == nil {
			b = &stat{}
			buckets[key] = b
		}
		if got == expect {
			b.pass++
			totPass++
		} else {
			b.fail++
			totFail++
			if len(b.fails) < 15 {
				verb := "rejected"
				if got {
					verb = "accepted"
				}
				b.fails = append(b.fails, fmt.Sprintf("%s [%s] wrongly %s", rt.t.ID, rt.t.Type, verb))
			}
		}
	}

	keys := make([]string, 0, len(buckets))
	for k := range buckets {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fmt.Println("collection            category  pass  fail   rate")
	for _, k := range keys {
		b := buckets[k]
		tot := b.pass + b.fail
		rate := 100.0
		if tot > 0 {
			rate = 100 * float64(b.pass) / float64(tot)
		}
		parts := strings.SplitN(k, "|", 2)
		fmt.Printf("%-20s  %-7s  %4d  %4d  %5.1f%%\n", parts[0], parts[1], b.pass, b.fail, rate)
	}
	fmt.Printf("TOTAL pass=%d fail=%d (%.2f%%)\n", totPass, totFail,
		100*float64(totPass)/float64(totPass+totFail))
	fmt.Printf("skipped: %v\n", skips)
	if verbose {
		for _, k := range keys {
			for _, f := range buckets[k].fails {
				fmt.Printf("  %s: %s\n", k, f)
			}
		}
	}
}
