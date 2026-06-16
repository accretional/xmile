package service

// pe.go — parameter-entity expansion for the DTD validation view.
//
// The well-formedness pass reads the DTD as written. Validation, though, needs
// the *effective* declarations, and a DTD may build them through parameter
// entities: `<!ENTITY % p "<!ELEMENT a (b)>"> %p;`. Before validating we expand
// the internal parameter-entity references in the DOCTYPE body and reparse, so
// the content models and attribute lists they contribute become visible. This
// is confined to the validation path; the well-formedness reading is untouched.
//
// External parameter entities (declared SYSTEM/PUBLIC) cannot be resolved, so a
// reference to one — or to an undeclared one — makes expansion incomplete and
// the document CannotValidate rather than a guess.

import (
	"strings"

	pb "github.com/accretional/gluon/v2/pb"
)

// expandedDTD expands the internal parameter entities of the DOCTYPE body
// captured in the main parse, reparses the result, and returns the expanded
// DTD CST plus a dtdInfo for it. ok is false when a parameter entity is
// referenced that cannot be resolved (external or undeclared), or the expanded
// text no longer parses — i.e. when the document cannot be soundly validated.
func (p *Parser) expandedDTD(root *pb.ASTNode) (*pb.ASTNode, *dtdInfo, bool) {
	span := firstDescendant(root, "dtd_text")
	if span == nil {
		return nil, nil, false
	}
	expanded, complete := expandPEs(span.GetValue())
	if !complete {
		return nil, nil, false
	}
	dp, err := dtdParser()
	if err != nil {
		return nil, nil, false
	}
	cst, err := dp.ParseCST(expanded)
	if err != nil {
		return nil, nil, false
	}
	dtdRoot := cst.GetRoot()
	return dtdRoot, buildDTDInfo(dtdRoot), true
}

// expandPEs replaces markup-level parameter-entity references (%name;) in a DTD
// body with their replacement text, repeating until none remain (a parameter
// entity's replacement may itself declare or reference others). Each inclusion
// is padded with surrounding spaces (XML 4.4.8) so adjacent tokens stay
// separate. It returns the expanded text and whether every reference resolved.
func expandPEs(dtd string) (string, bool) {
	complete := true
	for round := 0; round < 64; round++ {
		defs, external := collectPEDefs(dtd)
		out, changed, ok := replacePERefs(dtd, defs, external)
		if !ok {
			complete = false
		}
		dtd = out
		if !changed {
			break
		}
	}
	return dtd, complete
}

// collectPEDefs scans a DTD body for parameter-entity declarations, returning
// the internal ones (name -> replacement literal) and the names of external
// ones (declared SYSTEM/PUBLIC, which we cannot expand).
func collectPEDefs(s string) (map[string]string, map[string]bool) {
	defs := map[string]string{}
	external := map[string]bool{}
	for i := 0; ; {
		k := strings.Index(s[i:], "<!ENTITY")
		if k < 0 {
			break
		}
		p := skipSpace(s, i+k+len("<!ENTITY"))
		i = i + k + len("<!ENTITY")
		if p >= len(s) || s[p] != '%' { // a general entity, not a PE
			continue
		}
		p = skipSpace(s, p+1)
		start := p
		for p < len(s) && isNameByte(s[p]) {
			p++
		}
		name := s[start:p]
		if name == "" {
			continue
		}
		p = skipSpace(s, p)
		if p < len(s) && (s[p] == '"' || s[p] == '\'') {
			q := s[p]
			p++
			v := p
			for p < len(s) && s[p] != q {
				p++
			}
			defs[name] = s[v:p]
		} else {
			external[name] = true // SYSTEM / PUBLIC
		}
	}
	return defs, external
}

// replacePERefs performs one pass of %name; substitution outside comments,
// PIs and quoted literals. ok is false if a reference named an external or
// undeclared parameter entity.
func replacePERefs(s string, defs map[string]string, external map[string]bool) (string, bool, bool) {
	var b strings.Builder
	changed, ok := false, true
	for i := 0; i < len(s); {
		switch {
		case strings.HasPrefix(s[i:], "<!--"):
			j := strings.Index(s[i+4:], "-->")
			if j < 0 {
				b.WriteString(s[i:])
				return b.String(), changed, ok
			}
			b.WriteString(s[i : i+4+j+3])
			i += 4 + j + 3
		case strings.HasPrefix(s[i:], "<?"):
			j := strings.Index(s[i+2:], "?>")
			if j < 0 {
				b.WriteString(s[i:])
				return b.String(), changed, ok
			}
			b.WriteString(s[i : i+2+j+2])
			i += 2 + j + 2
		case s[i] == '"' || s[i] == '\'':
			q := s[i]
			b.WriteByte(s[i])
			i++
			for i < len(s) && s[i] != q {
				b.WriteByte(s[i])
				i++
			}
			if i < len(s) {
				b.WriteByte(s[i])
				i++
			}
		case s[i] == '%':
			j := i + 1
			for j < len(s) && isNameByte(s[j]) {
				j++
			}
			if j < len(s) && s[j] == ';' && j > i+1 {
				name := s[i+1 : j]
				if val, isInternal := defs[name]; isInternal {
					b.WriteByte(' ')
					b.WriteString(val)
					b.WriteByte(' ')
					changed = true
				} else {
					ok = false // external or undeclared parameter entity
					b.WriteString(s[i : j+1])
				}
				i = j + 1
			} else {
				b.WriteByte(s[i])
				i++
			}
		default:
			b.WriteByte(s[i])
			i++
		}
	}
	return b.String(), changed, ok
}

func skipSpace(s string, i int) int {
	for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\r' || s[i] == '\n') {
		i++
	}
	return i
}
