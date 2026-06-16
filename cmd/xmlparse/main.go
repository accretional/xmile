// Command xmlparse parses an XML file (or stdin) with the service parser
// and prints the resulting AST as textproto. With -cst it prints the raw
// concrete syntax tree instead. Not-well-formed input ("not well-formed: ...")
// and DTD-invalid input ("invalid: ...") both exit non-zero.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"google.golang.org/protobuf/encoding/prototext"

	"github.com/accretional/xmile/service"
)

func main() {
	cst := flag.Bool("cst", false, "print the raw concrete syntax tree")
	validate := flag.Bool("validate", false, "validate against the DTD (validating mode)")
	flag.Parse()

	var src []byte
	var err error
	if args := flag.Args(); len(args) > 0 {
		src, err = os.ReadFile(args[0])
	} else {
		src, err = io.ReadAll(os.Stdin)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	p, err := service.Default()
	if err != nil {
		fmt.Fprintln(os.Stderr, "init:", err)
		os.Exit(1)
	}

	if *cst {
		tree, perr := p.ParseCST(string(src))
		if perr != nil {
			fmt.Fprintln(os.Stderr, "parse error:", perr)
			os.Exit(1)
		}
		fmt.Print(service.DumpCST(tree.GetRoot(), 0))
		return
	}

	doc, perr := p.Parse(string(src), *validate)
	if perr != nil {
		// WFError and ValidityError each format their own category prefix.
		fmt.Fprintln(os.Stderr, perr)
		os.Exit(1)
	}
	fmt.Print(prototext.Format(doc))
}
