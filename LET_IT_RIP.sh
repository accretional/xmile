#!/usr/bin/env bash
# LET_IT_RIP.sh — the full gate: set up, build, fetch corpus, test everything,
# then a live parse demo. Chains: LET_IT_RIP.sh -> test.sh -> build.sh ->
# setup.sh. A clean checkout needs nothing else installed.
set -euo pipefail
cd "$(dirname "$0")"
./test.sh

echo "[rip] live parse demo:"
printf '<?xml version="1.0"?><!DOCTYPE doc [<!ENTITY who "world">]><doc x="1">hello &who;<b/></doc>' \
  | go run ./cmd/xmlparse

echo "[rip] OK"
