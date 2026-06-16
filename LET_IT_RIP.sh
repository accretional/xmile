#!/usr/bin/env bash
# LET_IT_RIP.sh — the full gate: set up, build, fetch corpus, test everything,
# then a live parse demo. Chains: LET_IT_RIP.sh -> test.sh -> build.sh ->
# setup.sh. A clean checkout needs nothing else installed.
set -euo pipefail
cd "$(dirname "$0")"
./test.sh

# A document with a full internal DTD: it must parse, validate against its
# content models and attribute declarations, and expand the entity — all of it.
DEMO='<?xml version="1.0"?><!DOCTYPE doc [<!ELEMENT doc (#PCDATA|b)*><!ATTLIST doc x CDATA #IMPLIED><!ELEMENT b EMPTY><!ENTITY who "world">]><doc x="1">hello &who;<b/></doc>'
echo "[rip] live parse demo:"
echo "$DEMO"
echo "[rip] parsed (validating mode -> well-formed + DTD-valid):"
printf '%s' "$DEMO" | go run ./cmd/xmlparse -validate

echo "[rip] OK"
