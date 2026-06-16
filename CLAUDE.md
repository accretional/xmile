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
  5. DTD validity (validate.go): content models, attribute decls, ID/IDREF,
     when the document has an internal subset (no external subset / PEs)
        │
        ▼
XmlService.Parse(bytes) -> Document
  not-well-formed -> INVALID_ARGUMENT ; DTD-invalid -> FAILED_PRECONDITION
  (cmd/xmlserve serves it; cmd/xmlparse is a CLI)
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
  defaults, ID/IDREF, and the DTD-level VCs. It runs only for an internal subset
  with no external declarations or parameter entities (which it does not expand),
  so a valid document is never wrongly rejected. See ADR 0005.

## Testing

- **`service` is the gate** (`go test ./...`): `TestCorpusWellFormed` runs over
  `testing/corpus/xml/<verdict>/` and requires every valid document to parse,
  every invalid document to be rejected as DTD-invalid (a `*ValidityError`), and
  every not-wf document to be rejected, at or above `minRejectRate`.
- The corpus is fetched and organized by file type via `go run ./testing`;
  `go run ./testing/xml-parse` prints the corpus report (xml, docx, xlsx, rss).
- The corpus is the applicable subset (XML 1.0 5th edition and 1.1; no
  namespaces or external entities), filtered at fetch time, so nothing is
  skipped at test time. `invalid` tests with no internal subset, or whose DTD
  uses parameter entities, are out of the validity scope and also filtered (see
  `testing/README.md`). The IBM `P85-P89` name-character tests are 4th-edition
  only and are excluded by the edition filter.

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
