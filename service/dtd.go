package service

import (
	"sync"

	pb "github.com/accretional/gluon/v2/pb"

	"github.com/accretional/xmile/lang"
	dtdpb "github.com/accretional/xmile/proto/pb/dtd"
)

var (
	dtdOnce sync.Once
	dtdP    *Parser
	dtdErr  error
)

// dtdParser returns the process-wide DTD parser (lang/dtd.ebnf +
// proto/pb/dtd lexical table).
func dtdParser() (*Parser, error) {
	dtdOnce.Do(func() {
		dtdP, dtdErr = NewParser(lang.DTDGrammar, dtdpb.Lexical)
	})
	return dtdP, dtdErr
}

// parseDTD runs the second pass: it parses the DOCTYPE body (the dtd_text
// span captured by the main parse) with the DTD grammar and returns the DTD
// CST. A parse failure — a malformed declaration, illegal name, bad content
// model, etc. — is a not-well-formedness error. A nil root (no DOCTYPE) is
// fine and returns (nil, nil).
func parseDTD(root *pb.ASTNode) (*pb.ASTNode, error) {
	span := firstDescendant(root, "dtd_text")
	if span == nil {
		return nil, nil
	}
	dp, err := dtdParser()
	if err != nil {
		return nil, &WFError{Msg: err.Error()}
	}
	cst, err := dp.ParseCST(span.GetValue())
	if err != nil {
		return nil, &WFError{Msg: "malformed DOCTYPE: " + err.Error()}
	}
	return cst.GetRoot(), nil
}
