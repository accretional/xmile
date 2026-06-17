package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// xsdDir is the corpus subfolder for W3C XSD test-suite schema files.
const xsdDir = "xsd"

// xsdTestsRepo is the official W3C XML Schema test suite (see docs/REFERENCES.md).
const xsdTestsRepo = "https://github.com/w3c/xsdtests"

// downloadXSD shallow-clones the W3C XSD test suite and copies its .xsd schema
// files into testing/corpus/xsd/ (flattened names, capped). The xsd-parse
// harness compiles each and reports coverage of the supported subset — reported,
// not gating, since the suite spans the whole language (and includes
// deliberately-invalid schemas) while the front-end targets a subset.
func downloadXSD() {
	dst := filepath.Join(testingDir, xsdDir)
	if entries, _ := os.ReadDir(dst); len(entries) > 0 {
		fmt.Println("xsd: already present")
		return
	}
	tmp, err := os.MkdirTemp("", "xsdtests-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "xsd:", err)
		return
	}
	defer os.RemoveAll(tmp)
	fmt.Println("xsd: cloning", xsdTestsRepo)
	if err := exec.Command("git", "clone", "--depth", "1", xsdTestsRepo, tmp).Run(); err != nil {
		fmt.Printf("xsd: clone failed (%v)\n", err)
		return
	}
	os.MkdirAll(dst, 0o755)
	const capN = 1000
	n := 0
	filepath.WalkDir(tmp, func(p string, e os.DirEntry, err error) error {
		if err != nil || e.IsDir() || n >= capN {
			return nil
		}
		if !strings.EqualFold(filepath.Ext(p), ".xsd") {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(tmp, p)
		name := strings.ReplaceAll(rel, string(filepath.Separator), "__")
		if os.WriteFile(filepath.Join(dst, name), b, 0o644) == nil {
			n++
		}
		return nil
	})
	fmt.Printf("xsd: -> %d schema files\n", n)
}
