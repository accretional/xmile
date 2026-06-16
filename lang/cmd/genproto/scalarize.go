package main

import (
	"github.com/accretional/gluon/v2/compiler"
	pb "github.com/accretional/gluon/v2/pb"
)

// dtdTokenMatchers are the lexical runs in lang/dtd.ebnf that have no
// production — they are scanned by the runtime lexer (see lang/dtd.lex),
// not parsed structurally. In the proto they become plain `string` fields.
var dtdTokenMatchers = map[string]bool{
	"S":            true,
	"Name":         true,
	"Nmtoken":      true,
	"litDq":        true,
	"litSq":        true,
	"comment_text": true,
	"pi_text":      true,
}

// scalarizeTokens replaces every nonterminal reference to a token-matcher
// name with a scalar node, so the compiler lowers it to a proto3 `string`
// field rather than a (dangling) message reference. The input is not
// mutated; a deep copy is returned.
func scalarizeTokens(root *pb.ASTNode) *pb.ASTNode {
	if root == nil {
		return nil
	}
	if root.GetKind() == compiler.KindNonterminal && dtdTokenMatchers[root.GetValue()] {
		return &pb.ASTNode{Kind: compiler.KindScalar, Value: root.GetValue()}
	}
	kids := make([]*pb.ASTNode, 0, len(root.GetChildren()))
	for _, c := range root.GetChildren() {
		kids = append(kids, scalarizeTokens(c))
	}
	return &pb.ASTNode{Kind: root.GetKind(), Value: root.GetValue(), Children: kids}
}
