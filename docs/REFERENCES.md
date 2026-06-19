# Some useful referenes I found along the way

The main XML language spec is taken from here: [XML Spec sheet](https://www.w3.org/TR/xml/). We had to make a few changes in the grammar structure from this spec to make it just plainly better, but it is still XML true.

XML syntax to keep in mind: https://www.w3schools.com/xml/xml_syntax.asp

XML Schema Definition (XSD) Langauge spec: https://www.w3.org/TR/xmlschema-ref/

The normative XSD parts the front-end (`service/language/xsd.go`) is built against:
- XML Schema Part 1: Structures — https://www.w3.org/TR/xmlschema-1/ (elements, complex types, sequence/choice/all, attributes, occurrence, groups/attribute groups, wildcards, substitution groups, derivation extension/restriction, import/include)
- XML Schema Part 2: Datatypes — https://www.w3.org/TR/xmlschema-2/ (built-in simple types; facets and union/list are validation concerns, currently lowered to a string leaf)
- W3C XML Schema test suite (official conformance corpus, `.xsd` test files) — https://github.com/w3c/xsdtests — fetched by `go run ./testing fetch`; the corpus runner (`go run ./testing`) compiles each and reports coverage of the supported subset (reported, not gating).

Office Open XML / OPC packages (DOCX, XLSX) — the package layer (`service/opc.go`) is built against:
- ECMA-376, Office Open XML File Formats (official standard) — https://ecma-international.org/publications-and-standards/standards/ecma-376/
- Open Packaging Conventions = ECMA-376 **Part 2** (the ZIP package, `[Content_Types].xml`, and `_rels` relationship graph — format-agnostic, shared by .docx/.xlsx/.pptx)
- WordprocessingML (.docx) and SpreadsheetML (.xlsx) part vocabularies = ECMA-376 **Part 1** (Fundamentals and Markup Language Reference)
- Mirrored as ISO/IEC 29500 (the OPC half is ISO/IEC 29500-2)
- Real-world `.docx` test corpus — [superdoc-dev/docx-corpus](https://github.com/superdoc-dev/docx-corpus) (736K+ `.docx` scraped from the public web via Common Crawl; served at https://docxcorp.us, listed by https://api.docxcorp.us/manifest). The testing runner samples ~2000 into `testing/corpus/docx-web/` and reports the parse rate (`go run ./testing fetch`).

The level-2 part vocabularies are first-class `formats/` specs that ride the
compile→project path (like `rss-2.0`), authored against ECMA-376 Part 1 /
ISO/IEC 29500-1 in the supported XSD subset and loaded *open* so a minimal
schema still accepts every valid part (the modeled core is typed, the rest
passes through):
- `formats/docx.xsd` — the XML parts a real `.docx` carries (one descriptor, a message per part root, routed by root local name): the WordprocessingML main document (`document`/`body`/`p`/`r`/`t`/tables/`sectPr`) and its companions — `styles`, `fonts` (fontTable), `settings`, `webSettings`, `numbering`, `hdr`/`ftr`, `footnotes`/`endnotes` — plus the shared OOXML parts a Word package also includes: the DrawingML `theme` and the docProps `coreProperties`/`Properties`. ECMA-376 Part 1 / ISO/IEC 29500-1 §17 (WordprocessingML) & §20 (DrawingML theme); ISO/IEC 29500-2 §11/§15 (core & extended properties).
- `formats/xlsx.xsd` — the XML parts a real `.xlsx` carries (one descriptor, a message per part root, routed by root local name): the SpreadsheetML worksheet (`worksheet`/`sheetData`/`row`/`c` with `v`/`f`/`is`), workbook (`workbook`/`sheets`/`sheet`), shared strings (`sst`/`si`/`t`), the style sheet (`styleSheet`: numFmts/fonts/fills/borders/cellXfs/cellStyles), `table`, `comments`, `calcChain`, `chartsheet`, and (recognized at the root only) the worksheet drawing (`wsDr`) and chart (`chartSpace`) — plus the shared OOXML parts: the DrawingML `theme` and the docProps `coreProperties`/`Properties`. ECMA-376 Part 1 / ISO/IEC 29500-1 §18 (SpreadsheetML) & §20 (DrawingML theme); ISO/IEC 29500-2 §11/§15 (core & extended properties).

