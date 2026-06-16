# DOCX test fixtures

Real-world WordprocessingML (`.docx`, ISO/IEC 29500 Office Open XML) files for testing
XML parsing/transformation. Each `.docx` is a ZIP container of XML parts.

Pulled 2026-06-15 from the test suites of two reference implementations. Only the
`.docx` files were taken (via blob-filtered sparse checkout); upstream history,
binaries, and source code were not copied.

| Subfolder      | Count | Source repo (default branch tip)                     | License     |
|----------------|-------|------------------------------------------------------|-------------|
| `poi/`         | 178   | https://github.com/apache/poi — `test-data/`         | Apache-2.0  |
| `python-docx/` | 46    | https://github.com/python-openxml/python-docx        | MIT         |

- **poi/** preserves the upstream `test-data/` layout (`document/`, `openxml4j/`, etc.).
  A broad corpus of valid, malformed, and edge-case documents used by the Apache POI
  conformance tests.
- **python-docx/** are the fixture documents from the python-docx unit/feature tests
  (`tests/test_files/`, `features/steps/test_files/`).

Total: 224 `.docx` files.
