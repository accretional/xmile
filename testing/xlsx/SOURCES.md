# XLSX test fixtures

Real-world SpreadsheetML (`.xlsx`, ISO/IEC 29500 Office Open XML) files for testing
XML parsing/transformation. Each `.xlsx` is a ZIP container of XML parts.

Pulled 2026-06-15 from the test suites of two reference implementations. Only the
`.xlsx` files were taken (via blob-filtered sparse checkout); upstream history,
binaries, and source code were not copied.

| Subfolder     | Count | Source repo (default branch tip)                          | License      |
|---------------|-------|-----------------------------------------------------------|--------------|
| `poi/`        | 369   | https://github.com/apache/poi — `test-data/`              | Apache-2.0   |
| `xlsxwriter/` | 987   | https://github.com/jmcnamara/XlsxWriter — `test/comparison/` | BSD-2-Clause |

- **poi/** preserves the upstream `test-data/` layout (`spreadsheet/`, `openxml4j/`,
  `integration/`, etc.). A broad corpus of valid, malformed, and edge-case workbooks
  used by the Apache POI conformance tests.
- **xlsxwriter/** are small, single-feature reference workbooks (`xlsx_files/`) each
  exercising one SpreadsheetML feature — useful for fine-grained parser coverage.

Total: 1,356 `.xlsx` files.
