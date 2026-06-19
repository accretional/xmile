# xmile architecture

One XML engine; formats are data. Two gRPC services:
**`Schemas.Compile`** turns a format spec into a proto descriptor (type-level);
**`Documents.Process`** parses a document and projects it into a vocabulary's
typed tree (instance-level), and **`Documents.Generate`** is its inverse —
serialize the generic XML AST back to a document. Generic XML is just the
loosest schema.

```
                         LEVELS
  L0  XML itself ............ lang/xml.ebnf + the parser (one engine)
  L1  schema languages ...... DTD · EBNF-vocab · XSD  (service/language front-ends)
  L2  formats (DATA) ........ formats/*.ebnf|*.xsd   (rss-2.0, …; compiled on demand)
```

## The two services and how they connect

```
            ┌──────────────────────── Schemas.Compile (type-level) ───────────────┐
 format     │  formats/rss-2.0.ebnf ─┐                                             │
 spec ──────┤  a .dtd / .xsd        ─┼─▶ language front-end ─▶ gluon compiler ─────┼─▶ FileDescriptorProto
            │      (DTD|EBNF|XSD)     │   (CompileSource)        (compiler.Compile)  │   (one message/element)
            └────────────────────────┴─────────────────────────────────────────────┘
                                                         │  "compile once"
                                                         ▼  (cached per format)
            ┌──────────────────────── Documents.Process (instance-level) ──────────┐
 bytes ─────┤  parse (lang/xml.ebnf)  ─▶  Xml AST  ─┬─ no schema ─▶ document:{xml:…} │
 + schema?  │  (well-formed + ns)     (homogeneous) │                                │──▶ ProcessResponse
            │                                        └─ a schema ─▶ project walk ────┤    { document | error,
            │                                            (engine.project, by descr.) │      verdict, diagnostics }
            └────────────────────────────────────────────────── document:{rss:…} ───┘
```

- **One parser.** Every document is parsed once by the universal XML parser into
  the homogeneous `Xml`/`Tag` AST. A schema never changes parsing — it only
  decides the projection target.
- **Projection = one generic walk** (`service/engine.go`): place each element/
  attribute into the descriptor's matching field. The descriptor (the *shape*)
  comes from `Schemas.Compile`; the walk fills it.
- **Validity**: generic XML validates against its own DTD (`validate.go`, only in
  `VALIDATE` mode); a format validates by projection coverage + the few rules a
  grammar can't state (e.g. RSS's `version`/`<channel>`, the namespace rule).

## Instance pipeline (inside Process)

```
bytes ─▶ encoding/line-ends ─▶ gluon CST ─▶ well-formedness ─▶ DTD pass ─▶ project to Xml AST
       ─▶ attr normalization ─▶ namespaces ─▶ [validate vs DTD] ─▶ [project to schema] ─▶ result
```

## Examples (request ▶ response)

**Generic XML** — no schema, keyed `xml`:
```
Process{ source: <a x="1">hi</a> }
 ▶ { document: { xml: { root: { name:"a", attrs:[{name:"x",value:"1"}], contents:[{text:"hi"}] } } },
     verdict: WELL_FORMED }
```

**RSS 2.0** — `formats/rss-2.0.ebnf` compiled on demand, keyed by root `rss`:
```
Process{ format:"rss-2.0", mode:VALIDATE,
         source: <rss version="2.0"><channel><title>T</title>…</channel></rss> }
 ▶ { document: { rss: { version:"2.0", channel:{ alt1:[ {title:{text:"T"}}, … ] } } },
     verdict: VALID }
```

**Compile a new format, then process it** (the same path for any DTD/EBNF/XSD):
```
Schemas.Compile{ language: DTD, source: "<!ELEMENT note (#PCDATA)>" }
 ▶ { file: <FileDescriptorProto: message Note { string text = 1; }> }

Process{ compile:{ language: DTD, source:"…note dtd…" }, source: "<note>hi</note>" }
 ▶ { document: { note: { text:"hi" } }, verdict: VALID }
```

**Refused** — verdict, never a non-OK RPC status:
```
Process{ format:"rss-2.0", source:<rss version="2.0"><bogus/></rss> }
 ▶ { error:{ verdict: INVALID, reason:"out-of-vocabulary markup …" }, verdict: INVALID }
```

**Generate** — the inverse of Process: serialize the generic `Xml` AST back to a
document (no schema, no reflection — a walk over the Tag tree plus escaping; the
DOCTYPE is re-emitted from its concrete-syntax tree). Faithful at the infoset
level, so `parse(Generate(parse(b))) == parse(b)`. Only the *generic* AST
round-trips — a format's typed projection is a read-only view, so Generate takes
the `Xml` tree, not a typed message:
```
Generate{ document: { root:{ name:"a", attrs:[{name:"x",value:"1"}], contents:[{text:"hi"}] } } }
 ▶ { source: '<a x="1">hi</a>' }
```

## OPC packages (docx/xlsx)

A `.docx`/`.xlsx` is an OPC ZIP of XML parts. `ProcessPackage` unpacks it, parses
each part through the same engine, and resolves `[Content_Types].xml` + the
`_rels` graph — the package layer is format-agnostic; only the part vocabularies
differ.

```
.docx ─▶ unzip ─▶ [Content_Types] + _rels ─▶ per part: parse (+ project vs its schema) ─▶ typed package tree
```

## Where things live

| Path | Role |
|---|---|
| `lang/xml.ebnf`, `lang/dtd.ebnf`, `lang/*.lex` | L0 grammar + lexing (compiled by `genproto`) |
| `service/` (parser, project, validate, namespaces, engine) | the one XML engine + generic projection |
| `service/language/` | L1 front-ends: `CompileDTD`/`CompileGrammar`/`CompileXSD`/`CompileSource` |
| `formats/` | L2 format specs as data (`rss-2.0.ebnf`, …), compiled on demand |
| `service/{grpc,schema_grpc}.go`, `cmd/xmlserve` | the `Documents` + `Schemas` gRPC services |
| `testing/` | one fetcher + one corpus runner (`go run ./testing`) |
| `docs/decisions/0008-*` | the design record |
```
