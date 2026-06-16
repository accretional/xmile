#!/usr/bin/env bash
# serve.sh — set up, build, then serve the XmlService gRPC server.
# Chains: serve.sh -> build.sh -> setup.sh. Pass-through flags, e.g.
#   ./serve.sh -addr :60000
set -euo pipefail
cd "$(dirname "$0")"
./build.sh
echo "[serve] starting xmile XmlService"
exec go run ./cmd/xmlserve "$@"
