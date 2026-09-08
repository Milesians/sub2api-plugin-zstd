#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "$0")" && pwd)
OUT=${1:-"$ROOT/dist"}
GO_BIN=${GO_BIN:-go}
VERSION=${PLUGIN_VERSION:-0.2.0}

command -v "$GO_BIN" >/dev/null || { echo "Go 1.25+ is required" >&2; exit 1; }
command -v python3 >/dev/null || { echo "python3 is required" >&2; exit 1; }
stage=$(mktemp -d "${TMPDIR:-/tmp}/sub2api-plugin-zstd.XXXXXX")
trap 'rm -rf "$stage"' EXIT
mkdir -p "$stage/ui" "$stage/runtimes/linux-amd64" "$stage/runtimes/linux-arm64" "$stage/runtimes/windows-amd64"
cp "$ROOT/ui/index.html" "$stage/ui/index.html"

build_runtime() {
  local goos=$1 goarch=$2 destination=$3
  CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" "$GO_BIN" build -trimpath -buildvcs=false -ldflags="-s -w -X main.pluginVersion=$VERSION" -o "$stage/$destination" ./cmd/sub2api-plugin-zstd
}
build_runtime linux amd64 runtimes/linux-amd64/sub2api-plugin-zstd
build_runtime linux arm64 runtimes/linux-arm64/sub2api-plugin-zstd
build_runtime windows amd64 runtimes/windows-amd64/sub2api-plugin-zstd.exe

mkdir -p "$OUT"
rm -f "$OUT/sub2api-plugin-zstd.s2plugin"
python3 "$ROOT/tools/package.py" --root "$stage" --source "$ROOT/manifest.source.json" --out "$OUT/sub2api-plugin-zstd.s2plugin" --version "$VERSION"
echo "built $OUT/sub2api-plugin-zstd.s2plugin (version $VERSION)"
