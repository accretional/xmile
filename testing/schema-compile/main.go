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
	for _, fp := range files {
		base := filepath.Base(fp)
		data, err := os.ReadFile(fp)
		if err != nil {
			fail++
			fmt.Printf("[fail] %s: read: %v\n", base, err)
			continue
		}
		doc, err := parser.Parse(string(data))
		if err != nil {
			fail++
			fmt.Printf("[fail] %s: not well-formed: %v\n", base, err)
			continue
		}
		root := doc.GetRoot()
		if root.GetName() != "rss" {
			fail++
			fmt.Printf("[fail] %s: root <%s>, want <rss>\n", base, root.GetName())
			continue
		}

		msg := dynamicpb.NewMessage(rssDesc)
		unknown := dedup(service.ProjectTag(root, rssDesc, msg))
		if _, err := proto.Marshal(msg); err != nil {
			fail++
			fmt.Printf("[fail] %s: marshal projected message: %v\n", base, err)
			continue
		}
		items := itemCount(msg)
		totalItems += items
		switch {
		case len(unknown) > 0:
			fail++
			fmt.Printf("[fail] %s: %d items but %d out-of-vocabulary: %v\n", base, items, len(unknown), unknown)
		case items == 0:
			fail++
			fmt.Printf("[fail] %s: projected no items\n", base)
		default:
			pass++
			if *verbose {
				fmt.Printf("[pass] %s: %d items, full schema coverage\n", base, items)
			}
		}
	}

	fmt.Printf("\nschema-compile: %d/%d feeds projected with full coverage, %d items total\n", pass, pass+fail, totalItems)
	if fail > 0 {
		return fmt.Errorf("%d feed(s) did not project cleanly through the generated schema", fail)
	}
	return nil
}

// itemCount returns the number of channel items projected into rss.
func itemCount(rss protoreflect.Message) int {
	cf := rss.Descriptor().Fields().ByName("channel")
	if cf == nil || !rss.Has(cf) {
		return 0
	}
	ch := rss.Get(cf).Message()
	itf := ch.Descriptor().Fields().ByName("item")
	if itf == nil || !itf.IsList() {
		return 0
	}
	return ch.Get(itf).List().Len()
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
