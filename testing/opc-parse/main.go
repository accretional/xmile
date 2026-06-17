// Command opc-parse exercises the OPC package layer (ADR 0008 Phase 5) over the
// real docx/xlsx corpus: it unpacks each package with service.ProcessPackage,
// parses every XML part, and reports parts and relationships. These packages are
// deterministic local files whose parts already pass the xml-parse gate, so a
// package that fails to unpack or whose part is not well-formed is a genuine
// regression — opc-parse exits non-zero on any failure.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/accretional/xmile/service"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "opc-parse:", err)
		os.Exit(1)
	}
}

func run() error {
	var pkgs, failed, parts, rels int
	for _, label := range []string{"docx", "xlsx"} {
		dir := filepath.Join("testing", "corpus", label)
		// Packages live under the verdict subdirs (valid/…); also accept any
		// dropped directly in the format dir.
		var files []string
		for _, pat := range []string{
			filepath.Join(dir, "*."+label),
			filepath.Join(dir, "valid", "*."+label),
		} {
			m, _ := filepath.Glob(pat)
			files = append(files, m...)
		}
		sort.Strings(files)
		if len(files) == 0 {
			fmt.Printf("opc-parse: no %s packages under %s (skipped)\n", label, dir)
			continue
		}
		var lparts, lrels, lfail int
		for _, fp := range files {
			data, err := os.ReadFile(fp)
			if err != nil {
				return err
			}
			pkgs++
			pkg, perr := service.ProcessPackage(data)
			if perr != nil {
				failed++
				lfail++
				fmt.Printf("  FAIL %s: %v\n", filepath.Base(fp), perr)
				continue
			}
			lparts += len(pkg.Parts)
			lrels += len(pkg.Relationships)
			for _, p := range pkg.Parts {
				lrels += len(p.Rels)
			}
		}
		parts += lparts
		rels += lrels
		fmt.Printf("opc-parse: %s — %d packages, %d XML parts, %d relationships, %d failed\n",
			label, len(files), lparts, lrels, lfail)
	}
	fmt.Printf("opc-parse: %d packages total, %d XML parts, %d relationships, %d failed\n", pkgs, parts, rels, failed)
	if failed > 0 {
		return fmt.Errorf("%d package(s) failed to process", failed)
	}
	return nil
}
