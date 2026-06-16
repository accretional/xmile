#!/usr/bin/env bash
# regen.sh — regenerate the committed proto/lexical artifacts from the grammar.
# Run this after editing lang/*.ebnf, lang/*.lex, or the hand-written protos
# (proto/xml.proto, proto/xml_service.proto); build.sh consumes the output and
# does not regenerate. Chains: regen.sh -> setup.sh.
set -euo pipefail
cd "$(dirname "$0")"
./setup.sh

export PATH="$PATH:$(go env GOPATH)/bin"

echo "[regen] genproto: dtd.proto + lexical/prefix/separator tables"
go run ./lang/cmd/genproto

command -v protoc >/dev/null || { echo "[regen] FATAL: protoc missing (setup should have installed it)"; exit 1; }

echo "[regen] protoc -> proto/pb"
protoc -Iproto \
  --go_out=proto/pb --go_opt=module=github.com/accretional/xmile/proto/pb \
  --go-grpc_out=proto/pb --go-grpc_opt=module=github.com/accretional/xmile/proto/pb \
  dtd.proto xml.proto xml_service.proto

go mod tidy >/dev/null 2>&1 || true
echo "[regen] OK — commit the regenerated proto/pb/** and proto/dtd.proto"
