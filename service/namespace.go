package service

// namespace.go — Namespaces in XML, applied integrally over the projected tree.
//
// XML 1.0 treats a colon as an ordinary name character (§2.3): the base grammar
// is namespace-unaware on purpose. The namespace constraints, though, are
// context-sensitive (a prefix must be in scope, expanded attribute names must be
// unique) — a CFG cannot express them — so, like tag matching and the entity
// constraints, they live here as a tree walk rather than in the grammar.
//
// The walk resolves every element and attribute QName against the in-scope
// declarations, enforces the namespace constraints, and writes the resolved
// prefix / local name / namespace URI back onto the tree. A violation makes the
// document not namespace-well-formed, reported as a *WFError. is11 selects the
// Namespaces 1.1 rules (which allow undeclaring a prefix).

import (
	"strings"

	pb "github.com/accretional/gluon/v2/pb"

	xmlpb "github.com/accretional/xmile/proto/pb/xml"
)

const (
	xmlNamespace   = "http://www.w3.org/XML/1998/namespace"
	xmlnsNamespace = "http://www.w3.org/2000/xmlns/"
)

// checkNamespaces resolves and validates namespaces over the whole document:
// the element tree plus the processing-instruction targets in the prolog and
// epilog (a colon in a PI target is not namespace-well-formed).
func checkNamespaces(doc *xmlpb.Document, is11 bool) error {
	for _, m := range doc.GetPrologMisc() {
		if err := checkPITarget(m.GetPi()); err != nil {
			return err
		}
	}
	for _, m := range doc.GetEpilogMisc() {
		if err := checkPITarget(m.GetPi()); err != nil {
			return err
		}
	}
	root := doc.GetRoot()
	if root == nil {
		return nil
	}
	// The xml prefix is predefined; xmlns is never a usable prefix.
	return nsWalk(root, map[string]string{"xml": xmlNamespace}, is11)
}

// checkPITarget rejects a colon in a processing-instruction target.
func checkPITarget(pi *xmlpb.PI) error {
	if pi != nil && strings.Contains(pi.GetTarget(), ":") {
		return &WFError{Msg: "namespace: colon in processing-instruction target " + pi.GetTarget()}
	}
	return nil
}

// checkDTDNamespaceNames rejects a colon in the names a DTD declares — entity
// names, notation names, and PI targets in the internal subset — none of which
// may contain a colon in a namespace-well-formed document.
func checkDTDNamespaceNames(dtdRoot *pb.ASTNode) error {
	if dtdRoot == nil {
		return nil
	}
	for _, ed := range descendants(dtdRoot, "entityDecl") {
		if n := firstName(ed); strings.Contains(n, ":") {
			return &WFError{Msg: "namespace: colon in entity name " + n}
		}
	}
	for _, nd := range descendants(dtdRoot, "notationDecl") {
		if n := firstName(nd); strings.Contains(n, ":") {
			return &WFError{Msg: "namespace: colon in notation name " + n}
		}
	}
	for _, pi := range descendants(dtdRoot, "PI") {
		if n := firstDescendant(pi, "Name"); n != nil && strings.Contains(n.GetValue(), ":") {
			return &WFError{Msg: "namespace: colon in processing-instruction target " + n.GetValue()}
		}
	}
	return nil
}

// nsWalk resolves one element against the bindings in scope (prefix -> URI, with
// "" the default namespace) and recurses. bindings is shared with the parent
// and copied only when this element adds declarations, so scopes don't leak.
func nsWalk(tag *xmlpb.Tag, bindings map[string]string, is11 bool) error {
	scope := bindings
	if decls := nsDeclarations(tag); len(decls) > 0 {
		scope = copyBindings(bindings)
		for _, d := range decls {
			if err := applyNSDecl(scope, d, is11); err != nil {
				return err
			}
		}
	}

	prefix, local, err := splitQName(tag.GetName())
	if err != nil {
		return err
	}
	if prefix == "xmlns" {
		return &WFError{Msg: "namespace: element name must not use the xmlns prefix"}
	}
	uri, ok := resolveNS(scope, prefix)
	if !ok {
		return &WFError{Msg: "namespace: undeclared element prefix " + prefix + " in <" + tag.GetName() + ">"}
	}
	if uri != "" { // in a namespace (a default may bind an unprefixed element)
		tag.Namespace = &xmlpb.Namespace{NamespaceUri: uri, LocalName: local, Prefix: prefix}
	}

	seen := map[string]bool{}
	for _, a := range tag.GetAttrs() {
		if isNSDecl(a.GetName()) {
			continue // xmlns / xmlns:* are declarations, not namespaced attributes
		}
		apfx, alocal, err := splitQName(a.GetName())
		if err != nil {
			return err
		}
		auri := ""
		if apfx != "" { // an unprefixed attribute is in no namespace
			u, ok := resolveNS(scope, apfx)
			if !ok {
				return &WFError{Msg: "namespace: undeclared attribute prefix " + apfx + " in " + a.GetName()}
			}
			auri = u
		}
		if auri != "" {
			a.Namespace = &xmlpb.Namespace{NamespaceUri: auri, LocalName: alocal, Prefix: apfx}
		}
		// NC: Attributes Unique — no two attributes with the same expanded name.
		if key := auri + " " + alocal; seen[key] {
			return &WFError{Msg: "namespace: duplicate attribute " + alocal + " in namespace " + auri}
		} else {
			seen[key] = true
		}
	}

	for _, ci := range tag.GetContents() {
		switch it := ci.GetItem().(type) {
		case *xmlpb.ContentItem_Child:
			if err := nsWalk(it.Child, scope, is11); err != nil {
				return err
			}
		case *xmlpb.ContentItem_Pi:
			if err := checkPITarget(it.Pi); err != nil {
				return err
			}
		}
	}
	return nil
}

