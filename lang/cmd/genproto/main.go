// Command genproto turns the xmile grammar sources into generated
// artifacts under proto/. It is the ONLY place that understands the
// grammar; the runtime parser carries no grammar knowledge.
//
// It produces:
//   - proto/dtd.proto         (the DTD model, one message per dtd.ebnf rule)
//   - lang/dtd.fdset          (FileDescriptorSet for the above)
//   - proto/pb/dtd/prefix_map.go, separator_map.go  (keyword/separator maps)
//
// (proto/xml.proto is hand-written, not generated — see ADR 0003.)
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/accretional/gluon/v2/compiler"
	"github.com/accretional/gluon/v2/metaparser"
	pb "github.com/accretional/gluon/v2/pb"
	"github.com/accretional/merge/descriptor"
)

func main() {
	flag.Parse()
	if err := genDTD(); err != nil {
		log.Fatalf("genproto dtd: %v", err)
	}
	if err := genLex("lang/xml.lex", "proto/pb/xml/lexical.go", "xmlpb"); err != nil {
		log.Fatalf("genproto xml.lex: %v", err)
	}
	if err := genLex("lang/dtd.lex", "proto/pb/dtd/lexical.go", "dtdpb"); err != nil {
		log.Fatalf("genproto dtd.lex: %v", err)
	}
	fmt.Println("genproto: OK")
}

func genDTD() error {
	const (
		ebnfPath  = "lang/dtd.ebnf"
		pkgName   = "dtd"
		goPkg     = "github.com/accretional/xmile/proto/pb/dtd;dtdpb"
		bundled   = "proto/dtd.proto"
		fdsetOut  = "lang/dtd.fdset"
		prefixOut = "proto/pb/dtd/prefix_map.go"
		sepOut    = "proto/pb/dtd/separator_map.go"
	)

	src, err := os.ReadFile(ebnfPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", ebnfPath, err)
	}
	doc := metaparser.WrapString(string(src))
	doc.Name = ebnfPath

	gd, err := metaparser.ParseEBNF(doc)
	if err != nil {
		return fmt.Errorf("ParseEBNF: %w", err)
	}
	fmt.Printf("parsed %d rules from %s\n", len(gd.GetRules()), ebnfPath)

	ast, err := compiler.GrammarToAST(gd)
	if err != nil {
		return fmt.Errorf("GrammarToAST: %w", err)
	}
	ast.Language = pkgName

	// The lexical token matchers (S, Name, Nmtoken, litDq, litSq) have no
	// productions — they are scanned by the runtime lexer. Lower their
	// references to scalar string fields so the compiler emits a `string`
	// rather than a dangling message reference.
	ast.Root = scalarizeTokens(ast.Root)
	ast.Root = compiler.CollapseCommaList(ast.Root)
	ast.Root = compiler.NameSequence(ast.Root)

	// First pass: harvest leading keyword prefixes and field separators
	// before StripKeywords discards them.
	prefixes := map[string][]string{}
	separators := map[string]string{}
	if _, err := compiler.Compile(ast, compiler.Options{
		Package:   pkgName,
		OnMessage: collectPrefixes(prefixes),
		OnField:   collectSeparators(separators),
	}); err != nil {
		return fmt.Errorf("compile (prefix pass): %w", err)
	}

	ast.Root = compiler.StripKeywords(ast.Root)

	fdp, err := compiler.Compile(ast, compiler.Options{
		Package:   pkgName,
		GoPackage: goPkg,
		FileName:  bundled,
	})
	if err != nil {
		return fmt.Errorf("compile: %w", err)
	}
	fmt.Printf("generated %d messages from %d rules\n", len(fdp.GetMessageType()), len(gd.GetRules()))

	if err := writeFDSet(fdsetOut, fdp); err != nil {
		return err
	}
	if err := writeProto(bundled, fdp); err != nil {
		return err
	}
	if err := writeFile(prefixOut, formatPrefixMap(prefixes, "dtdpb")); err != nil {
		return err
	}
	if err := writeFile(sepOut, formatSeparatorMap(separators, "dtdpb")); err != nil {
		return err
	}
	fmt.Printf("wrote %s, %s (%d prefixes), %s (%d separators)\n",
		bundled, prefixOut, len(prefixes), sepOut, len(separators))
	return nil
}

