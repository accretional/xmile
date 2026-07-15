// Command testing fetches the corpus and runs every parser check over it, the
// single gate that replaces the old per-format harnesses. There is one parser
// (the service); this loops the corpus and calls it.
//
//	go run ./testing          # ensure the corpus, then run all checks (gates on xml + opc)
//	go run ./testing fetch    # (re)build the corpus only
//
// The checks, by kind:
//   - XML conformance (corpus/xml/<verdict>/): the base parser, validating; the
//     deterministic W3C set must be 100% — this GATES.
//   - Generate round-trip (corpus/xml/): parse -> Generate -> parse must be a
//     fixed point at the canonical infoset — this GATES.
//   - OPC packages (corpus/{docx,xlsx}/valid/): ProcessPackage unpacks each and
//     parses its parts, and every modeled part projects against its open schema —
//     this GATES (the bundled packages are deterministic).
//   - XSD suite (corpus/xsd/): CompileXSD over the W3C suite — reported.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"google.golang.org/protobuf/proto"

	xmlpb "github.com/accretional/xmile/proto/pb/xml"
	"github.com/accretional/xmile/service"
	"github.com/accretional/xmile/testing/progress"
)

const testingDir = "testing/corpus"

// formats drives both the fetcher (which builds corpus/<name>/<verdict>/) and
// the XML-conformance walk below.
var formats = []struct {
	name  string
	ext   string
	isZip bool
}{
	{"xml", ".xml", false},
	{"docx", ".docx", true},
	{"xlsx", ".xlsx", true},
}

var verdictDirs = []string{"valid", "invalid", "not-wf"}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "fetch" {
		fetchAll()
		return
	}
	ensureCorpus()
	if failed := runChecks(); failed {
		os.Exit(1)
	}
}

// ensureCorpus fetches any missing corpus piece (the corpus is normally cached,
// so this is a no-op in CI after the first build).
func ensureCorpus() {
	if empty("xml/not-wf") {
		fetchCorpus()
	}
	if empty("xsd") {
		downloadXSD()
	}
	if empty(docxCorpusDir) {
		downloadDocxCorpus()
	}
}

// fetchAll (re)builds the whole corpus.
func fetchAll() {
	fetchCorpus()
	downloadXSD()
	downloadDocxCorpus()
}

func empty(sub string) bool {
	e, _ := os.ReadDir(filepath.Join(testingDir, sub))
	return len(e) == 0
}

// runChecks runs every check and returns true if a gating check failed.
func runChecks() bool {
	gateFail := false

	fmt.Println("[xml] W3C conformance (gates):")
	if !checkXMLConformance() {
		gateFail = true
	}

	fmt.Println("\n[opc] docx/xlsx packages (gates):")
	if !checkOPC() {
		gateFail = true
	}

	fmt.Println("\n[opc-vocab] docx/xlsx part projection (gates):")
	if !checkOPCVocab() {
		gateFail = true
	}

	fmt.Println("\n[docx-web] real-world docx corpus (reported):")
	checkDocxCorpus()

	fmt.Println("\n[xsd] W3C XSD suite -> CompileXSD (reported):")
	checkXSD()

	fmt.Println("\n[generate] AST -> document round-trip over corpus/xml (gates):")
	if !checkGenerate() {
		gateFail = true
	}

	return gateFail
}

// --- Generate: parse -> generate -> parse fixed point, through the RPC ---

