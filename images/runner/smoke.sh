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

firecracker_version=v1.17.0
kernel_image=ghcr.io/liquidmetal-dev/firecracker-kernel:6.1-20260929-1349
tap=mstap0
host_ip=172.16.0.1

smoke_vm() {
  local missing=()
  [ -r /dev/kvm ] && [ -w /dev/kvm ] || missing+=(/dev/kvm)
  for tool in sudo docker mkfs.ext4 iptables ip sysctl curl jq sha256sum; do
    command -v "$tool" >/dev/null || missing+=("$tool")
  done
  if [ "${#missing[@]}" -gt 0 ]; then
    echo "smoke.sh vm: missing ${missing[*]}" >&2
    exit 2
  fi

  work="$(mktemp -d)"
  dnsmasq_ctr=""
  forward_before=""
  trap cleanup_vm EXIT

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
  docker save "$IMAGE" -o "$work/image.tar"
  tar -xf "$work/image.tar" -C "$work" manifest.json
  if [ "$(jq '.[0].Layers | length' "$work/manifest.json")" != 1 ]; then
    echo "smoke.sh vm: $IMAGE must have exactly one layer" >&2
    exit 2
  fi
  local layer
  layer="$(jq -r '.[0].Layers[0]' "$work/manifest.json")"
  tar -xf "$work/image.tar" -C "$work" "$layer"
  rm "$work/image.tar"
  mkdir "$work/rootfs"
  sudo tar -xpf "$work/$layer" -C "$work/rootfs"
  rm "$work/$layer"
  sudo install -D -m 0755 "$here/smoke/checks.sh" "$work/rootfs/usr/local/lib/muster-smoke/checks.sh"
  sudo install -m 0644 "$here/smoke/smoke.service" "$work/rootfs/etc/systemd/system/smoke.service"
  sudo ln -s ../smoke.service "$work/rootfs/etc/systemd/system/multi-user.target.wants/smoke.service"
  sudo mkfs.ext4 -q -d "$work/rootfs" -L root "$work/rootfs.ext4" 4G
  sudo chown "$(id -u):$(id -g)" "$work/rootfs.ext4"
  sudo rm -rf "$work/rootfs"

  sudo ip tuntap add "$tap" mode tap user "$(id -un)"
  sudo ip addr add "$host_ip/24" dev "$tap"
  sudo ip link set "$tap" up
  forward_before="$(sysctl -n net.ipv4.ip_forward)"
  sudo sysctl -q -w net.ipv4.ip_forward=1
  for rule in "${vm_rules[@]}"; do
    # shellcheck disable=SC2086 # each rule is a list of iptables arguments
    sudo iptables -I $rule
  done

  docker build -q -t muster-smoke-dnsmasq -f "$here/smoke/dnsmasq.Dockerfile" "$here/smoke" >/dev/null
  dnsmasq_ctr="$(docker run -d --network host --cap-add NET_ADMIN muster-smoke-dnsmasq \
    --no-daemon --port=0 --interface="$tap" --bind-interfaces \
    --dhcp-range=172.16.0.10,172.16.0.50,12h --dhcp-option=option:dns-server,1.1.1.1)"

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
    {"iface_id": "eth0", "guest_mac": "06:00:AC:10:00:02", "host_dev_name": "$tap"}
  ],
  "vsock": {"guest_cid": 3, "uds_path": "$work/vsock.sock"},
  "machine-config": {"vcpu_count": 2, "mem_size_mib": 2048}
}
JSON

  timeout 900 "$fc" --no-api --config-file "$work/vm.json" >"$work/console.log" 2>&1 || true
  # The console prefixes each line with the kernel time and the unit's process name.
  grep -ao 'SMOKE: .*' "$work/console.log" || { tail -50 "$work/console.log"; exit 1; }
  grep -aEq 'SMOKE: DONE pass=[0-9]+ fail=0' "$work/console.log"
}

vm_rules=(
  "INPUT -i $tap -j ACCEPT"
  "FORWARD -i $tap -j ACCEPT"
  "FORWARD -o $tap -j ACCEPT"
  "POSTROUTING -t nat -s 172.16.0.0/24 ! -o $tap -j MASQUERADE"
)

cleanup_vm() {
  set +e
  [ -n "$dnsmasq_ctr" ] && docker rm -f "$dnsmasq_ctr" >/dev/null
  for rule in "${vm_rules[@]}"; do
    # shellcheck disable=SC2086 # each rule is a list of iptables arguments
    sudo iptables -D $rule 2>/dev/null
  done
  [ -n "$forward_before" ] && sudo sysctl -q -w "net.ipv4.ip_forward=$forward_before"
  sudo ip link del "$tap" 2>/dev/null
  sudo rm -rf "$work"
}

case "$mode" in
  container) smoke_container ;;
  vm) smoke_vm ;;
  *) echo "usage: smoke.sh [container|vm]" >&2; exit 2 ;;
esac
