# xmile

A grammar-driven XML parser. XML 1.0 source is parsed against an EBNF
grammar by the gluon engine and projected into a homogeneous proto AST
(`proto/xml.proto`); the DOCTYPE/DTD is a generated proto model
(`proto/dtd.proto`). Parsing is exposed as a gRPC service. The design
record lives in `docs/decisions/`.

## Build Discipline

**This is the most important section. Violating it will break the project.**

- **NEVER build/test/run code outside of `setup.sh`, `build.sh`, `test.sh`,
  `serve.sh`, `LET_IT_RIP.sh`.**
- **NEVER commit or push without running `./LET_IT_RIP.sh` first.**
- **NEVER commit or push if `./LET_IT_RIP.sh` is failing or has skipped tests.**
- Scripts are idempotent and chained:
  `build.sh` -> `setup.sh`; `test.sh` -> `build.sh`; `LET_IT_RIP.sh` -> `test.sh`;
  `serve.sh` -> `setup.sh`.
- **All grammar logic lives in the grammar.** The runtime parser carries no
  grammar knowledge. Structure is in `lang/*.ebnf`; lexing is in `lang/*.lex`;
  both are compiled by `genproto` into `proto/`. Never hand-code grammar rules,
  keyword lists, or character classes in Go.
- **NEVER edit generated files by hand.** Regenerate manually via
  `go run ./lang/cmd/genproto` plus protoc when the grammar or protos change;
  the artifacts are committed, so `build.sh` only sets up and builds. Generated:
  `proto/dtd.proto`, `proto/pb/dtd/{dtd.pb.go,prefix_map.go,separator_map.go,
  lexical.go}`, `proto/pb/xml/{*.pb.go,lexical.go}`, `lang/dtd.fdset`.
- `proto/xml.proto` and `proto/xml_service.proto` are **hand-written** (see
  ADR 0003) and run through `protoc` during that manual regeneration step.
- Any changes made in the project must be then updated in the respective documents.
  There should be NO stale document in the project.

## Pipeline

```
lang/dtd.ebnf  ─┐
lang/xml.lex   ─┤ go run ./lang/cmd/genproto
lang/dtd.lex   ─┘   (ParseEBNF -> GrammarToAST -> transforms -> Compile; lex tables)
        │
        ▼
proto/dtd.proto + proto/pb/{xml,dtd}/lexical.go + prefix/separator maps
        │  protoc  (xml.proto, dtd.proto, xml_service.proto are hand-written)
        ▼
proto/pb/{xml,dtd}/*.pb.go
        │
        ▼
service/  parse pipeline:
  1. gluon CST parse (lang/xml.ebnf + generated lexical matchers via pkg lex)
  2. well-formedness walk (tag match, dup attrs, PI target, charref, pubid)
  3. DTD second pass (lang/dtd.ebnf): parse internal subset + entity WFCs
  4. project CST -> xml.proto Document (general entities expanded into content)
  5. attribute-value normalization by declared type
  6. namespaces (namespace.go, integral): resolve QNames, enforce the namespace
     constraints, fill Tag/Attribute.namespace
  7. DTD validity (validate.go), only when validating: content models, attribute
     decls, ID/IDREF; internal parameter entities expanded (pe.go); an external
     subset / external PE yields CannotValidate
        │
        ▼
XmlService.Parse(bytes, validate) -> ParseResponse
  oneof: Document | ParseError{verdict, reason}
  verdict: NOT_WELL_FORMED | INVALID | CANNOT_VALIDATE   (RPC status stays OK)
  (cmd/xmlserve serves it; cmd/xmlparse is a CLI, -validate to validate)
```

## Architecture

- **Generated lexical tables.** `lang/*.lex` declares each token matcher
  (`run` / `name` / `except` / `until` / `balanced`); `genproto` compiles it to
  `proto/pb/*/lexical.go`. The generic engine in package `lex` turns the table
  into gluon token matchers. No character ranges or delimiters live in Go.
- **Homogeneous AST.** Every element is a `Tag`; the element type is data
  (`Tag.name`), not a message type. `Document.doctype` references the generated
  `dtd.Doctype`.
- **Two grammars, two alphabets.** `xml.ebnf` parses characters -> element tree.
  `dtd.ebnf` parses the DOCTYPE body. Entity/well-formedness constraints that a
  CFG can't express (tag matching, entity declaration/recursion, char/pubid
  legality) are tree-level walks in `service/`, not grammar.
- **Validity is a separate pass.** `validate.go` builds a model from the parsed
  DTD (content models, attribute declarations, notations) and checks the
  projected tree against it: element content models, attribute types and
  defaults, ID/IDREF, and the DTD-level VCs. It runs only in validating mode;
  internal parameter entities are expanded (`pe.go`) and an external subset / PE
  yields CannotValidate, so a valid document is never wrongly rejected. ADR 0005.
- **Namespaces are integral, not grammar.** XML requires `:` to be a name
  character, so the grammar stays namespace-unaware; the namespace constraints
  are context-sensitive, so `namespace.go` resolves QNames and enforces them as
  a tree walk applied in both modes, filling `Tag/Attribute.namespace`. ADR 0006.
- **Parse is a mode-aware classifier.** `Parse(src, validating)` is the
  validating vs non-validating processor; it returns the AST or a typed error
  (`*WFError` / `*ValidityError` / `*CannotValidateError`), surfaced over gRPC as
  a `ParseResponse` oneof (`Document` | `ParseError{verdict, reason}`). ADR 0006.

## Testing

- **Two gates.** `service/conformance_test.go` (`go test ./...`) is
  self-contained — it carries its own XML samples, so individual testing needs
  nothing fetched. The full W3C corpus is gated by the harness
  (`go run ./testing/xml-parse`, run by `test.sh`): the deterministic `xml/`
  corpus must be 100% (non-zero exit otherwise); the real-world `docx/xlsx/rss`
  corpora are reported but do not gate.
- The corpus is fetched and organized by file type via `go run ./testing`.
- The corpus is the applicable subset (XML 1.0 5th edition and 1.1, plus
  Namespaces; no external entities), filtered at fetch time. Out of scope and
  filtered: external entities, the `NAMESPACE="no"` tests (written for a
  non-namespace processor), 4th-edition name-character tests, and `invalid`
  tests with an external subset. See `testing/README.md`.

## Layout

| Path | Role |
|---|---|
| `lang/xml.ebnf`, `lang/dtd.ebnf` | grammars (hand-edited) |
| `lang/xml.lex`, `lang/dtd.lex` | lexical specs (hand-edited) |
| `lang/cmd/genproto/` | grammar -> proto + maps + lexical tables |
| `lang/embed.go` | embeds the grammars for the runtime |
| `lex/` | generic lexical-matcher engine (no grammar knowledge) |
| `proto/xml.proto`, `proto/xml_service.proto` | hand-written AST + service |
| `proto/dtd.proto`, `proto/pb/**` | generated |
| `service/` | parser + gRPC server |
| `cmd/xmlparse/` | CLI: file/stdin -> AST (or `-cst`) |
| `cmd/xmlserve/` | gRPC server |
| `testing/` | corpus fetcher (`go run ./testing`); corpora are gitignored |
| `testing/xml-parse/` | corpus harness (`go run ./testing/xml-parse`) |
| `docs/decisions/` | ADRs |