// checkGenerate verifies the Documents.Generate RPC is a faithful inverse of the
// parser over every well-formed xml corpus document: parse(b) yields the AST,
// Generate (the RPC) serializes it back, and parsing that output yields an equal
// AST — i.e. parse(Generate(parse(b))) == parse(b). Equality is at the canonical
// infoset level (the contract is infoset-equivalent, not byte-identical):
// consecutive character-data runs are coalesced (entity/char-reference expansion
// splits one run into several items, which Generate cannot and need not
// reproduce) and the encoding declaration is normalized (Generate emits UTF-8).
// Not-well-formed inputs are skipped; any other mismatch, or a generated document
// that fails to re-parse, GATES.
func checkGenerate() bool {
	var files []string
	for _, v := range verdictDirs {
		m, _ := filepath.Glob(filepath.Join(testingDir, "xml", v, "*.xml"))
		files = append(files, m...)
	}
	sort.Strings(files)
	if len(files) == 0 {
		fmt.Println("  (no xml corpus — run: go run ./testing fetch)")
		return true
	}
	return roundTripCorpus("generate", files)
}

// roundTripCorpus asserts parse(Generate(parse(b))) == parse(b) at the canonical
// infoset over files, through the Documents.Generate RPC. Not-well-formed inputs
// are skipped; any mismatch or a generated document that fails to re-parse GATES.
func roundTripCorpus(label string, files []string) bool {
	p, err := service.Default()
	if err != nil {
		fmt.Printf("  cannot start parser: %v\n", err)
		return false
	}
	srv, err := service.NewDocumentsServer()
	if err != nil {
		fmt.Printf("  cannot start Documents server: %v\n", err)
		return false
	}
	ctx := context.Background()

	roundTripped, skipped, failed := 0, 0, 0
	bar := progress.New(label, len(files))
	for _, fp := range files {
		bar.Inc()
		b, err := os.ReadFile(fp)
		if err != nil {
			continue
		}
		// Parse to the AST. A document the parser rejects (not-wf) has nothing to
		// generate, so it is skipped, not failed.
		x1, perr := p.Parse(string(b), false)
		if perr != nil {
			skipped++
			continue
		}
		// Serialize the AST back through the RPC, then re-parse it.
		g, err := srv.Generate(ctx, &xmlpb.GenerateRequest{Document: x1})
		if err != nil || g.GetError() != nil {
			failed++
			fmt.Printf("  [FAIL] %s: generate: %v %s\n", filepath.Base(fp), err, g.GetError().GetReason())
			continue
		}
		x2, perr := p.Parse(string(g.GetSource()), false)
		if perr != nil {
			failed++
			fmt.Printf("  [FAIL] %s: generated document does not re-parse: %v\n", filepath.Base(fp), perr)
			continue
		}
		if !proto.Equal(canonicalXML(x1), canonicalXML(x2)) {
			failed++
			fmt.Printf("  [FAIL] %s: round-trip AST differs\n", filepath.Base(fp))
			continue
		}
		roundTripped++
	}
	bar.Finish()
	fmt.Printf("  %s: %d round-tripped, %d skipped (not-wf), %d failed\n", label, roundTripped, skipped, failed)
	return failed == 0
}

// canonicalXML reduces an Xml AST to its canonical infoset form for round-trip
// comparison: the encoding declaration is cleared (Generate always emits UTF-8)
// and each element's content has consecutive character-data items coalesced and
// empty ones dropped (reference expansion splits a run into several text items;
// the coalesced run is the single character-data item the infoset defines).
func canonicalXML(x *xmlpb.Xml) *xmlpb.Xml {
	c, _ := proto.Clone(x).(*xmlpb.Xml)
	if d := c.GetXmlDecl(); d != nil {
		d.Encoding = ""
	}
	coalesceText(c.GetRoot())
	return c
}

func coalesceText(t *xmlpb.Tag) {
	if t == nil {
		return
	}
	var out []*xmlpb.ContentItem
	for _, ci := range t.GetContents() {
		switch it := ci.GetItem().(type) {
		case *xmlpb.ContentItem_Text:
			if n := len(out); n > 0 {
				if prev, ok := out[n-1].GetItem().(*xmlpb.ContentItem_Text); ok {
					prev.Text += it.Text
					continue
				}
			}
			out = append(out, ci)
		case *xmlpb.ContentItem_Child:
			coalesceText(it.Child)
			out = append(out, ci)
		default:
			out = append(out, ci)
		}
	}
	// Drop empty character-data items (a reference to an empty entity leaves one);
	// they carry no content and Generate emits nothing for them.
	final := out[:0]
	for _, ci := range out {
		if txt, ok := ci.GetItem().(*xmlpb.ContentItem_Text); ok && txt.Text == "" {
			continue
		}
		final = append(final, ci)
	}
	t.Contents = final
}

