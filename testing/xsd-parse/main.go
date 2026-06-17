// Command xsd-parse exercises the XSD front-end (ADR 0008 Phase 4) over the W3C
// XSD test suite (go run ./testing xsd). It compiles each .xsd with
// service.CompileXSD and reports how many fall within the supported subset.
//
// Reported, NOT gating: the suite spans the whole XML Schema language —
// derivation, substitution groups, redefine, identity constraints, datatype
// facets — and includes deliberately-invalid schemas, while the front-end
// targets an element-vocabulary subset (see service/xsd.go). The deterministic
// gate for XSD is service/xsd_test.go under `go test ./...`.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/accretional/xmile/service"
)

func main() {
	files, _ := filepath.Glob(filepath.Join("testing", "corpus", "xsd", "*.xsd"))
	sort.Strings(files)
	if len(files) == 0 {
		fmt.Println("xsd-parse: no XSD corpus (run: go run ./testing xsd) — skipped")
		return
	}

	var compiled, failed int
	for _, fp := range files {
		if compileOne(fp) {
			compiled++
		} else {
			failed++
		}
	}
	pct := 100 * float64(compiled) / float64(len(files))
	fmt.Printf("xsd-parse: %d/%d schemas compiled within the supported subset (%.1f%%), %d outside it\n",
		compiled, len(files), pct, failed)
	fmt.Println("xsd-parse: reported, not gating — the suite spans full XSD; the front-end targets a subset (ADR 0008)")
}

// compileOne reports whether a schema compiles, guarding against a panic on a
// pathological input so one bad file never aborts the run.
func compileOne(fp string) (ok bool) {
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	b, err := os.ReadFile(fp)
	if err != nil {
		return false
	}
	_, err = service.CompileXSD(b, service.SchemaOptions{Package: "t"})
	return err == nil
}
