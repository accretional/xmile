package service

import (
	"unicode/utf16"

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

// firstIllegalChar returns the byte offset of the first character outside
// the legal XML Char set, or -1 if all are legal. The character set is the
// generated "Char" lexical class — no character ranges live in Go.
func firstIllegalChar(s string) int {
	char := xmlpb.Lexical["Char"]
	for i, r := range s {
		if !char.Contains(r) {
			return i
		}
	}
	return -1
}
