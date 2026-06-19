#!/usr/bin/env bash
# LET_IT_RIP.sh — the full gate: set up, build, fetch corpus, test everything.
# Chains: LET_IT_RIP.sh -> test.sh -> build.sh -> setup.sh. A clean checkout
# needs nothing else installed.
set -euo pipefail
cd "$(dirname "$0")"
./test.sh

echo "[rip] OK"
