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
| `main.go` | fetcher entry point: `go run ./testing` builds the corpus; `go run ./testing rss0.91` / `rss2.0` fetch just that vocabulary's set |
| `fetch.go` | drives the fetch, classifies the W3C suite by manifest, fetches and routes RSS by reference oracle |
| `download.go` | downloads the W3C conformance zip and the OOXML reference repos |
| `rssfetch.go` | HTTP / OPML helpers for reaching the RSS feeds |
| `rss091.go` | fetches the RSS 0.91 set used by the schema compiler |
| `rss2.go` | fetches the large real-world RSS 2.0 set (OPML + TSV catalogs) for the rss-parse harness |
| `progress/` | a small terminal progress bar |
| `xml-parse/` | full-corpus gate: runs the parser over the corpus (`go run ./testing/xml-parse`); the `xml/` corpus must be 100% (non-zero exit otherwise), real-world corpora are reported |
| `schema-compile/` | DTD-as-schema harness: compiles `rss.dtd` and projects the RSS 0.91 corpus |
| `rss-parse/` | RSS 2.0 harness: runs `service.ParseRSS` over the `rss2.0` corpus, reports the valid-set pass rate and the invalid-set reject rate; `-classify` splits feeds into valid/invalid (not gating; ADR 0007) |
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
  rss2.0/   invalid/                   # real RSS 2.0 feeds; invalid/ = genuine spec violations (rss-parse)
```

### Sources

| Format | Source | Notes |
|---|---|---|
| `xml/` | [W3C XML Conformance Test Suite](https://www.w3.org/XML/Test/xmlts20130923.zip) (`xmlts20130923.zip`) | classified by the suite's own manifests (`TYPE` attribute) |
| `docx/` | [python-openxml/python-docx](https://github.com/python-openxml/python-docx) (`tests`, `features`) | real OOXML WordprocessingML parts |
| `xlsx/` | [jmcnamara/XlsxWriter](https://github.com/jmcnamara/XlsxWriter) (`xlsxwriter/test/comparison/xlsx_files`) | real OOXML SpreadsheetML parts |
| `rss/` | [plenaryapp/awesome-rss-feeds](https://github.com/plenaryapp/awesome-rss-feeds) OPML + the live feeds they list | routed valid/not-wf by Go's `encoding/xml` (plus a charset reader and a misplaced-`<?xml?>` check, to match libxml2) |
| `rss0.91/` | the [RSS Advisory Board sample](https://www.rssboard.org/files/sample-rss-091.xml) and Wayback-archived 0.91 feeds | genuine RSS 0.91, for the schema compiler |
| `rss2.0/` | [plenaryapp/awesome-rss-feeds](https://github.com/plenaryapp/awesome-rss-feeds) + [kilimchoi/engineering-blogs](https://github.com/kilimchoi/engineering-blogs) OPML and the [tfederman/fountain-of-rss](https://github.com/tfederman/fountain-of-rss) TSV catalog | thousands of live feeds, kept only when well-formed and `version="2.0"`; for the rss-parse harness |

The W3C subset is filtered at fetch time to what this parser targets: XML 1.0
5th edition and 1.1, Namespaces in XML, no external entities. See the scope
notes below.

## Scope filters

Some W3C tests are outside what this parser implements and are skipped at fetch
time (in `classifyTest`/`classifyXML`), so nothing is skipped at test time:

- **External entities and the external DTD subset** — out of scope
  (`ENTITIES` other than `none`).
- **4th-edition-only name-character tests** — we target 5th edition
  (`EDITION` without `5`).
- **`NAMESPACE="no"` tests** — written for a *non-namespace* processor. We apply
  namespaces integrally, so these are out of scope. (The namespace-recommendation
  tests themselves are kept — see below.)
- **`invalid` tests with an external subset** — we do not load external subsets,
  so such a test would come back `CANNOT_VALIDATE` rather than `INVALID`.

What we used to drop but now keep: the no-DTD `invalid` tests, and the
parameter-entity tests.

### Validity is now mode-aware (so the no-DTD tests are kept)

48 of the suite's `invalid` documents have no DTD at all (mostly OASIS
`o-pNNpassM` production tests). The suite's own `testcases.dtd` says:

> Each test has a TYPE:
> - All parsers must accept "valid" testcases.
> - **Nonvalidating parsers must also accept "invalid" testcases, but
>   validating ones must reject them.**

So `TYPE='invalid'` means "reject iff you are validating", and the reference
parser shows it on a no-DTD document directly:

```
xmllint --noout         p44pass1.xml   ->  accepted (well-formed)
xmllint --valid --noout p44pass1.xml   ->  validity error : Validation failed: no DTD found !
```

Our parser now has both modes (`ParseRequest.validate`; ADR 0006). The harness
checks the `xml/` corpus in **validating** mode, where a no-DTD document is
correctly `INVALID` (nothing declares its elements) and an internal-subset
document is validated for real — so these tests are kept, not filtered. The
real-world corpora (DTD-less xlsx/docx/RSS) are checked **non-validating**, where
they are simply well-formed. Parameter-entity tests are kept too: internal
parameter entities are expanded before validating.
