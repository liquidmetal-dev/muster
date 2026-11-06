#!/usr/bin/env bash
# Prepare an Ubuntu 24.04 host for the e2e suite. Run as root from the repository, with go on
# PATH: sudo env "PATH=$PATH" hack/e2e/provision.sh
# It installs pinned binaries in /opt/muster-e2e/bin, starts a containerd of its own with a
# loop-device thin-pool, adds the bridge muster-e2e-br0, runs a registry on localhost:5000
# with the e2e runner image, and writes /run/muster-e2e/env for the suite.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo="$(cd "$here/../.." && pwd)"
# shellcheck source=hack/e2e/versions.env
. "$here/versions.env"

bin=/opt/muster-e2e/bin
cache=/var/cache/muster-e2e
lib=/var/lib/muster-e2e
run=/run/muster-e2e
etc=/etc/muster-e2e
sock="$run/containerd/containerd.sock"
image=localhost:5000/muster-runner:e2e

die() { echo "provision.sh: $*" >&2; exit 1; }

# fetch <url> <sha256> <dest>: downloads into the cache once and checks the sum every time.
fetch() {
  local url="$1" want="$2" dest="$3" got
  if [ ! -f "$dest" ]; then
    curl -fsSLo "$dest.part" "$url" || die "download failed: $url"
    mv "$dest.part" "$dest"
  fi
  got="$(sha256sum "$dest" | cut -d' ' -f1)"
  if [ "$got" != "$want" ]; then
    rm -f "$dest"
    die "checksum mismatch for $url: expected $want, got $got"
  fi
}

[ "$(id -u)" = 0 ] || die "run as root"
# shellcheck source=/dev/null
. /etc/os-release
[ "${ID:-}" = ubuntu ] && [ "${VERSION_ID:-}" = 24.04 ] || die "needs Ubuntu 24.04, found ${PRETTY_NAME:-unknown}"
command -v go >/dev/null || die "go is not on PATH; run with sudo env \"PATH=\$PATH\""
mod_battery="$(cd "$repo" && go list -m -f '{{.Version}}' github.com/liquidmetal-dev/battery)"
[ "$mod_battery" = "$BATTERY_VERSION" ] ||
  die "versions.env BATTERY_VERSION $BATTERY_VERSION differs from go.mod $mod_battery"

"$here/cleanup.sh" || die "cleanup.sh could not remove the state of an earlier run"

if [ ! -c /dev/kvm ] || [ "$(stat -c %a /dev/kvm)" != 666 ]; then
  echo 'KERNEL=="kvm", GROUP="kvm", MODE="0666", OPTIONS+="static_node=kvm"' \
    >/etc/udev/rules.d/99-muster-e2e-kvm.rules
  udevadm control --reload-rules
  udevadm trigger --action=add --name-match=kvm
  udevadm settle
fi
[ -c /dev/kvm ] || die "/dev/kvm is not a character device"

for tool in dmsetup losetup jq docker curl ip; do
  command -v "$tool" >/dev/null || die "$tool is missing"
done

install -d "$bin" "$cache" "$lib" "$run" "$etc"

gh=https://github.com
fetch "$gh/liquidmetal-dev/flintlock/releases/download/$FLINTLOCK_VERSION/flintlockd_amd64" \
  "$FLINTLOCKD_SHA256" "$cache/flintlockd-$FLINTLOCK_VERSION"
install -m 0755 "$cache/flintlockd-$FLINTLOCK_VERSION" "$bin/flintlockd"

fetch "$gh/liquidmetal-dev/battery/releases/download/$BATTERY_VERSION/poolmgrd_${BATTERY_VERSION#v}_linux_amd64.tar.gz" \
  "$POOLMGRD_SHA256" "$cache/poolmgrd-$BATTERY_VERSION.tar.gz"
tar -xzf "$cache/poolmgrd-$BATTERY_VERSION.tar.gz" -C "$bin" poolmgrd