// --- XML conformance: the base parser over corpus/xml, validating, 100% gate ---

func checkXMLConformance() bool {
	type task struct{ path, want string }
	var tasks []task
	for _, v := range verdictDirs {
		dir := filepath.Join(testingDir, "xml", v)
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if !e.IsDir() {
				tasks = append(tasks, task{filepath.Join(dir, e.Name()), v})
			}
		}
	}
	if len(tasks) == 0 {
		fmt.Println("  (no xml corpus — run: go run ./testing fetch)")
		return true
	}

	pass := make([]bool, len(tasks))
	detail := make([]string, len(tasks))
	bar := progress.New("xml", len(tasks))
	idx := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < runtime.NumCPU(); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range idx {
				ok, d := checkXMLFile(tasks[i].path, tasks[i].want)
				pass[i], detail[i] = ok, d
				bar.Inc()
			}
		}()
	}
	for i := range tasks {
		idx <- i
	}
	close(idx)
	wg.Wait()
	bar.Finish()

	tally := map[string]*[2]int{} // verdict dir -> [pass, fail]
	shown := 0
	for i, t := range tasks {
		dir := dirOf(t.path)
		c := tally[dir]
		if c == nil {
			c = &[2]int{}
			tally[dir] = c
		}
		if pass[i] {
			c[0]++
		} else {
			c[1]++
			if shown < 30 {
				fmt.Printf("  [fail] %s: %s\n", t.path, detail[i])
				shown++
			}
		}
	}
	total, fails := 0, 0
	for _, k := range sortedKeys(tally) {
		c := tally[k]
		total += c[0] + c[1]
		fails += c[1]
		fmt.Printf("  xml/%-8s %5d ok %4d fail\n", k, c[0], c[1])
	}
	fmt.Printf("  xml TOTAL %d, %d fail (%.1f%%)\n", total, fails, pct(total-fails, total))
	return fails == 0
}

func checkXMLFile(path, want string) (bool, string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, "read: " + err.Error()
	}
	got := classify(string(data))
	return got == want, "got " + got
}

// classify parses src in validating mode and maps the outcome to a W3C verdict.
func classify(src string) string {
	p, err := service.Default()
	if err != nil {
		return "not-wf"
	}
	_, perr := p.Process(src, nil, true)
	switch perr.(type) {
	case nil:
		return "valid"
	case *service.ValidityError:
		return "invalid"
	case *service.CannotValidateError:
		return "cannot-validate"
	default:
		return "not-wf"
	}
}

// --- format vocabularies: compile the spec from formats/, project the docs ---

// --- OPC packages: ProcessPackage over docx/xlsx, gates on failure ---

func checkOPC() bool {
	var pkgs, parts, rels, failed int
	for _, label := range []string{"docx", "xlsx"} {
		var files []string
		for _, pat := range []string{
			filepath.Join(testingDir, label, "*."+label),
			filepath.Join(testingDir, label, "valid", "*."+label),
		} {
			m, _ := filepath.Glob(pat)
			files = append(files, m...)
		}
		sort.Strings(files)
		lp, lr, lf := 0, 0, 0
		for _, fp := range files {
			data, err := os.ReadFile(fp)
			if err != nil {
				continue
			}
			pkgs++
			pkg, perr := service.ProcessPackage(data)
			if perr != nil {
				failed++
				lf++
				fmt.Printf("  [FAIL] %s: %v\n", filepath.Base(fp), perr)
				continue
			}
			lp += len(pkg.Parts)
			lr += len(pkg.Relationships)
			for _, pt := range pkg.Parts {
				lr += len(pt.Rels)
			}
		}
		parts += lp
		rels += lr
		fmt.Printf("  %s: %d packages, %d parts, %d relationships, %d failed\n", label, len(files), lp, lr, lf)
	}
	fmt.Printf("  opc TOTAL %d packages, %d parts, %d relationships, %d failed\n", pkgs, parts, rels, failed)
	return failed == 0
}

