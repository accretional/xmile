# ADR 0009 — Resource limits, and the adversarial-review findings behind them

Status: **accepted** (2026-07-03).

## Context

xmile parses untrusted XML (the corpus runner fetches real documents off the
public web; downstream services — e.g. proto-sitemap — hand it bytes from
arbitrary servers). An adversarial review probed the parser for
denial-of-service, XXE/SSRF, round-trip, and projection-soundness defects. This
ADR records what the review found and the guards added in response. Regression
gates: `service/hardening_test.go`.

## Findings

### Live defects — fixed here

1. **Deep-nesting stack-overflow crash (DoS).** The gluon recursive-descent
   parser and the ~dozen recursive Go tree walks after it (`wellformed.go`,
   `project.go`, `engine.go`, `generate.go`, the v1→v2 CST copy) carry no depth
   guard. Confirmed: a balanced `<a>…</a>` document ~500k deep (~3.5 MiB source,
   well under any size limit) triggers `runtime: goroutine stack exceeds
   1000000000-byte limit` — a **fatal** error `recover()` cannot catch, so the
   whole process dies. **Fix:** `nestingDepthExceeds` (`limits.go`) rejects input
   past `MaxNestingDepth` (10000) with a `*WFError` before the recursive parser
   runs. The bound is ~50× below the crash threshold and ~100× above any real
   document; a single cheap linear pre-scan, safe against false positives
   (empty-element tags don't count as depth; comments/CDATA/PIs/declarations are
   skipped).

2. **Billion-laughs entity expansion (DoS).** Internal general-entity expansion
   (`entities.go`) was unbounded and un-memoized, so nested definitions expand as
   `2ⁿ`, and it fires in the **default non-validating** path (`checkEntities` runs
   unconditionally). Three sub-paths each needed bounding:
   - `expandValue`/`expandText` (materializes the string): a shared byte budget,
     `maxEntityExpansionBytes` (10 MiB), via `expandValueCapped`.
   - `expandCheck` (structural WF walk, runs first, exponential *time* even
     without building a string): a `checked` memo — a nil return is a global
     "clean" property, safe to cache — collapsing it to O(V+E).
   - `externalInChain` (attribute-value path): a `hasExternalGeneral`
     short-circuit — a bomb declares no external entity, so the graph walk is
     skipped entirely (O(1)).
   Result: a 30-level bomb (~8 GiB expanded) is now a `*WFError` in ~10 ms.

3. **Validating-mode parameter-entity blowup (DoS, narrower).** `expandPEs`
   (`pe.go`) doubles the DTD body per round for 64 rounds (up to `2⁶⁴`), reachable
   only in validating mode. **Fix:** cap the expanded body at `maxPEExpansionBytes`
   (10 MiB); past it the document is `CannotValidate`.

4. **DOCTYPE silently dropped by Generate (correctness/honesty).** The projector
   never populates `Xml.Doctype` (`project.go`'s `dtd_text` TODO), so
   `GetDoctype()` is always nil, the `unparseCST` DOCTYPE re-emitter in
   `generate.go` is dead, and the package comment's claim that "a document with a
   DOCTYPE round-trips like any other" was false. **Fix (this pass):** correct the
   comments to state the limitation, mark `unparseCST` dormant, and pin the
   behavior with a test. **Not fixed:** actually reconstructing `Xml.Doctype` from
   the parsed internal subset — a CST→`dtd.Doctype` projection — remains the
   original TODO (a feature, tracked, not a soundness bug: the round-trip
   invariant holds because Doctype is absent on both sides, and the subset's
   side effects are already baked into the element tree).

### Sound — confirmed closed (no change needed)

- **XXE / external-entity SSRF: closed.** External general and parameter entities
  are recorded but **never** read from disk or network (`project.go:344`,
  `entities.go` `ent.external` short-circuits, `pe.go` refuses external PEs → the
  reference is left literal or the document is `CannotValidate`). No `os.Open`/
  `http` exists in the entity path.
- **Round-trip invariant holds.** `parse(Generate(parse(b))) == parse(b)` at the
  canonical infoset survived ~80 adversarial probes with a fixed-point detector
  across CDATA, char/entity references, comments, PIs, attribute normalization,
  namespaces/rebinding, and whitespace. The parser bakes every lossy transform
  into the AST at parse time and Generate re-emits the transformed values; the
  one intended asymmetry (reference-split text runs) is absorbed by `coalesceText`.
- **No catastrophic backtracking.** gluon's alternation is longest-match-and-
  commit (not PEG ordered choice) and `xml.ebnf`'s alternatives are
  prefix-disjoint, so parsing is linear in node count; no superlinear input
  exists over this grammar.

### By-design, documented (not changed)

- `Open` projection never rejects and silently drops unmatched markup — including
  markup in the schema's own namespace (`engine.go`); under `Open`, local-name
  matching ignores namespace, so a namespaced element sharing a core local name
  can be absorbed into a core field. `nsExtensible` routes namespaced markup to a
  discarded `foreign` bucket. These are the intended "projection is the loosest
  reading; a typed view may drop markup" contract (ADR 0008); a caller who needs
  completeness reads the generic no-schema parse. Downstream vocabularies that
  rely on namespaced pass-through (e.g. sitemap extensions) must model
  `nsExtensible` or accept the local-name-collision caveat — noted for consumers.

## Decision

Add `service/limits.go` (`MaxNestingDepth`, `maxEntityExpansionBytes`,
`maxPEExpansionBytes`, `nestingDepthExceeds`); bound the three entity paths;
correct the DOCTYPE comments. Limits are deliberately generous constants — they
bound only pathological input, and no conforming document approaches them. They
are enforced in both parser modes (a small bomb must not crash a non-validating
parse).

## Consequences

- A document deeper than 10000 elements, or whose internal entities expand past
  10 MiB, is now a `*WFError` instead of a crash/hang. Real documents are
  unaffected (W3C XML conformance and the generate/rss/opc gates stay green).
- DOCTYPE reproduction remains future work; the internal subset is dropped by a
  round-trip, now truthfully documented.
- Consumers still parsing very large *shallow* documents are unaffected (no
  global input-size cap is imposed here — that is a per-consumer policy; e.g.
  proto-sitemap caps its own input at its boundary).
