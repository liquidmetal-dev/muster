#!/usr/bin/env bash
# Remove everything hack/e2e/provision.sh and the e2e suite create. Run as root. Safe to run
# when nothing is there. It touches only muster-e2e paths, pids and devices.
set -uo pipefail

lib=/var/lib/muster-e2e
run=/run/muster-e2e

for pidfile in "$run"/*.pid; do
  [ -f "$pidfile" ] || continue
  pid="$(cat "$pidfile")"
  kill "$pid" 2>/dev/null
  for _ in $(seq 20); do kill -0 "$pid" 2>/dev/null || break; sleep 0.5; done
  kill -9 "$pid" 2>/dev/null
done
# flintlock detaches its firecracker processes, so they have no pidfile. Only the suite runs
# this firecracker binary.
pkill -9 -f "^/opt/muster-e2e/bin/firecracker" 2>/dev/null

if dmsetup info muster-e2e-thinpool >/dev/null 2>&1; then
  dmsetup remove muster-e2e-thinpool || echo "cleanup.sh: dmsetup remove muster-e2e-thinpool failed" >&2
fi
losetup -nO NAME,BACK-FILE 2>/dev/null | while read -r dev file; do
  case "$file" in "$lib"/*.img*) losetup -d "$dev" ;; esac
done

ip link show muster-e2e-br0 >/dev/null 2>&1 && ip link delete muster-e2e-br0
docker rm -f muster-e2e-registry >/dev/null 2>&1

rm -rf "$lib" "$run" /etc/muster-e2e
exit 0