func collectPrefixes(prefixes map[string][]string) func(string, *pb.ASTNode) {
	return func(fqn string, node *pb.ASTNode) {
		if node.GetKind() == compiler.KindTerminal {
			prefixes[fqn] = []string{node.GetValue()}
			return
		}
		var kids []*pb.ASTNode
		switch node.GetKind() {
		case compiler.KindSequence:
			kids = node.GetChildren()
		case compiler.KindRule:
			body := node.GetChildren()
			if len(body) == 1 && body[0].GetKind() == compiler.KindSequence {
				kids = body[0].GetChildren()
			}
		}
		var toks []string
		for _, c := range kids {
			if c.GetKind() != compiler.KindTerminal {
				break
			}
			toks = append(toks, c.GetValue())
		}
		if len(toks) > 0 {
			prefixes[fqn] = toks
		}
	}
}

func collectSeparators(separators map[string]string) func(string, string, *pb.ASTNode) {
	return func(parent, name string, node *pb.ASTNode) {
		if node.GetKind() == compiler.KindRepetition && node.GetValue() != "" {
			separators[parent+"."+name] = node.GetValue()
		}
	}
}

func writeFDSet(path string, fdp *descriptorpb.FileDescriptorProto) error {
	set := &descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{fdp}}
	blob, err := proto.Marshal(set)
	if err != nil {
		return fmt.Errorf("marshal fdset: %w", err)
	}
	return writeBytes(path, blob)
}

func writeProto(path string, fdp *descriptorpb.FileDescriptorProto) error {
	protoSrc, err := descriptor.ToString(fdp)
	if err != nil {
		return fmt.Errorf("descriptor.ToString: %w", err)
	}
	return writeFile(path, protoSrc)
}

func writeFile(path, content string) error  { return writeBytes(path, []byte(content)) }
func writeBytes(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

func formatPrefixMap(prefixes map[string][]string, pkg string) string {
	keys := sortedKeys(prefixes)
	var b strings.Builder
	b.WriteString("// Code generated by genproto. DO NOT EDIT.\n\n")
	fmt.Fprintf(&b, "package %s\n\n", pkg)
	b.WriteString("// MessagePrefix maps a fully-qualified proto message name to the\n")
	b.WriteString("// leading terminal tokens StripKeywords removed from its schema.\n")
	b.WriteString("var MessagePrefix = map[string][]string{\n")
	for _, k := range keys {
		fmt.Fprintf(&b, "\t%s: {", strconv.Quote(k))
		for i, tok := range prefixes[k] {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(strconv.Quote(tok))
		}
		b.WriteString("},\n")
	}
	b.WriteString("}\n")
	return b.String()
}

func formatSeparatorMap(separators map[string]string, pkg string) string {
	keys := make([]string, 0, len(separators))
	for k := range separators {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString("// Code generated by genproto. DO NOT EDIT.\n\n")
	fmt.Fprintf(&b, "package %s\n\n", pkg)
	b.WriteString("// FieldSeparator maps \"parentFQN.fieldName\" to the literal that\n")
	b.WriteString("// CollapseCommaList dropped when rewriting \"X (SEP X)*\" to a repeated field.\n")
	b.WriteString("var FieldSeparator = map[string]string{\n")
	for _, k := range keys {
		fmt.Fprintf(&b, "\t%s: %s,\n", strconv.Quote(k), strconv.Quote(separators[k]))
	}
	b.WriteString("}\n")
	return b.String()
}

func sortedKeys(m map[string][]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
