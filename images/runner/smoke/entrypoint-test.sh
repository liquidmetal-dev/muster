#!/usr/bin/env bash
# Behaviour tests for muster-runner with a stub run.sh. Usage: IMAGE=<ref> entrypoint-test.sh
set -uo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=images/runner/lib.sh
. "$here/../lib.sh"
IMAGE="${IMAGE:-$default_image}"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

failures=0

# run <stdin> [docker run flags...] [-- muster-runner args...]; sets out, err, code.
run() {
  local input="$1"; shift
  local flags=() args=()
  while [ $# -gt 0 ] && [ "$1" != -- ]; do flags+=("$1"); shift; done
  [ $# -gt 0 ] && shift
  args=("$@")
  printf '%b' "$input" | docker run --rm -i \
    -v "$here/stub-run.sh:/opt/actions-runner/run.sh:ro" \
    "${flags[@]}" --entrypoint /usr/local/bin/muster-runner "$IMAGE" "${args[@]}" \
    >"$work/out" 2>"$work/err"
  code=$?
  out="$(cat "$work/out")"
  err="$(cat "$work/err")"
}

# expect <name> <status of the assertion just run>
expect() {
  if [ "$2" = 0 ]; then
    echo "PASS $1"
  else
    echo "FAIL $1: code=$code out=$out err=$err"
    failures=$((failures + 1))
  fi
}

jit_passed() {
  run 'abc123=='
  [ "$code" = 0 ] && [[ "$out" == *"args=--jitconfig abc123=="* ]]
  expect jit_passed $?
}

jit_trailing_newline_stripped() {
  run 'abc123==\n'
  [[ "$out" == *"args=--jitconfig abc123==" ]]
  expect jit_trailing_newline_stripped $?
}

exit_code_propagates() {
  run 'abc' -e STUB_EXIT=7
  [ "$code" = 7 ]
  expect exit_code_propagates $?
}

root_drops_to_runner() {
  run 'abc'
  [[ "$out" == *"user=runner"* ]]
  expect root_drops_to_runner $?
}

runner_runs_directly() {
  run 'abc' --user 1000:1000
  [ "$code" = 0 ] && [[ "$out" == *"user=runner"* ]]
  expect runner_runs_directly $?
}

empty_stdin_exits_2() {
  run ''
  [ "$code" = 2 ] && [ "$err" = "muster-runner: no JIT configuration on stdin" ] && [[ "$out" != *"args="* ]]
  expect empty_stdin_exits_2 $?
}

bad_arg_exits_2() {
  run '' -- --nope
  [ "$code" = 2 ] && [[ "$err" == "usage: muster-runner"* ]]
  expect bad_arg_exits_2 $?
}

extra_arg_exits_2() {
  run '' -- --check extra
  [ "$code" = 2 ] && [[ "$err" == "usage: muster-runner"* ]]
  expect extra_arg_exits_2 $?
}

empty_arg_exits_2() {
  run 'abc' -- ''
  [ "$code" = 2 ] && [[ "$err" == "usage: muster-runner"* ]] && [[ "$out" != *"args="* ]]
  expect empty_arg_exits_2 $?
}

check_fails_without_dockerd() {
  run '' -- --check
  [ "$code" = 1 ] && [[ "$out" == *"fail docker"* ]] && [[ "$out" == *"ok user"* ]]
  expect check_fails_without_dockerd $?
}

check_fails_on_non_exec_run_sh() {
  out="$(docker run --rm --entrypoint bash "$IMAGE" -c 'chmod -x /opt/actions-runner/run.sh; /usr/local/bin/muster-runner --check' 2>&1)"
  code=$?
  err=""
  [ "$code" = 1 ] && [[ "$out" == *"fail run_sh"* ]]
  expect check_fails_on_non_exec_run_sh $?
}

jit_passed
jit_trailing_newline_stripped
exit_code_propagates
root_drops_to_runner
runner_runs_directly
empty_stdin_exits_2
bad_arg_exits_2
extra_arg_exits_2
empty_arg_exits_2
check_fails_without_dockerd
check_fails_on_non_exec_run_sh

if [ "$failures" -gt 0 ]; then
  echo "$failures test(s) failed"
  exit 1
fi
