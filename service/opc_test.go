package service

// opc_test.go — the self-contained gate for OPC packaging (ADR 0008 Phase 5):
// build a minimal package in-memory and assert ProcessPackage unpacks it,
// resolves the content type, parses the part, and resolves the relationship.

import (
	"archive/zip"
	"bytes"
	"strings"
	"testing"
)

func buildPackage(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

const ctXML = `<?xml version="1.0"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
  <Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
  <Default Extension="xml" ContentType="application/xml"/>
  <Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/>
</Types>`

const dotRels = `<?xml version="1.0"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/>
</Relationships>`

const docXML = `<?xml version="1.0"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
  <w:body><w:p><w:r><w:t>Hello</w:t></w:r></w:p></w:body>
</w:document>`

func TestProcessPackage(t *testing.T) {
	data := buildPackage(t, map[string]string{
		"[Content_Types].xml": ctXML,
		"_rels/.rels":         dotRels,
		"word/document.xml":   docXML,
		"word/media/logo.png": "\x89PNG\r\n\x1a\n binary, not parsed",
	})
	pkg, err := ProcessPackage(data)
	if err != nil {
		t.Fatalf("ProcessPackage: %v", err)
	}
	if len(pkg.Parts) != 1 {
		t.Fatalf("got %d XML parts, want 1 (the .png is binary)", len(pkg.Parts))
	}
	part := pkg.Parts[0]
	if part.Name != "/word/document.xml" {
		t.Errorf("part name = %q, want /word/document.xml", part.Name)
	}
	if !strings.Contains(part.ContentType, "wordprocessingml") {
		t.Errorf("content type = %q", part.ContentType)
	}
	if got := part.Document.GetRoot().GetName(); got != "w:document" {
		t.Errorf("part root = %q, want w:document", got)
	}
	if len(pkg.Relationships) != 1 || pkg.Relationships[0].ResolvedTarget != "/word/document.xml" {
		t.Errorf("relationship not resolved to /word/document.xml: %+v", pkg.Relationships)
	}
}
