package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
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
// W3C XML conformance suite (classified by its manifests into xml/<verdict>/),
// the OOXML reference files, and real-world RSS feeds — all organized by file
// type. Idempotent: a corpus already present is left in place.
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
	fetchRSS()
	return nil
}

func xmlCorpusPresent() bool {
	e, _ := os.ReadDir(filepath.Join(testingDir, "xml", "not-wf"))
	return len(e) > 0
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
	os.MkdirAll(filepath.Join(testingDir, "rss", "not-wf"), 0o755)

	// Idempotent: if feeds are already fetched, don't hit the network again —
	// just re-check their classification against the current oracle (so an
	// oracle fix relabels the existing corpus without a volatile re-fetch).
	if rssCorpusPresent() {
		fmt.Println("rss:  corpus already present")
		reconcileRSS()
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

// rssCorpusPresent reports whether any rss feeds have already been fetched.
func rssCorpusPresent() bool {
	for _, v := range []string{"valid", "not-wf"} {
		e, _ := os.ReadDir(filepath.Join(testingDir, "rss", v))
		if len(e) > 0 {
			return true
		}
	}
	return false
}

// reconcileRSS re-evaluates the already-fetched feeds with the current
// reference oracle and moves any whose verdict changed, so the corpus labels
// stay correct after an oracle change without re-fetching from the network.
func reconcileRSS() {
	moved := 0
	for _, v := range []string{"valid", "not-wf"} {
		dir := filepath.Join(testingDir, "rss", v)
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			body, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				continue
			}
			want := "not-wf"
			if referenceWellFormed(body) {
				want = "valid"
			}
			if want != v {
				if os.Rename(filepath.Join(dir, e.Name()), filepath.Join(testingDir, "rss", want, e.Name())) == nil {
					moved++
				}
			}
		}
	}
	if moved > 0 {
		fmt.Printf("rss:  reclassified %d feed(s) to match the reference oracle\n", moved)
	} else {
		fmt.Println("rss:  corpus labels already consistent with the oracle")
	}
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
// accepts the document as well-formed. Two adjustments make it a more faithful
// conformance oracle than the stock decoder (both verified against libxml2):
//   - a CharsetReader for the non-UTF-8 encodings this project supports, so a
//     well-formed Latin-1 feed is judged on its structure rather than rejected
//     for a charset the bare decoder cannot read; and
//   - rejecting a "<?xml ...?>" processing instruction anywhere but the very
//     start of the document — elsewhere its target is the reserved name "xml"
//     (XML 1.0 §2.6), which the stock decoder otherwise tolerates.
func referenceWellFormed(b []byte) bool {
	dec := xml.NewDecoder(bytes.NewReader(b))
	dec.Strict = true
	dec.CharsetReader = referenceCharset
	first := true
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return true
		}
		if err != nil {
			return false
		}
		if pi, ok := tok.(xml.ProcInst); ok && strings.EqualFold(pi.Target, "xml") && !first {
			return false
		}
		first = false
	}
}