fetch "$gh/firecracker-microvm/firecracker/releases/download/$FIRECRACKER_VERSION/firecracker-$FIRECRACKER_VERSION-x86_64.tgz" \
  "$FIRECRACKER_SHA256" "$cache/firecracker-$FIRECRACKER_VERSION.tgz"
tar -xzf "$cache/firecracker-$FIRECRACKER_VERSION.tgz" -C "$cache" \
  "release-$FIRECRACKER_VERSION-x86_64/firecracker-$FIRECRACKER_VERSION-x86_64"
install -m 0755 "$cache/release-$FIRECRACKER_VERSION-x86_64/firecracker-$FIRECRACKER_VERSION-x86_64" "$bin/firecracker"

fetch "$gh/containerd/containerd/releases/download/v$CONTAINERD_VERSION/containerd-$CONTAINERD_VERSION-linux-amd64.tar.gz" \
  "$CONTAINERD_SHA256" "$cache/containerd-$CONTAINERD_VERSION.tar.gz"
tar -xzf "$cache/containerd-$CONTAINERD_VERSION.tar.gz" -C "$bin" --strip-components=1 bin/containerd bin/ctr

truncate -s 40G "$lib/data.img"
truncate -s 2G "$lib/meta.img"
data_dev="$(losetup --find --show "$lib/data.img")"
meta_dev="$(losetup --find --show "$lib/meta.img")"
dmsetup create muster-e2e-thinpool \
  --table "0 $(blockdev --getsz "$data_dev") thin-pool $meta_dev $data_dev 128 32768 1 skip_block_zeroing"

install -d "$etc/certs.d/localhost:5000"
cat >"$etc/certs.d/localhost:5000/hosts.toml" <<TOML
server = "http://localhost:5000"

[host."http://localhost:5000"]
  capabilities = ["pull", "resolve"]
TOML
cat >"$etc/containerd.toml" <<TOML
version = 3
root = "$lib/containerd"
state = "$run/containerd"
disabled_plugins = ["io.containerd.grpc.v1.cri", "io.containerd.cri.v1.runtime", "io.containerd.cri.v1.images"]

[grpc]
  address = "$sock"

[plugins."io.containerd.snapshotter.v1.devmapper"]
  pool_name = "muster-e2e-thinpool"
  root_path = "$lib/containerd/devmapper"
  base_image_size = "10GB"
  discard_blocks = true
TOML
nohup "$bin/containerd" --config "$etc/containerd.toml" >"$run/containerd.log" 2>&1 &
echo $! >"$run/containerd.pid"
for _ in $(seq 60); do [ -S "$sock" ] && break; sleep 0.5; done
[ -S "$sock" ] || die "containerd did not start; see $run/containerd.log"

ip link add muster-e2e-br0 type bridge
ip link set muster-e2e-br0 up

docker run -d --name muster-e2e-registry -p 127.0.0.1:5000:5000 "$REGISTRY_IMAGE" >/dev/null
bash "$repo/images/runner/build.sh" minimal >/dev/null
e2e_tag="$(bash "$repo/images/runner/build.sh" e2e)"
docker tag "$e2e_tag" "$image"
# The registry takes a moment to listen after docker run returns.
pushed=
for _ in $(seq 20); do
  if docker push -q "$image" >/dev/null; then pushed=1; break; fi
  sleep 1
done
[ -n "$pushed" ] || die "push of $image failed"

cat >"$run/env" <<ENV
E2E_BIN_DIR=$bin
E2E_BRIDGE=muster-e2e-br0
E2E_CONTAINERD_SOCKET=$sock
E2E_HOSTS_DIR=$etc/certs.d
E2E_STATE_DIR=$lib
E2E_RUN_DIR=$run
E2E_IMAGE=$image
E2E_KERNEL_IMAGE=$KERNEL_IMAGE
ENV
echo "provision.sh: ready; the suite reads $run/env"
