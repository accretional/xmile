#!/usr/bin/env bash
# build.sh — regenerate proto artifacts from the grammar and build.
# Chains: build.sh -> setup.sh.
set -euo pipefail
cd "$(dirname "$0")"
./setup.sh

export PATH="$PATH:$(go env GOPATH)/bin"

echo "[build] genproto: dtd.proto + keyword/lexical tables"
go run ./lang/cmd/genproto

if command -v protoc >/dev/null; then
  echo "[build] protoc -> proto/pb"
  protoc -Iproto \
    --go_out=proto/pb --go_opt=module=github.com/accretional/xmile/proto/pb \
    --go-grpc_out=proto/pb --go-grpc_opt=module=github.com/accretional/xmile/proto/pb \
    dtd.proto xml.proto xml_service.proto
else
  echo "[build] WARN: protoc missing — using committed proto/pb/*.pb.go"
fi

echo "[build] go build ./..."
go build ./...
go mod tidy >/dev/null 2>&1 || true
echo "[build] OK"
