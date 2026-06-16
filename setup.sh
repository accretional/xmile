#!/usr/bin/env bash
# setup.sh — make a clean checkout buildable with no manual steps: clone the
# local-module dependencies as sibling repos and install the toolchain.
# Idempotent: anything already present is skipped.
set -euo pipefail
cd "$(dirname "$0")"

export PATH="$PATH:$(go env GOPATH 2>/dev/null || echo "$HOME/go")/bin"

# 1. Go toolchain (required; cannot be auto-installed portably).
command -v go >/dev/null || { echo "[setup] FATAL: install Go (https://go.dev/dl/) and re-run"; exit 1; }

# 2. Sibling dependency repos. go.mod pins them via `replace => ../<dep>`,
#    so a clean clone needs them checked out next to this repo.
for dep in gluon proto-merge; do
  if [ ! -d "../$dep/.git" ]; then
    echo "[setup] cloning $dep -> ../$dep"
    git clone --depth 1 "https://github.com/accretional/$dep" "../$dep"
  else
    echo "[setup] updating $dep -> latest"
    git -C "../$dep" fetch --quiet origin || true
    git -C "../$dep" pull --ff-only --quiet 2>/dev/null \
      || echo "[setup] WARN: $dep not fast-forwarded (diverged or local changes) — using current state"
  fi
done

# 3. protoc (Protocol Buffers compiler).
if ! command -v protoc >/dev/null; then
  echo "[setup] installing protoc"
  if   command -v brew    >/dev/null; then brew install protobuf
  elif command -v apt-get >/dev/null; then sudo apt-get update -qq && sudo apt-get install -y -qq protobuf-compiler
  elif command -v dnf     >/dev/null; then sudo dnf install -y protobuf-compiler
  else echo "[setup] WARN: install Protocol Buffers compiler (protoc) manually"; fi
fi

# 4. protoc Go plugins.
command -v protoc-gen-go      >/dev/null || { echo "[setup] installing protoc-gen-go";      go install google.golang.org/protobuf/cmd/protoc-gen-go@latest; }
command -v protoc-gen-go-grpc >/dev/null || { echo "[setup] installing protoc-gen-go-grpc"; go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest; }

go mod download
echo "[setup] OK"
