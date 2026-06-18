# ADR 0008 — Gluon-central redesign: schema-as-AST, unified Process, AST-internal instances

- **Status:** Accepted — Phases 1–5 implemented; Phase 6 deferred
- **Date:** 2026-06-17
- **Decision:** Re-found the parser/service stack on the principle that **gluon is
  the central AST substrate and every transform is a gluon AST transform**. The
  format-specific Go that today hand-codes XML→DTD-validation (`validate.go`) and
  XML→RSS (`rss.go`) is replaced by data: a **canonical schema IR expressed as a
  gluon schema-dialect `ASTDescriptor`**, into which every metagrammar (DTD, EBNF,
  XSD, RELAX NG) compiles via an AST→AST front-end; a **single generic
  project+validate walk** consumes it; and `Parse` collapses into a **unified
  `Process`** RPC for which generic XML is just the loosest schema. Instances move
  to `ASTNode`-internal with typed protos as derived edge views — staged last and
  gated (§7, §10). **No gluon change is required to begin** (§9,
  [gluon-upstream.md](../gluon-upstream.md)).

**Implementation status (Phases 1–5 shipped).** What was built deviates from the
original plan in three deliberate, recorded ways: (1) the canonical schema-dialect
`ASTDescriptor` with `occurs`/`facet`/`identity` kinds + lowering-view + hook
side-table (Phase 1) was *not* built — instead `Schema{File, Root, NSExtensible,
PreValidate, Open}` (`service/process.go`) carries the compiled descriptor plus a
thin structural pre-check; the full constraint-IR stays deferred (§11). (2) The DTD
document validator (`validate.go`) is preserved intact and reached *through*
`Process`, rather than re-expressed via the IR, to protect the 100% W3C corpus
gate (§11). (3) Large formats (OOXML docx/xlsx) are modeled by a **minimal,
open schema** (`Schema.Open`): the format spec in `formats/*.xsd` types only the
core elements, and the generic walk *tolerates* unmodeled markup instead of
rejecting it (`projectOptions.open`, `engine.go`). This is what lets a small spec
accept *every* valid container — modeled markup is projected and typed, the rest
passes through, and the full untyped tree is always available from a no-schema
parse. It is a partial-projection contract, **not** a raw catch-all field on the
typed message; nothing is silently dropped that the no-schema AST does not already
hold. Shipped and green: the generic projection engine (`engine.go`), the unified
`Documents.Process` + `Schemas.Compile` service, the full XSD front-end (`xsd.go`:
groups, attribute groups, wildcards, substitution groups, derivation
extension/restriction, union/list, import/include; 90.0% of the W3C XSD suite
compiles within the supported subset), and the OPC package layer (`opc.go`, ~1060
docx/xlsx packages from three producers, every part parsed + projected). Phase 6
(instances as `ASTNode`) remains optional/deferred. ADRs 0004/0006/0007 are
superseded in part (see §8).

---

## 1. Context — why redesign

The two existing format transforms are hand-coded and do not generalize:

- **DTD validation** (`validate.go`) re-walks the DTD CST into bespoke Go structs
  (`parseElemDecl`, `parseContentModel` NFA, `parseAttType`) and checks the tree
  against them — a procedural constraint walk.
- **RSS** (`rss.go`) is partly grammar-driven (`rss.ebnf` → `CompileGrammar` →
  descriptor) but wraps it in hand-written Go: `projectRSS`, `validateRSS` (hard
  rules), `RSSConformance` (soft rules).

Adding a format (XSD/OOXML for DOCX, Atom, …) today means writing another such
walk. ADR 0002 deferred a multi-front-end design; ADR 0007 took the first step
("schema-as-schema": one IR, one backend, a front-end per schema *language*). This
ADR completes that direction and removes the per-format Go.

## 2. Principle — one substrate, no parallel IR

Gluon's homogeneous `pb.ASTNode` tree is the working representation; `astkit`
(Walk/Find/ReplaceKind/Filter), the `Transform` surface, and `compiler.Compile`
are the transform/lowering engines. **A bespoke typed `Schema` proto would be a
second IR none of that machinery can touch, and is rejected.** Everything xmile
adds is either (a) a documented `kind` convention over `ASTNode`, (b) an AST→AST
transform, or (c) a derived typed view at an edge.

## 3. The canonical schema IR — a schema-dialect `ASTDescriptor`

The IR is an `ASTDescriptor` in a **schema dialect**: the kinds
`compiler.Compile` already lowers (`file`/`rule`/`sequence`/`alternation`/
`optional`/`repetition`/`group`/`nonterminal`/`scalar`/`terminal`/`range`) plus
xmile **constraint kinds** the compiler does not lower — `occurs`, `facet`,
`identity`, `attribute`, `required`, `mixed`. The full dialect is specified in
`docs/schema-dialect.md` (Phase 1 deliverable).

