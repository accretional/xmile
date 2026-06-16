# ADR 0005 — DTD validity: checking a document against its internal subset

- **Status:** Accepted
- **Date:** 2026-06-16
- **Decision:** Add a validity pass (`service/validate.go`) that checks a parsed,
  well-formed document against the content models and attribute declarations of
  its **internal DTD subset**, and reports a well-formed-but-invalid document as
  a distinct error (`*ValidityError`, gRPC `FAILED_PRECONDITION`). It runs only
  when the whole DTD is readable: an internal subset with no external
  declarations and no parameter entities.

---

## 1. Instance-level, after well-formedness

`checkWellFormed` answers "is this syntactically a document?"; validity answers
"does it obey its DTD?" — the W3C Validity Constraints (VCs). The two are kept
separate, as XML keeps them separate: a not-well-formed document is a fatal
error (`*WFError`, `INVALID_ARGUMENT`); a well-formed-but-invalid one is a
different category (`*ValidityError`, `FAILED_PRECONDITION`). `Parse` returns the
first violation it finds and no tree, mirroring the WF path.

No grammar lives in the validator. It walks the DTD CST (the `lang/dtd.ebnf` node
kinds) into a model — element content specs, attribute lists, notations — reusing
the same helpers and the content-model parser the schema compiler (ADR 0004)
uses, then walks the projected `xml.proto` tree against that model.

## 2. What is checked

- **Element Valid.** Every element must be declared; its content must match the
  declared model. `EMPTY` (no content), `ANY` (any declared children), `Mixed`
  (`#PCDATA` plus a fixed set of children in any order), and element content
  (a regular expression over child names) are all enforced. Element content is
  matched with a small Thompson-style NFA over the particle tree, so arbitrary
  nesting and `?`/`*`/`+` occurrences work, and character data other than XML
  white space between child elements is rejected.
- **Attributes.** Every attribute must be declared; values must satisfy their
  type (`ID`/`IDREF(S)`, `ENTITY`/`ENTITIES`, `NMTOKEN(S)`, enumerations,
  `NOTATION`); `#REQUIRED` must be present; `#FIXED` must match. ID values must
  be unique and every `IDREF` must resolve. Tokenized values split on XML white
  space (so a character-referenced separator like `&#xD;` still separates, while
  a non-S character like a referenced NEL stays in its token and fails).
- **Root Element Type**, and the DTD-level constraints: Unique Element Type
  Declaration, One ID per Element Type, ID Attribute Default, No Duplicate Types
  in mixed content, Notation Attributes (declared), Attribute Default Legal, and
  Notation Declared.

## 3. General entities are expanded into the tree

Validity is defined over the content *after* entity expansion: `<doc>&e;</doc>`
with `<!ENTITY e "<foo/>">` and `<!ELEMENT doc (foo)>` is valid because `&e;`
contributes a `foo` element. The projector therefore expands a general-entity
reference in element content into the content its replacement parses to (reusing
the same expand-and-reparse the entity WF check already performs), rather than
leaving it as literal `&e;` text. This makes the projected AST match the XML
infoset and lets the content-model check see the real structure.

## 4. Scope: only what we can fully read

The pass runs **only** for a document whose entire effective DTD is the internal
subset we parsed — no external subset, and no parameter entities. We do not
expand parameter entities (consistent with ADR 0004), so a declaration could be
hidden inside an unexpanded PE; validating then could report a declared element
as undeclared and **wrongly reject a valid document**. Wrongly rejecting valid
input is worse than missing an invalid one, so when the DTD is not fully
readable we report the document as well-formed and never invalid.

This also means a document with **no DTD** is never "invalid": there is nothing
to validate against. That is a deliberate contract — we validate a document
*against its DTD when it has one* — and it is what lets the real-world,
DTD-less corpora (xlsx, docx, RSS) be accepted as valid.

## 5. Corpus consequence: dropping the no-DTD "invalid" tests

48 of the W3C suite's `invalid` documents have no DTD at all (mostly OASIS
`o-pNNpassM` production tests). They are marked `invalid` because the suite is
written for a *mandatory-validating* processor; the suite's own `testcases.dtd`
says:

> Nonvalidating parsers must also accept "invalid" testcases, but validating
> ones must reject them.

For these the only reason to reject is the absence of a DTD — XML §2.8 defines
"valid" as having an associated DTD *and* complying with it, and the reference
validating parser shows it directly:

    xmllint --noout         p44pass1.xml   ->  accepted (well-formed)
    xmllint --valid --noout p44pass1.xml   ->  validity error: no DTD found !

We are not a mandatory-validating processor and cannot be (our `valid/` corpus
is overwhelmingly DTD-less real-world XML). Honoring that verdict would flip
every DTD-less document to invalid, so these — and the few `invalid` tests whose
declarations live in parameter entities — are filtered at fetch time, the same
way namespaces and external entities are. The rationale and the
quotation live next to the fetcher in `testing/README.md`.

## 6. Result

The conformance gate (`TestCorpusWellFormed`) now requires every `valid`
document to parse, every `invalid` document to be rejected as `*ValidityError`,
and every `not-wf` document to be rejected: 277 valid, 46 invalid, 814 not-wf,
all classified correctly.
