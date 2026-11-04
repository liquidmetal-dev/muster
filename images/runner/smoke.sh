#!/usr/bin/env bash
# Boot a runner image under systemd and run smoke/checks.sh in it.
# Usage: [IMAGE=<ref>] smoke.sh [container|vm]
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
IMAGE="${IMAGE:-ghcr.io/liquidmetal-dev/muster-runner:minimal-latest}"
mode="${1:-container}"

smoke_container() {
  # Nothing serves DHCP on a docker network, so wait-online would hold docker.service for 2 minutes.
  ctr="$(docker run -d --privileged --cgroupns=private \
    --tmpfs /run --tmpfs /run/lock \
    --mount type=volume,dst=/var/lib/docker \
    -v /dev/null:/etc/systemd/system/systemd-networkd-wait-online.service:ro \
    -v "$here/smoke:/smoke:ro" \
    "$IMAGE" /sbin/init)"
  trap 'docker rm -fv "$ctr" >/dev/null' EXIT
  docker exec -e SMOKE_MODE=container "$ctr" bash /smoke/checks.sh
}

case "$mode" in
  container) smoke_container ;;
  *) echo "usage: smoke.sh [container|vm]" >&2; exit 2 ;;
esac