// --- OPC vocab projection: project each package's modeled parts, gates ---
//
// The companion to checkOPC's well-formedness gate: docx/xlsx are now first-class
// formats/ specs that ride the compile->project path. For each valid package we
// project every part whose root local-name the format models (word/document.xml
// -> document, a worksheet -> worksheet, workbook -> workbook, sharedStrings ->
// sst) against the open docx/xlsx Schema. Open mode guarantees a valid part
// projects without error once the schema compiles and the root type exists, so a
// projection failure is a real regression — this GATES (0 failures).
func checkOPCVocab() bool {
	docx, derr := service.Format("docx")
	xlsx, xerr := service.Format("xlsx")
	if derr != nil || xerr != nil {
		fmt.Printf("  cannot load docx/xlsx formats: %v %v\n", derr, xerr)
		return false
	}

	var totalPkgs, totalParts, failed int
	for _, label := range []string{"docx", "xlsx"} {
		schema := docx
		if label == "xlsx" {
			schema = xlsx
		}
		var files []string
		for _, pat := range []string{
			filepath.Join(testingDir, label, "*."+label),
			filepath.Join(testingDir, label, "valid", "*."+label),
		} {
			m, _ := filepath.Glob(pat)
			files = append(files, m...)
		}
		sort.Strings(files)

		lp, lf := 0, 0
		bar := progress.New(label+" project", len(files))
		for _, fp := range files {
			bar.Inc()
			data, err := os.ReadFile(fp)
			if err != nil {
				continue
			}
			totalPkgs++
			pkg, perr := service.ProcessPackage(data)
			if perr != nil {
				// Well-formedness is checkOPC's gate; a package that fails to
				// unpack there cannot be projected here. Count it as a failure so
				// the two gates agree.
				failed++
				lf++
				fmt.Printf("  [FAIL] %s: unpack: %v\n", filepath.Base(fp), perr)
				continue
			}
			for _, pt := range pkg.Parts {
				root := pt.Document.GetRoot()
				if root == nil {
					continue
				}
				if !schema.HasRoot(localOf(root.GetName())) {
					continue // an unmodeled part (styles, theme, …)
				}
				if _, _, e := schema.Project(pt.Document); e != nil {
					failed++
					lf++
					fmt.Printf("  [FAIL] %s part %s: %v\n", filepath.Base(fp), pt.Name, e)
					continue
				}
				lp++
				totalParts++
			}
		}
		bar.Finish()
		fmt.Printf("  %s: %d packages, %d parts projected, %d failed\n", label, len(files), lp, lf)
	}
	fmt.Printf("  opc-vocab TOTAL %d packages, %d parts projected, %d failed\n", totalPkgs, totalParts, failed)
	return failed == 0
}

// --- docx-web: real-world docx corpus, reported (not gating) ---

