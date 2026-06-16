// Command schema-compile closes the DTD-as-schema loop on real data: it
// compiles the RSS 0.91 DTD (testing/schema-compile/rss.dtd) into a proto
// descriptor via service.CompileDTD, then uses that descriptor to parse the
// real-world RSS 0.91 corpus under testing/corpus/rss0.91/ — each feed is
// parsed by the xmile parser and projected into a dynamic message built from
// the generated schema. A genuine 0.91 feed must project with full coverage
// (no element/attribute outside the schema). Run by test.sh; also:
//
//	go run ./testing/schema-compile [-v]
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/accretional/xmile/service"
	"github.com/accretional/xmile/testing/progress"
)

const (
	dtdPath   = "testing/schema-compile/rss.dtd"
	corpusDir = "testing/corpus/rss0.91"
)

var verbose = flag.Bool("v", false, "print every feed, not just failures")

func main() {
	flag.Parse()
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "schema-compile:", err)
		os.Exit(1)
	}
}

func run() error {
	// 1. Compile the DTD into a proto descriptor and link it (no network).
	dtd, err := os.ReadFile(dtdPath)
	if err != nil {
		return fmt.Errorf("read DTD: %w", err)
	}
	fdp, err := service.CompileDTD(dtd, service.SchemaOptions{Package: "rss", FileName: "rss.proto"})
	if err != nil {
		return fmt.Errorf("compile DTD: %w", err)
	}
	fd, err := protodesc.NewFile(fdp, new(protoregistry.Files))
	if err != nil {
		return fmt.Errorf("generated descriptor does not link: %w", err)
	}
	rssDesc := fd.Messages().ByName("Rss")
	if rssDesc == nil {
		return fmt.Errorf("generated schema has no Rss message")
	}
	fmt.Printf("schema-compile: rss.dtd -> %d proto messages (package %q)\n", fd.Messages().Len(), fd.Package())

	// 2. Parse the real-world 0.91 corpus through the generated schema.
	parser, err := service.Default()
	if err != nil {
		return fmt.Errorf("parser: %w", err)
	}
	files, _ := filepath.Glob(filepath.Join(corpusDir, "*.xml"))
	sort.Strings(files)
	if len(files) == 0 {
		fmt.Printf("schema-compile: WARNING no corpus under %s — run: go run ./testing %s\n", corpusDir, "rss0.91")
		fmt.Println("schema-compile: schema compiled+linked OK; projection skipped (no corpus)")
		return nil
	}

	pass, fail, totalItems := 0, 0, 0
	var report []string
	bar := progress.New("project", len(files))
	for _, fp := range files {
		line, ok, items := checkFeed(parser, rssDesc, filepath.Base(fp), fp)
		totalItems += items
		if ok {
			pass++
		} else {
			fail++
		}
		if line != "" {
			report = append(report, line)
		}
		bar.Inc()
	}
	bar.Finish()
	for _, line := range report {
		fmt.Println(line)
	}

	fmt.Printf("\nschema-compile: %d/%d feeds projected with full coverage, %d items total\n", pass, pass+fail, totalItems)
	if fail > 0 {
		return fmt.Errorf("%d feed(s) did not project cleanly through the generated schema", fail)
	}
	return nil
}

// checkFeed parses one corpus feed and projects it into a fresh Rss message
// built from the generated descriptor. It returns a report line (empty for a
// silent pass), whether the feed projected cleanly, and its item count.
func checkFeed(parser *service.Parser, rssDesc protoreflect.MessageDescriptor, base, fp string) (string, bool, int) {
	data, err := os.ReadFile(fp)
	if err != nil {
		return fmt.Sprintf("[fail] %s: read: %v", base, err), false, 0
	}
	doc, err := parser.Parse(string(data))
	if err != nil {
		return fmt.Sprintf("[fail] %s: not well-formed: %v", base, err), false, 0
	}
	root := doc.GetRoot()
	if root.GetName() != "rss" {
		return fmt.Sprintf("[fail] %s: root <%s>, want <rss>", base, root.GetName()), false, 0
	}
	msg := dynamicpb.NewMessage(rssDesc)
	unknown := dedup(service.ProjectTag(root, rssDesc, msg))
	if _, err := proto.Marshal(msg); err != nil {
		return fmt.Sprintf("[fail] %s: marshal projected message: %v", base, err), false, 0
	}
	items := itemCount(msg)
	switch {
	case len(unknown) > 0:
		return fmt.Sprintf("[fail] %s: %d items but %d out-of-vocabulary: %v", base, items, len(unknown), unknown), false, items
	case items == 0:
		return fmt.Sprintf("[fail] %s: projected no items", base), false, 0
	default:
		line := ""
		if *verbose {
			line = fmt.Sprintf("[pass] %s: %d items, full schema coverage", base, items)
		}
		return line, true, items
	}
}

// itemCount returns the number of channel items projected into rss. The
// channel's children are a repeated oneof wrapper (`entry`), so an item is an
// entry whose oneof selects the `item` variant.
func itemCount(rss protoreflect.Message) int {
	cf := rss.Descriptor().Fields().ByName("channel")
	if cf == nil || !rss.Has(cf) {
		return 0
	}
	ch := rss.Get(cf).Message()
	ef := ch.Descriptor().Fields().ByName("entry")
	if ef == nil || !ef.IsList() {
		return 0
	}
	entries := ch.Get(ef).List()
	n := 0
	for i := 0; i < entries.Len(); i++ {
		e := entries.Get(i).Message()
		if iv := e.Descriptor().Fields().ByName("item"); iv != nil && e.Has(iv) {
			n++
		}
	}
	return n
}

func dedup(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}
