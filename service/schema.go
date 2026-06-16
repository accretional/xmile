package service

// schema.go — the DTD-as-schema compiler (the type-level companion to the
// instance-level Parse).
//
// Parse answers "is THIS document well-formed?"; CompileDTD answers "what is
// the SHAPE of the family of documents this DTD describes?" — it lowers a DTD
// into a google.protobuf.FileDescriptorProto, one proto message per
// <!ELEMENT>, so a generic feed gains a unique typed AST (an rss.Rss) rather
// than the homogeneous Tag. See ADR 0004.
//
// It carries no grammar knowledge: it parses the DTD with the same grammar-
// driven dtdParser the runtime uses, walks the resulting CST, and hands the
// shape to gluon's compiler — the same engine that generates dtd.proto from
// dtd.ebnf, applied one meta-level down (a particular DTD, not the grammar of
// DTDs).

import (
	"fmt"
	"sort"
	"strings"
	"unicode"

	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/accretional/gluon/v2/compiler"
	pb "github.com/accretional/gluon/v2/pb"

	xmlpb "github.com/accretional/xmile/proto/pb/xml"
)

// SchemaOptions configure CompileDTD's FileDescriptorProto output. All fields
// are optional; defaults derive from the package name.
type SchemaOptions struct {
	Package   string // proto package (e.g. "rss"); defaults to "lang".
	GoPackage string // go_package file option; omitted when empty.
	FileName  string // FileDescriptorProto.name; defaults to "<package>.proto".
}

// CompileDTD validates DTD bytes and lowers them into a FileDescriptorProto.
//
// The input is an *external subset*: a bare sequence of markup declarations as
// found in a standalone .dtd file (no <!DOCTYPE …> wrapper). Each <!ELEMENT>
// becomes one message; <!ATTLIST> attributes become string fields. Content
// models map structurally:
//
//	<!ELEMENT e (#PCDATA)>      → message E { string text = 1; }
//	<!ELEMENT e (a)>            → message E { A a = 1; }
//	<!ELEMENT e (a | b | c)*>   → message E { repeated A a; repeated B b; repeated C c; }
//	<!ELEMENT e (a+)>           → message E { repeated A a; }
//	<!ELEMENT e (a, b?, c*)>    → message E { A a; B b; repeated C c; }
//	<!ATTLIST e k CDATA …>      → adds  string k  to message E
//
// A repeated choice lowers to a bag of repeated fields, not a repeated oneof
// (illegal proto3), which is also the idiomatic shape for an order-insensitive
// vocabulary like RSS. Entity/notation declarations carry no document
// structure and are ignored. Returns an error if the bytes are not a
// well-formed DTD.
func CompileDTD(dtd []byte, opts SchemaOptions) (*descriptorpb.FileDescriptorProto, error) {
	root, err := parseExternalSubset(string(dtd))
	if err != nil {
		return nil, err
	}
	ast, err := schemaAST(root, opts.Package)
	if err != nil {
		return nil, err
	}
	return compiler.Compile(ast, compiler.Options{
		Package:   opts.Package,
		GoPackage: opts.GoPackage,
		FileName:  opts.FileName,
	})
}

// parseExternalSubset parses a bare external-subset DTD into its CST.
//
// lang/dtd.ebnf's start rule (doctype) expects a DOCTYPE *body* — a root name
// then the bracketed internal subset. A standalone DTD file is just the
// declaration list, so we wrap it in a synthetic DOCTYPE body and reuse the
// runtime DTD parser unchanged. The synthetic root name is never read.
func parseExternalSubset(subset string) (*pb.ASTNode, error) {
	dp, err := dtdParser()
	if err != nil {
		return nil, err
	}
	cst, err := dp.ParseCST("_xmile_schema_root [" + subset + "]")
	if err != nil {
		return nil, fmt.Errorf("not a well-formed DTD: %w", err)
	}
	return cst.GetRoot(), nil
}

