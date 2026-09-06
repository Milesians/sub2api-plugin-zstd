#!/usr/bin/env bash
set -euo pipefail
ROOT=$(cd "$(dirname "$0")" && pwd)
OUT=${1:-dist}
mkdir -p "$OUT"
if ! command -v go >/dev/null; then echo 'Go 1.27+ required' >&2; exit 1; fi
go build -trimpath -ldflags='-s -w' -o "$OUT/sub2api-plugin-zstd" ./cmd/sub2api-plugin-zstd
cp manifest.source.json "$OUT/manifest.json"
mkdir -p "$OUT/ui"; cp ui/index.html "$OUT/ui/"
tar -czf "$OUT/sub2api-plugin-zstd.s2plugin" -C "$OUT" sub2api-plugin-zstd manifest.json ui
