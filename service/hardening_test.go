package service

// hardening_test.go — resource-exhaustion regression gates. These lock in the
// fixes from limits.go / entities.go / pe.go: a small hostile document must be
// rejected with a *WFError, never crash the process or hang. They also guard
// against false positives (shallow-but-wide documents must still parse).

import (
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
)

func hp(t *testing.T) *Parser {
	t.Helper()
	p, err := Default()
	if err != nil {
		t.Fatalf("Default: %v", err)
	}
	return p
}

// Deep nesting must be rejected up front, not drive the recursive parser to an
// unrecoverable stack-overflow crash (confirmed at ~500k deep pre-fix).
func TestHardening_DeepNestingRejected(t *testing.T) {
	p := hp(t)
	depth := MaxNestingDepth + 5000
	src := strings.Repeat("<a>", depth) + strings.Repeat("</a>", depth)
	_, err := p.Parse(src, false)
	if err == nil {
		t.Fatalf("deep nesting (%d) was accepted; want a WFError", depth)
	}
	if _, ok := err.(*WFError); !ok {
		t.Fatalf("want *WFError, got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), "nesting") {
		t.Errorf("error should mention nesting depth; got %q", err.Error())
	}
}

// False-positive guard 1: a document nested well within the bound parses fine.
func TestHardening_ModerateNestingAccepted(t *testing.T) {
	p := hp(t)
	depth := 1000 // real documents nest in the dozens; 1000 is already generous
	src := strings.Repeat("<a>", depth) + strings.Repeat("</a>", depth)
	if _, err := p.Parse(src, false); err != nil {
		t.Fatalf("depth %d (< limit %d) should parse; got %v", depth, MaxNestingDepth, err)
	}
}

// False-positive guard 2: many sibling empty-element tags are shallow, not deep
// — the depth scanner must not count <a/> as nesting.
func TestHardening_WideEmptyElementsAccepted(t *testing.T) {
	p := hp(t)
	src := "<root>" + strings.Repeat("<a/>", MaxNestingDepth+5000) + "</root>"
	if _, err := p.Parse(src, false); err != nil {
		t.Fatalf("wide self-closing siblings (depth 2) should parse; got %v", err)
	}
}

// A nested internal-entity bomb ("billion laughs") must be rejected quickly, not
// expand as 2ⁿ. 30 levels would be ~8 GiB expanded; the byte budget + expandCheck
// memoization must turn it into a fast WFError.
func TestHardening_EntityBombRejected(t *testing.T) {
	p := hp(t)
	const levels = 30
	var d strings.Builder
	d.WriteString("<!DOCTYPE r [\n<!ENTITY e0 \"AAAAAAAA\">\n")
	for i := 1; i <= levels; i++ {
		d.WriteString("<!ENTITY e")
		d.WriteString(itoa(i))
		d.WriteString(" \"&e")
		d.WriteString(itoa(i - 1))
		d.WriteString(";&e")
		d.WriteString(itoa(i - 1))
		d.WriteString(";\">\n")
	}
	d.WriteString("]>\n<r>&e")
	d.WriteString(itoa(levels))
	d.WriteString(";</r>")
	src := d.String()

	done := make(chan error, 1)
	go func() {
		_, err := p.Parse(src, false)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("entity bomb was accepted; want a WFError")
		}
		if _, ok := err.(*WFError); !ok {
			t.Fatalf("want *WFError, got %T: %v", err, err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("entity bomb parse did not terminate within 10s (expansion not bounded)")
	}
}

// The DOCTYPE is preserved: the projector stores the verbatim body in
// Xml.Doctype and Generate re-emits it, so a document round-trips with its
// DOCTYPE intact (project.go / generate.go).
func TestHardening_DoctypeReproduced(t *testing.T) {
	p := hp(t)
	const doc = `<?xml version="1.0"?><!DOCTYPE a [ <!ELEMENT a (#PCDATA)> ]><a>hi</a>`
	x, err := p.Parse(doc, false)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if x.GetDoctype() == nil {
		t.Fatal("Doctype should be populated from the parsed DOCTYPE")
	}
	out, err := Generate(x)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !strings.Contains(string(out), "<!DOCTYPE a [ <!ELEMENT a (#PCDATA)> ]>") {
		t.Errorf("DOCTYPE not reproduced verbatim; got: %s", out)
	}
	// Full round-trip including the DOCTYPE: the re-parsed AST equals the first.
	x2, err := p.Parse(string(out), false)
	if err != nil {
		t.Fatalf("reparse: %v", err)
	}
	if !proto.Equal(x, x2) {
		t.Errorf("AST changed across round-trip (DOCTYPE or element tree)")
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [12]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
