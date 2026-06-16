package main

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const xmlconfURL = "https://www.w3.org/XML/Test/xmlts20130923.zip"

// downloadXMLConf fetches the W3C XML Conformance Test Suite zip into a
// temporary directory and returns the path to its xmlconf/ root. The caller
// classifies the test files out of it into testing/corpus/xml/<verdict>/ and
// removes the temp dir; the raw suite is not part of the corpus.
func downloadXMLConf() (string, error) {
	fmt.Println("w3c:  downloading", xmlconfURL)
	b, err := httpGet(xmlconfURL, 120*time.Second)
	if err != nil {
		return "", fmt.Errorf("download xmlconf: %w", err)
	}
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		return "", fmt.Errorf("open xmlconf zip: %w", err)
	}
	tmp, err := os.MkdirTemp("", "xmlconf-")
	if err != nil {
		return "", err
	}
	for _, f := range zr.File {
		out := filepath.Join(tmp, f.Name) // entries are "xmlconf/..."
		if f.FileInfo().IsDir() {
			os.MkdirAll(out, 0o755)
			continue
		}
		os.MkdirAll(filepath.Dir(out), 0o755)
		rc, err := f.Open()
		if err != nil {
			continue
		}
		data, _ := io.ReadAll(rc)
		rc.Close()
		os.WriteFile(out, data, 0o644)
	}
	return filepath.Join(tmp, "xmlconf"), nil
}

// ooxmlRepos are OOXML fixture sources: one blob-filtered sparse subtree of
// generated, well-formed reference files each. (Apache POI's test-data is
// deliberately excluded — it mixes valid and intentionally-malformed files
// without per-file expectations.)
var ooxmlRepos = []struct {
	repo    string
	subdirs []string
	format  string
}{
	{"https://github.com/python-openxml/python-docx", []string{"tests", "features"}, "docx"},
	{"https://github.com/jmcnamara/XlsxWriter", []string{"xlsxwriter/test/comparison/xlsx_files"}, "xlsx"},
}

// downloadOOXML sparse-checks-out the OOXML fixture subtrees and copies their
// containers into testing/corpus/<docx|xlsx>/valid/ (organized by file type,
// not source). Best-effort: a source that fails is logged and skipped.
func downloadOOXML() {
	for _, s := range ooxmlRepos {
		dst := filepath.Join(testingDir, s.format, "valid")
		if entries, _ := os.ReadDir(dst); len(entries) > 0 {
			fmt.Printf("ooxml: %s already present\n", s.format)
			continue
		}
		tmp, err := os.MkdirTemp("", "ooxml-")
		if err != nil {
			continue
		}
		if err := sparseCheckout(s.repo, s.subdirs, tmp); err != nil {
			fmt.Printf("ooxml: %s skipped (%v)\n", s.repo, err)
			os.RemoveAll(tmp)
			continue
		}
		os.MkdirAll(dst, 0o755)
		n := 0
		want := "." + s.format
		filepath.WalkDir(tmp, func(p string, e os.DirEntry, err error) error {
			if err != nil || e.IsDir() || !strings.EqualFold(filepath.Ext(p), want) {
				return nil
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return nil
			}
			if os.WriteFile(filepath.Join(dst, filepath.Base(p)), b, 0o644) == nil {
				n++
			}
			return nil
		})
		os.RemoveAll(tmp)
		fmt.Printf("ooxml: %s -> %d files\n", s.format, n)
	}
}

// sparseCheckout clones one subtree of repo into dir with blob filtering, so
// only the needed files' blobs are fetched.
func sparseCheckout(repo string, subdirs []string, dir string) error {
	if err := exec.Command("git", "clone", "--filter=blob:none", "--no-checkout", "--depth", "1", repo, dir).Run(); err != nil {
		return err
	}
	run := func(args ...string) error {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		return cmd.Run()
	}
	if err := run(append([]string{"sparse-checkout", "set", "--no-cone"}, subdirs...)...); err != nil {
		return err
	}
	return run("checkout")
}
