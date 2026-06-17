# ADR 0007 — RSS 2.0 as an EBNF schema grammar, parsed by walking the XML AST

- **Status:** Accepted
- **Date:** 2026-06-17
- **Decision:** Express RSS 2.0 as a hand-written **EBNF schema grammar**
  (`lang/rss.ebnf`), compile it to a typed proto AST through the same gluon
  engine that lowers `dtd.ebnf` (`service.CompileGrammar`, the EBNF sibling of
  `CompileDTD`), and **parse a feed by parsing it as generic XML and walking the
  resulting `Tag` tree against that compiled descriptor** (`service/rss.go`).
  Exposed as `XmlService.ParseRss`. No new gRPC schema-compile surface: gluon
  *is* the EBNF→proto compiler.

---

## 1. Why a grammar, and why EBNF (not DTD/XSD)

RSS 2.0 is a *dialect of XML* with its own **prose** spec — there is no
machine-readable schema to ingest. So the grammar is hand-authored, and EBNF is
the right surface for three reasons:

- It is the front-end gluon already has (`metaparser.ParseEBNF →
  compiler.GrammarToAST → compiler.Compile`), so `rss.proto` falls out of the
  existing pipeline with no new compiler — `CompileDTD` only exists because DTD
  *isn't* EBNF.
- It is a **schema grammar over the element vocabulary**, not a character
  grammar like `xml.ebnf`: `lang/xml.ebnf` already turns the bytes into the
  homogeneous `Tag` tree; `rss.ebnf` only describes the content models, so "RSS
  is XML" stays literally true (one parser, no re-lexing).
- It can state **namespace extensibility**, which a DTD provably cannot
  (ADR 0002 §1.1: DTDs are a closed, namespace-blind vocabulary). RSS 2.0
  permits any element/attribute "only if defined in a namespace."

This is the first concrete instance of the multi-front-end direction ADR 0002
deferred: one shared IR (the gluon AST) and backend (`compiler.Compile`), fed by
a front-end per schema *language* — DTD (`CompileDTD`), now EBNF
(`CompileGrammar`), later XSD for OOXML. It generalizes ADR 0004 from
"DTD-as-schema" to "schema-as-schema."

## 2. Grammar conventions (`lang/rss.ebnf` → `proto/rss.proto`)

`CompileGrammar` reuses CompileDTD's "declared → message, undeclared → string"
rule. A reference with no rule of its own lowers to a `string` field:

| In the grammar | Lowers to |
|---|---|
| `channel`, `item`, … (a rule) | a message field |
| `text` (no rule) | `string text` — a leaf's character content |
| `at_<name>` (no rule, marked) | `string <name>` — an attribute |
| `{ a \| b \| c }` | `repeated Alt { oneof value { A a; B b; C c; } }` (doc order) |
| `a , b , c` | flat message fields in order |

The `at_` marker is required because an attribute and an element can share a
name: `<url>` is a child of `<image>`, but `url=` is an attribute of
`<enclosure>`/`<source>`. `genproto_rss` writes the committed `proto/rss.proto`
+ `lang/rss.fdset`; the runtime recompiles the embedded grammar to a descriptor
and projects into `dynamicpb` (no committed Go for the vocabulary — same
lifecycle as the RSS 0.91 schema-compile harness).

## 3. Parsing = generic XML AST + a namespace-aware walk

`ParseRSS` runs the universal `Parser.Parse` (well-formedness + namespaces,
unchanged) to get the `Tag` tree, then `projectRSS` walks it against the
`rss.Rss` descriptor:

- **Attributes** fill string fields by name; a **leaf's** text fills its `text`
  field; **child elements** recurse into message fields (a lowered choice is a
  repeated wrapper-with-oneof, placed by the shared `placeChild`).
- **Namespace rule (the one a CFG can't express, enforced here):** RSS 2.0 core
  elements are unprefixed (no namespace). A **namespace-qualified** element or
  attribute is a *tolerated extension*; an **unprefixed out-of-vocabulary** one
  is **INVALID**. (`xmlns`/`xmlns:*` declarations are skipped.)
- **#PCDATA leaves carry inline HTML.** `<title>`/`<description>` may contain
  inline markup — entity-encoded or, as real feeds do, raw
  (`<title><a …>…</a></title>`). That markup is the element's *text*, not RSS
  vocabulary, so a leaf's value is all its descendant character data and the
  walk does not recurse into it (`gatherText`).

## 4. Hard validity vs soft conformance

The CFG-inexpressible constraints split two ways, mirroring the project's
"deterministic gates, real-world reports" testing stance:

- **Hard (`validateRSS` → `*ValidityError`, surfaced as `INVALID`):** the
  structural identity — root is `<rss version="2.0">` with exactly one
  `<channel>` — plus the namespace rule above. These decide whether the bytes
  are RSS 2.0 at all.
- **Soft (`RSSConformance` → warnings, never a parse failure):** rules the spec
  states but real feeds routinely bend — "required" `<channel>` title/link/
  description, and an `<item>` needing at least a title or description. A reader
  still uses such a feed (corpus `feed_015` has a contentless item), so these
  are reported, not gated.

## 5. Service surface

`XmlService.ParseRss(ParseRssRequest{xml}) → ParseRssResponse{oneof: Any rss |
ParseError error}`. The typed AST rides in a `google.protobuf.Any` so the
server need not link a generated Go `rss` type (it projects into `dynamicpb`); a
client decodes it with the descriptor from compiling `lang/rss.ebnf`. `Parse`
(bytes → `Tag`) is unchanged. Verdicts are reused: `NOT_WELL_FORMED` (bad XML),
`INVALID` (well-formed but not RSS 2.0).

## 6. Scope boundaries (v1)

- **Extensions are tolerated, not yet captured.** A namespaced element/attribute
  passes validation but is not stored as a field in the typed AST (that needs a
  `repeated xml.Tag` catch-all + a cross-file import the generic compiler does
  not yet emit). Tracked as follow-up.
- **Leaves are `string`.** No typed/`int`/`date`/enum leaves yet (`ttl`,
  `pubDate`, `skipDays/day`): EBNF carries no datatypes. Typed terminals are the
  next IR step, shared with the future XSD front-end.
- **Soft conformance is reported, not enforced** by `ParseRss` (see §4).

## 7. Validation

`testing/rss-parse` (run by `test.sh`) runs `ParseRSS` over a large real-world
corpus — thousands of live feeds fetched by `go run ./testing rss2.0` from OPML
lists (plenaryapp, kilimchoi) and the fountain-of-rss TSV catalog, kept only
when well-formed and `version="2.0"`. `-classify` splits them into the valid set
(`testing/corpus/rss2.0/*.xml`) and a curated `invalid/` set of genuine spec
violations found in the wild: wrong-position core elements (`item>image`,
`channel>author`, `rss>meta`), miscased names (`isPermalink`, `pubdate`),
unprefixed foreign markup (`<og>`, `feed_asset`, …), and XML/namespace
well-formedness errors (undeclared `media:` prefix, duplicate `xmlns:media`,
content after `</rss>`). The harness then checks both directions — every valid
feed must project (4310/4310 = 100%, ~147k items at time of writing) and every
invalid feed must be rejected (165/165) — reporting rather than gating, since
the wild long tail is for hardening (cf. the docx/xlsx/rss corpora).
`service/rss_test.go` is the self-contained correctness gate (core projection,
extension tolerance, the namespace rule, leaf folding, conformance warnings)
under `go test ./...`.
