// Package service parses XML 1.0 into the xml.proto AST and serves it over
// gRPC. It carries no grammar knowledge: structure comes from the embedded
// EBNF (package lang) and lexing from the generated table (proto/pb/xml),
// both driven through gluon and the generic lex engine.
package service

import (
	"fmt"
	"strings"
	"sync"

	"github.com/accretional/gluon/v2/metaparser"
	pb "github.com/accretional/gluon/v2/pb"

	"github.com/accretional/xmile/lang"
	"github.com/accretional/xmile/lex"
	xmlpb "github.com/accretional/xmile/proto/pb/xml"
)

// Parser parses XML 1.0 source into a gluon concrete syntax tree. It loads
// the grammar and builds the token matchers once.
type Parser struct {
	grammar  *pb.GrammarDescriptor
	matchers map[string]metaparser.TokenMatchFunc
}

var (
	defaultOnce sync.Once
	defaultP    *Parser
	defaultErr  error
)

// Default returns the process-wide XML 1.0 parser.
func Default() (*Parser, error) {
	defaultOnce.Do(func() {
		defaultP, defaultErr = NewParser(lang.XMLGrammar, xmlpb.Lexical)
	})
	return defaultP, defaultErr
}

// NewParser builds a parser from an EBNF grammar and a lexical table.
func NewParser(ebnf string, lexical map[string]lex.Matcher) (*Parser, error) {
	gd, err := metaparser.ParseEBNF(metaparser.WrapString(ebnf))
	if err != nil {
		return nil, fmt.Errorf("parse grammar: %w", err)
	}
	// Empty lex => gluon auto-skips nothing; XML whitespace is positional
	// and handled explicitly by the grammar's S / optS.
	gd.Lex = &pb.LexDescriptor{Name: "xml"}

	matchers := make(map[string]metaparser.TokenMatchFunc)
	for name, fn := range lex.Table(lexical) {
		matchers[name] = fn
	}
	return &Parser{grammar: gd, matchers: matchers}, nil
}

// ParseCST parses src into a gluon CST. The whole input must be consumed.
func (p *Parser) ParseCST(src string) (*pb.ASTDescriptor, error) {
	req := &pb.CstRequest{
		Grammar:  p.grammar,
		Document: metaparser.WrapString(src),
	}
	return metaparser.ParseCSTWithOptions(req, &metaparser.ParseOptions{
		DisableAutoComments: true,
		TokenMatchers:       p.matchers,
	})
}

// Parse parses src into the xml.proto AST. The error is non-nil exactly when
// src is rejected, as one of three types: *WFError (not well-formed),
// *ValidityError (DTD-invalid, only when validating), or *CannotValidateError
// (validating requested but the DTD cannot be fully read).
//
// validating selects the parser mode. When false (non-validating) any
// well-formed document is accepted regardless of its DTD. When true the
// document must have a DTD and satisfy it (a validating processor).
//
// Namespaces are applied integrally in both modes.
func (p *Parser) Parse(src string, validating bool) (*xmlpb.Xml, error) {
	return p.parse(src, validating, true)
}

// parse is Parse with an internal switch to suppress namespace checking. The
// entity-replacement well-formedness reparse uses checkNS=false: a fragment is
// checked out of its namespace context, where an in-scope prefix would look
// undeclared.
func (p *Parser) parse(src string, validating, checkNS bool) (*xmlpb.Xml, error) {
	src = normalizeEncoding(src)
	src = decodeDeclaredEncoding(src)
	is11 := detectVersion(src)
	// Check character legality before line-end normalization, which []rune-
	// decodes and would replace illegal bytes with U+FFFD.
	if off := firstIllegalChar(src, is11); off >= 0 {
		return nil, &WFError{Msg: "illegal XML character", Offset: int32(off)}
	}
	// Reject pathologically deep nesting before the recursive parser runs: past
	// this bound it would overflow the goroutine stack — an unrecoverable crash,
	// not an error (see limits.go).
	if nestingDepthExceeds(src, MaxNestingDepth) {
		return nil, &WFError{Msg: fmt.Sprintf("document nesting exceeds the maximum depth of %d (possible denial-of-service)", MaxNestingDepth)}
	}
	src = normalizeLineEnds(src, is11)
	cst, err := p.ParseCST(src)
	if err != nil {
		return nil, &WFError{Msg: err.Error()}
	}
	if err := checkWellFormed(cst.GetRoot(), is11); err != nil {
		return nil, err
	}
	dtdRoot, err := parseDTD(cst.GetRoot())
	if err != nil {
		return nil, err
	}
	if err := checkPubid(dtdRoot); err != nil {
		return nil, err
	}
	if err := checkDTDPI(dtdRoot); err != nil {
		return nil, err
	}
	info := buildDTDInfo(dtdRoot)
	if err := checkDTDRefs(dtdRoot, info, is11); err != nil {
		return nil, err
	}
	if err := checkDeclOrder(dtdRoot); err != nil {
		return nil, err
	}
	if err := p.checkEntities(cst.GetRoot(), info, is11); err != nil {
		return nil, err
	}
	doc := projectDocument(p, cst.GetRoot(), info, is11)
	// Attribute-value normalization by declared type (required for the infoset,
	// and for namespace declarations to compare correctly).
	normalizeAttrTypes(doc, dtdRoot)
	// Namespaces are integral: resolve and check them in both modes (unless this
	// is the entity-replacement reparse, whose fragment lacks the real context).
	if checkNS {
		if nerr := checkNamespaces(doc, is11); nerr != nil {
			return nil, nerr
		}
		if nerr := checkDTDNamespaceNames(dtdRoot); nerr != nil {
			return nil, nerr
		}
	}
	if !validating {
		return doc, nil
	}
	// Validating mode: the document must have a DTD and satisfy it. A document
	// with no DTD is invalid (nothing declares its elements). A DTD we cannot
	// fully read — an external subset or external parameter entities — yields
	// CannotValidate rather than a guess: a declaration could hide there, and
	// claiming "invalid" might wrongly reject a valid document.
	if dtdRoot == nil {
		return nil, &ValidityError{Msg: "document has no DTD to validate against"}
	}
	if info.hasExternal {
		return nil, &CannotValidateError{Msg: "DTD has an external subset"}
	}
	// Expand internal parameter entities for the validation view; bail to
	// CannotValidate if a reference cannot be resolved (external/undeclared PE).
	vRoot, vInfo, ok := p.expandedDTD(cst.GetRoot())
	if !ok {
		return nil, &CannotValidateError{Msg: "DTD uses external or undeclared parameter entities"}
	}
	if verr := validate(doc, buildModel(vRoot, vInfo), is11); verr != nil {
		return nil, verr
	}
	return doc, nil
}

// DumpCST renders a CST for debugging.
func DumpCST(n *pb.ASTNode, depth int) string {
	if n == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString(strings.Repeat("  ", depth))
	b.WriteString(n.GetKind())
	if v := n.GetValue(); v != "" {
		fmt.Fprintf(&b, " = %q", v)
	}
	b.WriteByte('\n')
	for _, c := range n.GetChildren() {
		b.WriteString(DumpCST(c, depth+1))
	}
	return b.String()
}
