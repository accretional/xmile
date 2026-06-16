#!/usr/bin/env bash
# serve.sh — run the XmlService gRPC server. Chains: serve.sh -> setup.sh.
# Pass-through flags, e.g. ./serve.sh -addr :60000
set -euo pipefail
cd "$(dirname "$0")"
./setup.sh
echo "[serve] starting xmile XmlService"
exec go run ./cmd/xmlserve "$@"
