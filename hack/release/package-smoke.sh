#!/usr/bin/env bash
# Installs the snapshot packages from a goreleaser dist directory in a
# container and checks the layout. Usage: package-smoke.sh <image> <dist-dir>
set -euo pipefail

image="${1:?usage: package-smoke.sh <image> <dist-dir>}"
dist="$(cd "${2:?usage: package-smoke.sh <image> <dist-dir>}" && pwd)"
repo="$(cd "$(dirname "$0")/../.." && pwd)"

version="$(grep -o '"version":"[^"]*"' "$dist/metadata.json" | cut -d'"' -f4)"
[ -n "$version" ] || { echo "package-smoke: no version in $dist/metadata.json" >&2; exit 1; }

docker run --rm -v "$dist:/dist:ro" -v "$repo:/src:ro" "$image" \
	bash /src/hack/release/package-smoke-inner.sh "$version"