// schemaAST walks the DTD CST into a gluon schema-AST (file → rule* → body)
// that compiler.Compile consumes. Elements are emitted in sorted name order
// for deterministic output.
func schemaAST(root *pb.ASTNode, language string) (*pb.ASTDescriptor, error) {
	// Element content specs, first declaration binding.
	specs := map[string]*pb.ASTNode{}
	var names []string
	for _, decl := range descendants(root, "elementDecl") {
		name := firstName(decl)
		if name == "" {
			continue
		}
		if _, dup := specs[name]; dup {
			continue
		}
		specs[name] = firstDescendant(decl, "contentspec")
		names = append(names, name)
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("DTD declares no elements")
	}

	// Attribute names per element, first declaration binding, sorted.
	attrs := map[string][]string{}
	for _, decl := range descendants(root, "attlistDecl") {
		elem := firstName(decl)
		if elem == "" {
			continue
		}
		seen := map[string]bool{}
		for _, a := range attrs[elem] {
			seen[a] = true
		}
		for _, ad := range descendants(decl, "attDef") {
			an := firstName(ad)
			if an == "" || seen[an] {
				continue
			}
			seen[an] = true
			attrs[elem] = append(attrs[elem], an)
		}
	}

	sort.Strings(names)
	file := &pb.ASTNode{Kind: compiler.KindFile}
	declared := func(n string) bool { _, ok := specs[n]; return ok }
	for _, name := range names {
		rule := &pb.ASTNode{Kind: compiler.KindRule, Value: name}
		if body := elementBody(specs[name], sortedCopy(attrs[name]), declared); body != nil {
			rule.Children = []*pb.ASTNode{body}
		}
		file.Children = append(file.Children, rule)
	}
	return &pb.ASTDescriptor{Language: language, Root: file}, nil
}

// elementBody builds a rule body: attribute string fields, then the fields
// from the content spec.
func elementBody(cs *pb.ASTNode, attrNames []string, declared func(string) bool) *pb.ASTNode {
	var fields []*pb.ASTNode
	for _, an := range attrNames {
		fields = append(fields, scalarNode(an))
	}
	switch {
	case cs == nil, hasTerminal(cs, "EMPTY"), hasTerminal(cs, "ANY"):
		// EMPTY/ANY/absent → no structural content fields.
	case firstDescendant(cs, "mixed") != nil:
		// (#PCDATA) or (#PCDATA | a | b)*: a text run plus a repeated field
		// per allowed child element.
		fields = append(fields, scalarNode("text"))
		mixed := firstDescendant(cs, "mixed")
		seen := map[string]bool{}
		for _, nm := range descendants(mixed, "Name") {
			if n := nm.GetValue(); n != "" && !seen[n] {
				seen[n] = true
				fields = append(fields, repeatedOf(childField(n, declared)))
			}
		}
	case firstDescendant(cs, "children") != nil:
		model := parseContentModel(leafText(firstDescendant(cs, "children")))
		fields = append(fields, particleFields(model, declared)...)
	}
	return seqOrSingle(fields)
}

// particleFields lowers a content-model particle into the proto fields it
// contributes to the enclosing message.
func particleFields(p *particle, declared func(string) bool) []*pb.ASTNode {
	if p == nil {
		return nil
	}
	if p.name != "" {
		return []*pb.ASTNode{occWrap(childField(p.name, declared), p.occ)}
	}
	groupRepeated := p.occ == '*' || p.occ == '+'

	if p.choice {
		// (a | b | c …): one field per distinct member. Under a repetition
		// every member is repeated; a bare choice keeps each member singular
		// (a per-member '?' optional, '+'/'*' repeated).
		seen := map[string]bool{}
		var out []*pb.ASTNode
		for _, c := range p.children {
			if c.name == "" {
				continue // nested groups in a choice don't occur in RSS DTDs
			}
			if seen[c.name] {
				continue
			}
			seen[c.name] = true
			field := childField(c.name, declared)
			switch {
			case groupRepeated || c.occ == '*' || c.occ == '+':
				field = repeatedOf(field)
			case c.occ == '?':
				field = optionalOf(field)
			}
			out = append(out, field)
		}
		return out
	}

	// Sequence: members flatten into sibling fields, each keeping its own
	// occurrence. (RSS DTDs have no repeating multi-member sequence.)
	var out []*pb.ASTNode
	for _, c := range p.children {
		out = append(out, particleFields(c, declared)...)
	}
	if groupRepeated {
		for i, f := range out {
			out[i] = repeatedOf(f)
		}
	}
	return out
}