`compiler.Compile` is **closed-vocabulary**: unknown kinds are a hard error
(`compiler.go:250`; `kinds.go`: *"Any other kind at any position is an error."*).
So constraints cannot ride through `Compile`. They are carried by the established
gluon idiom — **derive a reduced lowering view, stash metadata on known nodes,
harvest via hooks** (cf. `strip_keywords.go`, `CollapseCommaList`, and xmile's own
`scalarizeUndeclared`, `schema.go:127`):

```
canonical schema-AST            (full dialect: structure + constraint kinds)
   ├─ derive lowering view      (compiler-known kinds only)
   │     → compiler.Compile      → FileDescriptorProto  (structure → projection)
   │       (OnField/OnMessage correlate each emitted field/message ↔ schema-AST node)
   └─ constraint model           (occurs / facets / identity / required / mixed)
         → read by the validator (the part the descriptor structurally cannot carry)
```

The hooks observe but cannot mutate the descriptor, so constraints live in a Go
side-table keyed by field FQN (serializing them into the `.proto` is a deferred
gluon nicety — gluon-upstream.md §1). This is exactly the lossy-by-design split:
the descriptor holds *structure* (drives projection); the constraint model holds
*order/occurrence/absence/identity* (drives validation).

## 4. Metagrammar front-ends — one per schema *language*

A front-end is an AST→AST transform producing the canonical schema-AST. The
elegant case: **XML-based schema languages are parsed by xmile's own XML parser,
then walked** — only non-XML languages need a bespoke parse.

| Language | Front-end | Status |
|---|---|---|
| **EBNF-vocab** (`rss.ebnf`) | `metaparser.ParseEBNF` → `GrammarToAST` → normalize | exists (`CompileGrammar`) |
| **DTD** | `dtd.ebnf` parse → walk CST → normalize | exists (`CompileDTD`), to fold in constraints |
| **XSD** | `Parser.Process(xsd)` → walk `xs:*` → normalize | new (Phase 4) |
| **RELAX NG** | `Parser.Process(rng)` → walk → normalize | later |

XSD-specific resolution (derivation flatten, substitution-group expansion,
`minOccurs`/`maxOccurs`→`occurs`, facets, `key`/`keyref`→`identity`,
import/include) happens **in the front-end**, so the runtime carries no XSD
knowledge.

## 5. Unified `Process` — parsing is the loosest projection

Generic XML is the result of processing against the loosest schema; a format
schema is a refinement on the same axis. `Parse` is therefore the empty-schema
corner of `Process`, and the service collapses to two:

```proto
service Schemas   { rpc Compile(CompileRequest) returns (CompileResponse); }   // metagrammar → schema-AST (+ derived descriptor)
service Documents { rpc Process(ProcessRequest) returns (ProcessResponse); }   // bytes (+ optional schema) → result + verdict

message ProcessRequest {
  bytes source = 1;
  SchemaSelector schema = 2;   // EMPTY ⇒ generic XML AST (old Parse); else format name / inline / compile-then-use
  Mode mode = 3;               // PROJECT_ONLY | VALIDATE
}
message ProcessResponse {
  oneof result {
    xml.Document document = 1;       // no schema: the generic tree
    google.protobuf.Any typed = 2;   // a schema: dynamicpb from the derived descriptor
    ParseError error = 3;
  }
  Verdict verdict = 4;               // NOT_WELL_FORMED | WELL_FORMED | VALID | INVALID | CANNOT_VALIDATE
  repeated Diagnostic diagnostics = 5;  // soft conformance (severity-tagged)
}
```

`SchemaSelector.compile` lets a document carry its own schema (the internal DTD
subset, an `xsi:schemaLocation`), so today's "DTD second pass" becomes
`Process(schema = compile(internal subset, DTD))` with no special case.

## 6. One walk, two inputs — and severity as data

A single traversal builds the typed message *and*, when `mode = VALIDATE`, checks
constraints (content-model order via the surviving NFA, `occurs`, facets,
identity, `required`, `mixed`), accumulating the ID table on the way. It consumes
two inputs: the **descriptor** (placement) and the **constraint model** (checks).
This fuses `projectRSS`, `ProjectTag`, and `validate.go` into one engine.

RSS's hard/soft split becomes data: each decl/constraint carries a `Severity`
(`ERROR` | `WARNING`). `VALIDATE` enforces `ERROR` and reports `WARNING` as
`diagnostics` — so `validateRSS` (root is `<rss version="2.0">`, one `<channel>`,
the namespace-extensibility rule) and `RSSConformance` (the "SHOULD" rules)
become constraints in the RSS schema, not Go.

## 7. Gradual typing — AST-internal instances, typed edges

End-state: the working instance representation is `pb.ASTNode` (not the
`xml.Document` Tag tree), so `astkit`/`compiler`/`Transform` apply uniformly to
documents and schemas, the Tag↔ASTNode bridge disappears, and format-specific
derived data (DOCX relationship targets, computed styles) attaches as open `kind`
nodes without amending `xml.proto`.

