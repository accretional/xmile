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
	"github.com/accretional/gluon/v2/metaparser"
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

// CompileGrammar lowers an EBNF *schema grammar* (a grammar over an element
// vocabulary, e.g. lang/rss.ebnf) into a FileDescriptorProto — one message per
// rule, the EBNF analogue of CompileDTD. It rides gluon's own EBNF front-end
// (ParseEBNF -> GrammarToAST) and the shared compiler.Compile backend, so no
// hand-coded grammar knowledge lives here.
//
// A reference with no rule of its own lowers to a `string` field rather than a
// dangling message reference (same convention CompileDTD uses for #PCDATA
// leaves and attributes): a leading `at_` marks an attribute (the marker is
// stripped to name the field), and the bare `text` reference is a leaf's
// character content. Every other reference targets a declared rule and becomes
// a message field. Returns an error if the bytes are not a well-formed EBNF.
func CompileGrammar(ebnf []byte, opts SchemaOptions) (*descriptorpb.FileDescriptorProto, error) {
	gd, err := metaparser.ParseEBNF(metaparser.WrapString(string(ebnf)))
	if err != nil {
		return nil, fmt.Errorf("parse grammar: %w", err)
	}
	ast, err := compiler.GrammarToAST(gd)
	if err != nil {
		return nil, fmt.Errorf("grammar to AST: %w", err)
	}
	pkg := opts.Package
	if pkg == "" {
		pkg = "lang"
	}
	ast.Language = pkg

	declared := map[string]bool{}
	for _, r := range ast.GetRoot().GetChildren() {
		if r.GetKind() == compiler.KindRule {
			declared[r.GetValue()] = true
		}
	}
	ast.Root = scalarizeUndeclared(ast.Root, declared)

	return compiler.Compile(ast, compiler.Options{
		Package:   pkg,
		GoPackage: opts.GoPackage,
		FileName:  opts.FileName,
	})
}

// CompileSource lowers a metagrammar into a FileDescriptorProto, dispatching on
// the schema language: DTD (CompileDTD), an EBNF element vocabulary
// (CompileGrammar, the default), or XSD (CompileXSD). It is the single entry the
// Schemas.Compile RPC and the Documents.Process compile-then-use path share.
func CompileSource(src []byte, language xmlpb.SchemaLanguage, opts SchemaOptions) (*descriptorpb.FileDescriptorProto, error) {
	switch language {
	case xmlpb.SchemaLanguage_DTD:
		return CompileDTD(src, opts)
	case xmlpb.SchemaLanguage_XSD:
		return CompileXSD(src, opts)
	default: // EBNF_VOCAB / unspecified
		return CompileGrammar(src, opts)
	}
}

// attrMarker prefixes a grammar reference to mark it as an attribute (a string
// field) rather than a child element, so an attribute can share a name with an
// element (e.g. enclosure's `url=` vs <image>'s <url>). See lang/rss.ebnf.
const attrMarker = "at_"

// scalarizeUndeclared rewrites every nonterminal reference that has no rule of
// its own into a scalar (string) node, stripping the attribute marker so the
// field is named after the attribute. The input is not mutated.
func scalarizeUndeclared(root *pb.ASTNode, declared map[string]bool) *pb.ASTNode {
	if root == nil {
		return nil
	}
	if root.GetKind() == compiler.KindNonterminal {
		name := root.GetValue()
		if strings.HasPrefix(name, attrMarker) {
			return &pb.ASTNode{Kind: compiler.KindScalar, Value: name[len(attrMarker):]}
		}
		if !declared[name] {
			return &pb.ASTNode{Kind: compiler.KindScalar, Value: name}
		}
	}
	kids := make([]*pb.ASTNode, 0, len(root.GetChildren()))
	for _, c := range root.GetChildren() {
		kids = append(kids, scalarizeUndeclared(c, declared))
	}
	return &pb.ASTNode{Kind: root.GetKind(), Value: root.GetValue(), Children: kids}
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
		mixed := firstDescendant(cs, "mixed")
		var names []string
		seen := map[string]bool{}
		for _, nm := range descendants(mixed, "Name") {
			if n := nm.GetValue(); n != "" && !seen[n] {
				seen[n] = true
				names = append(names, n)
			}
		}
		if len(names) == 0 {
			// (#PCDATA): a text run.
			fields = append(fields, scalarNode("text"))
		} else {
			// (#PCDATA | a | b)*: interleaved text and children in document
			// order, lowered to a repeated message-with-oneof over a text
			// variant and one variant per child (the same shape as xml.proto's
			// ContentItem).
			alt := &pb.ASTNode{Kind: compiler.KindAlternation, Value: choiceWrapperName, Children: []*pb.ASTNode{scalarNode("text")}}
			for _, n := range names {
				alt.Children = append(alt.Children, childField(n, declared))
			}
			fields = append(fields, repeatedOf(alt))
		}
	case firstDescendant(cs, "children") != nil:
		model := parseContentModel(leafText(firstDescendant(cs, "children")))
		fields = append(fields, particleFields(model, declared)...)
	}
	return seqOrSingle(fields)
}

// choiceWrapperName names the nested message that wraps a repeated choice's
// oneof (e.g. Channel.Entry). It is scoped inside its element message, so the
// same short name is reused across elements without collision.
const choiceWrapperName = "Entry"

