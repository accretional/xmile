#!/usr/bin/env bash
# test.sh — build, fetch the corpus if missing, run unit + conformance tests,
# and print the corpus report. Chains: test.sh -> build.sh -> setup.sh.
set -euo pipefail
cd "$(dirname "$0")"
./build.sh

# Fetch the test corpus (W3C suite, OOXML, RSS) on first run so the
# conformance tests have something to run against.
if [ -z "$(ls -A testing/corpus/xml/not-wf 2>/dev/null || true)" ]; then
  echo "[test] fetching corpus"
  go run ./testing
fi

# Ensure the large real-world RSS 2.0 corpus for the rss-parse harness. Fetched
# once (thousands of live feeds) then cached; gitignored like the rest.
if [ -z "$(ls -A testing/corpus/rss2.0 2>/dev/null || true)" ]; then
  echo "[test] fetching rss 2.0 corpus"
  go run ./testing rss2.0
  # Split the fetched feeds into the valid set and a curated invalid/ set of
  # genuine spec violations (parser-defined, so done here, not at fetch time).
  go run ./testing/rss-parse -classify
fi

# Ensure the W3C XSD test-suite corpus for the xsd-parse harness (reported, not
# gating); fetched once then cached.
if [ -z "$(ls -A testing/corpus/xsd 2>/dev/null || true)" ]; then
  echo "[test] fetching xsd corpus"
  go run ./testing xsd
fi

echo "[test] go test ./..."
go test ./...

echo "[test] corpus report:"
go run ./testing/xml-parse

echo "[test] rss-parse (rss.ebnf -> proto -> parse rss 2.0 corpus):"
go run ./testing/rss-parse

echo "[test] opc-parse (OPC packages -> parts + relationships):"
go run ./testing/opc-parse

echo "[test] xsd-parse (W3C xsd suite -> CompileXSD; reported, not gating):"
go run ./testing/xsd-parse

echo "[test] OK"