// nsDecl is a namespace declaration. prefixed distinguishes the xmlns:p form
// from the default xmlns form, so an empty prefix in the xmlns: form ("xmlns:")
// is caught rather than mistaken for a default declaration.
type nsDecl struct {
	prefix, uri string
	prefixed    bool
}

// nsDeclarations returns the namespace declarations carried by an element's
// attributes (xmlns="uri" with prefix "", and xmlns:p="uri").
func nsDeclarations(tag *xmlpb.Tag) []nsDecl {
	var out []nsDecl
	for _, a := range tag.GetAttrs() {
		switch n := a.GetName(); {
		case n == "xmlns":
			out = append(out, nsDecl{prefix: "", uri: a.GetValue(), prefixed: false})
		case strings.HasPrefix(n, "xmlns:"):
			out = append(out, nsDecl{prefix: n[len("xmlns:"):], uri: a.GetValue(), prefixed: true})
		}
	}
	return out
}

// applyNSDecl applies one declaration to the scope, enforcing the reserved
// prefix/namespace rules (NC: Reserved Prefixes and Namespace Names) and the
// prefix-undeclaration rule (allowed only in Namespaces 1.1).
func applyNSDecl(scope map[string]string, d nsDecl, is11 bool) error {
	if !d.prefixed { // default namespace (xmlns="…")
		switch d.uri {
		case xmlNamespace, xmlnsNamespace:
			return &WFError{Msg: "namespace: " + d.uri + " must not be the default namespace"}
		case "":
			delete(scope, "") // undeclare the default (legal in both editions)
		default:
			scope[""] = d.uri
		}
		return nil
	}

	if !isNCName(d.prefix) {
		return &WFError{Msg: "namespace: invalid namespace prefix " + d.prefix}
	}
	switch d.prefix {
	case "xmlns":
		return &WFError{Msg: "namespace: the xmlns prefix must not be declared"}
	case "xml":
		if d.uri != xmlNamespace {
			return &WFError{Msg: "namespace: the xml prefix must be bound to " + xmlNamespace}
		}
		return nil // re-declaring xml to its own namespace is a no-op
	}
	switch d.uri {
	case xmlNamespace:
		return &WFError{Msg: "namespace: only the xml prefix may be bound to " + xmlNamespace}
	case xmlnsNamespace:
		return &WFError{Msg: "namespace: no prefix may be bound to " + xmlnsNamespace}
	case "":
		if !is11 {
			return &WFError{Msg: "namespace: prefix " + d.prefix + " must not be undeclared in XML 1.0"}
		}
		delete(scope, d.prefix)
		return nil
	}
	scope[d.prefix] = d.uri
	return nil
}

// resolveNS returns the namespace URI bound to a prefix. The empty prefix is the
// default namespace (URI "" when none is in scope — an unprefixed name is then
// simply in no namespace, which is not an error). A non-empty prefix that is not
// bound returns ok=false.
func resolveNS(scope map[string]string, prefix string) (string, bool) {
	if prefix == "" {
		return scope[""], true
	}
	uri, ok := scope[prefix]
	return uri, ok
}

// splitQName splits a QName into prefix and local part and validates its syntax
// (at most one colon; each part a non-empty NCName).
func splitQName(name string) (prefix, local string, err error) {
	i := strings.IndexByte(name, ':')
	if i < 0 {
		if !isNCName(name) {
			return "", "", &WFError{Msg: "namespace: not a valid name: " + name}
		}
		return "", name, nil
	}
	prefix, local = name[:i], name[i+1:]
	if !isNCName(prefix) || !isNCName(local) {
		return "", "", &WFError{Msg: "namespace: malformed QName: " + name}
	}
	return prefix, local, nil
}

// isNCName reports whether s is a valid NCName: an XML Name with no colon.
func isNCName(s string) bool {
	return s != "" && !strings.Contains(s, ":") && isXMLName(s)
}

func isNSDecl(name string) bool {
	return name == "xmlns" || strings.HasPrefix(name, "xmlns:")
}

func copyBindings(m map[string]string) map[string]string {
	out := make(map[string]string, len(m)+2)
	for k, v := range m {
		out[k] = v
	}
	return out
}
