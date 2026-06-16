package main

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const xmlconfRoot = "testing/conformance_test/xmlconf"

// xmlManifests are the W3C suite leaf manifests with <TEST> entries
// (ASCII/UTF-8 collections).
var xmlManifests = []string{
	"xmltest/xmltest.xml",
	"sun/sun-valid.xml", "sun/sun-invalid.xml", "sun/sun-not-wf.xml",
	"oasis/oasis.xml",
	"ibm/ibm_oasis_valid.xml", "ibm/ibm_oasis_invalid.xml", "ibm/ibm_oasis_not-wf.xml",
}

// ooxmlSources maps a format to the vendored source dirs and extension.
var ooxmlSources = map[string]struct {
	dirs []string
	ext  string
}{
	"docx": {[]string{"testing/docx/poi", "testing/docx/python-docx"}, ".docx"},
	"xlsx": {[]string{"testing/xlsx/poi", "testing/xlsx/xlsxwriter"}, ".xlsx"},
}

type tcGroup struct {
	Base   string    `xml:"base,attr"`
	Groups []tcGroup `xml:"TESTCASES"`
	Tests  []tcTest  `xml:"TEST"`
}

type tcTest struct {
	ID        string `xml:"ID,attr"`
	Type      string `xml:"TYPE,attr"`
	Entities  string `xml:"ENTITIES,attr"`
	URI       string `xml:"URI,attr"`
	Version   string `xml:"VERSION,attr"`
	Namespace string `xml:"NAMESPACE,attr"`
	Rec       string `xml:"RECOMMENDATION,attr"`
}

// fetchCorpus (re)builds the testing/<format>/<verdict>/ folders from the
// vendored sources: the W3C conformance suite (classified by its manifests)
// and the OOXML reference files.
func fetchCorpus() error {
	for _, f := range formats {
		for _, v := range verdictDirs {
			dir := filepath.Join(testingDir, f.name, v)
			os.RemoveAll(dir)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return err
			}
		}
	}

	if _, err := os.Stat(xmlconfRoot); err != nil {
		fmt.Printf("note: W3C suite not at %s — skipping XML corpus\n", xmlconfRoot)
	} else {
		n := classifyXML()
		fmt.Printf("xml:  %d files organized into valid/invalid/not-wf\n", n)
	}

	for format, src := range ooxmlSources {
		n := copyOOXML(format, src.dirs, src.ext)
		fmt.Printf("%s: %d files copied into valid/\n", format, n)
	}

	fetchRSS()
	return nil
}

const rssRepo = "plenaryapp/awesome-rss-feeds"

// fetchRSS pulls the repo's OPML feed-list files (which are XML) into
// rss/valid/, and best-effort fetches a sample of the actual RSS/Atom feeds
// those OPMLs point to, so the corpus exercises real-world feed XML.
func fetchRSS() {
	dst := filepath.Join(testingDir, "rss", "valid")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return
	}
	paths, err := listRepoOPML(rssRepo)
	if err != nil {
		fmt.Printf("rss:  could not reach %s (%v) — skipping\n", rssRepo, err)
		return
	}

	_ = dst
	opml, feeds := 0, 0
	var feedURLs []string
	for _, p := range paths {
		body, err := httpGet(rawURL(rssRepo, p), 10*time.Second)
		if err != nil {
			continue
		}
		// Real-world OPML often has unescaped '&'; classify by a reference
		// parser (encoding/xml) so the folder reflects true well-formedness.
		if routeRSS(body, "opml_"+sanitizeName(p)) {
			opml++
		}
		if len(feedURLs) < 300 {
			feedURLs = append(feedURLs, extractXMLUrls(body)...)
		}
	}

	seen := map[string]bool{}
	for _, u := range feedURLs {
		if feeds >= 30 {
			break
		}
		if seen[u] {
			continue
		}
		seen[u] = true
		body, err := httpGet(u, 6*time.Second)
		if err != nil || !looksLikeXML(body) {
			continue
		}
		if routeRSS(body, fmt.Sprintf("feed_%03d.xml", feeds)) {
			feeds++
		}
	}
	fmt.Printf("rss:  %d OPML + %d live feeds (classified valid/not-wf by encoding/xml)\n", opml, feeds)
}

