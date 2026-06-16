#!/usr/bin/env bash
# setup.sh — install/verify the toolchain. Idempotent: skips what is present.
set -euo pipefail
cd "$(dirname "$0")"

export PATH="$PATH:$(go env GOPATH)/bin"

command -v go >/dev/null || { echo "[setup] FATAL: go not found"; exit 1; }

if ! command -v protoc >/dev/null; then
  echo "[setup] WARN: protoc not found — install Protocol Buffers compiler to regenerate proto"
fi
if ! command -v protoc-gen-go >/dev/null; then
  echo "[setup] installing protoc-gen-go"
  go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
fi
if ! command -v protoc-gen-go-grpc >/dev/null; then
  echo "[setup] installing protoc-gen-go-grpc"
  go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
fi

go mod download
echo "[setup] OK"
