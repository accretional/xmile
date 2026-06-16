package service

import (
	"fmt"
	"strconv"
	"strings"

	pb "github.com/accretional/gluon/v2/pb"

	xmlpb "github.com/accretional/xmile/proto/pb/xml"
)

// WFError reports input that is not well-formed XML — either a grammar parse
// failure or a tree-level constraint (tag-name match, unique attributes, PI
// target, literal "]]>"). It maps to the W3C "not-wf" category.
type WFError struct {
	Msg    string
	Offset int32
}

func (e *WFError) Error() string {
	if e.Offset > 0 {
		return fmt.Sprintf("not well-formed (offset %d): %s", e.Offset, e.Msg)
	}
	return "not well-formed: " + e.Msg
}

// checkWellFormed runs the schema-independent well-formedness checks over a
// CST: tag-name equality, no duplicate attributes, PI target != "xml", and
// no literal "]]>" in character data. (Single-root and structural
// completeness are guaranteed by the grammar + EOF-complete parsing.)
func checkWellFormed(root *pb.ASTNode) error { return walkWF(root) }

func walkWF(n *pb.ASTNode) error {
	if n == nil {
		return nil
	}
	switch n.GetKind() {
	case "element":
		stag := directChild(n, "STag")
		etag := directChild(n, "ETag")
		if stag != nil && etag != nil {
			if sn, en := tagName(stag), tagName(etag); sn != en {
				return &WFError{
					Msg:    fmt.Sprintf("end tag </%s> does not match start tag <%s>", en, sn),
					Offset: etag.GetLocation().GetOffset(),
				}
			}
		}
	case "STag", "EmptyElemTag":
		if err := checkDupAttrs(n); err != nil {
			return err
		}
	case "CharData":
		if strings.Contains(n.GetValue(), "]]>") {
			return &WFError{Msg: `literal "]]>" in character data`, Offset: n.GetLocation().GetOffset()}
		}
	case "PI":
		if t := firstDescendant(n, "Name"); t != nil && strings.EqualFold(t.GetValue(), "xml") {
			return &WFError{Msg: `processing instruction target may not be "xml"`, Offset: n.GetLocation().GetOffset()}
		}
	case "CharRef":
		// WFC: Legal Character — a character reference must denote a legal
		// XML Char.
		if !xmlpb.Lexical["Char"].Contains(charRefRune(n)) {
			return &WFError{Msg: "character reference to an illegal character", Offset: n.GetLocation().GetOffset()}
		}
	}
	for _, c := range n.GetChildren() {
		if err := walkWF(c); err != nil {
			return err
		}
	}
	return nil
}

func checkDupAttrs(tag *pb.ASTNode) error {
	seen := map[string]bool{}
	for _, attr := range descendants(tag, "Attribute") {
		name := tagName(attr)
		if name == "" {
			continue
		}
		if seen[name] {
			return &WFError{
				Msg:    fmt.Sprintf("duplicate attribute %q", name),
				Offset: attr.GetLocation().GetOffset(),
			}
		}
		seen[name] = true
	}
	return nil
}

// checkPubid enforces the PubidChar constraint on every public identifier
// in a parsed DTD.
func checkPubid(dtdRoot *pb.ASTNode) error {
	if dtdRoot == nil {
		return nil
	}
	for _, pl := range descendants(dtdRoot, "PubidLiteral") {
		s := tokenText(pl, "litDq")
		if s == "" {
			s = tokenText(pl, "litSq")
		}
		for _, r := range s {
			if !isPubidChar(r) {
				return &WFError{Msg: "illegal character in public identifier"}
			}
		}
	}
	return nil
}

func isPubidChar(r rune) bool {
	switch {
	case r == 0x20 || r == 0xD || r == 0xA:
		return true
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return true
	case strings.ContainsRune("-'()+,./:=?;!*#@$_%", r):
		return true
	}
	return false
}

// charRefRune resolves a CharRef CST node to its rune, or -1 if it is
// malformed or out of range (which the Char legality check then rejects).
func charRefRune(n *pb.ASTNode) rune {
	t := strings.TrimSuffix(strings.TrimPrefix(leafText(n), "&#"), ";")
	if t == "" {
		return -1
	}
	var v int64
	var err error
	if t[0] == 'x' || t[0] == 'X' {
		v, err = strconv.ParseInt(t[1:], 16, 64)
	} else {
		v, err = strconv.ParseInt(t, 10, 64)
	}
	if err != nil || v < 0 || v > 0x10FFFF {
		return -1
	}
	return rune(v)
}

// --- CST helpers ---

func tagName(n *pb.ASTNode) string {
	if name := firstDescendant(n, "Name"); name != nil {
		return name.GetValue()
	}
	return ""
}

func directChild(n *pb.ASTNode, kind string) *pb.ASTNode {
	for _, c := range n.GetChildren() {
		if c.GetKind() == kind {
			return c
		}
	}
	return nil
}

func firstDescendant(n *pb.ASTNode, kind string) *pb.ASTNode {
	if n == nil {
		return nil
	}
	if n.GetKind() == kind {
		return n
	}
	for _, c := range n.GetChildren() {
		if r := firstDescendant(c, kind); r != nil {
			return r
		}
	}
	return nil
}

func descendants(n *pb.ASTNode, kind string) []*pb.ASTNode {
	var out []*pb.ASTNode
	var rec func(*pb.ASTNode)
	rec = func(x *pb.ASTNode) {
		if x == nil {
			return
		}
		if x.GetKind() == kind {
			out = append(out, x)
		}
		for _, c := range x.GetChildren() {
			rec(c)
		}
	}
	rec(n)
	return out
}

// leafText concatenates every leaf value in n's subtree, in order.
func leafText(n *pb.ASTNode) string {
	if n == nil {
		return ""
	}
	if len(n.GetChildren()) == 0 {
		return n.GetValue()
	}
	var b strings.Builder
	for _, c := range n.GetChildren() {
		b.WriteString(leafText(c))
	}
	return b.String()
}
