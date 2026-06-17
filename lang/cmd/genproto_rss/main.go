// Command genproto_rss turns the RSS 2.0 schema grammar (lang/rss.ebnf) into
// the committed proto artifact (proto/rss.proto + lang/rss.fdset), the typed
// AST a parsed RSS feed is projected into (service/rss.go). It is the EBNF
// sibling of genproto: where genproto lowers dtd.ebnf (a grammar of DTDs),
// this lowers a grammar of one vocabulary, both through the same gluon engine
// (service.CompileGrammar -> compiler.Compile). The grammar is the only source
// of truth; this carries no RSS knowledge.
//
//	go run ./lang/cmd/genproto_rss
package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/accretional/merge/descriptor"

	"github.com/accretional/xmile/service"
)

const (
	ebnfPath = "lang/rss.ebnf"
	pkgName  = "rss"
	goPkg    = "github.com/accretional/xmile/proto/pb/rss;rsspb"
	protoOut = "proto/rss.proto"
	fdsetOut = "lang/rss.fdset"
)

func main() {
	if err := run(); err != nil {
		log.Fatalf("genproto_rss: %v", err)
	}
	fmt.Println("genproto_rss: OK")
}

func run() error {
	src, err := os.ReadFile(ebnfPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", ebnfPath, err)
	}

	fdp, err := service.CompileGrammar(src, service.SchemaOptions{
		Package:   pkgName,
		GoPackage: goPkg,
		FileName:  filepath.Base(protoOut),
	})
	if err != nil {
		return fmt.Errorf("compile grammar: %w", err)
	}
	fmt.Printf("compiled %s -> %d messages\n", ebnfPath, len(fdp.GetMessageType()))

	protoSrc, err := descriptor.ToString(fdp)
	if err != nil {
		return fmt.Errorf("descriptor.ToString: %w", err)
	}
	if err := writeBytes(protoOut, []byte(protoSrc)); err != nil {
		return err
	}

	set := &descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{fdp}}
	blob, err := proto.Marshal(set)
	if err != nil {
		return fmt.Errorf("marshal fdset: %w", err)
	}
	if err := writeBytes(fdsetOut, blob); err != nil {
		return err
	}
	fmt.Printf("wrote %s, %s\n", protoOut, fdsetOut)
	return nil
}

func writeBytes(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}
