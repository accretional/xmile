# xmile
XML EBNF grammar, metaparsing/formalization of the format, parsing, and transformations.

## Quick run

```bash
bash LET_IT_RIP.sh  # Setup + build + test. 
                    # Will take some time to test against the full corpus.
```

```bash
bash serve.sh # Setup + build + serve
```

## Scripts

All work goes through these. They are idempotent and chained (each runs the one before it), so a clean clone needs nothing else installed.

- `setup.sh`: clones the dependency repos (gluon, proto-merge) as siblings, pulls them to the latest if already present, and installs the toolchain (protoc and the Go plugins).
- `build.sh`: runs setup and builds. The generated protos and lexical tables are committed; regenerate them with `go run ./lang/cmd/genproto` plus protoc only when the grammar or protos change.
- `test.sh`: runs build, fetches the test corpus on the first run, runs `go test` (the conformance gate), and prints the corpus report.
- `serve.sh`: runs build and serves the `XmlService.Parse` gRPC server.
- `LET_IT_RIP.sh`: runs setup, build, test, and a live parse demo.

## How it works

The structural grammar lives in `lang/*.ebnf` and the lexical layer in `lang/*.lex`. genproto compiles both into `proto/`. The runtime parser in `service/` carries no grammar of its own. It drives gluon with the generated lexical table, projects the resulting tree into the hand-written AST (`proto/xml.proto`), runs the well-formedness walk, then parses the inline DTD and enforces the entity and DTD well-formedness constraints. Design notes are in `docs/decisions`.

## XML Parsing
### Done

- **Well-formedness parsing for XML 1.0 (5th edition) and 1.1**: version dispatch, restricted characters, Latin-1 and line-end handling, references, CDATA, comments, PIs, the inline DTD (internal subset), and the entity well-formedness constraints.
- **AST and service**: parses to the homogeneous `proto/xml.proto` tree with declared text entities resolved, exposed over gRPC.
- **Conformance covers 100% of the applicable W3C subset**. Every valid and invalid document parses and every not-wf document is rejected (277 valid, 97 invalid, 814 not-wf). OOXML parts also parse (986 xlsx, 45 docx).

### To do

- **DTD validity, the main remaining piece**. We parse the DTD and enforce well-formedness, but we do not yet validate a document against it. Element content models and attribute declarations are not checked, so valid and invalid documents are not distinguished (both are treated as well-formed). The next milestone is content-model checking and the Valid/Invalid verdict.
- **External entities and the external DTD subset**, currently skipped (the conformance subset excludes ENTITIES other than none).
- Namespaces, currently out of scope.
- A couple of real-world RSS feeds where our parser and Go's encoding/xml disagree (these do not gate the build).

## Schema compiling

A DTD describes one XML vocabulary. `SchemaService.Compile` turns a DTD into a proto descriptor (`FileDescriptorProto`), one message per element declaration, so a document in that vocabulary gets a typed AST instead of the homogeneous `Tag`. The runtime parser stays homogeneous; this is a separate offline step.

- Service: `SchemaService.Compile` in `proto/xml_service.proto`, served by `cmd/xmlserve` next to `XmlService` (started by `serve.sh`).
- Library: `service.CompileDTD(dtd, opts)` returns the descriptor. Link it with `protodesc` for dynamic use, or write it out as a `FileDescriptorSet`.
- Mapping: `(#PCDATA)` becomes a string field, `(a)` a message field, `(a | b)*` repeated fields, and `<!ATTLIST>` attributes string fields. A repeated choice becomes repeated fields, not a oneof.
- Test: `testing/schema-compile` compiles the RSS 0.91 DTD and parses a real RSS 0.91 corpus (`testing/corpus/rss0.91/`, fetched by `go run ./testing rss0.91`) through the generated proto. `test.sh` runs it.

See `docs/decisions/0004-dtd-as-schema.md`.
