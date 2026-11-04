#!/usr/bin/env bash
# Build a runner image variant locally. Usage: [ARCH=amd64] [RUNNER_VERSION=x.y.z] build.sh [variant]
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=images/runner/lib.sh
. "$here/lib.sh"
variant="${1:-minimal}"
dir="$here/$variant"
[ -f "$dir/Dockerfile" ] || { echo "build.sh: no Dockerfile for variant $variant" >&2; exit 2; }

arch="${ARCH:-amd64}"
runner_version="${RUNNER_VERSION:-$(dockerfile_runner_version "$dir/Dockerfile")}"
repo=ghcr.io/liquidmetal-dev/muster-runner
tag="$repo:$variant-24.04-$runner_version"

docker buildx build --platform "linux/$arch" --load \
  --build-arg "RUNNER_VERSION=$runner_version" \
  -t "$tag" -t "$repo:$variant-latest" "$dir" >&2

echo "$tag"
