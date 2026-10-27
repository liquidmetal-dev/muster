#!/usr/bin/env bash
# Generate Go code from the protos under api/proto with buf.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
if [ ! -f buf.gen.yaml ]; then
  echo "generate-proto: no buf.gen.yaml yet, nothing to generate" >&2
  exit 0
fi
buf generate
