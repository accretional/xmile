# ADR 0006 — Parser modes, a verdict response, and integral namespaces

- **Status:** Accepted
- **Date:** 2026-06-16
- **Decision:** Make `Parse` a mode-aware classifier. A `validate` flag selects
  XML's validating vs non-validating processor; the response is a oneof of the
  Document *or* a typed verdict; namespaces are applied integrally; and the
  service conformance test is self-contained, with the full corpus gated by the
  harness. This supersedes ADR 0003's "outcome rides on the gRPC status, no
  verdict field" and ADR 0005 §5's filtering of the no-DTD invalid tests.

---

## 1. Two processor modes (the `validate` flag)

XML defines two processor kinds. A **non-validating** processor checks
well-formedness; a **validating** one additionally checks the document against
its DTD, and the W3C suite's `testcases.dtd` says a `TYPE='invalid'` document
"[nonvalidating parsers] must accept … but validating ones must reject." We had
been a non-standard hybrid ("validate iff a DTD is present"). `ParseRequest.validate`
replaces it with the two real modes:

- `validate=false` — any well-formed document is accepted, DTD or not.
- `validate=true` — the document must have a DTD and satisfy it; no DTD is
  itself invalid (nothing declares its elements), matching `xmllint --valid`.

`Parser.Parse(src, validating)` carries the mode. A DTD we cannot fully read (an
external subset, or external/undeclared parameter entities) yields
`*CannotValidateError` — we decline rather than guess, so a valid document is
never wrongly rejected.

## 2. A verdict response, not a status code (supersedes ADR 0003)

A parser/validator is a *classifier*: "well-formed? valid? why?" is the result,
not a server fault. gRPC's status codes are also a fixed set that cannot name
`NOT_WELL_FORMED`/`INVALID`. So `ParseResponse` is a oneof — a `Document` on
acceptance, a `ParseError {verdict, reason}` on refusal — and the RPC status is
OK in both cases (a non-OK status is a genuine fault). `Verdict` is
`NOT_WELL_FORMED` / `INVALID` / `CANNOT_VALIDATE`. There is no point returning a
tree for input that is not well-formed or not valid, hence the oneof.

## 3. Internal parameter-entity expansion

A DTD may build its declarations through parameter entities
(`<!ENTITY % p "<!ELEMENT a (b)>"> %p;`). For the validation view we expand the
internal PE references in the DOCTYPE body and reparse, so the content models
and attribute lists they contribute become visible (`service/pe.go`). A
reference to an external or undeclared PE leaves expansion incomplete →
`CannotValidate`. The well-formedness reading is left on the unexpanded DTD.

## 4. Namespaces are integral, not in the grammar (and not a mode)

Per XML §2.3, a processor "MUST accept the colon as a name character," so the
base grammar stays namespace-unaware (and DTDs, which are namespace-unaware,
keep parsing literal QNames). The namespace *constraints* — prefix in scope,
attribute uniqueness after expansion, reserved `xml`/`xmlns` rules — are
context-sensitive and cannot live in a CFG, so, like tag matching and the entity
constraints, they are a tree walk (`service/namespace.go`), applied **integrally**
in both modes. It resolves every element/attribute QName, enforces the
constraints (including no colon in PI targets / entity / notation names, and
NCName-valued ID/IDREF), and writes the resolved `Namespace {uri, local, prefix}`
back onto the tree. A violation is a `*WFError` (not namespace-well-formed). The
edition (`is11`) selects the Namespaces 1.1 rules (prefix undeclaration).

Always-on is safe: the rest of the corpus is namespace-clean, and the real-world
OOXML/RSS parts declare their prefixes. The suite's `NAMESPACE="no"` tests —
written for a non-namespace processor — stay out of scope.

A prerequisite fell out of this: **attribute-value normalization by declared
type** (`normalizeAttrTypes`), required for the infoset and for two namespace
declarations of different declared types to compare equal.

## 5. Corpus consequence (supersedes ADR 0005 §5)

With validating mode, a no-DTD "invalid" document is correctly rejected (no DTD),
and with PE expansion the parameter-entity tests validate — so both are
un-filtered and kept. The namespace-recommendation tests are added to the corpus.
Only an external subset remains out of scope for an invalid test (it would come
back `CANNOT_VALIDATE`). Result: 289 valid, 114 invalid, 838 not-wf, all correct.

## 6. The conformance test is self-contained

`service/conformance_test.go` carries its own XML samples, so `go test ./service`
needs nothing fetched. The full W3C corpus is the harness's job
(`testing/xml-parse`, run by `test.sh`), which gates the deterministic xml corpus
at 100% and reports the real-world corpora.
