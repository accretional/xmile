# ADR 0002 — Schema-language scope: DTD in, XSD/OOXML deferred

- **Status:** Accepted
- **Date:** 2026-06-15
- **Decision:** Scope the validation pipeline to **DTD**-based well-formedness and
  validity over the generic `Tag` tree (`proto/xml.proto`). Per-vocabulary typed
  code generation and **XSD / OOXML** (docx, xlsx) binding are **deferred** to a
  separate, later effort.

---

## 1. Context

While exploring "generate a typed proto per vocabulary (`docx.proto`,
`xlsx.proto`) from the schema," we established that the DTD path does not reach
those formats and that a DTD is a limited schema source. The findings are
recorded here so the scope is explicit and we don't relitigate it.

### 1.1 docx / xlsx / OOXML are XSD, not DTD

- OOXML (the XML inside `.docx` / `.xlsx`, ECMA-376) is validated against
  **W3C XML Schema (XSD)**, not a DTD.
- It is **namespace-qualified** (`w:p`, `a:t`, `xl:c`, …). DTDs have **no
  namespace awareness** — they treat `w:p` as an opaque name. This is a hard
  wall, not a detail.
- A `.docx` / `.xlsx` is an **OPC package** (a ZIP of many XML parts —
  `document.xml`, `styles.xml`, …), not a single document with an internal
  subset.
- Net: there is **no DTD to read** for these formats. The DTD-tables pipeline
  (Step 0) has nothing to consume.

### 1.2 DTD is a lossy schema / codegen source even where present

- Attribute kinds are limited to `CDATA`, `ID`, `IDREF(S)`, `ENTITY(IES)`,
  `NMTOKEN(S)`, enumerations, and `NOTATION`. There are **no numeric / date /
  boolean datatypes**.
- A DTD-derived proto would be **structurally typed but not data-typed**: nearly
  every field is `string` (`string width`, never `int32 width`). Only
  **enumerations** carry real typing (and could map to a proto `enum`). XSD is
  what carries `int` / `dateTime` / etc.

### 1.3 Entities are reader-level substitution, not schema members

- General entities are **macro substitution** performed at read time
  (`&pub;` → text; `&tm;` → markup that becomes real nodes). They are
  **expanded before a typed tree exists**, so they have no place as fields in a
  typed schema. The entity table is a *reader input*, not a model member.

### 1.4 The `char_attrs` / `entity_attrs` split is the wrong axis

- An attribute is `name → typed value`; its value may *contain* an entity
  reference, but after expansion it is just a value. The two-map split in the
  `proto/xml.proto` sketch does not survive into a real model. Prefer **one
  typed entry per declared attribute**.

---

## 2. Consequences

- **In scope now:** DTD → tables (Step 0) → content-model validation +
  infoset augmentation over the generic `Tag` tree, producing
  *well-formed* / *valid* / *invalid* verdicts.
- **Deferred:** per-vocabulary typed codegen (DTD → grammar →
  gluon `compiler.Compile`) is possible but out of scope; it yields only a
  structural, mostly-`string` schema.
- **Future, separate effort:** typed **OOXML** protos, if ever pursued, require
  an **XSD → grammar/AST generator plus namespace + OPC handling**, sharing only
  the final `compiler.Compile` step with the DTD path. Tracked as future work,
  not part of this milestone.