The cost is real and bounded: xmile's **engine code** (well-formedness, namespace
resolution, the generic walk) loses `go build` type safety over the generic-XML
vocabulary; checks become runtime and **coverage-dependent** (a typo on a cold
path ships). It is recoverable only via typed-facade codegen for the closed
vocabularies (gluon-upstream.md §3, "Flavor B"). Static safety over the *format*
layer is impossible by construction — formats are runtime data. Therefore this
move is **staged last (Phase 6) and gated** on the facade being available (or an
explicit decision to accept a stringly-typed engine). Crucially, the format
machinery (Phases 1–5) does **not** depend on it: the generic walk runs on the Tag
tree today.

## 8. Supersedes / amends

- **ADR 0002** (schema-language scope) — *realizes* its deferred multi-front-end.
- **ADR 0003** (proto strategy) — *amends* on Phase 6: instances become `ASTNode`
  internally. `xml.proto` and `xml_service.proto` stay **hand-written**; the
  recommended outcome (§7) is to keep `xml.Document` as the public output
  contract, built at the edge — not to delete it. A generated facade over
  `ASTNode` is an alternative, not the default.
- **ADR 0004** (DTD-as-schema) — *generalizes*: `CompileDTD` is one front-end into
  the shared IR, now also carrying constraints.
- **ADR 0006** (modes/verdict/namespaces) — *amends*: `Parse` folds into
  `Process`; verdicts and the namespace walk are reused.
- **ADR 0007** (RSS) — *supersedes the Go*: `projectRSS`/`validateRSS`/
  `RSSConformance` become RSS-schema data + the generic engine; `rss.ebnf` stays.

These edits (and CLAUDE.md) are made **as each phase lands**, not now.

## 9. Gluon impact

**None required to start.** §3's mechanism rides stock gluon. Deferred niceties
(serialize constraints into the descriptor; opaque-annotation passthrough;
typed-facade codegen gating Phase 6; a reusable AST-schema validator; richer
`astkit` combinators) are logged in [gluon-upstream.md](../gluon-upstream.md) with
workarounds and revisit triggers.

## 10. Migration plan

Ordered by value and risk. Each phase keeps `LET_IT_RIP.sh` green; format work
(1–5) is independent of the instance-rep change (6). **Phases 1–5 are the
complete working system** (hardcoded transforms gone, XSD additive, DOCX
parsing). **Phase 6 is optional** — an internal-representation improvement paired
with gluon-upstream.md §3 (typed-facade codegen) and triggered by a concrete need
(open-ended annotation over the generic tree), not scheduled.

- **Phase 0 — Spike & spec.** Confirm the lowering-view + hook side-table carries
  an `occurs`/`facet` end-to-end on a toy schema. Write `docs/schema-dialect.md`
  (the kind vocabulary). Decide: constraints in a Go side-table (default) vs
  serialized field options (only if external consumers need them). Output: this
  ADR accepted.
- **Phase 1 — Canonical schema IR.** Make `CompileDTD`/`CompileGrammar` emit the
  canonical schema-AST, then derive the lowering view + `Compile`. Introduce the
  `Schema` Go value `{descriptor, constraint side-table}`. **No behavior change**
  (constraints not yet enforced); tests stay green.
- **Phase 2 — Generic project+validate engine.** One walk (§6) subsuming
  `projectRSS`+`ProjectTag`; port `validate.go` to read the DTD-derived constraint
  model (keep the content-model NFA); encode RSS hard/soft rules as schema
  severity. Delete `projectRSS`/`validateRSS`/`RSSConformance`.
- **Phase 3 — Service unification.** Add `Schemas`/`Documents`; fold `Parse`/
  `ParseRss` into `Process` (thin deprecated shims during migration, removed at
  the end). Update `cmd/xmlparse`, `cmd/xmlserve`, ADR 0006, CLAUDE.md.
- **Phase 4 — XSD front-end.** XSD-as-XML → walk → canonical schema-AST; add `XSD`
  to the language enum. Validate on a small XSD + instances corpus. Proves a
  non-EBNF language rides the IR with zero engine change.
- **Phase 5 — OPC package formats.** `Format{single | package}`; unpack the ZIP,
  route content-type → schema, process each part, assemble the typed package tree.
  DOCX/XLSX as the integration test, not the start.
- **Phase 6 — Instances as `ASTNode` (gated, §7).** Migrate `project.go`,
  well-formedness, `namespace.go`, and the engine to `ASTNode` (or a generated
  facade). Do only when the typed facade exists, or when DOCX's need to attach
  derived data forces the open vocabulary.

## 11. Risks & open questions

- **Order fidelity for XSD `all` / mixed content** — the NFA and the
  message-with-oneof wrapper (ADR 0004 §4) must extend cleanly; verify in Phase 4.
- **Constraint locality** — keying the side-table by field FQN must survive
  PascalCase/snake_case mangling and nested-message names; the hooks give the FQN,
  but round-tripping namespaced names (ADR 0004 §5) is still open.
- **Phase 6 safety regression** — quantify how much of the engine loses static
  checks before committing; the facade (gluon-upstream.md §3) is the mitigation.
- **Scalar datatypes** — typed leaves (`int`/`date`/enum) are an IR addition
  shared by XSD facets and EBNF terminals (ADR 0007 §6); lands with Phase 4.
</content>
