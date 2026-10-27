#!/usr/bin/env bash
# Update the buf dependencies that buf.yaml lists.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
if [ ! -f buf.yaml ]; then
  echo "update-proto-deps: no buf.yaml yet, nothing to update" >&2
  exit 0
fi
buf dep update
