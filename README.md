# xmile
XML EBNF grammar, metaparsing/formalization of the format, parsing, and transformations

## Quick run

```bash
bash LET_IT_RIP.sh # Setup + build + test. Will take some time to test against the full corpus.
```

```bash
bash serve.sh # Setup + build + serve
```

## Scripts

All work goes through these (idempotent, chained: each runs the one before it). A clean clone needs nothing else installed.

- **`setup.sh`** — clones the dependency repos (`gluon`, `proto-merge`) as siblings and installs the toolchain (protoc + Go plugins).
- **`build.sh`** — `setup` → `genproto` (grammar → `proto/dtd.proto` + lexical tables) → `protoc` → `go build`.
- **`test.sh`** — `build` → fetch the test corpus on first run (`go run ./testing`) → `go test ./...` (the conformance gate) → corpus report (`go run ./testing/xml-parse`).
- **`serve.sh`** — `build` → serve the `XmlService.Parse` gRPC server.
- **`LET_IT_RIP.sh`** — `setup` + `build` + `test` + a live parse demo.

## How it works

The structural grammar lives in `lang/*.ebnf` and the lexical layer in `lang/*.lex`; `genproto` compiles both into `proto/`. The runtime parser (`service/`) is grammar-free: it drives gluon with the generated lexical table, projects the CST into the hand-written AST (`proto/xml.proto`), runs the well-formedness walk, then parses the inline DTD and enforces the entity/DTD well-formedness constraints. See `docs/decisions/` for the design (ADRs).

## Done

- **Well-formedness parsing — XML 1.0 (5th edition) and 1.1.** Version dispatch, restricted-char / Latin-1 / line-end handling, references, CDATA, comments, PIs, the inline DTD (internal subset), and the entity well-formedness constraints.
- **AST + service.** Parses to the homogeneous `proto/xml.proto` tree (declared text entities resolved); exposed over gRPC.
- **Conformance: 100% of the applicable W3C subset** — every `valid`/`invalid` document parses and every `not-wf` document is rejected (277 / 97 / 814). OOXML parts parse (xlsx 986, docx 45).

## To do / not yet tested

- **DTD *validity*** (the big one). We parse the DTD and enforce well-formedness, but do **not** yet validate a document against it — element content models and attribute declarations aren't checked, so `valid` vs `invalid` is not distinguished (both are treated as well-formed). This is the next milestone: content-model checking → the `Valid` / `Invalid` verdict.
- **External entities / external DTD subset** — skipped (the conformance subset excludes `ENTITIES != none`).
- **Namespaces** (Namespaces in XML) — out of scope.
- **Typed per-vocabulary codegen** (DTD → one proto message per element) — deferred (ADR 0002); the runtime AST stays homogeneous.
- A couple of real-world RSS feeds where our parser and Go's `encoding/xml` oracle disagree (non-gating).
