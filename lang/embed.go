// Package lang holds the embedded grammar sources. The runtime parser
// loads these via gluon; the lexical layer is generated separately into
// proto/pb (see lang/cmd/genproto).
package lang

import _ "embed"

//go:embed xml.ebnf
var XMLGrammar string

//go:embed dtd.ebnf
var DTDGrammar string

//go:embed rss.ebnf
var RSSGrammar string
