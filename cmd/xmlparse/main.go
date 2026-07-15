// Command xmlparse processes an XML file (or stdin) with the service parser and
// prints the result as textproto. With no -schema it prints the generic XML AST
// (the homogeneous Tag tree); with -schema it prints the typed tree the document
// projects into. -cst prints the raw concrete syntax tree instead. Not-well-
// formed input ("not well-formed: ...") and invalid input ("invalid: ...") both
// exit non-zero.
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
	validate := flag.Bool("validate", false, "validate against the DTD (validating mode; generic XML only)")
	schema := flag.String("schema", "", "project against a registered format (e.g. docx); empty = generic XML")
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

	sch, serr := service.Format(*schema)
	if serr != nil {
		fmt.Fprintln(os.Stderr, serr)
		os.Exit(1)
	}
	res, perr := p.Process(string(src), sch, *validate)
	if perr != nil {
		// WFError / ValidityError / CannotValidateError each format their own prefix.
		fmt.Fprintln(os.Stderr, perr)
		os.Exit(1)
	}
	fmt.Print(prototext.Format(res.Document))
}
