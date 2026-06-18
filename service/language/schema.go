package language

// schema.go — the schema-language front-ends: the type-level companion to the
// instance-level Parse.
//
// Parse answers "is THIS document well-formed?"; the Compile* functions answer
// "what is the SHAPE of the family of documents this schema describes?" — they
// lower a schema (a DTD, an EBNF element vocabulary, or an XSD) into a
// google.protobuf.FileDescriptorProto, one proto message per declared element,
// so a generic feed gains a unique typed AST (an rss.Rss) rather than the
// homogeneous Tag. See ADR 0004 and ADR 0008.
//
// This package carries no grammar knowledge and never imports the service
// package: the parsers the front-ends need (a DTD parser, an XML parser) are
// injected as function parameters, so the service package can wrap these with
// its own grammar-driven parsers without a dependency cycle.

import (
	"fmt"
	"sort"
	"strings"

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
//
// parseDTD parses a bare external-subset DTD string into its CST root; the
// service package supplies it (its grammar-driven DTD parser) so this package
// never imports the parser directly.
func CompileDTD(dtd []byte, opts SchemaOptions, parseDTD func(subset string) (*pb.ASTNode, error)) (*descriptorpb.FileDescriptorProto, error) {
	root, err := parseDTD(string(dtd))
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
//
// parseDTD and parseXML are injected so this package never imports the parser
// (CompileDTD needs the DTD parser, CompileXSD the XML parser).
func CompileSource(src []byte, language xmlpb.SchemaLanguage, opts SchemaOptions, parseDTD func(string) (*pb.ASTNode, error), parseXML func(string) (*xmlpb.Xml, error)) (*descriptorpb.FileDescriptorProto, error) {
	switch language {
	case xmlpb.SchemaLanguage_DTD:
		return CompileDTD(src, opts, parseDTD)
	case xmlpb.SchemaLanguage_XSD:
		return CompileXSD(src, opts, parseXML)
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
		model := ParseContentModel(leafText(firstDescendant(cs, "children")))
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
func particleFields(p *Particle, declared func(string) bool) []*pb.ASTNode {
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
func groupNode(p *Particle, declared func(string) bool) *pb.ASTNode {
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

// Particle is a node of a parsed DTD content model — a small regular
// expression over child element names. The service package holds it opaquely
// (as *Particle) and passes it to MatchContentModel; its fields stay
// unexported.
type Particle struct {
	name     string
	children []*Particle
	choice   bool // group is a choice (|) rather than a sequence (,)
	occ      byte // 0, '?', '*', '+'
}

// ParseContentModel parses "(a, b?, (c|d)+)" into a particle tree. The text is
// the leaf text of an already grammar-validated `children` node.
func ParseContentModel(s string) *Particle {
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

func (p *cmParser) particle() *Particle {
	p.ws()
	var cp *Particle
	if p.pos < len(p.s) && p.s[p.pos] == '(' {
		cp = p.group()
	} else {
		cp = &Particle{name: p.name()}
	}
	cp.occ = p.occ()
	return cp
}

func (p *cmParser) group() *Particle {
	p.pos++ // '('
	g := &Particle{children: []*Particle{p.particle()}}
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

// --- content-model matching (a regular expression over child element names) ---

// MatchContentModel reports whether the sequence of child element names is
// accepted by the content model. It threads a set of reachable positions
// through the particle tree (a small Thompson-style NFA), so it handles
// arbitrary nesting, choices, sequences, and ?/*/+ occurrences.
func MatchContentModel(p *Particle, names []string) bool {
	ends := matchOcc(p, names, map[int]bool{0: true})
	return ends[len(names)]
}

// matchOcc applies a particle's occurrence indicator (?/*/+ or none).
func matchOcc(p *Particle, names []string, starts map[int]bool) map[int]bool {
	switch p.occ {
	case '?':
		return union(starts, matchOnce(p, names, starts))
	case '*':
		return closure(p, names, starts, true)
	case '+':
		return closure(p, names, starts, false)
	default:
		return matchOnce(p, names, starts)
	}
}

// matchOnce matches exactly one instance of the particle (ignoring its own
// occurrence indicator, which matchOcc has already handled).
func matchOnce(p *Particle, names []string, starts map[int]bool) map[int]bool {
	if p.name != "" {
		ends := map[int]bool{}
		for s := range starts {
			if s < len(names) && names[s] == p.name {
				ends[s+1] = true
			}
		}
		return ends
	}
	if p.choice {
		ends := map[int]bool{}
		for _, c := range p.children {
			for e := range matchOcc(c, names, starts) {
				ends[e] = true
			}
		}
		return ends
	}
	cur := starts
	for _, c := range p.children {
		cur = matchOcc(c, names, cur)
		if len(cur) == 0 {
			break
		}
	}
	return cur
}

// closure matches one-or-more (zeroOK=false) or zero-or-more (zeroOK=true)
// repetitions of the particle, accumulating every reachable position.
func closure(p *Particle, names []string, starts map[int]bool, zeroOK bool) map[int]bool {
	reached := map[int]bool{}
	if zeroOK {
		for s := range starts {
			reached[s] = true
		}
	}
	frontier := starts
	for len(frontier) > 0 {
		next := matchOnce(p, names, frontier)
		newFront := map[int]bool{}
		for e := range next {
			if !reached[e] {
				reached[e] = true
				newFront[e] = true
			}
		}
		frontier = newFront
	}
	return reached
}

func union(a, b map[int]bool) map[int]bool {
	out := map[int]bool{}
	for k := range a {
		out[k] = true
	}
	for k := range b {
		out[k] = true
	}
	return out
}

// --- generic CST helpers (own copies; they only walk *pb.ASTNode / strings) ---

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

// hasTerminal reports whether n's subtree contains a terminal with value val.
func hasTerminal(n *pb.ASTNode, val string) bool {
	if n == nil {
		return false
	}
	if n.GetKind() == "terminal" && n.GetValue() == val {
		return true
	}
	for _, c := range n.GetChildren() {
		if hasTerminal(c, val) {
			return true
		}
	}
	return false
}

func isNameByte(b byte) bool {
	switch {
	case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9':
		return true
	case b == '_' || b == '-' || b == '.' || b == ':' || b >= 0x80:
		return true
	}
	return false
}