// checkDocxCorpus runs ProcessPackage over the real-world docx-web corpus
// (superdoc-dev/docx-corpus) and reports the parse rate. It does NOT gate: these
// are messy documents scraped from the public web, so a malformed package is an
// expectation, not a parser bug — the curated docx/ set is the deterministic
// gate. Modeled parts are projected against the open docx schema as an extra
// signal of how much real-world WordprocessingML the format types.
func checkDocxCorpus() {
	files, _ := filepath.Glob(filepath.Join(testingDir, docxCorpusDir, "*.docx"))
	if len(files) == 0 {
		fmt.Println("  (no docx-web corpus — run: go run ./testing fetch)")
		return
	}
	sort.Strings(files)
	schema, _ := service.Format("docx")

	var parsed, failed, parts, projected int
	bar := progress.New("docx-web", len(files))
	for _, fp := range files {
		bar.Inc()
		data, err := os.ReadFile(fp)
		if err != nil {
			continue
		}
		pkg, perr := service.ProcessPackage(data)
		if perr != nil {
			failed++
			if failed <= 10 { // surface the messy ones; these don't gate
				fmt.Printf("  [warn] %s: %v\n", filepath.Base(fp), perr)
			}
			continue
		}
		parsed++
		if schema == nil {
			continue
		}
		for _, pt := range pkg.Parts {
			root := pt.Document.GetRoot()
			if root == nil || !schema.HasRoot(localOf(root.GetName())) {
				continue
			}
			parts++
			if _, _, e := schema.Project(pt.Document); e == nil {
				projected++
			}
		}
	}
	bar.Finish()
	rate := 100 * float64(parsed) / float64(len(files))
	fmt.Printf("  docx-web: %d packages, %d parsed (%.1f%%), %d failed, %d/%d modeled parts projected\n",
		len(files), parsed, rate, failed, projected, parts)
}

// localOf returns the local part of a possibly-prefixed element name.
func localOf(qname string) string {
	if i := strings.IndexByte(qname, ':'); i >= 0 {
		return qname[i+1:]
	}
	return qname
}

// --- XSD suite: CompileXSD over the W3C suite, reported ---

func checkXSD() {
	files, _ := filepath.Glob(filepath.Join(testingDir, "xsd", "*.xsd"))
	if len(files) == 0 {
		fmt.Println("  (no xsd corpus — run: go run ./testing fetch)")
		return
	}
	compiled := 0
	for _, fp := range files {
		if compileXSDOne(fp) {
			compiled++
		}
	}
	fmt.Printf("  %d/%d schemas compiled within the supported subset (%.1f%%) — reported, not gating\n",
		compiled, len(files), pct(compiled, len(files)))
}

func compileXSDOne(fp string) (ok bool) {
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	b, err := os.ReadFile(fp)
	if err != nil {
		return false
	}
	_, err = service.CompileXSDWithResolver(b, service.SchemaOptions{Package: "t"}, flatSiblingResolver(fp))
	return err == nil
}

// flatSiblingResolver resolves an xs:import / xs:include schemaLocation against
// the *flattened* corpus layout downloadXSD produces: a schema fetched from
// suite/sub/ipo.xsd lands as "suite__sub__ipo.xsd", and its
// schemaLocation="address.xsd" sibling lands as "suite__sub__address.xsd". So a
// location is resolved by swapping the compiling file's last "__"-segment for
// the (slash-flattened) location, then reading that sibling. A location that
// escapes the corpus or is unreadable returns an error, so CompileXSD skips it.
func flatSiblingResolver(fp string) service.XSDResolver {
	dir := filepath.Dir(fp)
	base := filepath.Base(fp)
	prefix := ""
	if i := strings.LastIndex(base, "__"); i >= 0 {
		prefix = base[:i+len("__")]
	}
	return func(location string) ([]byte, error) {
		if location == "" || strings.Contains(location, "://") {
			return nil, fmt.Errorf("not a local schema location: %q", location)
		}
		flat := strings.ReplaceAll(filepath.ToSlash(location), "/", "__")
		return os.ReadFile(filepath.Join(dir, prefix+flat))
	}
}

// --- helpers ---

func dirOf(path string) string { return filepath.Base(filepath.Dir(path)) }

func pct(n, total int) float64 {
	if total == 0 {
		return 100
	}
	return 100 * float64(n) / float64(total)
}

func sortedKeys[V any](m map[string]V) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}
