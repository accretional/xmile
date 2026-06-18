// Command example demonstrates the xmile service API end-to-end — the same
// surface the Documents/Schemas gRPC services wrap. Run it with:
//
//	go run ./examples/process
//
// It shows the four capabilities: parse generic XML, project a feed into the
// typed RSS 2.0 AST, compile a DTD into a schema and project against it, and
// unpack an OPC package.
package main

import (
	"archive/zip"
	"bytes"
	"fmt"
	"log"

	"google.golang.org/protobuf/encoding/prototext"

	xmlpb "github.com/accretional/xmile/proto/pb/xml"
	"github.com/accretional/xmile/service"
)

func main() {
	p, err := service.Default()
	if err != nil {
		log.Fatal(err)
	}

	// 1. Generic XML — no schema, the loosest projection (the former Parse).
	res, err := p.Process(`<a x="1">hi<b/></a>`, nil, false)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("== 1. generic XML AST ==")
	fmt.Print(prototext.Format(res.Document))

	// 2. Typed RSS 2.0 — Format selects a registered vocabulary.
	rss, err := service.Format("rss-2.0")
	if err != nil {
		log.Fatal(err)
	}
	res, err = p.Process(`<rss version="2.0"><channel><title>T</title>`+
		`<link>L</link><description>D</description>`+
		`<item><title>first</title></item></channel></rss>`, rss, true)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("\n== 2. typed RSS 2.0 AST ==")
	fmt.Print(prototext.Format(res.Document))

	// 3. Compile a DTD into a schema, then project a document against it
	//    (the compile-then-use path; the same works for XSD and EBNF).
	const noteDTD = `<!ELEMENT note (to, from, body)>` +
		`<!ELEMENT to (#PCDATA)><!ELEMENT from (#PCDATA)><!ELEMENT body (#PCDATA)>`
	note, err := service.CompileSchema([]byte(noteDTD), xmlpb.SchemaLanguage_DTD,
		service.SchemaOptions{Package: "note"}, false)
	if err != nil {
		log.Fatal(err)
	}
	res, err = p.Process(`<note><to>Tove</to><from>Jani</from><body>Hi</body></note>`, note, true)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("\n== 3. document projected against a compiled DTD schema ==")
	fmt.Print(prototext.Format(res.Document))

	// 4. Unpack an OPC package (built in-memory here; normally a .docx/.xlsx).
	pkg, err := service.ProcessPackage(samplePackage())
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("\n== 4. OPC package ==")
	for _, part := range pkg.Parts {
		fmt.Printf("part %s (%s): root <%s>\n", part.Name, part.ContentType, part.Document.GetRoot().GetName())
	}
	for _, r := range pkg.Relationships {
		fmt.Printf("rel %s -> %s\n", r.ID, r.ResolvedTarget)
	}
}

func samplePackage() []byte {
	files := map[string]string{
		"[Content_Types].xml": `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">` +
			`<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>` +
			`<Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/></Types>`,
		"_rels/.rels": `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
			`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/></Relationships>`,
		"word/document.xml": `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">` +
			`<w:body><w:p><w:r><w:t>Hello</w:t></w:r></w:p></w:body></w:document>`,
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, _ := zw.Create(name)
		w.Write([]byte(content))
	}
	zw.Close()
	return buf.Bytes()
}
