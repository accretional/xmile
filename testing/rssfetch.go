package main

import (
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

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
