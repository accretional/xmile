package service

// xlsx_test.go — the self-contained gate for the expanded SpreadsheetML
// vocabulary (xlsx.xsd): the parts a real .xlsx carries besides the workbook
// and worksheets — the style sheet, tables, comments, the DrawingML theme, and
// the docProps core/extended properties. Each must be a modeled root and project
// its common markup typed, while open mode tolerates the rest. Reuses the
// projectPart/typedChild/stringField helpers from docx_test.go.

import "testing"

func TestXlsxCompanionParts(t *testing.T) {
	schema, err := Format("xlsx")
	if err != nil {
		t.Fatalf("Format(xlsx): %v", err)
	}
	p, err := Default()
	if err != nil {
		t.Fatalf("Default: %v", err)
	}

	for _, root := range []string{
		"styleSheet", "table", "comments", "calcChain", "chartsheet",
		"wsDr", "chartSpace", "theme", "coreProperties", "Properties",
	} {
		if !schema.HasRoot(root) {
			t.Errorf("xlsx schema does not model companion root %q", root)
		}
	}

	// styles.xml: a font and a cell-format record. <fancyBit/> is unmodeled and
	// must be tolerated.
	styles := `<styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">
	  <fonts count="1"><font><sz val="11"/><name val="Calibri"/><b/></font></fonts>
	  <cellXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0"><alignment horizontal="center"/></xf></cellXfs>
	  <fancyBit/>
	</styleSheet>`
	sm := projectPart(t, p, schema, styles, "styleSheet")
	font := typedChild(t, typedChild(t, sm, "Fonts"), "Font")
	if got := stringField(typedChild(t, font, "Sz"), "val"); got != "11" {
		t.Errorf("font sz val = %q, want 11", got)
	}
	if got := stringField(typedChild(t, font, "Name"), "val"); got != "Calibri" {
		t.Errorf("font name val = %q, want Calibri", got)
	}
	xf := typedChild(t, typedChild(t, sm, "CellXfs"), "Xf")
	if got := stringField(xf, "font_id"); got != "0" {
		t.Errorf("xf fontId = %q, want 0", got)
	}
	if got := stringField(typedChild(t, xf, "Alignment"), "horizontal"); got != "center" {
		t.Errorf("alignment horizontal = %q, want center", got)
	}

	// tables/table1.xml: a defined table with its columns.
	table := `<table xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"
	    id="1" name="Table1" displayName="Table1" ref="A1:B3">
	  <tableColumns count="2">
	    <tableColumn id="1" name="First"/>
	    <tableColumn id="2" name="Second"/>
	  </tableColumns>
	</table>`
	tm := projectPart(t, p, schema, table, "table")
	if got := stringField(tm, "display_name"); got != "Table1" {
		t.Errorf("table displayName = %q, want Table1", got)
	}
	if got := stringField(typedChild(t, typedChild(t, tm, "TableColumns"), "TableColumn"), "name"); got != "First" {
		t.Errorf("first column name = %q, want First", got)
	}

	// docProps/core.xml is shared with docx: a text leaf comes through typed.
	core := `<cp:coreProperties
	    xmlns:cp="http://schemas.openxmlformats.org/package/2006/metadata/core-properties"
	    xmlns:dc="http://purl.org/dc/elements/1.1/">
	  <dc:creator>Ada</dc:creator>
	</cp:coreProperties>`
	cm := projectPart(t, p, schema, core, "cp:coreProperties")
	if got := stringField(typedChild(t, cm, "Creator"), "text"); got != "Ada" {
		t.Errorf("core creator text = %q, want Ada", got)
	}
}
