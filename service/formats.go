package service

// formats.go — the standard-format registry (ADR 0008). A format name resolves
// to a spec file under formats/ (the `formats` package), whose extension selects
// the schema language; the spec is compiled on first use and the resulting
// Schema cached. Adding a standard format is dropping a spec file in formats/ —
// no code here, except the small per-format metadata a schema language cannot
// express (namespace extensibility, a structural pre-check).

import (
	"fmt"
	"strings"
	"sync"

	"github.com/accretional/xmile/formats"
	xmlpb "github.com/accretional/xmile/proto/pb/xml"
)

// formatMeta carries the few per-format semantics no schema language expresses.
// A format absent here is a closed vocabulary with no pre-check. This map is the
// only format-specific Go in the engine; the structure always lives in the spec
// file under formats/.
var formatMeta = map[string]struct {
	nsExtensible bool
	preValidate  func(*xmlpb.Xml) error
	// open marks a partial vocabulary: a minimal schema for a large format (the
	// OOXML packages, docx/xlsx) types its modeled markup and tolerates the rest,
	// so every valid part projects without error. See projectOptions.open.
	open bool
}{
	"rss-2.0": {nsExtensible: true, preValidate: validateRSS},
	"docx":    {open: true},
	"xlsx":    {open: true},
}

// langByExt maps a spec-file extension to its schema language.
var langByExt = []struct {
	ext  string
	lang xmlpb.SchemaLanguage
}{
	{".ebnf", xmlpb.SchemaLanguage_EBNF_VOCAB},
	{".xsd", xmlpb.SchemaLanguage_XSD},
	{".dtd", xmlpb.SchemaLanguage_DTD},
}

var formatCache sync.Map // name -> *Schema

// Format resolves a registered standard format to its Schema, compiling its spec
// from formats/ on first use and caching the result. "" returns (nil, nil) —
// generic XML. An unknown name (no spec file in formats/) is an error.
func Format(name string) (*Schema, error) {
	if name == "" {
		return nil, nil
	}
	if s, ok := formatCache.Load(name); ok {
		return s.(*Schema), nil
	}
	spec, lang, err := loadFormatSpec(name)
	if err != nil {
		return nil, err
	}
	meta := formatMeta[name]
	schema, err := CompileSchema(spec, lang, SchemaOptions{Package: protoPackage(name)}, meta.nsExtensible)
	if err != nil {
		return nil, fmt.Errorf("format %q: %w", name, err)
	}
	schema.PreValidate = meta.preValidate
	schema.Open = meta.open
	formatCache.Store(name, schema)
	return schema, nil
}

func loadFormatSpec(name string) ([]byte, xmlpb.SchemaLanguage, error) {
	for _, e := range langByExt {
		if b, err := formats.FS.ReadFile(name + e.ext); err == nil {
			return b, e.lang, nil
		}
	}
	return nil, 0, fmt.Errorf("unknown format %q (no spec in formats/)", name)
}

// protoPackage derives a valid proto package name from a format name (e.g.
// "rss-2.0" -> "rss20"). Cosmetic only: projection matches messages by name.
func protoPackage(name string) string {
	var b strings.Builder
	for _, r := range name {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "lang"
	}
	return b.String()
}
