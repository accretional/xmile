# Some useful referenes I found along the way

The main XML language spec is taken from here: [XML Spec sheet](https://www.w3.org/TR/xml/). We had to make a few changes in the grammar structure from this spec to make it just plainly better, but it is still XML true.

XML syntax to keep in mind: https://www.w3schools.com/xml/xml_syntax.asp

XML Schema Definition (XSD) Langauge spec: https://www.w3.org/TR/xmlschema-ref/

The normative XSD parts the front-end (`service/xsd.go`) is built against:
- XML Schema Part 1: Structures — https://www.w3.org/TR/xmlschema-1/ (elements, complex types, sequence/choice/all, attributes, occurrence)
- XML Schema Part 2: Datatypes — https://www.w3.org/TR/xmlschema-2/ (built-in simple types; facets are validation, currently lowered to a string leaf)
- W3C XML Schema test suite (official conformance corpus, `.xsd` test files) — https://github.com/w3c/xsdtests — fetched by `go run ./testing xsd`; the `xsd-parse` harness compiles each and reports coverage of the supported subset (reported, not gating).

Office Open XML / OPC packages (DOCX, XLSX) — the package layer (`service/opc.go`) is built against:
- ECMA-376, Office Open XML File Formats (official standard) — https://ecma-international.org/publications-and-standards/standards/ecma-376/
- Open Packaging Conventions = ECMA-376 **Part 2** (the ZIP package, `[Content_Types].xml`, and `_rels` relationship graph — format-agnostic, shared by .docx/.xlsx/.pptx)
- WordprocessingML (.docx) and SpreadsheetML (.xlsx) part vocabularies = ECMA-376 **Part 1** (Fundamentals and Markup Language Reference)
- Mirrored as ISO/IEC 29500 (the OPC half is ISO/IEC 29500-2)

