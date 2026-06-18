// Package formats holds the standard format specs as data — each a schema in a
// supported language (an EBNF element vocabulary, a DTD, or an XSD), compiled on
// demand by the service's format registry (service.Format). Adding a standard
// format is dropping its spec file here; there is no per-format Go. The service
// maintains XML and the schema languages (ADR 0008 levels 0 and 1); the formats
// themselves (level 2) live here as data.
//
// A spec is looked up as "<name>.<ext>", where the extension selects the
// language: .ebnf, .xsd, .dtd. A package format (e.g. an OPC .docx) is a
// subdirectory of part schemas plus a manifest (added when wired).
package formats

import "embed"

//go:embed *.ebnf
var FS embed.FS
