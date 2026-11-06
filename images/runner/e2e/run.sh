#!/usr/bin/env bash
# Stand-in for the actions runner. Decodes the fake GitHub's JIT configuration, sleeps for
# its sleep_seconds and exits with its exit_code.
set -euo pipefail

bad() { echo "run.sh: bad --jitconfig${1:+: $1}" >&2; exit 2; }

[ $# = 2 ] && [ "$1" = --jitconfig ] || bad "usage: run.sh --jitconfig <config>"
config="$(base64 -d <<<"$2" 2>/dev/null)" || bad "not base64"
read -r secs code < <(jq -er '"\(.sleep_seconds) \(.exit_code)"' <<<"$config" 2>/dev/null) ||
  bad "not a JSON object with sleep_seconds and exit_code"
[[ "$code" =~ ^[0-9]+$ ]] || bad "exit_code $code"

echo "run.sh: runner $(jq -r .runner_name <<<"$config") sleeps ${secs}s, then exits $code"
sleep "$secs"
exit "$code"
