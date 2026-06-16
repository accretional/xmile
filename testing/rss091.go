package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// rss091Dir is the corpus folder for the (near-extinct) RSS 0.91 vocabulary.
// Unlike the format/<verdict>/ corpora, it is a flat folder of real 0.91 feeds
// used by the schema-compile harness (testing/schema-compile), not the
// well-formedness corpus check.
const rss091Dir = "rss0.91"

// rss091Sources are real-world RSS 0.91 feeds: the canonical RSS Advisory
// Board sample plus Wayback-archived originals from publishers that served
// 0.91 (~2000–2001). RSS 0.91 is effectively extinct on the live web, so the
// archived snapshots are pinned by timestamp; the Wayback `id_` modifier
// returns the original bytes unmodified.
var rss091Sources = []struct{ name, url string }{
	{"feed_01_sample.xml", "https://www.rssboard.org/files/sample-rss-091.xml"},
	{"feed_02_linuxtoday.xml", "https://web.archive.org/web/20010312020959id_/http://linuxtoday.com:80/backend/biglt.rss"},
	{"feed_03_xmlcom.xml", "https://web.archive.org/web/20001119025100id_/http://www.xml.com:80/xml/news.rss"},
	{"feed_04_dictionary.xml", "https://web.archive.org/web/20001202143100id_/http://www.dictionary.com:80/wordoftheday/wotd.rss"},
	{"feed_05_newsforge.xml", "https://web.archive.org/web/20010302130417id_/http://www.newsforge.com:80/newsforge.rss"},
}

// downloadRSS091 fetches the RSS 0.91 corpus into testing/corpus/rss0.91/.
// Idempotent (skips when already populated) and best-effort (a source that
// fails or no longer serves 0.91 is logged and skipped).
func downloadRSS091() {
	dst := filepath.Join(testingDir, rss091Dir)
	if e, _ := os.ReadDir(dst); len(e) > 0 {
		fmt.Println("rss0.91: corpus already present")
		return
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		fmt.Println("rss0.91:", err)
		return
	}
	n := 0
	for _, s := range rss091Sources {
		body, err := httpGet(s.url, 30*time.Second)
		if err != nil {
			fmt.Printf("rss0.91: %s skipped (%v)\n", s.name, err)
			continue
		}
		if !strings.Contains(string(body), `version="0.91"`) {
			fmt.Printf("rss0.91: %s skipped (not 0.91)\n", s.name)
			continue
		}
		if os.WriteFile(filepath.Join(dst, s.name), body, 0o644) == nil {
			n++
		}
	}
	fmt.Printf("rss0.91: %d feeds -> corpus/%s/\n", n, rss091Dir)
}
