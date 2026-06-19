package service

// generate_test.go — the self-contained gate for Generate: parse a feature-rich
// set of documents, serialize each back through the Documents.Generate RPC, and
// re-parse, asserting the AST is unchanged (parse(Generate(parse(b))) == parse(b)).
// The corpus runner exercises this over the whole W3C suite; these samples pin
// the specific constructs — xml declaration, an internal DTD subset, namespaces,
// CDATA, comments, PIs, prolog/epilog misc, and whitespace carried by references.

import (
	"context"
	"testing"

	"google.golang.org/protobuf/proto"

	xmlpb "github.com/accretional/xmile/proto/pb/xml"
)

func TestGenerateRoundTrip(t *testing.T) {
	samples := []struct {
		name, src string
	}{
		{"attrs + mixed content", `<?xml version="1.0"?><a x="1" y="two">hi<b/>there</a>`},
		{"internal DTD subset", `<?xml version="1.0" encoding="UTF-8"?><!DOCTYPE doc [<!ELEMENT doc (#PCDATA|b)*><!ATTLIST doc x CDATA #IMPLIED><!ELEMENT b EMPTY>]><doc x="1">hello<b/></doc>`},
		{"namespaces", `<r xmlns:n="urn:x" n:a="v"><n:c>text</n:c></r>`},
		{"cdata", `<a><![CDATA[<not> & markup]]></a>`},
		{"comment + pi in content", `<a><!--c--><?pi data?>x</a>`},
		{"prolog/epilog misc", `<!-- before --><?t instr?><root/><?after ?>`},
		{"whitespace via references", `<a tab="x&#9;y" nl="p&#10;q"/>`},
	}

	p, err := Default()
	if err != nil {
		t.Fatalf("Default: %v", err)
	}
	srv, err := NewDocumentsServer()
	if err != nil {
		t.Fatalf("NewDocumentsServer: %v", err)
	}
	ctx := context.Background()

	for _, s := range samples {
		t.Run(s.name, func(t *testing.T) {
			x1, perr := p.Parse(s.src, false)
			if perr != nil {
				t.Fatalf("parse: %v", perr)
			}
			resp, err := srv.Generate(ctx, &xmlpb.GenerateRequest{Document: x1})
			if err != nil {
				t.Fatalf("Generate RPC: %v", err)
			}
			if e := resp.GetError(); e != nil {
				t.Fatalf("generate: %s", e.GetReason())
			}
			out := resp.GetSource()
			x2, perr := p.Parse(string(out), false)
			if perr != nil {
				t.Fatalf("re-parse of %q: %v", out, perr)
			}
			if !proto.Equal(x1, x2) {
				t.Errorf("round-trip AST differs\n  generated: %q", out)
			}
		})
	}
}