// referenceCharset lets the reference decoder read the encodings this project
// supports: UTF-8/ASCII pass through unchanged, and Latin-1 maps each byte to
// its Unicode rune. Any other declared encoding returns an error, so the
// decoder rejects it — the same scope as the runtime parser.
func referenceCharset(label string, input io.Reader) (io.Reader, error) {
	switch strings.ToLower(strings.TrimSpace(label)) {
	case "utf-8", "utf8", "us-ascii", "ascii", "":
		return input, nil
	case "iso-8859-1", "latin1", "latin-1", "iso8859-1", "iso_8859-1":
		data, err := io.ReadAll(input)
		if err != nil {
			return nil, err
		}
		var sb strings.Builder
		for _, c := range data {
			sb.WriteRune(rune(c))
		}
		return strings.NewReader(sb.String()), nil
	}
	return nil, fmt.Errorf("unsupported encoding %q", label)
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

// --- Real-world RSS 2.0 corpus at scale (was rss2.go) ---

// rss2Dir is the dedicated real-world RSS 2.0 corpus, fetched at scale for the
// rss-parse harness (testing/rss-parse). Unlike the small curated rss0.91 set,
// this aims for thousands of genuine RSS 2.0 feeds drawn from many publishers,
// so the projector is exercised against the long tail of real-world markup.
const rss2Dir = "rss2.0"

// rss2OPMLRepos are GitHub repos of OPML feed-list files spanning many topics
// and countries.
var rss2OPMLRepos = []string{
	"plenaryapp/awesome-rss-feeds",
	"kilimchoi/engineering-blogs",
}

// rss2TSVSources are raw URLs of tab-separated feed catalogs whose first column
// is a feed URL (e.g. tfederman/fountain-of-rss, a crawler's catalog of tens of
// thousands of live feeds). The largest, most diverse source.
var rss2TSVSources = []string{
	"https://raw.githubusercontent.com/tfederman/fountain-of-rss/main/feeds.tsv",
}

// rss2TSVCap bounds how many URLs are taken from each TSV catalog, so the fetch
// stays within a few minutes (the catalogs hold tens of thousands).
const rss2TSVCap = 5000

// rss2Versioned matches the <rss version="2.0"> signature so only genuine RSS
// 2.0 feeds are kept (Atom, RSS 1.0/RDF and 0.9x are dropped).
var rss2Versioned = regexp.MustCompile(`(?s)<rss\b[^>]*\bversion\s*=\s*["']2\.0["']`)

const (
	rss2Concurrency = 64
	rss2Timeout     = 8 * time.Second
)

// downloadRSS2 fetches as many real-world RSS 2.0 feeds as it can into
// testing/corpus/rss2.0/. It harvests candidate feed URLs from the OPML and TSV
// sources, then fetches them concurrently, keeping each response that is both
// XML-well-formed (by the encoding/xml reference oracle) and a version-2.0
// <rss> document. Idempotent: a populated corpus is left untouched.
func downloadRSS2() {
	dst := filepath.Join(testingDir, rss2Dir)
	if e, _ := os.ReadDir(dst); len(e) > 0 {
		fmt.Println("rss2.0: corpus already present")
		return
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		fmt.Println("rss2.0:", err)
		return
	}

	urls := gatherFeedURLs()
	if len(urls) == 0 {
		fmt.Println("rss2.0: no candidate feed URLs (network?) — skipping")
		return
	}
	fmt.Printf("rss2.0: %d candidate feed URLs; fetching with %d workers...\n", len(urls), rss2Concurrency)
	n := fetchInto(dst, urls, 0)
	fmt.Printf("rss2.0: %d RSS 2.0 feeds -> corpus/%s/ (from %d candidates)\n", n, rss2Dir, len(urls))
}

// gatherFeedURLs collects and de-duplicates candidate feed URLs from every
// source: OPML files in the source repos, then the TSV catalogs.
func gatherFeedURLs() []string {
	seen := map[string]bool{}
	var urls []string
	add := func(u string) {
		if u != "" && !seen[u] {
			seen[u] = true
			urls = append(urls, u)
		}
	}

	for _, repo := range rss2OPMLRepos {
		opmls := repoFilesRawURLs(repo, ".opml")
		if len(opmls) == 0 {
			fmt.Printf("rss2.0: %s -> no OPML files (skipped)\n", repo)
			continue
		}
		before := len(urls)
		for _, ou := range opmls {
			if body, err := httpGet(ou, 15*time.Second); err == nil {
				for _, u := range extractXMLUrls(body) {
					add(u)
				}
			}
		}
		fmt.Printf("rss2.0: %s -> %d OPML files, %d new feed URLs\n", repo, len(opmls), len(urls)-before)
	}

	for _, src := range rss2TSVSources {
		body, err := httpGet(src, 30*time.Second)
		if err != nil {
			fmt.Printf("rss2.0: could not fetch %s (%v)\n", src, err)
			continue
		}
		before := len(urls)
		taken := 0
		for _, line := range strings.Split(string(body), "\n") {
			if taken >= rss2TSVCap {
				break
			}
			first, _, _ := strings.Cut(line, "\t")
			first = strings.TrimSpace(first)
			if strings.HasPrefix(first, "http") {
				add(first)
				taken++
			}
		}
		fmt.Printf("rss2.0: TSV catalog -> %d new feed URLs (of %d taken)\n", len(urls)-before, taken)
	}

	return urls
}

// fetchInto fetches urls concurrently and writes each well-formed version-2.0
// <rss> response to dst as feed_NNNN.xml, numbering from startIdx. Returns the
// count written.
func fetchInto(dst string, urls []string, startIdx int) int {
	var written, attempts int64
	var wg sync.WaitGroup
	ch := make(chan string)
	worker := func() {
		defer wg.Done()
		for u := range ch {
			if k := atomic.AddInt64(&attempts, 1); k%500 == 0 {
				fmt.Printf("rss2.0: %d/%d tried, %d kept\n", k, len(urls), atomic.LoadInt64(&written))
			}
			body, err := httpGet(u, rss2Timeout)
			if err != nil || !rss2Versioned.Match(body) || !referenceWellFormed(body) {
				continue
			}
			idx := int(atomic.AddInt64(&written, 1)) - 1 + startIdx
			_ = os.WriteFile(filepath.Join(dst, fmt.Sprintf("feed_%04d.xml", idx)), body, 0o644)
		}
	}
	for range rss2Concurrency {
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

// repoFilesRawURLs returns raw.githubusercontent URLs for every file with the
// given extension in a GitHub repo, trying the master then main branch (repos
// differ), via the git-tree API.
func repoFilesRawURLs(repo, ext string) []string {
	for _, branch := range []string{"master", "main"} {
		body, err := httpGet(fmt.Sprintf("https://api.github.com/repos/%s/git/trees/%s?recursive=1", repo, branch), 15*time.Second)
		if err != nil {
			continue
		}
		var tree struct {
			Tree []struct {
				Path string `json:"path"`
				Type string `json:"type"`
			} `json:"tree"`
		}
		if json.Unmarshal(body, &tree) != nil {
			continue
		}
		var out []string
		for _, e := range tree.Tree {
			if e.Type == "blob" && strings.HasSuffix(strings.ToLower(e.Path), ext) {
				segs := strings.Split(e.Path, "/")
				for i := range segs {
					segs[i] = url.PathEscape(segs[i])
				}
				out = append(out, "https://raw.githubusercontent.com/"+repo+"/"+branch+"/"+strings.Join(segs, "/"))
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	return nil
}

// --- HTTP + OPML helpers shared by the RSS fetchers (was rssfetch.go) ---

// listRepoOPML returns the repo-relative paths of every .opml file in a
// GitHub repository (default branch), via the git-tree API.
func listRepoOPML(repo string) ([]string, error) {
	body, err := httpGet("https://api.github.com/repos/"+repo+"/git/trees/master?recursive=1", 15*time.Second)
	if err != nil {
		return nil, err
	}
	var tree struct {
		Tree []struct {
			Path string `json:"path"`
			Type string `json:"type"`
		} `json:"tree"`
	}
	if err := json.Unmarshal(body, &tree); err != nil {
		return nil, err
	}
	var out []string
	for _, e := range tree.Tree {
		if e.Type == "blob" && strings.HasSuffix(e.Path, ".opml") {
			out = append(out, e.Path)
		}
	}
	return out, nil
}

// rawURL builds a raw.githubusercontent.com URL, percent-encoding each path
// segment (the RSS repo has spaces and parentheses in filenames).
func rawURL(repo, path string) string {
	segs := strings.Split(path, "/")
	for i := range segs {
		segs[i] = url.PathEscape(segs[i])
	}
	return "https://raw.githubusercontent.com/" + repo + "/master/" + strings.Join(segs, "/")
}

// httpGet fetches a URL with a timeout and a 5 MB cap, following redirects.
func httpGet(u string, timeout time.Duration) ([]byte, error) {
	c := &http.Client{Timeout: timeout}
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return nil, err
	}
	// A browser-like UA: many feed hosts reject unknown bot agents with 403,
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

// extractXMLUrls pulls the xmlUrl="..." values out of an OPML document.
func extractXMLUrls(opml []byte) []string {
	var out []string
	s := string(opml)
	for {
		i := strings.Index(s, "xmlUrl=")
		if i < 0 {
			break
		}
		s = s[i+len("xmlUrl="):]
		if s == "" {
			break
		}
		q := s[0]
		if q != '"' && q != '\'' {
			continue
		}
		j := strings.IndexByte(s[1:], q)
		if j < 0 {
			break
		}
		u := html.UnescapeString(s[1 : 1+j])
		s = s[1+j:]
		if strings.HasPrefix(u, "http") {
			out = append(out, u)
		}
	}
	return out
}

// looksLikeXML reports whether a response body is plausibly an XML feed
// (and not an HTML error/landing page), without fully parsing it.
func looksLikeXML(b []byte) bool {
	n := len(b)
	if n > 256 {
		n = 256
	}
	t := strings.TrimSpace(strings.TrimPrefix(string(b[:n]), "\ufeff"))
	lt := strings.ToLower(t)
	if strings.HasPrefix(lt, "<!doctype html") || strings.HasPrefix(lt, "<html") {
		return false
	}
	return strings.HasPrefix(t, "<?xml") || strings.HasPrefix(t, "<rss") ||
		strings.HasPrefix(t, "<feed") || strings.HasPrefix(t, "<rdf")
}

// sanitizeName turns a repo path into a flat, filesystem-safe filename.
func sanitizeName(p string) string {
	r := strings.NewReplacer("/", "_", " ", "_", "(", "", ")", "")
	return r.Replace(p)
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
// gating on it — the docx analogue of the rss2.0 set.
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
