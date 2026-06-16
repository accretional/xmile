#!/usr/bin/env bash
# test.sh — build, then run unit tests and the W3C conformance report.
# Chains: test.sh -> build.sh -> setup.sh.
set -euo pipefail
cd "$(dirname "$0")"
./build.sh

echo "[test] go test ./..."
go test ./...

echo "[test] W3C XML conformance (edition-5 applicable subset):"
go run ./cmd/conformance

echo "[test] OK"