// childField references a child element: a message field when declared, else
// a string field named after it, so the descriptor never dangles.
func childField(name string, declared func(string) bool) *pb.ASTNode {
	if declared(name) {
		return &pb.ASTNode{Kind: compiler.KindNonterminal, Value: name}
	}
	return scalarNode(name)
}

func scalarNode(name string) *pb.ASTNode {
	return &pb.ASTNode{Kind: compiler.KindScalar, Value: name}
}

func repeatedOf(n *pb.ASTNode) *pb.ASTNode {
	if n.GetKind() == compiler.KindRepetition {
		return n
	}
	return &pb.ASTNode{Kind: compiler.KindRepetition, Children: []*pb.ASTNode{n}}
}

func optionalOf(n *pb.ASTNode) *pb.ASTNode {
	return &pb.ASTNode{Kind: compiler.KindOptional, Children: []*pb.ASTNode{n}}
}

func occWrap(n *pb.ASTNode, occ byte) *pb.ASTNode {
	switch occ {
	case '*', '+':
		return repeatedOf(n)
	case '?':
		return optionalOf(n)
	}
	return n
}

func seqOrSingle(fields []*pb.ASTNode) *pb.ASTNode {
	switch len(fields) {
	case 0:
		return nil
	case 1:
		return fields[0]
	default:
		return &pb.ASTNode{Kind: compiler.KindSequence, Children: fields}
	}
}

func firstName(n *pb.ASTNode) string {
	if nm := firstDescendant(n, "Name"); nm != nil {
		return nm.GetValue()
	}
	return ""
}

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

// --- content model: a small regex over child element names ---

type particle struct {
	name     string
	children []*particle
	choice   bool // group is a choice (|) rather than a sequence (,)
	occ      byte // 0, '?', '*', '+'
}

// parseContentModel parses "(a, b?, (c|d)+)" into a particle tree. The text is
// the leaf text of an already grammar-validated `children` node.
func parseContentModel(s string) *particle {
	p := &cmParser{s: s}
	return p.particle()
}

type cmParser struct {
	s   string
	pos int
}

func (p *cmParser) ws() {
	for p.pos < len(p.s) {
		switch p.s[p.pos] {
		case ' ', '\t', '\r', '\n':
			p.pos++
		default:
			return
		}
	}
}

func (p *cmParser) particle() *particle {
	p.ws()
	var cp *particle
	if p.pos < len(p.s) && p.s[p.pos] == '(' {
		cp = p.group()
	} else {
		cp = &particle{name: p.name()}
	}
	cp.occ = p.occ()
	return cp
}

func (p *cmParser) group() *particle {
	p.pos++ // '('
	g := &particle{children: []*particle{p.particle()}}
	p.ws()
	for p.pos < len(p.s) && (p.s[p.pos] == ',' || p.s[p.pos] == '|') {
		if p.s[p.pos] == '|' {
			g.choice = true
		}
		p.pos++
		g.children = append(g.children, p.particle())
		p.ws()
	}
	if p.pos < len(p.s) && p.s[p.pos] == ')' {
		p.pos++
	}
	return g
}

func (p *cmParser) name() string {
	start := p.pos
	for p.pos < len(p.s) && isNameByte(p.s[p.pos]) {
		p.pos++
	}
	return p.s[start:p.pos]
}

