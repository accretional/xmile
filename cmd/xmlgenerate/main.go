// Command xmlgenerate round-trips an XML document through the AST: it parses a
// file (or stdin) into the generic XML AST and serializes it back to bytes,
// exercising the Documents service's Process then Generate RPCs (the same path
// the round-trip gate uses). The output is infoset-equivalent to the input — a
// faithful regeneration, not necessarily byte-identical. Not-well-formed input
// exits non-zero.
//
// A format's typed tree is intentionally not a generate input: it is a read-only
// projection that may drop unmodeled markup, so only the lossless generic AST is
// serialized. (xmlparse shows the AST; xmlgenerate writes the document back.)
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/accretional/xmile/service"
	xmlpb "github.com/accretional/xmile/proto/pb/xml"
)

func main() {
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
	srv, err := service.NewDocumentsServer()
	if err != nil {
		fmt.Fprintln(os.Stderr, "init:", err)
		os.Exit(1)
	}

	// Parse to the generic XML AST, then serialize it back via the Generate RPC.
	x, perr := p.Parse(string(src), false)
	if perr != nil {
		fmt.Fprintln(os.Stderr, perr)
		os.Exit(1)
	}
	gresp, err := srv.Generate(context.Background(), &xmlpb.GenerateRequest{Document: x})
	if err != nil {
		fmt.Fprintln(os.Stderr, "generate:", err)
		os.Exit(1)
	}
	if e := gresp.GetError(); e != nil {
		fmt.Fprintln(os.Stderr, e.GetReason())
		os.Exit(1)
	}
	os.Stdout.Write(gresp.GetSource())
}