// routeRSS writes a fetched feed into rss/valid or rss/not-wf depending on
// whether Go's standard encoding/xml accepts it — an independent reference
// oracle. The corpus check then verifies our parser agrees.
func routeRSS(body []byte, name string) bool {
	verdict := "not-wf"
	if referenceWellFormed(body) {
		verdict = "valid"
	}
	return os.WriteFile(filepath.Join(testingDir, "rss", verdict, name), body, 0o644) == nil
}

// referenceWellFormed reports whether the standard library's XML decoder
// accepts the document as well-formed.
func referenceWellFormed(b []byte) bool {
	dec := xml.NewDecoder(bytes.NewReader(b))
	dec.Strict = true
	for {
		_, err := dec.Token()
		if err == io.EOF {
			return true
		}
		if err != nil {
			return false
		}
	}
}

// classifyXML copies each conformance test file into the folder named by its
// TYPE (valid / invalid / not-wf), filtered to the XML-1.0, no-external-
// entity, namespace-safe subset this parser targets.
func classifyXML() int {
	count := 0
	for _, m := range xmlManifests {
		mp := filepath.Join(xmlconfRoot, m)
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
		coll := strings.SplitN(m, "/", 2)[0]
		var tests []resolvedT
		collectT(&root, filepath.Dir(mp), &tests)
		for _, rt := range tests {
			verdict, skip := classifyTest(rt.t)
			if skip {
				continue
			}
			b, err := os.ReadFile(rt.path)
			if err != nil {
				continue
			}
			id := strings.TrimSuffix(rt.t.ID, ".xml")
			dst := filepath.Join(testingDir, "xml", verdict, coll+"_"+id+".xml")
			if os.WriteFile(dst, b, 0o644) == nil {
				count++
			}
		}
	}
	return count
}

func classifyTest(t tcTest) (verdict string, skip bool) {
	if t.Version == "1.1" || strings.HasPrefix(t.Rec, "XML1.1") || strings.HasPrefix(t.Rec, "NS") {
		return "", true
	}
	if t.Namespace == "no" {
		return "", true
	}
	if t.Entities != "" && t.Entities != "none" {
		return "", true
	}
	switch t.Type {
	case "valid", "invalid", "not-wf":
		return t.Type, false
	}
	return "", true // "error" and anything unexpected
}

type resolvedT struct {
	t    tcTest
	path string
}

func collectT(g *tcGroup, base string, out *[]resolvedT) {
	b := base
	if g.Base != "" {
		b = filepath.Join(base, g.Base)
	}
	for _, t := range g.Tests {
		*out = append(*out, resolvedT{t, filepath.Join(b, t.URI)})
	}
	for i := range g.Groups {
		collectT(&g.Groups[i], b, out)
	}
}

// copyOOXML flattens every container of the given extension from the source
// dirs into testing/<format>/valid/ (these reference files are expected to
// be well-formed).
func copyOOXML(format string, srcDirs []string, ext string) int {
	dst := filepath.Join(testingDir, format, "valid")
	count := 0
	for _, sd := range srcDirs {
		_ = filepath.WalkDir(sd, func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.EqualFold(filepath.Ext(p), ext) {
				return nil
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return nil
			}
			name := strings.ReplaceAll(strings.TrimPrefix(p, filepath.Join("testing", format)+string(os.PathSeparator)), string(os.PathSeparator), "_")
			if os.WriteFile(filepath.Join(dst, name), b, 0o644) == nil {
				count++
			}
			return nil
		})
	}
	return count
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
