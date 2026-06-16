# testing/

Corpus tooling for the xmile parser: it fetches real-world and conformance XML
from upstream sources, organizes it by file type and expected verdict, and runs
the parser over it.

Nothing here is part of the shipped parser. The fetcher classifies files with
an oracle that is independent of our parser (the W3C manifests, and for RSS the
Go standard library), so the corpus is a fair test and not a restatement of our
own behavior.

## Layout

| Path | Role |
|---|---|
| `main.go` | fetcher entry point: `go run ./testing` builds the corpus; `go run ./testing rss0.91` fetches just the RSS 0.91 set |
| `fetch.go` | drives the fetch, classifies the W3C suite by manifest, fetches and routes RSS by reference oracle |
| `download.go` | downloads the W3C conformance zip and the OOXML reference repos |
| `rssfetch.go` | HTTP / OPML helpers for reaching the RSS feeds |
| `rss091.go` | fetches the RSS 0.91 set used by the schema compiler |
| `progress/` | a small terminal progress bar |
| `xml-parse/` | parser harness: runs the parser over the corpus and reports (`go run ./testing/xml-parse`) |
| `schema-compile/` | DTD-as-schema harness: compiles `rss.dtd` and projects the RSS 0.91 corpus |
| `corpus/` | the fetched corpus (gitignored; rebuilt on demand) |

## Corpus

`corpus/<format>/<verdict>/`, organized by **file type**, not by source. The
folder name is the expected outcome for every file inside it.

```
corpus/
  xml/   valid/  invalid/  not-wf/     # W3C XML Conformance Test Suite
  docx/  valid/                        # real .docx parts
  xlsx/  valid/                        # real .xlsx parts
  rss/   valid/  not-wf/               # real-world RSS/Atom feeds
  rss0.91/                             # real RSS 0.91 feeds (schema-compile)
```

### Sources

| Format | Source | Notes |
|---|---|---|
| `xml/` | [W3C XML Conformance Test Suite](https://www.w3.org/XML/Test/xmlts20130923.zip) (`xmlts20130923.zip`) | classified by the suite's own manifests (`TYPE` attribute) |
| `docx/` | [python-openxml/python-docx](https://github.com/python-openxml/python-docx) (`tests`, `features`) | real OOXML WordprocessingML parts |
| `xlsx/` | [jmcnamara/XlsxWriter](https://github.com/jmcnamara/XlsxWriter) (`xlsxwriter/test/comparison/xlsx_files`) | real OOXML SpreadsheetML parts |
| `rss/` | [plenaryapp/awesome-rss-feeds](https://github.com/plenaryapp/awesome-rss-feeds) OPML + the live feeds they list | routed valid/not-wf by Go's `encoding/xml` (plus a charset reader and a misplaced-`<?xml?>` check, to match libxml2) |
| `rss0.91/` | the [RSS Advisory Board sample](https://www.rssboard.org/files/sample-rss-091.xml) and Wayback-archived 0.91 feeds | genuine RSS 0.91, for the schema compiler |

The W3C subset is filtered at fetch time to what this parser targets: XML 1.0
5th edition and 1.1, no namespaces, no external entities. See the scope notes
below.

## Scope filters

Some W3C tests are outside what this parser implements and are skipped at fetch
time (in `classifyTest`/`classifyXML`), so nothing is skipped at test time:

- **Namespaces** — out of scope (`NAMESPACE="no"` / `RECOMMENDATION` starting
  `NS`).
- **External entities and the external DTD subset** — out of scope
  (`ENTITIES` other than `none`).
- **4th-edition-only name-character tests** — we target 5th edition
  (`EDITION` without `5`).
- **`invalid` tests with no internal DTD subset** — see below.
- **`invalid` tests whose DTD uses parameter entities** — we do not expand
  parameter entities, so we cannot read the declarations they contribute and
  cannot soundly validate the document. Validating without expanding them would
  risk wrongly rejecting a valid document (a declaration could be hidden in an
  unexpanded PE), so these are out of the validity scope.

### Why we drop `invalid` tests that have no DTD

48 of the suite's `invalid` documents have no document type declaration at all
(most are the OASIS `o-pNNpassM` production tests; a few are edition
character-range tests). We do not validate them and instead skip them. The
reason is that these are not invalid because of anything *in* the document —
they are invalid only under a parser running in mandatory-validating mode.

The suite's own `testcases.dtd` defines the `TYPE` values:

> Each test has a TYPE:
> - All parsers must accept "valid" testcases.
> - **Nonvalidating parsers must also accept "invalid" testcases, but
>   validating ones must reject them.**

So `TYPE='invalid'` means "reject iff you are validating." For these 48 the
*only* reason to reject is the absence of a DTD. XML 1.0 §2.8 defines validity
as having "an associated document type declaration **and** [complying] with the
constraints expressed in it"; with no DTD the first clause already fails. The
reference validating parser shows this directly — same well-formed file, two
modes:

```
xmllint --noout         p44pass1.xml   ->  accepted (well-formed)
xmllint --valid --noout p44pass1.xml   ->  validity error : Validation failed: no DTD found !
```

xmile is **not** a mandatory-validating parser, and cannot be: our own `valid/`
corpus is overwhelmingly DTD-less real-world XML (xlsx, docx, RSS) that we must
accept. We validate a document **against its DTD when it has one**; a document
with no DTD is reported as well-formed, never invalid. Honoring the suite's
verdict for these 48 would mean flipping every DTD-less document to invalid, so
they fall outside our validity scope the same way namespaces and external
entities do. The remaining `invalid` tests — those with an internal subset and
a real constraint to break — are kept and must be rejected as invalid.
