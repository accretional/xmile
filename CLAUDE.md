# xmile

A grammar-driven XML parser and schema-driven projector. XML 1.0 source is
parsed against an EBNF grammar by the gluon engine into a homogeneous proto AST
(`proto/xml.proto`); the DOCTYPE/DTD is a generated proto model
(`proto/dtd.proto`). Given a schema (a DTD, an EBNF element vocabulary, or an
XSD), the same parsed tree is projected into that vocabulary's typed AST —
generic XML is just the loosest schema. OPC packages (`.docx`/`.xlsx`) are a
layer over the same parser. Exposed as gRPC services (`Documents.Process`,
`Schemas.Compile`). The design record lives in `docs/decisions/`; the current
architecture is ADR 0008.

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
- **NEVER edit generated files by hand.** Regenerate with `./regen.sh` (runs
  `go run ./lang/cmd/genproto`, `go run ./lang/cmd/genproto_rss`, then protoc)
  when a grammar or proto changes; the artifacts are committed, so `build.sh`
  only sets up and builds. Generated: `proto/dtd.proto`,
  `proto/pb/dtd/{dtd.pb.go,prefix_map.go,separator_map.go,lexical.go}`,
  `proto/pb/xml/{*.pb.go,lexical.go}`, `lang/dtd.fdset`, and from the RSS 2.0
  grammar `proto/rss.proto` + `lang/rss.fdset` + `proto/pb/rss/rss.pb.go` (the
  typed `rss.Rss` AST a feed projects into).
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
Documents.Process(bytes, schema, mode) -> ProcessResponse
  no schema -> Document (the generic XML AST; the former Parse)
  a schema  -> TypedTree (the vocabulary's typed AST, self-describing on the wire)
  oneof: Document | TypedTree | ProcessError{verdict, reason}
  verdict: NOT_WELL_FORMED | WELL_FORMED | VALID | INVALID | CANNOT_VALIDATE
  Schemas.Compile(source, language) -> FileDescriptorProto   (DTD | EBNF | XSD)
  (cmd/xmlserve serves both; cmd/xmlparse is a CLI: -schema <format>, -validate)
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
- **Process is the unified, mode-aware classifier.** `Process(src, schema,
  validating)` (`service/process.go`) subsumes the former Parse and ParseRSS:
  with no schema it returns the generic XML AST (the loosest projection); with a
  schema it projects the parsed tree into that vocabulary's typed message. It
  returns the AST/typed tree or a typed error (`*WFError` / `*ValidityError` /
  `*CannotValidateError`), surfaced over gRPC as `Documents.Process` (a
  `ProcessResponse` oneof). ADR 0008.
- **Vocabularies are schemas, projected by one generic engine.** A format's
  schema compiles to a typed proto descriptor through gluon's `compiler.Compile`,
  via a front-end per schema *language*: a DTD (`CompileDTD`), an EBNF element
  vocabulary (`CompileGrammar`), or an XSD (`CompileXSD`, `service/xsd.go`, which
  parses the XSD as XML and walks `xs:*`); `CompileSource` dispatches on the
  language. A single generic walk (`service/engine.go`, `project`) then projects
  any parsed `Tag` tree into the descriptor's typed message — it subsumes the old
  per-format `projectRSS`/`ProjectTag`. Namespace extensibility (which a DTD
  cannot express) is enforced in the walk: namespaced markup is a tolerated
  extension, unprefixed out-of-vocabulary markup is invalid. RSS 2.0 is
  `lang/rss.ebnf`; `Format("rss-2.0")` selects it. ADR 0008 (generalizing 0004/0007).
- **OPC packages are a layer over the parser.** A `.docx`/`.xlsx` is an OPC ZIP
  of XML parts plus `[Content_Types].xml` and a relationship graph;
  `ProcessPackage` (`service/opc.go`) unpacks it, parses each XML part through
  the same parser, and resolves content types and relationships into a typed
  package tree. The package layer is format-agnostic; only the part vocabularies
  (WordprocessingML, SpreadsheetML) differ. ADR 0008 Phase 5.

## Testing

- **Two gates.** `service/conformance_test.go` (`go test ./...`) is
  self-contained — it carries its own XML samples, so individual testing needs
  nothing fetched. The full W3C corpus is gated by the harness
  (`go run ./testing/xml-parse`, run by `test.sh`): the deterministic `xml/`
  corpus must be 100% (non-zero exit otherwise); the real-world `docx/xlsx/rss`
  corpora are reported but do not gate.
- **Vocabulary harnesses (reported, not gating).** `testing/schema-compile`
  compiles the RSS 0.91 DTD and projects the 0.91 corpus; `testing/rss-parse`
  runs `service.ParseRSS` over the real-world RSS 2.0 corpus
  (`testing/corpus/rss2.0`, 1000+ feeds fetched by `go run ./testing rss2.0`),
  reporting the projection pass rate. The deterministic RSS-2.0 correctness gate
  is `service/rss_test.go` under `go test ./...`. See ADR 0007.
- **OPC + XSD harnesses (ADR 0008).** `testing/opc-parse` runs `ProcessPackage`
  over the docx/xlsx corpus and **gates** (every package must unpack and every
  XML part parse; ~1000 packages, ~8000 parts). `testing/xsd-parse` compiles the
  W3C XSD test suite (`go run ./testing xsd`) with `CompileXSD` and reports
  coverage of the supported subset (reported, not gating — the suite spans full
  XSD). Both also have deterministic self-contained gates under `go test ./...`
  (`service/opc_test.go`, `service/xsd_test.go`).
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
| `lang/rss.ebnf` | RSS 2.0 schema grammar over the element vocabulary (hand-edited; ADR 0007) |
| `lang/xml.lex`, `lang/dtd.lex` | lexical specs (hand-edited) |
| `lang/cmd/genproto/` | grammar -> proto + maps + lexical tables |
| `lang/cmd/genproto_rss/` | rss.ebnf -> `proto/rss.proto` + `lang/rss.fdset` |
| `lang/embed.go` | embeds the grammars for the runtime |
| `lex/` | generic lexical-matcher engine (no grammar knowledge) |
| `proto/xml.proto` | hand-written homogeneous AST (Document/Tag) |
| `proto/xml_service.proto` | hand-written `Documents` + `Schemas` services |
| `proto/dtd.proto`, `proto/rss.proto`, `proto/pb/**`, `lang/*.fdset` | generated |
| `service/process.go` | `Process` (unified entry) + `Schema` + `Format` registry |
| `service/engine.go` | generic schema-driven projection walk (`project`) |
| `service/rss.go` | RSS 2.0 hard/soft constraints (`validateRSS`, `RSSConformance`) |
| `service/xsd.go` | XSD front-end (`CompileXSD`); `service/opc.go` = OPC packages (`ProcessPackage`) |
| `service/` (rest) | parser, well-formedness, namespaces, DTD validity, gRPC servers |
| `cmd/xmlparse/` | CLI: file/stdin -> AST (`-schema <format>`, `-validate`, `-cst`) |
| `cmd/xmlserve/` | gRPC server (Documents + Schemas) |
| `testing/` | corpus fetcher (`go run ./testing`, `… rss0.91`, `… rss2.0`, `… xsd`); corpora gitignored |
| `testing/xml-parse/` | W3C XML conformance harness (gates `xml/` at 100%) |
| `testing/schema-compile/` | RSS 0.91 DTD->proto + projection harness |
| `testing/rss-parse/` | RSS 2.0 corpus harness |
| `testing/opc-parse/` | OPC docx/xlsx package harness (gates) |
| `testing/xsd-parse/` | W3C XSD suite harness (reported) |
| `docs/decisions/` | ADRs (0008 = current architecture) |