func (p *cmParser) occ() byte {
	if p.pos < len(p.s) {
		switch p.s[p.pos] {
		case '?', '*', '+':
			c := p.s[p.pos]
			p.pos++
			return c
		}
	}
	return 0
}

// --- projecting a parsed document into a generated schema ---

// ProjectTag fills msg (described by md, a message from CompileDTD output) from
// a parsed Tag. Attributes map to string fields by snake-cased name; the text
// of a leaf maps to its `text` field; child elements map to the field whose
// message type is named after the element. It returns the names of elements /
// attributes (@-prefixed) with no matching field — empty for a document fully
// covered by the schema, non-empty for one carrying out-of-vocabulary markup.
func ProjectTag(tag *xmlpb.Tag, md protoreflect.MessageDescriptor, msg protoreflect.Message) []string {
	var unknown []string
	for _, a := range tag.GetAttrs() {
		if f := scalarField(md, a.GetName()); f != nil {
			msg.Set(f, protoreflect.ValueOfString(a.GetValue()))
		} else {
			unknown = append(unknown, "@"+a.GetName())
		}
	}

	var children []*xmlpb.Tag
	var text strings.Builder
	for _, ci := range tag.GetContents() {
		switch it := ci.GetItem().(type) {
		case *xmlpb.ContentItem_Child:
			children = append(children, it.Child)
		case *xmlpb.ContentItem_Text:
			text.WriteString(it.Text)
		case *xmlpb.ContentItem_Cdata:
			text.WriteString(it.Cdata)
		}
	}

	if len(children) == 0 {
		if strings.TrimSpace(text.String()) != "" {
			if f := md.Fields().ByName("text"); f != nil && f.Kind() == protoreflect.StringKind {
				msg.Set(f, protoreflect.ValueOfString(text.String()))
			}
		}
		return unknown
	}

	for _, child := range children {
		f := childMessageField(md, child.GetName())
		if f == nil {
			unknown = append(unknown, child.GetName())
			continue
		}
		var sub protoreflect.Message
		if f.IsList() {
			sub = msg.Mutable(f).List().AppendMutable().Message()
		} else {
			sub = msg.Mutable(f).Message()
		}
		unknown = append(unknown, ProjectTag(child, f.Message(), sub)...)
	}
	return unknown
}

func childMessageField(md protoreflect.MessageDescriptor, element string) protoreflect.FieldDescriptor {
	want := pascalName(element)
	fs := md.Fields()
	for i := 0; i < fs.Len(); i++ {
		f := fs.Get(i)
		if f.Kind() == protoreflect.MessageKind && string(f.Message().Name()) == want {
			return f
		}
	}
	return nil
}

func scalarField(md protoreflect.MessageDescriptor, attr string) protoreflect.FieldDescriptor {
	f := md.Fields().ByName(protoreflect.Name(snakeName(attr)))
	if f != nil && f.Kind() == protoreflect.StringKind {
		return f
	}
	return nil
}

// --- name mangling, mirroring gluon/v2/compiler/names.go so projector lookups
// match the names compiler.Compile emits ---

func idParts(s string) []string {
	var parts []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			parts = append(parts, cur.String())
			cur.Reset()
		}
	}
	runes := []rune(s)
	for i, r := range runes {
		if r == '-' || r == '_' || r == ' ' {
			flush()
			continue
		}
		if i > 0 && unicode.IsUpper(r) && unicode.IsLower(runes[i-1]) {
			flush()
		}
		cur.WriteRune(r)
	}
	flush()
	return parts
}

func pascalName(s string) string {
	var out strings.Builder
	for _, p := range idParts(s) {
		if p == "" {
			continue
		}
		r := []rune(strings.ToLower(p))
		r[0] = unicode.ToUpper(r[0])
		out.WriteString(string(r))
	}
	return out.String()
}

func snakeName(s string) string {
	parts := idParts(s)
	for i, p := range parts {
		parts[i] = strings.ToLower(p)
	}
	return strings.Join(parts, "_")
}
