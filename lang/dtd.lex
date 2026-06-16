# dtd.lex — lexical token matchers for lang/dtd.ebnf. Compiled by genproto
# into proto/pb/dtd/lexical.go. Same format as xml.lex.

S            run     9 A D 20

Name         name    3A 5F 41-5A 61-7A C0-D6 D8-F6 F8-2FF 370-37D 37F-1FFF 200C-200D 2070-218F 2C00-2FEF 3001-D7FF F900-FDCF FDF0-FFFD 10000-EFFFF / 2D 2E 30-39 B7 300-36F 203F-2040

Nmtoken      run     2D 2E 30-39 3A 41-5A 5F 61-7A B7 C0-D6 D8-F6 F8-2FF 300-36F 370-37D 37F-1FFF 200C-200D 203F-2040 2070-218F 2C00-2FEF 3001-D7FF F900-FDCF FDF0-FFFD 10000-EFFFF

# Literals run up to (and may be empty before) their closing quote.
litDq        until   "
litSq        until   '

comment_text until   --
pi_text      until   ?>
