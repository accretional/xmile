# xml.lex — XML 1.0 lexical token matchers. Compiled by genproto into
# proto/pb/xml/lexical.go. The runtime parser carries none of this; it
# reads the generated table through the generic engine in package lex.
#
# Format: <name> <kind> <args>
#   run    <range>...               maximal run of 1+ chars in the ranges
#   name   <range>... / <range>...  NameStartChar class / extra NameChar class
#   except <hex>...                 run of 1+ chars, none being a listed byte
#   until  <delim>                  chars up to (not including) the delimiter
#   balanced                        XML DOCTYPE internal-subset scan
# Values are hex codepoints; "lo-hi" is an inclusive range.

S            run     9 A D 20

# Char is the legal XML 1.0 character set, used for the illegal-character
# pre-check (not referenced by the grammar).
Char         run     9 A D 20-D7FF E000-FFFD 10000-10FFFF

CharData     except  3C 26
dqText       except  22 3C 26
sqText       except  27 3C 26

comment_text until   --
cdata_text   until   ]]>
pi_text      until   ?>
xmldecl_text until   ?>

dtd_text     balanced

Name         name    3A 5F 41-5A 61-7A C0-D6 D8-F6 F8-2FF 370-37D 37F-1FFF 200C-200D 2070-218F 2C00-2FEF 3001-D7FF F900-FDCF FDF0-FFFD 10000-EFFFF / 2D 2E 30-39 B7 300-36F 203F-2040
