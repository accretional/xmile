package service

// limits.go — resource guards against maliciously small inputs that would
// otherwise exhaust memory or the goroutine stack. XML's recursive shape lets a
// few kilobytes force unbounded work in two ways, both of which crash the
// process rather than return an error:
//
//   - Deep element nesting drives the recursive-descent parser (and the ~dozen
//     recursive tree walks that follow it) to a Go stack-overflow, which is a
//     fatal runtime error that recover() cannot catch. Confirmed at ~500k deep
//     (~3.5 MiB source). MaxNestingDepth rejects such input up front with a
//     *WFError, far below the crash threshold and far above any real document.
//   - Nested internal entity definitions expand as 2ⁿ (the "billion laughs"
//     attack). maxEntityExpansionBytes caps how large a single entity may expand
//     to (see entities.go / pe.go), turning the bomb into a *WFError.
//
// These are deliberately generous: real XML nests in the dozens and uses
// entities for small boilerplate, so a conforming document never approaches
// them. They bound only pathological input.

import "strings"

const (
	// MaxNestingDepth is the deepest element nesting Parse accepts. Beyond it the
	// parser would risk a stack-overflow crash. Exported so a caller with an
	// unusual-but-trusted corpus can reason about the bound; it is not currently
	// configurable per-parse (no real document approaches it).
	MaxNestingDepth = 10000

	// maxEntityExpansionBytes caps the expanded size of a single internal general
	// entity (entities.go). 10 MiB is orders of magnitude above any legitimate
	// entity while stopping a 2ⁿ bomb long before it exhausts memory.
	maxEntityExpansionBytes = 10 << 20

	// maxPEExpansionBytes caps DTD parameter-entity expansion in validating mode
	// (pe.go), where nested %refs; can likewise double each round.
	maxPEExpansionBytes = 10 << 20
)

// nestingDepthExceeds reports whether src nests elements deeper than limit. It
// is a cheap single linear pass over the raw source run before the recursive
// parser, so it must not itself recurse. It over-approximates safely: it never
// under-counts real element depth (so it cannot miss a bomb), and the only
// inputs it might over-count — pathologically deep fake markup inside an entity
// value or other declaration — are not legitimate documents. Comments, CDATA,
// PIs, and markup declarations are skipped; empty-element tags (<a/>) do not add
// depth.
func nestingDepthExceeds(src string, limit int) bool {
	depth, n := 0, len(src)
	for i := 0; i < n; {
		if src[i] != '<' {
			i++
			continue
		}
		switch {
		case strings.HasPrefix(src[i:], "<!--"):
			j := strings.Index(src[i+4:], "-->")
			if j < 0 {
				return false // unterminated; the parser will reject it
			}
			i += 4 + j + 3
		case strings.HasPrefix(src[i:], "<![CDATA["):
			j := strings.Index(src[i+9:], "]]>")
			if j < 0 {
				return false
			}
			i += 9 + j + 3
		case strings.HasPrefix(src[i:], "<?"):
			j := strings.Index(src[i+2:], "?>")
			if j < 0 {
				return false
			}
			i += 2 + j + 2
		case i+1 < n && src[i+1] == '!':
			// A markup declaration (DOCTYPE and, inside a subset, ELEMENT/ENTITY/…).
			// Skip to its closing '>', honoring quoted literals so a '>' inside an
			// entity value does not end it early. Element markup never begins "<!".
			i, _ = scanToGt(src, i+2)
		case i+1 < n && src[i+1] == '/':
			depth-- // end tag
			i, _ = scanToGt(src, i+2)
		default:
			end, selfClosing := scanToGt(src, i+1)
			if !selfClosing {
				depth++
				if depth > limit {
					return true
				}
			}
			i = end
		}
	}
	return false
}

// scanToGt scans from i to just past the next '>' that is not inside a quoted
// attribute literal, returning that index and whether the tag self-closed (the
// last non-space byte before '>' was '/'). If no '>' is found it returns len(src)
// and false, leaving the malformed tail for the parser to reject.
func scanToGt(src string, i int) (int, bool) {
	n := len(src)
	lastNonSpace := byte(0)
	for i < n {
		c := src[i]
		switch c {
		case '"', '\'':
			i++
			for i < n && src[i] != c {
				i++
			}
			if i < n {
				i++ // closing quote
			}
			lastNonSpace = c
		case '>':
			return i + 1, lastNonSpace == '/'
		default:
			if c != ' ' && c != '\t' && c != '\r' && c != '\n' {
				lastNonSpace = c
			}
			i++
		}
	}
	return n, false
}
