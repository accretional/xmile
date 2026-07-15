package main

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// xmlManifests are the W3C suite leaf manifests with <TEST> entries, plus the
// XML 1.1 collection.
var xmlManifests = []string{
	"xmltest/xmltest.xml",
	"sun/sun-valid.xml", "sun/sun-invalid.xml", "sun/sun-not-wf.xml",
	"oasis/oasis.xml",
	"ibm/ibm_oasis_valid.xml", "ibm/ibm_oasis_invalid.xml", "ibm/ibm_oasis_not-wf.xml",
	"eduni/xml-1.1/xml11.xml",
	"eduni/namespaces/1.0/rmt-ns10.xml", "eduni/namespaces/1.1/rmt-ns11.xml",
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
	Edition   string `xml:"EDITION,attr"`
}

// fetchCorpus builds testing/corpus/<format>/<verdict>/ by downloading the
// W3C XML conformance suite (classified by its manifests into xml/<verdict>/)
// and the OOXML reference files — all organized by file type. Idempotent: a
// corpus already present is left in place.
func fetchCorpus() error {
	for _, f := range formats {
		for _, v := range verdictDirs {
			os.MkdirAll(filepath.Join(testingDir, f.name, v), 0o755)
		}
	}

	if xmlCorpusPresent() {
		fmt.Println("xml:  corpus already present")
	} else if root, err := downloadXMLConf(); err != nil {
		fmt.Println("xml: ", err)
	} else {
		n := classifyXML(root)
		os.RemoveAll(filepath.Dir(root))
		fmt.Printf("xml:  %d test files -> xml/<verdict>/\n", n)
	}

	downloadOOXML()
	return nil
}

func xmlCorpusPresent() bool {
	e, _ := os.ReadDir(filepath.Join(testingDir, "xml", "not-wf"))
	return len(e) > 0
}

// hasExternalSubset reports whether a real DOCTYPE references an external
// subset (a SYSTEM or PUBLIC external identifier). We do not load external
// subsets, so an invalid test that has one comes back CANNOT_VALIDATE rather
// than INVALID. Comments / PIs / CDATA are skipped so a "<!DOCTYPE" buried in
// one (as in some OASIS production tests) does not count.
func hasExternalSubset(b []byte) bool {
	s := string(b)
	for i := 0; i < len(s); {
		switch {
		case strings.HasPrefix(s[i:], "<!--"):
			j := strings.Index(s[i+4:], "-->")
			if j < 0 {
				return false
			}
			i += 4 + j + 3
		case strings.HasPrefix(s[i:], "<?"):
			j := strings.Index(s[i+2:], "?>")
			if j < 0 {
				return false
			}
			i += 2 + j + 2
		case strings.HasPrefix(s[i:], "<![CDATA["):
			j := strings.Index(s[i+9:], "]]>")
			if j < 0 {
				return false
			}
			i += 9 + j + 3
		case strings.HasPrefix(s[i:], "<!DOCTYPE"):
			// The external ID, if any, is the SYSTEM/PUBLIC keyword before the
			// internal subset '[' or the closing '>'.
			for j := i + len("<!DOCTYPE"); j < len(s) && s[j] != '[' && s[j] != '>'; j++ {
				if strings.HasPrefix(s[j:], "SYSTEM") || strings.HasPrefix(s[j:], "PUBLIC") {
					return true
				}
			}
			return false
		default:
			i++
		}
	}
	return false
}