// particleFields lowers a content-model particle into the proto fields it
// contributes to the enclosing message.
//
// A DTD content model is an ordered regular expression over child elements, so
// the lowering preserves that order:
//
//   - a choice (a | b | c) is "exactly one of", i.e. a proto oneof;
//   - a repeated choice (…)* / (…)+ is an ordered sequence of those, so it
//     becomes a repeated message-with-oneof (proto3 cannot repeat a oneof field
//     directly; the compiler builds the wrapper from a repeated KindAlternation)
//     — preserving the interleaved order of children across variants, which a
//     bag of per-type repeated fields would discard;
//   - a sequence (a, b, c) keeps its fixed order as flat sibling fields.
func particleFields(p *particle, declared func(string) bool) []*pb.ASTNode {
	if p == nil {
		return nil
	}
	if p.name != "" {
		return []*pb.ASTNode{occWrap(childField(p.name, declared), p.occ)}
	}
	groupRepeated := p.occ == '*' || p.occ == '+'

	if p.choice {
		// One variant per distinct member. A member's own occurrence is
		// subsumed by the choice (and, when repeated, by the outer repetition),
		// so it is dropped: `(a | b+ | c?)*` accepts the same language as
		// `(a | b | c)*`.
		alt := &pb.ASTNode{Kind: compiler.KindAlternation, Value: choiceWrapperName}
		seen := map[string]bool{}
		for _, c := range p.children {
			if c.name == "" {
				alt.Children = append(alt.Children, groupNode(c, declared))
				continue
			}
			if seen[c.name] {
				continue
			}
			seen[c.name] = true
			alt.Children = append(alt.Children, childField(c.name, declared))
		}
		if groupRepeated {
			return []*pb.ASTNode{repeatedOf(alt)}
		}
		return []*pb.ASTNode{alt}
	}

	// Sequence: members flatten into sibling fields in order, each keeping its
	// occurrence. A repeating multi-member sequence becomes one repeated nested
	// message (rare; not in the RSS DTD).
	var out []*pb.ASTNode
	for _, c := range p.children {
		out = append(out, particleFields(c, declared)...)
	}
	if groupRepeated && len(out) > 1 {
		return []*pb.ASTNode{repeatedOf(seqOrSingle(out))}
	}
	if groupRepeated {
		for i, f := range out {
			out[i] = repeatedOf(f)
		}
	}
	return out
}

// groupNode lowers a nested group (used as a oneof variant or a single field)
// into a message node: an alternation for a choice, a sequence otherwise.
// Nested groups do not occur in the RSS DTD; this keeps the lowering total.
func groupNode(p *particle, declared func(string) bool) *pb.ASTNode {
	if p.choice {
		alt := &pb.ASTNode{Kind: compiler.KindAlternation, Value: choiceWrapperName}
		for _, c := range p.children {
			if c.name != "" {
				alt.Children = append(alt.Children, childField(c.name, declared))
			} else {
				alt.Children = append(alt.Children, groupNode(c, declared))
			}
		}
		return alt
	}
	var fields []*pb.ASTNode
	for _, c := range p.children {
		fields = append(fields, particleFields(c, declared)...)
	}
	return seqOrSingle(fields)
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
// of a leaf maps to its `text` field; each child element maps either to a
// direct message field named after it or, for a lowered choice, to a oneof
// variant inside a wrapper message (appended in document order for a repeated
// choice). It returns the names of elements / attributes (@-prefixed) with no
// matching field — empty for a document fully covered by the schema, non-empty
// for one carrying out-of-vocabulary markup.
func ProjectTag(tag *xmlpb.Tag, md protoreflect.MessageDescriptor, msg protoreflect.Message) []string {
	_, unknown := project(tag, md, msg, projectOptions{nsExtensible: false})
	return unknown
}

// placeChild finds where a child element belongs in msg (described by md) and
// returns the descriptor and mutable message to project it into. It handles a
// direct message field (a sequence/single element, or an inline oneof variant)
// and a message-with-oneof wrapper field (a lowered choice): for the wrapper it
// appends a new element to the repeated wrapper (or takes the singular one) and
// selects the matching oneof variant.
func placeChild(md protoreflect.MessageDescriptor, msg protoreflect.Message, element string) (protoreflect.MessageDescriptor, protoreflect.Message, bool) {
	if f := msgFieldByType(md, element); f != nil {
		return f.Message(), mutableMessage(msg, f), true
	}
	fs := md.Fields()
	for i := 0; i < fs.Len(); i++ {
		f := fs.Get(i)
		if f.Kind() != protoreflect.MessageKind {
			continue
		}
		if vf := msgFieldByType(f.Message(), element); vf != nil {
			wrap := mutableMessage(msg, f)
			return vf.Message(), mutableMessage(wrap, vf), true
		}
	}
	return nil, nil, false
}

// msgFieldByType returns the message field of md whose message type is named
// after element (PascalCase), matching how CompileDTD names element messages.
func msgFieldByType(md protoreflect.MessageDescriptor, element string) protoreflect.FieldDescriptor {
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

// mutableMessage returns a mutable message to fill for field f: a freshly
// appended element for a repeated field, or the field's message otherwise
// (which selects f's oneof variant when f belongs to a oneof).
func mutableMessage(msg protoreflect.Message, f protoreflect.FieldDescriptor) protoreflect.Message {
	if f.IsList() {
		return msg.Mutable(f).List().AppendMutable().Message()
	}
	return msg.Mutable(f).Message()
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
