# xmile

A grammar-driven XML 1.0 parser and schema-driven projector. XML is parsed
against an EBNF grammar into a homogeneous AST; given a schema — a **DTD**, an
**EBNF element vocabulary**, or an **XSD** — the same tree is projected into that
vocabulary's *typed* AST (generic XML is just the loosest schema). OPC packages
(`.docx`/`.xlsx`) ride the same parser. Exposed as two gRPC services. Design
record: `docs/decisions/` (ADR 0008).

## Quick run

```bash
bash LET_IT_RIP.sh  # setup + build + test (full corpus; takes a while) + a live demo
bash serve.sh       # setup + build + serve the gRPC services on :50051
```

## How it works

The structural grammar (`lang/*.ebnf`) and lexer (`lang/*.lex`) compile to
`proto/` via gluon; the runtime parser carries no grammar of its own. `Process`
parses XML into the homogeneous AST and, given a schema (compiled to a proto
descriptor by a per-language front-end), projects it through one generic walk
into that vocabulary's typed message. OPC packages are unpacked over the same
parser, resolving content types and the relationship graph.

## Using the services

`bash serve.sh` serves both on `:50051` with reflection enabled, so
[`grpcurl`](https://github.com/fullstorydev/grpcurl) can call them directly
(`source` is a `bytes` field, so it is base64-encoded — the examples pipe through
`base64`).

### `Documents.Process` — parse, and optionally project against a schema

No schema → the generic XML AST (the verdict is `WELL_FORMED`):

```bash
grpcurl -plaintext \
  -d "{\"source\": \"$(printf '<a x="1">hi</a>' | base64)\"}" \
  :50051 xml.Documents/Process
```
```json
{
  "document": {
    "root": { "name": "a", "attrs": [{"name": "x", "value": "1"}], "contents": [{"text": "hi"}] }
  },
  "verdict": "WELL_FORMED"
}
```

A `format` (or an inline `compile`) projects into that vocabulary's typed tree;
the reply is self-describing (`schema` is the descriptor, `message` the
base64-serialized typed message):

```bash
grpcurl -plaintext \
  -d "{\"format\": \"rss-2.0\", \"mode\": \"VALIDATE\", \"source\": \"$(printf '%s' \
     '<rss version="2.0"><channel><title>T</title><link>L</link><description>D</description></channel></rss>' \
     | base64)\"}" \
  :50051 xml.Documents/Process
```
```json
{
  "typed": { "schema": { "name": "rss.proto", "package": "rss", "...": "..." }, "rootMessage": "Rss", "message": "Cg..." },
  "verdict": "VALID"
}
```

A rejected document returns `error` with a `verdict` (`NOT_WELL_FORMED` /
`INVALID` / `CANNOT_VALIDATE`); the RPC status stays `OK`.

### `Schemas.Compile` — a metagrammar → a proto descriptor

`language` is `DTD`, `EBNF_VOCAB`, or `XSD`; the reply is a `FileDescriptorProto`
of the document family (one message per element):

```bash
grpcurl -plaintext \
  -d "{\"language\": \"DTD\", \"package\": \"note\", \"source\": \"$(printf '%s' \
     '<!ELEMENT note (#PCDATA)>' | base64)\"}" \
  :50051 xml.Schemas/Compile
```
```json
{
  "file": {
    "name": "note.proto",
    "package": "note",
    "messageType": [
      { "name": "Note", "field": [{"name": "text", "number": 1, "label": "LABEL_OPTIONAL", "type": "TYPE_STRING"}] }
    ],
    "syntax": "proto3"
  }
}
```
