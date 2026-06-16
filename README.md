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

The structural grammar lives in `lang/*.ebnf` and the lexical layer in `lang/*.lex`. genproto compiles both into `proto/`. The runtime parser in `service/` carries no grammar of its own. It drives gluon with the generated lexical table, runs the well-formedness walk, parses the inline DTD and enforces the entity well-formedness constraints, projects the tree into the hand-written AST (`proto/xml.proto`) with general entities expanded, and finally validates the document against its DTD. Design notes are in `docs/decisions`.

## XML Parsing
### Done

- **Well-formedness parsing for XML 1.0 (5th edition) and 1.1**: version dispatch, restricted characters, Latin-1 and line-end handling, references, CDATA, comments, PIs, the inline DTD (internal subset), and the entity well-formedness constraints.
- **DTD validity** against an internal subset: element content models (EMPTY / ANY / mixed / children, with full occurrence matching), attribute types and defaults (ID/IDREF(S), ENTITY/ENTITIES, NMTOKEN(S), enumerations, NOTATION, #REQUIRED/#FIXED), ID uniqueness and IDREF resolution, and the DTD-level constraints. A well-formed-but-invalid document is reported over gRPC as `FAILED_PRECONDITION`. See `docs/decisions/0005-dtd-validity.md`.
- **AST and service**: parses to the homogeneous `proto/xml.proto` tree with general entities expanded, exposed over gRPC.
- **Conformance covers 100% of the applicable W3C subset**. Every valid document parses, every invalid document is rejected as DTD-invalid, and every not-wf document is rejected (277 valid, 46 invalid, 814 not-wf). OOXML parts also parse (986 xlsx, 45 docx).

### To do

- **External entities, the external DTD subset, and parameter entities**, currently out of scope. Validity runs only against an internal subset with no parameter entities; a document that relies on external or parameter-entity declarations is reported as well-formed, never invalid.
- Namespaces, currently out of scope.

## Schema compiling

A DTD describes one XML vocabulary. `SchemaService.Compile` turns a DTD into a proto descriptor (`FileDescriptorProto`), one message per element declaration, so a document in that vocabulary gets a typed AST instead of the homogeneous `Tag`. The runtime parser stays homogeneous; this is a separate offline step.

- Service: `SchemaService.Compile` in `proto/xml_service.proto`, served by `cmd/xmlserve` next to `XmlService` (started by `serve.sh`).
- Library: `service.CompileDTD(dtd, opts)` returns the descriptor. Link it with `protodesc` for dynamic use, or write it out as a `FileDescriptorSet`.
- Mapping: `(#PCDATA)` becomes a string field, `(a)` a message field, `(a | b)` a oneof, `(a | b)*` a repeated message holding that oneof (which keeps child order), and `<!ATTLIST>` attributes string fields. This mirrors `proto/xml.proto`'s `ContentItem`.
- Test: `testing/schema-compile` compiles the RSS 0.91 DTD and parses a real RSS 0.91 corpus (`testing/corpus/rss0.91/`, fetched by `go run ./testing rss0.91`) through the generated proto. `test.sh` runs it.

See `docs/decisions/0004-dtd-as-schema.md`.
