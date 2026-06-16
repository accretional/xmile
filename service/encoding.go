package service

import (
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	xmlpb "github.com/accretional/xmile/proto/pb/xml"
)

// normalizeEncoding strips a UTF-8 BOM and transcodes UTF-16 (LE/BE, by
// BOM) to UTF-8. Input without a BOM is assumed UTF-8. (Encoding handling
// is an input concern, outside the grammar.)
func normalizeEncoding(s string) string {
	b := []byte(s)
	switch {
	case len(b) >= 3 && b[0] == 0xEF && b[1] == 0xBB && b[2] == 0xBF:
		return string(b[3:])
	case len(b) >= 2 && b[0] == 0xFF && b[1] == 0xFE:
		return decodeUTF16(b[2:], false)
	case len(b) >= 2 && b[0] == 0xFE && b[1] == 0xFF:
		return decodeUTF16(b[2:], true)
	}
	return s
}

func decodeUTF16(b []byte, bigEndian bool) string {
	if len(b)%2 != 0 {
		b = b[:len(b)-1]
	}
	u := make([]uint16, len(b)/2)
	for i := range u {
		if bigEndian {
			u[i] = uint16(b[2*i])<<8 | uint16(b[2*i+1])
		} else {
			u[i] = uint16(b[2*i+1])<<8 | uint16(b[2*i])
		}
	}
	return string(utf16.Decode(u))
}

// firstIllegalChar returns the byte offset of the first character that may
// not appear literally, or -1 if all are legal. The set is version-specific
// (XML 1.1 forbids the restricted control characters literally), read from
// the generated lexical classes — no character ranges live in Go.
func firstIllegalChar(s string, is11 bool) int {
	char := xmlpb.Lexical["Char"]
	if is11 {
		char = xmlpb.Lexical["Char11Literal"]
	}
	for i := 0; i < len(s); {
		r, sz := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && sz == 1 { // invalid UTF-8 / surrogate byte
			return i
		}
		if !char.Contains(r) {
			return i
		}
		i += sz
	}
	return -1
}

// declPseudoAttr extracts a pseudo-attribute value (version / encoding /
// standalone) from the XML declaration, which, if present, is at the start.
func declPseudoAttr(s, name string) string {
	if !strings.HasPrefix(s, "<?xml") {
		return ""
	}
	end := strings.Index(s, "?>")
	if end < 0 {
		return ""
	}
	decl := s[len("<?xml"):end]
	i := strings.Index(decl, name)
	if i < 0 {
		return ""
	}
	rest := decl[i+len(name):]
	q := strings.IndexAny(rest, "\"'")
	if q < 0 {
		return ""
	}
	rest = rest[q+1:]
	if e := strings.IndexAny(rest, "\"'"); e >= 0 {
		return rest[:e]
	}
	return ""
}

// detectVersion reports whether the document declares version="1.1".
func detectVersion(s string) bool { return declPseudoAttr(s, "version") == "1.1" }

// decodeDeclaredEncoding transcodes the single-byte encodings xmile handles
// directly (ISO-8859-1 / Latin-1) into UTF-8, based on the declaration. The
// declaration itself is ASCII, so it is read before transcoding.
func decodeDeclaredEncoding(s string) string {
	switch strings.ToLower(declPseudoAttr(s, "encoding")) {
	case "iso-8859-1", "latin-1", "latin1":
		rs := make([]rune, len(s))
		for i := 0; i < len(s); i++ {
			rs[i] = rune(s[i])
		}
		return string(rs)
	}
	return s
}

// normalizeLineEnds applies XML line-end normalization: CR and CRLF become
// LF; XML 1.1 additionally normalizes NEL (#x85), CR-NEL, and LINE SEPARATOR
// (#x2028).
func normalizeLineEnds(s string, is11 bool) string {
	const nel = '\u0085'  // NEL
	const lsep = '\u2028' // LINE SEPARATOR
	if !strings.ContainsRune(s, '\r') &&
		!(is11 && (strings.ContainsRune(s, nel) || strings.ContainsRune(s, lsep))) {
		return s
	}
	rs := []rune(s)
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(rs); i++ {
		switch r := rs[i]; {
		case r == '\r':
			b.WriteByte('\n')
			if i+1 < len(rs) && (rs[i+1] == '\n' || (is11 && rs[i+1] == nel)) {
				i++
			}
		case is11 && (r == nel || r == lsep):
			b.WriteByte('\n')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
