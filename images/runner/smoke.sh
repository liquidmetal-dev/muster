#!/usr/bin/env bash
# Boot a runner image under systemd and run smoke/checks.sh in it.
# Usage: [IMAGE=<ref>] smoke.sh [container|vm]
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=images/runner/lib.sh
. "$here/lib.sh"
IMAGE="${IMAGE:-$default_image}"
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

firecracker_version=v1.17.0
kernel_image=ghcr.io/liquidmetal-dev/firecracker-kernel:6.1-20260929-1349
tap=mstap0
subnet=172.16.0.0/24
host_ip=172.16.0.1
dhcp_range=172.16.0.10,172.16.0.50
guest_mac=06:00:AC:10:00:02
guest_agent_port=1024
vm_rules=(
  "INPUT -i $tap -j ACCEPT"
  "FORWARD -i $tap -j ACCEPT"
  "FORWARD -o $tap -j ACCEPT"
  "POSTROUTING -t nat -s $subnet ! -o $tap -j MASQUERADE"
)

smoke_vm() {
  local missing=()
  [ -r /dev/kvm ] && [ -w /dev/kvm ] || missing+=(/dev/kvm)
  for tool in sudo docker mkfs.ext4 iptables ip sysctl curl jq sha256sum python3; do
    command -v "$tool" >/dev/null || missing+=("$tool")
  done
  if [ "${#missing[@]}" -gt 0 ]; then
    echo "smoke.sh vm: missing ${missing[*]}" >&2
    exit 2
  fi

  work="$(mktemp -d)"
  dnsmasq_ctr=""
  forward_before=""
  tap_created=""
  added_rules=()
  trap cleanup_vm EXIT

  if ip link show "$tap" >/dev/null 2>&1; then
    echo "smoke.sh vm: $tap already exists; remove it or wait for the other run to end" >&2
    exit 2
  fi

  local cache="${XDG_CACHE_HOME:-$HOME/.cache}/muster-smoke"
  local fc_tgz="firecracker-$firecracker_version-x86_64.tgz"
  local fc="$cache/release-$firecracker_version-x86_64/firecracker-$firecracker_version-x86_64"
  if [ ! -x "$fc" ]; then
    mkdir -p "$cache"
    local base="https://github.com/firecracker-microvm/firecracker/releases/download/$firecracker_version"
    curl -fsSLo "$cache/$fc_tgz" "$base/$fc_tgz"
    curl -fsSLo "$cache/$fc_tgz.sha256.txt" "$base/$fc_tgz.sha256.txt"
    (cd "$cache" && sha256sum -c "$fc_tgz.sha256.txt")
    tar -xzf "$cache/$fc_tgz" -C "$cache"
  fi

  local kernel_ctr
  kernel_ctr="$(docker create "$kernel_image" true)"
  docker cp -q "$kernel_ctr:/boot/vmlinux" "$work/vmlinux"
  docker rm "$kernel_ctr" >/dev/null

  # The VM boots the image's own layer: a container export would carry Docker's resolv.conf,
  # hostname and hosts files instead of the image's.
  local layer
  layer="$(image_layer "$IMAGE" "$work")" || exit 2
  mkdir "$work/rootfs"
  sudo tar -xpf "$layer" -C "$work/rootfs"
  rm "$layer"
  sudo install -D -m 0755 "$here/smoke/checks.sh" "$work/rootfs/usr/local/lib/muster-smoke/checks.sh"
  sudo install -m 0644 "$here/smoke/smoke.service" "$work/rootfs/etc/systemd/system/smoke.service"
  sudo ln -s ../smoke.service "$work/rootfs/etc/systemd/system/multi-user.target.wants/smoke.service"
  sudo mkfs.ext4 -q -d "$work/rootfs" -L root "$work/rootfs.ext4" 4G
  sudo chown "$(id -u):$(id -g)" "$work/rootfs.ext4"
  sudo rm -rf "$work/rootfs"

  sudo ip tuntap add "$tap" mode tap user "$(id -un)"
  tap_created=1
  sudo ip addr add "$host_ip/${subnet#*/}" dev "$tap"
  sudo ip link set "$tap" up
  forward_before="$(sysctl -n net.ipv4.ip_forward)"
  sudo sysctl -q -w net.ipv4.ip_forward=1
  for rule in "${vm_rules[@]}"; do
    # shellcheck disable=SC2086 # each rule is a list of iptables arguments
    sudo iptables -I $rule
    added_rules+=("$rule")
  done

  docker build -q -t muster-smoke-dnsmasq -f "$here/smoke/dnsmasq.Dockerfile" "$here/smoke" >/dev/null
  dnsmasq_ctr="$(docker run -d --network host --cap-add NET_ADMIN muster-smoke-dnsmasq \
    --no-daemon --port=0 --interface="$tap" --bind-interfaces \
    --dhcp-range="$dhcp_range,12h" --dhcp-option=option:dns-server,1.1.1.1)"

  cat >"$work/vm.json" <<JSON
{
  "boot-source": {
    "kernel_image_path": "$work/vmlinux",
    "boot_args": "console=ttyS0 reboot=k panic=1 pci=off i8042.noaux i8042.nomux i8042.nopnp i8042.dumbkbd root=/dev/vda rw systemd.unified_cgroup_hierarchy=1"
  },
  "drives": [
    {"drive_id": "rootfs", "path_on_host": "$work/rootfs.ext4", "is_root_device": true, "is_read_only": false}
  ],
  "network-interfaces": [
    {"iface_id": "eth0", "guest_mac": "$guest_mac", "host_dev_name": "$tap"}
  ],
  "vsock": {"guest_cid": 3, "uds_path": "$work/vsock.sock"},
  "machine-config": {"vcpu_count": 2, "mem_size_mib": 2048}
}
JSON

  local fc_status=0 fc_pid vsock=fail
  timeout 900 "$fc" --no-api --config-file "$work/vm.json" >"$work/console.log" 2>&1 &
  fc_pid=$!
  # The guest reboots once its checks end, so probe guest-agent while the VM runs.
  while kill -0 "$fc_pid" 2>/dev/null; do
    if python3 -I "$here/smoke/vsock-probe.py" "$work/vsock.sock" "$guest_agent_port"; then
      vsock=pass
      break
    fi
    sleep 0.5
  done
  wait "$fc_pid" || fc_status=$?
  if [ "$fc_status" != 0 ]; then
    tail -50 "$work/console.log"
    echo "smoke.sh vm: firecracker exited with status $fc_status (124 is the 900 s timeout)" >&2
    exit 1
  fi
  # The console prefixes each line with the kernel time and the unit's process name.
  grep -ao 'SMOKE: .*' "$work/console.log" || { tail -50 "$work/console.log"; exit 1; }
  if [ "$vsock" = pass ]; then
    echo "SMOKE: PASS guest_agent_vsock"
  else
    echo "SMOKE: FAIL guest_agent_vsock: no answer on vsock port $guest_agent_port"
    exit 1
  fi
  grep -aEq 'SMOKE: DONE pass=[0-9]+ fail=0' "$work/console.log"
}


cleanup_vm() {
  set +e
  [ -n "$dnsmasq_ctr" ] && docker rm -f "$dnsmasq_ctr" >/dev/null
  for rule in "${added_rules[@]}"; do
    # shellcheck disable=SC2086 # each rule is a list of iptables arguments
    sudo iptables -D $rule
  done
  [ -n "$forward_before" ] && sudo sysctl -q -w "net.ipv4.ip_forward=$forward_before"
  [ -n "$tap_created" ] && sudo ip link del "$tap"
  sudo rm -rf "$work"
}

case "$mode" in
  container) smoke_container ;;
  vm) smoke_vm ;;
  *) echo "usage: smoke.sh [container|vm]" >&2; exit 2 ;;
esac
