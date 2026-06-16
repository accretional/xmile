# ADR 0003 — Proto strategy: hand-written XML AST, generated DTD, parse-as-service

- **Status:** Accepted
- **Date:** 2026-06-15
- **Decision:**
  1. `proto/xml.proto` is **hand-written** — the homogeneous `Tag` / `ContentItem`
     AST, expanded to cover all XML node kinds.
  2. `proto/dtd.proto` is **generated** from `lang/dtd.ebnf` via a
     `lang/cmd/genproto` driver, following the proto-sqlite / proto-http
     pipeline (`ParseEBNF → GrammarToAST → transforms → Compile`).
  3. Parsing is exposed as a **gRPC service** (`proto/xml_service.proto`,
     `XmlService.Parse`), modeled on proto-http's `HttpService.Parse`: bytes in,
     AST out. There is no separate public parser API — callers invoke the service.
  4. The DOCTYPE **internal subset (inline DTD)** is parsed, not treated as an
     opaque string.

---

## 1. Why XML hand-written but DTD generated

`gluon`'s `compiler.Compile` emits **one message per grammar production** — a CST
mirror. The two grammars sit on opposite sides of that fact:

- **XML well-formedness is homogeneous and recursive** — one element shape,
  repeated. `Compile(xml.ebnf)` would yield `Element` / `STag` / `ETag` /
  `Content` / … , not the desired single `Tag`. Collapsing that into `Tag` would
  need a bespoke transform, and the target proto is only ~6 messages — so
  **hand-writing is simpler and clearer**. The parser parses with the gluon
  engine + `xml.ebnf`, then **projects** the CST into this clean model.
- **DTD is heterogeneous** — many distinct declaration kinds (`ElementDecl`,
  `AttlistDecl`, `EntityDecl`, `NotationDecl`, content models). That is exactly
  what `Compile` produces well (as SQL statements are in proto-sqlite). So
  **`dtd.proto` is generated**.

This is the same homogeneous-vs-heterogeneous asymmetry noted throughout the
design: one recursive shape wants a hand model; many declaration shapes want
grammar-derived generation.

## 2. Parse-as-service

- Shape mirrors proto-http: `XmlService.Parse(ParseRequest{bytes}) →
  ParseResponse{Document}`.
- **Contract:** returns the parsed **and augmented** `Document` AST on success.
  The outcome is carried by the **RPC status**, not a verdict field — there is no
  `Verdict` enum:
  - **success** — a well-formed tree (and, when a DTD is present, DTD-valid);
    whether it was validated is visible from `Document.doctype`.
  - **not-well-formed** — error, `INVALID_ARGUMENT`, no tree.
  - **invalid** (well-formed but fails DTD validity) — error,
    `FAILED_PRECONDITION`, no tree.
  Distinct status codes keep the `not-wf` vs `invalid` taxonomy (ADR 0001 §5)
  recoverable without parsing the message string.
- The service handler *is* the parser; the parse steps live under `xml/`.

## 3. Inline DTD (internal subset) — the common case

Most documents carry their DTD **inline** as the DOCTYPE internal subset:

```xml
<!DOCTYPE root [ <!ELEMENT root (a)> <!ENTITY x "…"> … ]>
```

External-only DTDs are the exception. Today the grammar captures the subset as an
opaque `dtd_text` string and the pipeline ignores it. That changes:

- The internal subset is **parsed** — a second pass over the captured span using
  `lang/dtd.ebnf` → `dtd.proto` → the Step-0 tables — and used for validation +
  infoset augmentation.
- **Grammar consequence — the DOCTYPE matcher must be bracket-aware.** The
  internal subset `[ … ]` contains `>` characters inside declarations
  (`<!ELEMENT a (b)>`), so a naive "up to the next `>`" matcher is wrong. The
  matcher must span the balanced `[ … ]` and then the closing `>`. (Tracked as a
  `matchers.go` / `xml.ebnf` fix.)

## 4. Consequences

- `Document.doctype` references the generated **`dtd.Doctype`** rather than a
  hand-written header: the DOCTYPE body (root name, external ID, inline internal
  subset) is wholly a DTD construct, so it lives in `dtd.proto` and the parsed
  inline DTD rides directly on the `Document`. This couples `xml.proto → dtd.proto`,
  so `genproto` must produce `dtd.proto` **before** `protoc` compiles `xml.proto`.
- The `genproto` driver must **stub the `dtd.ebnf` token matchers**
  (`S` / `Name` / `Nmtoken` / `litDq` / `litSq`) — they have no productions and
  would compile to dangling refs (cf. proto-sqlite `scalarizeX`, proto-http
  `stubs.go`).
- Generated files (`proto/dtd.proto`, `proto/pb/*`) are **never hand-edited** —
  regenerate via `go run ./lang/cmd/genproto`.
- Build wiring follows the sibling repos: `build.sh` runs `genproto` then
  `protoc`; generated artifacts are committed.
