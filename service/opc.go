package service

// opc.go — Open Packaging Conventions support (ADR 0008 Phase 5). A .docx /
// .xlsx / .pptx is an OPC "package": a ZIP of XML "parts" plus a content-type
// map ([Content_Types].xml) and a relationship graph (_rels/*.rels).
// ProcessPackage unpacks it, parses each XML part into the generic AST through
// the same Parser, and resolves the content types and relationships into a
// typed package tree — the package-level analogue of Process. The part
// vocabularies (WordprocessingML, SpreadsheetML) differ; the package layer does
// not, so one implementation serves every OPC format.
//
// Spec: ECMA-376 Part 2 / ISO-IEC 29500-2, Open Packaging Conventions
// (see docs/REFERENCES.md).

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"

	xmlpb "github.com/accretional/xmile/proto/pb/xml"
)

// Package is a processed OPC package: its content-type map, its XML parts (each
// parsed into the generic AST), and the package-level relationships.
type Package struct {
	ContentTypes  *ContentTypes
	Parts         []*Part
	Relationships []*Relationship // package root (_rels/.rels)
}

// Part is one XML part of a package.
type Part struct {
	Name        string          // absolute part name, e.g. "/word/document.xml"
	ContentType string          // resolved from [Content_Types].xml
	Document    *xmlpb.Document // the parsed generic AST
	Rels        []*Relationship // the part's relationships (<dir>/_rels/<file>.rels)
}

// Relationship is one OPC relationship (a _rels entry).
type Relationship struct {
	ID             string
	Type           string
	Target         string // as written (relative or absolute)
	ResolvedTarget string // internal targets resolved to an absolute part name
	Mode           string // "" (internal) or "External"
}

// ContentTypes is the package's [Content_Types].xml: per-extension defaults and
// per-part overrides.
type ContentTypes struct {
	Defaults  map[string]string // lowercased extension -> content type
	Overrides map[string]string // absolute part name -> content type
}

// lookup resolves a part's content type: an override wins, else the default for
// its extension.
func (c *ContentTypes) lookup(partName string) string {
	if c == nil {
		return ""
	}
	if ov, ok := c.Overrides[partName]; ok {
		return ov
	}
	ext := strings.ToLower(strings.TrimPrefix(path.Ext(partName), "."))
	return c.Defaults[ext]
}

// ProcessPackage unpacks an OPC package and parses its XML parts. Binary parts
// (images, fonts, …) are recorded only through the content-type map and
// relationships, not parsed. A part that is not well-formed XML fails the whole
// package, naming the part.
func ProcessPackage(data []byte) (*Package, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("not a ZIP package: %w", err)
	}
	files := map[string][]byte{}
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("open %s: %w", f.Name, err)
		}
		b, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", f.Name, err)
		}
		files["/"+strings.TrimPrefix(path.Clean(f.Name), "/")] = b
	}

	ct, err := parseContentTypes(files["/[Content_Types].xml"])
	if err != nil {
		return nil, err
	}
	p, err := Default()
	if err != nil {
		return nil, err
	}

	pkg := &Package{ContentTypes: ct}
	if rb := files["/_rels/.rels"]; rb != nil {
		if rels, rerr := parseRels(rb, "/"); rerr == nil {
			pkg.Relationships = rels
		}
	}

	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if name == "/[Content_Types].xml" || isRelsPart(name) {
			continue // the content-type map and rels are read, not parts
		}
		cty := ct.lookup(name)
		if !isXMLPart(cty, name) {
			continue // a binary part (image, font, …)
		}
		doc, perr := p.Parse(string(files[name]), false)
		if perr != nil {
			return nil, fmt.Errorf("part %s: %w", name, perr)
		}
		part := &Part{Name: name, ContentType: cty, Document: doc}
		if rb := files[relsNameFor(name)]; rb != nil {
			if rels, rerr := parseRels(rb, path.Dir(name)); rerr == nil {
				part.Rels = rels
			}
		}
		pkg.Parts = append(pkg.Parts, part)
	}
	return pkg, nil
}

// parseContentTypes reads [Content_Types].xml into a ContentTypes. A missing map
// is tolerated (an empty one), so a malformed package still yields its parts by
// extension fallback.
func parseContentTypes(b []byte) (*ContentTypes, error) {
	ct := &ContentTypes{Defaults: map[string]string{}, Overrides: map[string]string{}}
	if len(b) == 0 {
		return ct, nil
	}
	p, err := Default()
	if err != nil {
		return nil, err
	}
	doc, err := p.Parse(string(b), false)
	if err != nil {
		return nil, fmt.Errorf("[Content_Types].xml: %w", err)
	}
	for _, d := range xsdChildrenLocal(doc.GetRoot(), "Default") {
		ct.Defaults[strings.ToLower(xsdAttr(d, "Extension"))] = xsdAttr(d, "ContentType")
	}
	for _, o := range xsdChildrenLocal(doc.GetRoot(), "Override") {
		ct.Overrides[xsdAttr(o, "PartName")] = xsdAttr(o, "ContentType")
	}
	return ct, nil
}

// parseRels reads a .rels part, resolving internal targets relative to baseDir.
func parseRels(b []byte, baseDir string) ([]*Relationship, error) {
	p, err := Default()
	if err != nil {
		return nil, err
	}
	doc, err := p.Parse(string(b), false)
	if err != nil {
		return nil, err
	}
	var out []*Relationship
	for _, r := range xsdChildrenLocal(doc.GetRoot(), "Relationship") {
		rel := &Relationship{
			ID:     xsdAttr(r, "Id"),
			Type:   xsdAttr(r, "Type"),
			Target: xsdAttr(r, "Target"),
			Mode:   xsdAttr(r, "TargetMode"),
		}
		if rel.Mode != "External" {
			t := rel.Target
			if !strings.HasPrefix(t, "/") {
				t = path.Join(baseDir, t)
			}
			rel.ResolvedTarget = "/" + strings.TrimPrefix(path.Clean(t), "/")
		}
		out = append(out, rel)
	}
	return out, nil
}

func isRelsPart(name string) bool { return strings.Contains(name, "/_rels/") }

// relsNameFor maps a part to its relationships part: /word/document.xml ->
// /word/_rels/document.xml.rels.
func relsNameFor(partName string) string {
	dir, file := path.Split(partName)
	return dir + "_rels/" + file + ".rels"
}

// isXMLPart reports whether a part should be parsed as XML, from its content
// type (the OOXML "+xml" convention) or, lacking one, its extension.
func isXMLPart(contentType, name string) bool {
	ct := strings.ToLower(contentType)
	if strings.HasSuffix(ct, "+xml") || ct == "application/xml" || ct == "text/xml" {
		return true
	}
	if contentType == "" {
		switch strings.ToLower(path.Ext(name)) {
		case ".xml", ".rels":
			return true
		}
	}
	return false
}
