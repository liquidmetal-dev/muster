#!/usr/bin/env bash
# Remove everything hack/e2e/provision.sh and the e2e suite create. Run as root. Safe to run
# when nothing is there. It touches only muster-e2e paths, pids and devices. It exits 1, and
# keeps the thin-pool's backing files, when the thin-pool stays busy.
set -uo pipefail

lib=/var/lib/muster-e2e
run=/run/muster-e2e

status=0

for pidfile in "$run"/*.pid; do
  [ -f "$pidfile" ] || continue
  pid="$(cat "$pidfile")"
  rm -f "$pidfile"
  # A stale pidfile can name a process that reused the pid.
  case "$(readlink "/proc/$pid/exe" 2>/dev/null)" in
    /opt/muster-e2e/bin/*) ;;
    *) continue ;;
  esac
  kill "$pid" 2>/dev/null
  for _ in $(seq 20); do kill -0 "$pid" 2>/dev/null || break; sleep 0.5; done
  kill -9 "$pid" 2>/dev/null
done
# flintlock detaches its firecracker processes, so they have no pidfile. Only the suite runs
# this firecracker binary.
pkill -9 -f "^/opt/muster-e2e/bin/firecracker" 2>/dev/null

# Snapshots that are still mounted or active keep the thin-pool busy.
awk -v lib="$lib/" -v run="$run/" 'index($2, lib) == 1 || index($2, run) == 1 {print $2}' /proc/mounts |
  sort -r | while read -r mnt; do umount -l "$mnt"; done
dmsetup ls 2>/dev/null | awk '$1 ~ /^muster-e2e-thinpool-snap-/ {print $1}' | while read -r dev; do
  dmsetup remove "$dev" || echo "cleanup.sh: dmsetup remove $dev failed" >&2
done
if dmsetup info muster-e2e-thinpool >/dev/null 2>&1 && ! dmsetup remove muster-e2e-thinpool; then
  echo "cleanup.sh: muster-e2e-thinpool is still busy; its backing files in $lib stay" >&2
  status=1
fi
if [ "$status" = 0 ]; then
  losetup -nO NAME,BACK-FILE 2>/dev/null | while read -r dev file; do
    case "$file" in "$lib"/*.img*) losetup -d "$dev" ;; esac
  done
fi

ip link show muster-e2e-br0 >/dev/null 2>&1 && ip link delete muster-e2e-br0
docker rm -f muster-e2e-registry >/dev/null 2>&1

if [ -f /etc/udev/rules.d/99-muster-e2e-kvm.rules ]; then
  rm -f /etc/udev/rules.d/99-muster-e2e-kvm.rules
  # Only an add event makes udev apply the default mode again.
  udevadm control --reload-rules && udevadm trigger --action=add --name-match=kvm && udevadm settle
fi

if [ "$status" = 0 ]; then
  rm -rf "$lib" "$run" /etc/muster-e2e
fi
exit "$status"
