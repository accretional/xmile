package service

import "testing"

// conformance_test.go is the service's self-contained gate. It carries its own
// XML samples so `go test ./service` needs nothing fetched; the full W3C corpus
// is a separate concern, run over the fetched corpus by `go run ./testing/xml-parse`
// (which test.sh invokes) — see testing/README.md.
//
// Each sample is checked in validating mode against its expected verdict, and in
// non-validating mode against the looser contract: a not-wf document is rejected
// in both modes, while a well-formed one (valid or merely DTD-invalid) is always
// accepted when not validating.

type verdictKind int

const (
	vValid   verdictKind = iota // well-formed and (validating) DTD-valid
	vInvalid                    // well-formed but DTD-invalid
	vNotWF                      // not well-formed (or not namespace-well-formed)
)

func (v verdictKind) String() string {
	switch v {
	case vValid:
		return "valid"
	case vInvalid:
		return "invalid"
	default:
		return "not-wf"
	}
}

// classify maps a Parse error to the verdict it represents.
func classify(err error) verdictKind {
	switch err.(type) {
	case nil:
		return vValid
	case *ValidityError:
		return vInvalid
	default: // *WFError, *CannotValidateError
		return vNotWF
	}
}

var conformanceCases = []struct {
	name string
	xml  string
	want verdictKind // the verdict in validating mode
}{
	// --- well-formed and valid ---
	{"element content", `<!DOCTYPE doc [<!ELEMENT doc (a)><!ELEMENT a EMPTY>]><doc><a/></doc>`, vValid},
	{"occurrences", `<!DOCTYPE doc [<!ELEMENT doc (a*,b?)><!ELEMENT a EMPTY><!ELEMENT b EMPTY>]><doc><a/><a/><b/></doc>`, vValid},
	{"mixed + ID", `<!DOCTYPE doc [<!ELEMENT doc (#PCDATA)><!ATTLIST doc id ID #IMPLIED>]><doc id="x">hi</doc>`, vValid},
	{"enumeration default", `<!DOCTYPE doc [<!ELEMENT doc EMPTY><!ATTLIST doc k (a|b) "a">]><doc/>`, vValid},
	{"general entity to element", `<!DOCTYPE doc [<!ELEMENT doc (a)><!ELEMENT a EMPTY><!ENTITY e "<a/>">]><doc>&e;</doc>`, vValid},
	{"parameter entity builds decl", `<!DOCTYPE doc [<!ENTITY % p "<!ELEMENT doc EMPTY>">%p;]><doc/>`, vValid},
	{"default namespace", `<!DOCTYPE doc [<!ELEMENT doc EMPTY><!ATTLIST doc xmlns CDATA #IMPLIED>]><doc xmlns="urn:x"/>`, vValid},
	{"prefixed namespace", `<!DOCTYPE p:doc [<!ELEMENT p:doc EMPTY><!ATTLIST p:doc xmlns:p CDATA #IMPLIED>]><p:doc xmlns:p="urn:x"/>`, vValid},

	// --- well-formed but DTD-invalid ---
	{"undeclared child", `<!DOCTYPE doc [<!ELEMENT doc (a)><!ELEMENT a EMPTY>]><doc><b/></doc>`, vInvalid},
	{"char data in element content", `<!DOCTYPE doc [<!ELEMENT doc (a)><!ELEMENT a EMPTY>]><doc>x<a/></doc>`, vInvalid},
	{"missing required attribute", `<!DOCTYPE doc [<!ELEMENT doc EMPTY><!ATTLIST doc id ID #REQUIRED>]><doc/>`, vInvalid},
	{"fixed attribute mismatch", `<!DOCTYPE doc [<!ELEMENT doc EMPTY><!ATTLIST doc k CDATA #FIXED "yes">]><doc k="no"/>`, vInvalid},
	{"wrong root element", `<!DOCTYPE doc [<!ELEMENT doc EMPTY>]><other/>`, vInvalid},
	{"no DTD to validate against", `<doc/>`, vInvalid},
	{"colon in ID value", `<!DOCTYPE doc [<!ELEMENT doc EMPTY><!ATTLIST doc id ID #IMPLIED>]><doc id="a:b"/>`, vInvalid},

	// --- not well-formed (XML 1.0) ---
	{"mismatched tags", `<doc></dox>`, vNotWF},
	{"undeclared entity", `<doc>&e;</doc>`, vNotWF},
	{"duplicate attribute", `<doc a="1" a="2"/>`, vNotWF},

	// --- not namespace-well-formed (integral) ---
	{"multi-colon QName", `<a:b:c/>`, vNotWF},
	{"undeclared element prefix", `<n:doc/>`, vNotWF},
	{"declaring the xmlns prefix", `<doc xmlns:xmlns="urn:x"/>`, vNotWF},
	{"colon in entity name", `<!DOCTYPE doc [<!ELEMENT doc EMPTY><!ENTITY a:b "x">]><doc/>`, vNotWF},
	{"colon in PI target", `<?a:b data?><doc/>`, vNotWF},
}

// TestAttrValueNormalization pins XML 3.3.3 attribute-value normalization in the
// projected AST: literal and entity-replacement white space fold to a space,
// but a direct character reference keeps its literal character.
func TestAttrValueNormalization(t *testing.T) {
	p, err := Default()
	if err != nil {
		t.Fatalf("parser init: %v", err)
	}
	const head = `<!DOCTYPE d [<!ELEMENT d EMPTY><!ATTLIST d a CDATA #IMPLIED>`
	cases := []struct{ name, xml, want string }{
		{"literal whitespace folds", head + `]><d a="x` + "\n" + `y"/>`, "x y"},
		{"entity whitespace folds", head + `<!ENTITY e "` + "\n" + `">]><d a="x&e;y"/>`, "x y"},
		{"direct char-ref preserved", head + `]><d a="x&#9;y"/>`, "x\ty"},
	}
	for _, c := range cases {
		doc, perr := p.Parse(c.xml, false)
		if perr != nil {
			t.Errorf("%s: parse: %v", c.name, perr)
			continue
		}
		got := ""
		for _, at := range doc.GetRoot().GetAttrs() {
			if at.GetName() == "a" {
				got = at.GetValue()
			}
		}
		if got != c.want {
			t.Errorf("%s: attr a = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestConformance(t *testing.T) {
	p, err := Default()
	if err != nil {
		t.Fatalf("parser init: %v", err)
	}
	for _, c := range conformanceCases {
		if _, verr := p.Parse(c.xml, true); classify(verr) != c.want {
			t.Errorf("%s: validating => %s, want %s (%v)", c.name, classify(verr), c.want, verr)
		}
		_, nverr := p.Parse(c.xml, false)
		switch {
		case c.want == vNotWF && nverr == nil:
			t.Errorf("%s: non-validating accepted a not-well-formed document", c.name)
		case c.want != vNotWF && nverr != nil:
			t.Errorf("%s: non-validating rejected a well-formed document: %v", c.name, nverr)
		}
	}
}
