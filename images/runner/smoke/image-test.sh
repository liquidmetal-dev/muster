#!/usr/bin/env bash
# Static checks of a built runner image. Usage: IMAGE=<ref> image-test.sh
set -uo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
IMAGE="${IMAGE:-ghcr.io/liquidmetal-dev/muster-runner:minimal-latest}"
RUNNER_VERSION="${RUNNER_VERSION:-$(sed -n 's/^ARG RUNNER_VERSION=//p' "$here/../minimal/Dockerfile")}"

failures=0

pass() { echo "PASS $1"; }
fail() { echo "FAIL $1: $2"; failures=$((failures + 1)); }

in_image() { docker run --rm --entrypoint "$1" "$IMAGE" "${@:2}" 2>&1; }

check_eq() {
  local name="$1" want="$2" got="$3"
  if [ "$got" = "$want" ]; then pass "$name"; else fail "$name" "want '$want', got '$got'"; fi
}

check_eq layers 1 "$(docker image inspect -f '{{len .RootFS.Layers}}' "$IMAGE" 2>&1)"

ids="$(in_image id runner)"
if [[ "$ids" == *"uid=1000(runner)"* && "$ids" == *"gid=1000(runner)"* && "$ids" == *"(docker)"* ]]; then
  pass runner_uid
else
  fail runner_uid "$ids"
fi

in_image getent passwd ubuntu >/dev/null
check_eq no_ubuntu_user 2 "$?"

check_eq sudoers_mode 440 "$(in_image stat -c %a /etc/sudoers.d/runner)"
if out="$(in_image visudo -cf /etc/sudoers.d/runner)"; then pass sudoers_valid; else fail sudoers_valid "$out"; fi

check_eq runner_home "runner runner" "$(in_image stat -c %U /opt/actions-runner /opt/actions-runner/run.sh | tr '\n' ' ' | sed 's/ $//')"
if out="$(in_image test -x /opt/actions-runner/run.sh)"; then pass run_sh_exec; else fail run_sh_exec "${out:-not executable}"; fi

check_eq runner_version "$RUNNER_VERSION" "$(in_image /opt/actions-runner/bin/Runner.Listener --version)"

check_eq machine_id_empty 0 "$(in_image stat -c %s /etc/machine-id)"

# A container, even a stopped one, shows Docker's own /etc/resolv.conf, so read the image layer.
work="$(mktemp -d)"
docker save "$IMAGE" -o "$work/image.tar"
tar -xf "$work/image.tar" -C "$work" manifest.json
layer="$(jq -r '.[0].Layers[0]' "$work/manifest.json")"
tar -xf "$work/image.tar" -C "$work" "$layer"
check_eq resolv_link ../run/systemd/resolve/stub-resolv.conf \
  "$(tar -tvf "$work/$layer" etc/resolv.conf 2>&1 | sed -n 's/.* -> //p')"
rm -rf "$work"

check_eq enabled_units "enabled enabled enabled enabled enabled" \
  "$(in_image systemctl is-enabled guest-agent docker containerd systemd-networkd systemd-resolved | tr '\n' ' ' | sed 's/ $//')"

# shellcheck disable=SC2016 # $c expands inside the image, not here
if out="$(in_image bash -c 'for c in git curl jq tar zstd make gcc docker containerd guest-agent; do command -v "$c" >/dev/null || echo "missing $c"; done')" && [ -z "$out" ]; then
  pass packages
else
  fail packages "$out"
fi

net="$(in_image cat /etc/systemd/network/10-dhcp.network)"
if [[ "$net" == *"Name=en* eth*"* && "$net" == *"DHCP=yes"* && "$net" == *"ClientIdentifier=mac"* ]]; then
  pass network_file
else
  fail network_file "$net"
fi

if [ "$failures" -gt 0 ]; then
  echo "$failures check(s) failed"
  exit 1
fi
