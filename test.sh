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

echo "[test] go test ./..."
go test ./...

echo "[test] corpus report:"
go run ./testing/xml-parse

echo "[test] OK"
