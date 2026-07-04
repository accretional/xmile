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
  `go run ./lang/cmd/genproto`, then protoc on `dtd.proto`/`xml.proto`/
  `xml_service.proto`) when a grammar or a hand-written proto changes; the
  artifacts are committed, so `build.sh` only sets up and builds. Generated:
  `proto/dtd.proto`, `proto/pb/dtd/{dtd.pb.go,prefix_map.go,separator_map.go,
  lexical.go}`, `proto/pb/xml/{*.pb.go,lexical.go}`, and `lang/dtd.fdset`.
  **No format proto is generated:** RSS 2.0 — like docx/xlsx and any future
  format — is *data* in `formats/`, compiled to a descriptor on demand (see
  Architecture); there is no committed `rss.proto`.
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
  4. project CST -> xml.proto Xml (general entities expanded into content)
  5. attribute-value normalization by declared type
  6. namespaces (namespace.go, integral): resolve QNames, enforce the namespace
     constraints, fill Tag/Attribute.namespace
  7. DTD validity (validate.go), only when validating: content models, attribute
     decls, ID/IDREF; internal parameter entities expanded (pe.go); an external
     subset / external PE yields CannotValidate
  8. with a schema: project the Xml tree into the vocabulary's typed message
     (engine.go); generic XML is the no-schema corner of the same step
        │
        ▼
