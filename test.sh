#!/usr/bin/env bash
# test.sh — build, then run the self-contained tests and the full corpus checks.
# The corpus checks live in one runner (go run ./testing), which fetches the
# corpus on first run and then parses every format's docs through the service.
# Chains: test.sh -> build.sh -> setup.sh.
set -euo pipefail
cd "$(dirname "$0")"
./build.sh

echo "[test] go test ./..."
go test ./...

echo "[test] corpus checks (fetches the corpus on first run; gates on xml + opc):"
go run ./testing

echo "[test] OK"
