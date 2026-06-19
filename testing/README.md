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
| `main.go` | the corpus runner: `go run ./testing` ensures the corpus then runs every check (gates `xml` + `opc`; reports `rss-2.0` + `xsd`); `go run ./testing fetch` rebuilds only the corpus |
| `fetch.go` | the single fetcher: downloads and classifies the W3C XML suite by manifest, sparse-checks-out the OOXML repos, fetches and routes RSS by the reference oracle, clones the W3C XSD suite |
| `progress/` | a small terminal progress bar |
| `corpus/` | the fetched corpus (gitignored; rebuilt on demand) |

## Corpus

`corpus/<format>/<verdict>/`, organized by **file type**, not by source. The
folder name is the expected outcome for every file inside it.

```
corpus/
  xml/      valid/  invalid/  not-wf/  # W3C XML Conformance Test Suite (gates, 100%)
  rss/      valid/  not-wf/            # awesome-rss-feeds OPML + the live feeds they list
  rss2.0/     invalid/                 # real RSS 2.0 feeds; invalid/ = genuine spec violations (reported)
  docx/     valid/                     # real .docx packages from several producers (gates)
  xlsx/     valid/                     # real .xlsx packages (gates)
  docx-web/                            # ~2000 web-scraped .docx (superdoc-dev/docx-corpus; reported)
  xsd/                                 # W3C XML Schema test suite (.xsd schemas; reported)
```

### Sources

| Format | Source | Notes |
|---|---|---|
| `xml/` | [W3C XML Conformance Test Suite](https://www.w3.org/XML/Test/xmlts20130923.zip) (`xmlts20130923.zip`) | classified by the suite's own manifests (`TYPE` attribute) |
| `docx/` | [python-openxml/python-docx](https://github.com/python-openxml/python-docx) (`tests`, `features`), [mwilliamson/mammoth.js](https://github.com/mwilliamson/mammoth.js) (`test/test-data`), [PHPOffice/PHPWord](https://github.com/PHPOffice/PHPWord) (`samples/resources`, reader fixtures) | real WordprocessingML packages from several producers; copied names are prefixed by source so same-named containers don't collide |
| `xlsx/` | [jmcnamara/XlsxWriter](https://github.com/jmcnamara/XlsxWriter) (`xlsxwriter/test/comparison/xlsx_files`) | real OOXML SpreadsheetML packages |
| `docx-web/` | [superdoc-dev/docx-corpus](https://github.com/superdoc-dev/docx-corpus) via its `/manifest` (docxcorp.us) | a 2000-doc sample of 736K+ real `.docx` scraped from the public web (Common Crawl); reported, not gating |
| `rss/` | [plenaryapp/awesome-rss-feeds](https://github.com/plenaryapp/awesome-rss-feeds) OPML + the live feeds they list | routed valid/not-wf by Go's `encoding/xml` (plus a charset reader and a misplaced-`<?xml?>` check, to match libxml2) |
| `rss2.0/` | [plenaryapp/awesome-rss-feeds](https://github.com/plenaryapp/awesome-rss-feeds) + [kilimchoi/engineering-blogs](https://github.com/kilimchoi/engineering-blogs) OPML and the [tfederman/fountain-of-rss](https://github.com/tfederman/fountain-of-rss) TSV catalog | thousands of live feeds, kept only when well-formed and `version="2.0"`; the runner's `rss-2.0` check compiles `formats/rss-2.0.ebnf` and projects them |
| `xsd/` | [w3c/xsdtests](https://github.com/w3c/xsdtests) (official W3C XML Schema test suite) | `.xsd` schema files (flattened names, capped); the runner compiles each with `CompileXSD` and reports coverage of the supported subset |

The W3C XML subset is filtered at fetch time to what this parser targets: XML 1.0
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

Our parser has both modes (validating vs non-validating; ADR 0006). The runner
checks the `xml/` corpus in **validating** mode, where a no-DTD document is
correctly `INVALID` (nothing declares its elements) and an internal-subset
document is validated for real — so these tests are kept, not filtered. The
real-world corpora (DTD-less xlsx/docx/RSS) are checked **non-validating**, where
they are simply well-formed. Parameter-entity tests are kept too: internal
parameter entities are expanded before validating.