Documents.Process(bytes, schema, mode) -> ProcessResponse
  no schema -> the generic XML AST (the former Parse), keyed `xml`
  a schema  -> the vocabulary's typed tree, keyed by its root element (e.g. `rss`)
  oneof result: { google.protobuf.Struct document | ProcessError{verdict, reason} }
    `document` is one Struct keyed by vocabulary: {xml:…} | {rss:…} | {note:…} | …
  verdict: NOT_WELL_FORMED | WELL_FORMED | VALID | INVALID | CANNOT_VALIDATE
  Schemas.Compile(source, language) -> FileDescriptorProto   (DTD | EBNF | XSD)
  Documents.Generate(Xml) -> bytes   (the inverse of Process: serialize the
    generic XML AST back to a document; parse(Generate(parse(b))) == parse(b).
    Takes the typed Xml tree — only the lossless generic AST round-trips; a
    format's typed projection is a read-only view, so it is not a Generate input.)
  (cmd/xmlserve serves all; cmd/xmlparse: doc -> AST; cmd/xmlgenerate: doc -> AST -> doc)
```

## Architecture

- **Generated lexical tables.** `lang/*.lex` declares each token matcher
  (`run` / `name` / `except` / `until` / `balanced`); `genproto` compiles it to
  `proto/pb/*/lexical.go`. The generic engine in package `lex` turns the table
  into gluon token matchers. No character ranges or delimiters live in Go.
- **Homogeneous AST.** Every element is a `Tag`; the element type is data
  (`Tag.name`), not a message type. `Xml.doctype` references the generated
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
  `ProcessResponse` oneof whose `document` is one Struct keyed by vocabulary).
  ADR 0008.
- **Vocabularies are schemas, projected by one generic engine.** A format's
  schema compiles to a typed proto descriptor through gluon's `compiler.Compile`,
  via a front-end per schema *language* in `service/language/`: a DTD
  (`CompileDTD`), an EBNF element vocabulary (`CompileGrammar`), or an XSD
  (`CompileXSD`, `service/language/xsd.go`, which parses the XSD as XML and walks
  `xs:*`); `CompileSource` dispatches on the language. (`service/schema.go` holds
  thin wrappers that inject the parser dependencies into those front-ends.) A
  single generic walk (`service/engine.go`, `project`) then projects any parsed
  `Tag` tree into the descriptor's typed message — it subsumes the old per-format
  `projectRSS`/`ProjectTag`. Two per-vocabulary knobs tune the walk: namespace
  extensibility (a DTD cannot express it — namespaced markup is a tolerated
  extension, unprefixed out-of-vocabulary markup is invalid) and `open` (a
  minimal schema for a large format tolerates unmodeled markup; see OPC below).
- **Formats are data.** Every format is a spec file under `formats/`
  (`rss-2.0.ebnf`, `docx.xsd`, `xlsx.xsd`), embedded by `formats/embed.go`.
  `Format(name)` (`service/formats.go`) reads the spec by extension, compiles it
  with the matching front-end, and applies that format's `formatMeta`
  (`nsExtensible`, `preValidate`, `open`). The only Go a format may need is the
  irreducible, CFG-inexpressible semantics: for RSS 2.0 that is `service/rss.go`
  (`validateRSS` — the `version`/`<channel>` pre-check wired in as the format's
  `PreValidate`; `RSSConformance` soft rules; the `ParseRSS` convenience).
- **Generate is the inverse of Process.** `Generate(*xmlpb.Xml)` (`service/
  generate.go`) serializes the generic XML AST back to bytes — a plain recursive
  walk over the concrete `Tag`/`Attribute`/`ContentItem` tree plus XML escaping
  (no schema, no reflection), and one generic CST-unparse of the DOCTYPE that
  re-emits each `dtd` message's stripped keywords (`dtdpb.MessagePrefix`) and its
  string leaves in field order. It is faithful at the infoset level
  (`parse(Generate(parse(b))) == parse(b)`; not byte-identical — entity spelling,
  quote style, encoding and insignificant whitespace are not recorded). Only the
  *generic* AST round-trips, so `Documents.Generate` takes the typed `Xml` tree,
  not a Struct — a format's typed projection is a read-only view that may drop
  unmodeled markup and is, by type, not a generate input. CLI: `cmd/xmlgenerate`.
- **OPC packages are a layer over the parser.** A `.docx`/`.xlsx` is an OPC ZIP
  of XML parts plus `[Content_Types].xml` and a relationship graph;
  `ProcessPackage` (`service/opc.go`) unpacks it, parses each XML part through
  the same parser, and resolves content types and relationships into a typed
  package tree. The package layer is format-agnostic; only the part vocabularies
  (WordprocessingML, SpreadsheetML) differ, and they are modeled by **minimal,
  `open` schemas** (`formats/docx.xsd`, `formats/xlsx.xsd`) so a small spec still
  accepts every valid container: the modeled core is typed, the rest passes
  through, and the full untyped tree is always available from a no-schema parse.
  ADR 0008 Phase 5.

## Testing

- **Two gates.** `go test ./...` is self-contained — `service/*_test.go`
  (`conformance_test.go`, `rss_test.go`, `opc_test.go`, `xsd_test.go`,
  `docx_test.go`, `xsd_structural_test.go`, `hardening_test.go` — the
  resource-limit DoS gates) carry their own samples, so unit
  testing needs nothing fetched. The full corpus is gated by one runner
  (`go run ./testing`, run by `test.sh`), which fetches the corpus on first run
  and parses every format's docs through the service.
- **What the corpus runner checks** (all in `testing/main.go`):
  - **xml — gates.** W3C XML conformance suite must be 100% (non-zero exit
    otherwise): every `valid`/`invalid`/`not-wf` test classified correctly.
  - **rss-2.0 — reported.** Projects the real-world RSS 2.0 corpus
    (`testing/corpus/rss2.0`, thousands of feeds) and confirms the curated
    invalid set is rejected. The deterministic RSS-2.0 correctness gate is
    `service/rss_test.go`.
  - **opc / opc-vocab — gates.** `ProcessPackage` over the docx/xlsx corpus:
    every package must unpack and every XML part parse, and every modeled part
    must project against its `open` format schema.
  - **docx-web — reported.** `ProcessPackage` over a sample of the real-world
    superdoc-dev/docx-corpus (`testing/corpus/docx-web`, ~2000 web-scraped
    `.docx`); reports the parse rate and modeled-part projection. Not gating —
    these are messy public-web documents, so a malformed package (e.g. an empty
    part) is expected, not a parser bug; the curated docx/ set is the gate.
  - **xsd — reported.** Compiles the W3C XSD test suite with `CompileXSD` and
    reports coverage of the supported subset (the suite spans full XSD and
    includes deliberately-invalid schemas, so it does not gate).
  - **generate — gates.** For every well-formed `xml/` doc: parse, serialize via
    the `Documents.Generate` RPC, re-parse, and assert the AST is unchanged
    (`parse(Generate(parse(b))) == parse(b)`, at the canonical infoset — text
    runs coalesced, encoding normalized to UTF-8). `service/generate_test.go` is
    the self-contained companion.
- The corpus is fetched and organized by file type via `go run ./testing` (or
  `go run ./testing fetch` to rebuild only the corpus). Corpora are gitignored.
- The XML corpus is the applicable subset (XML 1.0 5th edition and 1.1, plus
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
| `proto/xml.proto` | hand-written homogeneous AST (`Xml`/`Tag`) |
| `proto/xml_service.proto` | hand-written `Documents` + `Schemas` services |
| `proto/dtd.proto`, `proto/pb/**`, `lang/dtd.fdset` | generated |
| `service/process.go` | `Process` (unified entry) + `Schema` value |
| `service/generate.go` | `Generate` (inverse of Process): serialize the `Xml` AST -> bytes |
| `service/engine.go` | generic schema-driven projection walk (`project`) |
| `service/language/` | schema-language front-ends: `CompileDTD`/`CompileGrammar`/`CompileXSD`/`CompileSource` |
| `service/schema.go` | thin wrappers injecting parser deps into the front-ends + projection helpers |
| `service/formats.go` | `Format(name)` registry: read `formats/` spec -> compile -> apply `formatMeta` |
| `service/rss.go` | RSS 2.0's CFG-inexpressible semantics (`validateRSS`, `RSSConformance`, `ParseRSS`) |
| `service/opc.go` | OPC docx/xlsx packages (`ProcessPackage`) |
| `service/{grpc,schema_grpc}.go` | `Documents` + `Schemas` gRPC servers |
| `service/` (rest) | parser, well-formedness, namespaces, DTD validity |
| `formats/` | format specs as data (`rss-2.0.ebnf`, `docx.xsd`, `xlsx.xsd`), compiled on demand |
| `cmd/xmlparse/` | CLI: file/stdin -> AST (`-schema <format>`, `-validate`, `-cst`) |
| `cmd/xmlgenerate/` | CLI: file/stdin -> AST -> regenerated document (round-trips through Generate) |
| `cmd/xmlserve/` | gRPC server (Documents + Schemas) |
| `examples/process/` | runnable client example against the services |
| `testing/main.go` | one corpus runner (gates xml + opc; reports rss + xsd) |
| `testing/fetch.go` | one corpus fetcher (W3C XML suite, OOXML, RSS, W3C XSD suite) |
| `docs/decisions/` | ADRs (0008 = current architecture; 0009 = resource limits) |
| `service/limits.go` | DoS guards: nesting-depth + entity-expansion caps (ADR 0009) |
| `ARCHITECTURE.md`, `docs/REFERENCES.md` | data-flow overview + official spec sources |
