#!/usr/bin/env bash
# Test double for /opt/actions-runner/run.sh: reports how it was called.
echo "user=$(id -un)"
echo "args=$*"
exit "${STUB_EXIT:-0}"
