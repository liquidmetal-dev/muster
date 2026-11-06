#!/usr/bin/env bash
# Behaviour tests for the e2e variant's stub run.sh. Usage: IMAGE=<ref> e2e-stub-test.sh
set -uo pipefail

IMAGE="${IMAGE:-ghcr.io/liquidmetal-dev/muster-runner:e2e-latest}"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

failures=0

# blob <sleep seconds> <exit code>: a JIT configuration in the form the fake GitHub issues.
blob() {
  jq -cn --argjson s "$1" --argjson c "$2" \
    '{runner_id: 1, runner_name: "muster-e2e-p-abcdef123456", sleep_seconds: $s, exit_code: $c}' |
    base64 -w0
}

# run <stdin> [muster-runner args...]; sets out, err, code, secs.
run() {
  local input="$1" start
  shift
  start=$SECONDS
  printf '%s' "$input" | docker run --rm -i --entrypoint /usr/local/bin/muster-runner "$IMAGE" "$@" \
    >"$work/out" 2>"$work/err"
  code=$?
  secs=$((SECONDS - start))
  out="$(cat "$work/out")"
  err="$(cat "$work/err")"
}

# expect <name> <status of the assertion just run>
expect() {
  if [ "$2" = 0 ]; then
    echo "PASS $1"
  else
    echo "FAIL $1: code=$code secs=$secs out=$out err=$err"
    failures=$((failures + 1))
  fi
}

sleeps_then_exits_with_code() {
  run "$(blob 2 7)"
  [ "$code" = 7 ] && [ "$secs" -ge 2 ] && [ "$secs" -lt 10 ]
  expect sleeps_then_exits_with_code $?
}

zero_exits_zero() {
  run "$(blob 0 0)"
  [ "$code" = 0 ]
  expect zero_exits_zero $?
}

bad_blob_exits_2() {
  run 'not-base64!'
  [ "$code" = 2 ] && [[ "$err" == *"run.sh: bad --jitconfig"* ]]
  expect bad_blob_exits_2 $?
}

# The whole check fails without dockerd; only the run_sh line matters here.
check_accepts_stub() {
  run '' --check
  [[ "$out" == *"ok run_sh"* ]] && [[ "$out" != *"fail run_sh"* ]]
  expect check_accepts_stub $?
}

sleeps_then_exits_with_code
zero_exits_zero
bad_blob_exits_2
check_accepts_stub

[ "$failures" = 0 ]
