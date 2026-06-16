#!/usr/bin/env bash
# build.sh — set up dependencies and build, ready to test. The generated
# proto/pb files are committed; regenerate them with `go run ./lang/cmd/genproto`
# + protoc only when the grammar or protos change. Chains: build.sh -> setup.sh.
set -euo pipefail
cd "$(dirname "$0")"
./setup.sh

echo "[build] go build ./..."
go build ./...
go mod tidy >/dev/null 2>&1 || true
echo "[build] OK"