// classifyXML copies each conformance test file from the suite at root into
// testing/corpus/xml/<verdict>/, filtered to the subset this parser targets
// (XML 1.0 5th edition + 1.1; no namespaces or external entities).
func classifyXML(root string) int {
	count := 0
	for _, m := range xmlManifests {
		mp := filepath.Join(root, m)
		data, err := os.ReadFile(mp)
		if err != nil {
			continue
		}
		var grp tcGroup
		dec := xml.NewDecoder(bytes.NewReader(data))
		dec.Strict = false
		if dec.Decode(&grp) != nil {
			continue
		}
		coll := strings.SplitN(m, "/", 2)[0]
		var tests []resolvedT
		collectT(&grp, filepath.Dir(mp), &tests)
		for _, rt := range tests {
			verdict, skip := classifyTest(rt.t)
			if skip {
				continue
			}
			b, err := os.ReadFile(rt.path)
			if err != nil {
				continue
			}
			// In validating mode a no-DTD "invalid" document is correctly
			// rejected (nothing declares its elements), and internal parameter
			// entities are expanded, so those are kept. What we still cannot
			// validate is a DTD with an external subset: such an invalid test
			// comes back CANNOT_VALIDATE rather than INVALID, so it is skipped.
			// (External-parameter-entity tests are already excluded by the
			// ENTITIES filter above.)
			if verdict == "invalid" && hasExternalSubset(b) {
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

// classifyTest returns the verdict folder for a test, or skip=true for tests
// outside this parser's scope: external entities, 4th-edition-only character
// tests, "error"-type tests, and the namespace tests written for a
// non-namespace processor (NAMESPACE="no"). The namespace-recommendation tests
// are kept: we apply namespaces integrally.
func classifyTest(t tcTest) (verdict string, skip bool) {
	if t.Namespace == "no" {
		return "", true
	}
	if t.Edition != "" && !strings.Contains(" "+t.Edition+" ", " 5 ") {
		return "", true
	}
	if t.Entities != "" && t.Entities != "none" {
		return "", true
	}
	switch t.Type {
	case "valid", "invalid", "not-wf":
		return t.Type, false
	}
	return "", true
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

// --- W3C XML conformance-suite download + OOXML sparse checkout (was download.go) ---

const xmlconfURL = "https://www.w3.org/XML/Test/xmlts20130923.zip"

// downloadXMLConf fetches the W3C XML Conformance Test Suite zip into a
// temporary directory and returns the path to its xmlconf/ root. The caller
// classifies the test files out of it into testing/corpus/xml/<verdict>/ and
// removes the temp dir; the raw suite is not part of the corpus.
//
// The W3C endpoint intermittently 403s non-browser clients; set XMLCONF_ZIP to a
// locally-cached copy of the suite zip to run the conformance gate offline or
// when the download is blocked.
func downloadXMLConf() (string, error) {
	var b []byte
	if cached := os.Getenv("XMLCONF_ZIP"); cached != "" {
		fmt.Println("w3c:  using cached", cached)
		data, err := os.ReadFile(cached)
		if err != nil {
			return "", fmt.Errorf("read cached xmlconf: %w", err)
		}
		b = data
	} else {
		fmt.Println("w3c:  downloading", xmlconfURL)
		data, err := httpGet(xmlconfURL, 120*time.Second)
		if err != nil {
			return "", fmt.Errorf("download xmlconf: %w", err)
		}
		b = data
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

// ooxmlRepos are OOXML fixture sources: each is one blob-filtered sparse subtree
// of generated or hand-authored, well-formed reference containers. Several repos
// may target the same format so the corpus spans many producers (python-docx,
// Word-authored mammoth fixtures, PHPWord readers; XlsxWriter for spreadsheets).
// (Apache POI's test-data is deliberately excluded — it mixes valid and
// intentionally-malformed files without per-file expectations.)
var ooxmlRepos = []struct {
	repo    string
	tag     string // short, collision-safe filename prefix identifying the source
	subdirs []string
	format  string
}{
	{"https://github.com/python-openxml/python-docx", "pydocx", []string{"tests", "features"}, "docx"},
	{"https://github.com/mwilliamson/mammoth.js", "mammoth", []string{"test/test-data"}, "docx"},
	{"https://github.com/PHPOffice/PHPWord", "phpword", []string{"samples/resources", "tests/PhpWordTests/_files/documents"}, "docx"},
	{"https://github.com/jmcnamara/XlsxWriter", "xlsxw", []string{"xlsxwriter/test/comparison/xlsx_files"}, "xlsx"},
}

// downloadOOXML sparse-checks-out the OOXML fixture subtrees and copies their
// containers into testing/corpus/<docx|xlsx>/valid/ (organized by file type,
// not source). A format's dir is fetched as a whole: if it already holds files
// the format is left in place, otherwise every repo for that format is checked
// out. Copied names are prefixed with the source tag so containers with the same
// base name across repos (empty.docx, …) do not collide. Best-effort: a source
// that fails is logged and skipped.
func downloadOOXML() {
	// Snapshot which formats were already populated before this run; a format's
	// dir is fetched as a whole, so the presence decision is made once up front
	// (not re-read after the first repo writes into the shared dir).
	present := map[string]bool{}
	for _, s := range ooxmlRepos {
		if entries, _ := os.ReadDir(filepath.Join(testingDir, s.format, "valid")); len(entries) > 0 {
			if !present[s.format] {
				fmt.Printf("ooxml: %s already present\n", s.format)
			}
			present[s.format] = true
		}
	}
	for _, s := range ooxmlRepos {
		if present[s.format] {
			continue // dir was already populated before this run
		}
		dst := filepath.Join(testingDir, s.format, "valid")
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
			if os.WriteFile(filepath.Join(dst, s.tag+"_"+filepath.Base(p)), b, 0o644) == nil {
				n++
			}
			return nil
		})
		os.RemoveAll(tmp)
		fmt.Printf("ooxml: %s/%s -> %d files\n", s.format, s.tag, n)
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

// --- HTTP helper shared by the fetchers ---

// httpGet fetches a URL with a timeout and a 5 MB cap, following redirects.
func httpGet(u string, timeout time.Duration) ([]byte, error) {
	c := &http.Client{Timeout: timeout}
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return nil, err
	}
	// A browser-like UA: many hosts reject unknown bot agents with 403,
	// which would shrink the real-world corpus for no good reason.
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36")
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 5<<20))
}

// --- W3C XSD test-suite download (was xsd.go) ---

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

// --- Real-world docx corpus (superdoc-dev/docx-corpus) ---

// docxCorpusDir is the corpus subfolder for real-world .docx files scraped from
// the public web by the superdoc-dev/docx-corpus project. Unlike the curated
// docx/ set (python-docx / mammoth / PHPWord, which gate), these are messy
// real-world documents, so the runner reports their parse rate rather than
// gating on it.
const docxCorpusDir = "docx-web"

// docxManifestURL lists every document in the dataset as a direct download URL
// (the files are not in the GitHub repo, which holds only the scraping
// pipeline; they are served from docxcorp.us). See docs/REFERENCES.md.
const docxManifestURL = "https://api.docxcorp.us/manifest"

const (
	docxCorpusCap         = 2000
	docxCorpusConcurrency = 32
	docxFetchTimeout      = 20 * time.Second
)

// downloadDocxCorpus fetches a sample of the superdoc-dev/docx-corpus dataset
// (736K+ real .docx files from Common Crawl) into testing/corpus/docx-web/. It
// reads the manifest (a hash-sorted list of download URLs, so a prefix is an
// unbiased sample), takes the first docxCorpusCap, and fetches them
// concurrently, keeping each response whose magic is the ZIP/OPC signature.
// Idempotent: a populated corpus is left untouched.
func downloadDocxCorpus() {
	dst := filepath.Join(testingDir, docxCorpusDir)
	if e, _ := os.ReadDir(dst); len(e) > 0 {
		fmt.Println("docx-web: corpus already present")
		return
	}
	body, err := httpGet(docxManifestURL, 60*time.Second)
	if err != nil {
		fmt.Printf("docx-web: could not fetch manifest (%v) — skipping\n", err)
		return
	}
	var urls []string
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "http") {
			urls = append(urls, line)
			if len(urls) >= docxCorpusCap {
				break
			}
		}
	}
	if len(urls) == 0 {
		fmt.Println("docx-web: manifest empty — skipping")
		return
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		fmt.Println("docx-web:", err)
		return
	}
	fmt.Printf("docx-web: %d candidate URLs; fetching with %d workers...\n", len(urls), docxCorpusConcurrency)
	n := fetchDocxInto(dst, urls)
	fmt.Printf("docx-web: %d .docx -> corpus/%s/ (from %d candidates)\n", n, docxCorpusDir, len(urls))
}

// fetchDocxInto downloads urls concurrently and writes each response that begins
// with the ZIP local-file-header magic (PK\x03\x04 — the OPC container) to dst,
// named after the URL's basename. Returns the count written.
func fetchDocxInto(dst string, urls []string) int {
	var written, attempts int64
	var wg sync.WaitGroup
	ch := make(chan string)
	worker := func() {
		defer wg.Done()
		for u := range ch {
			if k := atomic.AddInt64(&attempts, 1); k%500 == 0 {
				fmt.Printf("docx-web: %d/%d tried, %d kept\n", k, len(urls), atomic.LoadInt64(&written))
			}
			body, err := httpGet(u, docxFetchTimeout)
			if err != nil || len(body) < 4 || string(body[:4]) != "PK\x03\x04" {
				continue
			}
			name := path.Base(u)
			if name == "" || name == "." || name == "/" {
				continue
			}
			if os.WriteFile(filepath.Join(dst, name), body, 0o644) == nil {
				atomic.AddInt64(&written, 1)
			}
		}
	}
	for range docxCorpusConcurrency {
		wg.Add(1)
		go worker()
	}
	for _, u := range urls {
		ch <- u
	}
	close(ch)
	wg.Wait()
	return int(written)
}
