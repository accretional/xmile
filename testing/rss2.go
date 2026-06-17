package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

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
