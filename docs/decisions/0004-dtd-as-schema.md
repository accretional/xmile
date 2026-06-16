# ADR 0004 — DTD-as-schema: compiling a DTD into a proto descriptor

- **Status:** Accepted
- **Date:** 2026-06-15
- **Decision:** Add a **type-level** compiler, `CompileDTD`, that turns a DTD
  into a `google.protobuf.FileDescriptorProto` — one message per `<!ELEMENT>` —
  so a generic feed gains a unique, typed AST (an `rss.Rss`) instead of the
  homogeneous `Tag`. It is a separate concern from `XmlService.Parse`, exposed
  as the gRPC `SchemaService` (`proto/xml_service.proto`) and served alongside
  `XmlService` by `cmd/xmlserve` (so `serve.sh` starts both).

---

## 1. Two axes: instance-level vs type-level

`XmlService.Parse` is **instance-level**: a document's bytes → that document's
AST, once per document at runtime. `SchemaService.Compile` / `CompileDTD` is
**type-level**: a schema's bytes → a descriptor of a *family* of documents, once
per spec at build time. Different input, output, and lifecycle — and a different
dependency surface (`CompileDTD` returns a `descriptorpb.FileDescriptorProto`).
So it is a distinct service in the same package, not bolted onto `Parse`.

This is ADR 0003's homogeneous-vs-heterogeneous theme one rung lower. There,
`xml.ebnf` (homogeneous) is hand-written and `dtd.ebnf` (heterogeneous) is
generated. Here, **a particular DTD** is itself a heterogeneous grammar of one
vocabulary, so "one message per element declaration" is the same move applied to
the instance level. The lowering rides the *same* gluon engine
(`compiler.Compile`) that generates `dtd.proto`.

## 2. Pipeline

    DTD bytes
      → parseExternalSubset   (wrap as a synthetic DOCTYPE body, reuse dtdParser)
      → walk the DTD CST       (elementDecl / attlistDecl / contentspec nodes)
      → schema-AST             (gluon file → rule* → sequence/nonterminal/scalar)
      → compiler.Compile       → FileDescriptorProto

It carries no grammar knowledge: it parses the DTD with the same grammar-driven
`dtdParser` the runtime uses and walks the resulting CST (content models via the
`children`/`mixed`/`occ` nodes), so structure stays in `lang/dtd.ebnf`. No
`.proto` file and no `protoc` are required to *use* the result: the descriptor is
produced in-memory and can be linked with `protodesc` for dynamic use, or saved
as a `FileDescriptorSet` to feed a static build later.

## 3. External subset, not DOCTYPE body

A standalone `.dtd` is the XML **external subset** — a bare `markupdecl*` with no
`<!DOCTYPE name [ … ]>` wrapper. `dtd.ebnf`'s start rule expects the DOCTYPE
body, so `parseExternalSubset` wraps the bytes in a synthetic
`_xmile_schema_root [ … ]` and reuses the two-pass DTD parser unchanged. The
synthetic root name is discarded.

## 4. Content-model → proto mapping

| DTD | proto |
|---|---|
| `<!ELEMENT e (#PCDATA)>` | `message E { string text = 1; }` |
| `<!ELEMENT e (a)>` | `message E { A a = 1; }` |
| `<!ELEMENT e (a \| b \| c)*>` | `message E { repeated A a; repeated B b; repeated C c; }` |
| `<!ELEMENT e (a+)>` | `message E { repeated A a; }` |
| `<!ELEMENT e (a, b?, c*)>` | `message E { A a; B b; repeated C c; }` |
| `<!ATTLIST e k CDATA …>` | adds `string k` to `message E` |

**A repeated choice becomes a bag of repeated fields, not a repeated oneof.**
`repeated oneof` is not legal proto3, and for an order-insensitive data
vocabulary like RSS the bag form is what consumers want. This is the load-bearing
design choice; it also makes a generic descriptor-driven projector trivial
(element name → field of the same message type).

## 5. Scope boundaries (v1)

- **Weak scalars.** DTDs type everything textual as `#PCDATA`/`CDATA`, so leaves
  are `string`. Structure is captured; scalar richness is not (that needs XSD).
- **`+` collapses to `repeated`** — proto3 cannot express "at least one"; the
  min-1 constraint stays a validation concern.
- **Parameter entities / conditional sections** are not expanded. The RSS 0.91
  DTD needs neither (its only `%…;` is inside a comment). Vocabularies that build
  content models through PEs (XHTML, DocBook) require an expansion pass first.
- **Mixed content** `(#PCDATA | a | b)*` lowers to a `text` string plus a
  repeated field per child; ordering between text and children is not retained.
- **Names.** RSS 0.91 element names are plain ASCII, so PascalCase/snake_case
  mangling round-trips. Namespaced vocabularies (`dc:date`) need reversible
  mangling before this generalizes.

## 6. Service and validation

- **Service.** `SchemaService.Compile(CompileRequest{dtd, …}) →
  CompileResponse{FileDescriptorProto}` lives in `proto/xml_service.proto`; the
  handler (`service.SchemaServer`) wraps `CompileDTD`, and `cmd/xmlserve`
  registers it next to `XmlService`, so `serve.sh` brings both up.
- **Validation.** `testing/schema-compile` (run by `test.sh`) compiles its
  `rss.dtd`, links the descriptor with `protodesc` (catching malformed output —
  e.g. a dangling type ref or a repeated field in a oneof), and parses the
  **real-world RSS 0.91 corpus** through it: each feed is parsed by the xmile
  parser and `ProjectTag`-projected into a dynamic message built from the
  generated schema. The corpus (`testing/corpus/rss0.91/`, fetched on demand by
  `go run ./testing rss0.91`) is genuine 0.91 — the canonical RSS Advisory Board
  sample plus Wayback-archived feeds (LinuxToday, XML.com, Dictionary.com,
  NewsForge). Because these are true 0.91 documents, every one must project with
  **full coverage** (no element/attribute outside the schema); the harness fails
  on any out-of-vocabulary markup or empty projection.
