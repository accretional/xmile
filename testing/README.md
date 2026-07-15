# testing/

Corpus tooling for the xmile parser: it fetches real-world and conformance XML
from upstream sources, organizes it by file type and expected verdict, and runs
the parser over it.

Nothing here is part of the shipped parser. The fetcher classifies files with
an oracle that is independent of our parser (the W3C manifests), so the corpus
is a fair test and not a restatement of our own behavior.

## Layout

| Path | Role |
|---|---|
| `main.go` | the corpus runner: `go run ./testing` ensures the corpus then runs every check (gates `xml` + `opc`; reports `docx-web` + `xsd`); `go run ./testing fetch` rebuilds only the corpus |
| `fetch.go` | the single fetcher: downloads and classifies the W3C XML suite by manifest, sparse-checks-out the OOXML repos, clones the W3C XSD suite |
| `progress/` | a small terminal progress bar |
| `corpus/` | the fetched corpus (gitignored; rebuilt on demand) |

## Corpus

`corpus/<format>/<verdict>/`, organized by **file type**, not by source. The
folder name is the expected outcome for every file inside it.

```
corpus/
  xml/      valid/  invalid/  not-wf/  # W3C XML Conformance Test Suite (gates, 100%)
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
real-world corpora (DTD-less xlsx/docx) are checked **non-validating**, where
they are simply well-formed. Parameter-entity tests are kept too: internal
parameter entities are expanded before validating.
