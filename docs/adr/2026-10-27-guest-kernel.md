# ADR: the runner guest kernel is the published liquidmetal Firecracker kernel

- Status: accepted
- Date: 2026-10-27
- Requirements: KRN-1, KRN-2, IMG-2

## Context

Issue #14 asked whether dockerd runs inside a Firecracker microVM on the published
liquidmetal kernels. It also asked what the guest kernel needs for `container:` jobs and
Docker-based actions. The issue named `ghcr.io/liquidmetal-dev/flintlock-kernel:5.10.77`. That image is
from 2023-01-17 and is superseded. The liquidmetal kernels are now built in
[mikrolite-images](https://github.com/liquidmetal-dev/mikrolite-images) and published as
`ghcr.io/liquidmetal-dev/firecracker-kernel` with the tags `6.1`, `5.10` and `5.10-no-acpi`,
and as `ghcr.io/liquidmetal-dev/cloudhypervisor-kernel:6.2`. Each build also gets an
immutable tag such as `6.1-20260929-1349`.

The mikrolite-images kernels come from Firecracker's own CI guest configs with an EROFS
fragment, and with modules disabled. A flintlock guest boots with no initrd that carries
modules, so an option built as a module is never loaded.

Docker needs cgroup v2, overlayfs, bridge, veth, netfilter and nf_tables in the guest kernel.
muster v0.1 boots its `minimal` image in full root volume mode on Firecracker.

## Findings

### Boot test

The spike booted each published amd64 kernel with raw Firecracker v1.17.0 on a KVM host, in
full root volume mode. The guest was Ubuntu 24.04.5 with systemd as init and docker-ce
29.9.2 from Docker's apt repository. Docker used containerd, runc v1.5.2 and the overlayfs
snapshotter. A systemd unit ran nine checks after `docker.service` and printed one line per
check to the serial console. The "How to reproduce" section has every file.

| check | 6.1 | 5.10 |
| --- | --- | --- |
| kernel | PASS 6.1.0 | PASS 5.10.245 |
| cgroup2 | PASS | PASS |
| dns | PASS | PASS |
| dockerd | PASS | FAIL docker.service is activating |
| hello | PASS | FAIL skipped |
| portmap | PASS | FAIL skipped |
| usernet | PASS | FAIL skipped |
| build | PASS | FAIL skipped |
| bindexec | PASS | FAIL skipped |

The checks are:

- `cgroup2`: `/sys/fs/cgroup` has the type `cgroup2fs`
- `dockerd`: `docker.service` is active and reports cgroup version 2
- `hello`: `docker run --rm hello-world` exits 0
- `portmap`: `nginx:alpine` with `-p 8080:80` answers `curl` from the guest
- `usernet`: two containers on a user-defined network reach each other by name
- `build`: `docker build` of a two-line Dockerfile succeeds
- `bindexec`: `docker exec` writes a file through a bind mount

The images tested, by immutable tag and digest:

| line | tag | digest |
| --- | --- | --- |
| 6.1 | `firecracker-kernel:6.1-20260929-1349` | `sha256:2934a9cc48aed54909bcfa8b8ce31e458997b772c526163f8dec32041c0e33cc` |
| 5.10 | `firecracker-kernel:5.10-20260929-1349` | `sha256:3f397fa551e367f2cfac2bf15aa7920a4c46fbcd5f949a1a7019625885e7cb29` |

On 6.1 the guest went from kernel start to a clean Firecracker exit in 22 seconds, with all
nine checks passed. `docker info` reported `Firewall Backend: iptables`, so the test covers
Docker's default iptables-nft path and not its native nftables backend. dockerd logged two
warnings on 6.1 that did not stop any check: the IPv6 NAT table cannot be created
(`ip6tables -t nat -N DOCKER` fails with `Operation not supported`, because
`CONFIG_NF_TABLES_IPV6` is not set), and conntrack flushes fail with `invalid argument`
(`CONFIG_NF_CT_NETLINK` is not set). Docker IPv6 is off by default, and the conntrack flush
is an optimization.

On 5.10 `docker.service` failed three times and stopped. Every attempt failed on the first
iptables rule:

```
iptables --wait -t nat -A PREROUTING -m addrtype --dst-type LOCAL -j DOCKER:
iptables v1.8.10 (nf_tables): RULE_APPEND failed (No such file or directory): rule in chain PREROUTING
```

The probable cause is `# CONFIG_NFT_COUNTER is not set` in the 5.10 config. iptables-nft
attaches a counter expression to every rule. The 6.1 config has no `NFT_COUNTER` symbol,
because the counter is part of the nf_tables core there. The spike did not rebuild 5.10 to
prove this, because 6.1 passed.

### Static review

The table compares the Docker-relevant options across the kernels in scope. `y` is built
in, `m` is a module, `n` is not set and `-` means the symbol is absent from that config.
The arm64 column is Firecracker's published `microvm-kernel-ci-aarch64-6.1.config`, because
liquidmetal publishes no arm64 kernel. The Cloud Hypervisor column is the config shipped in
`cloudhypervisor-kernel:6.2`.

| option | flintlock-5.10.77 | fc-6.1-amd64 | fc-5.10-amd64 | fc-6.1-arm64-config | ch-6.2-amd64 |
| --- | --- | --- | --- | --- | --- |
| `CGROUPS` | y | y | y | y | y |
| `MEMCG` | y | y | y | y | y |
| `CGROUP_PIDS` | y | y | y | y | y |
| `CGROUP_DEVICE` | y | y | y | y | y |
| `CGROUP_FREEZER` | y | y | y | y | y |
| `CGROUP_BPF` | y | y | y | y | y |
| `BLK_CGROUP` | y | y | y | y | y |
| `OVERLAY_FS` | y | y | y | y | y |
| `BRIDGE` | y | y | y | y | n |
| `BRIDGE_NETFILTER` | y | y | y | y | - |
| `VETH` | y | y | y | y | y |
| `NETFILTER` | y | y | y | y | n |
| `NF_TABLES` | m | y | y | y | - |
| `NF_TABLES_INET` | n | n | n | n | - |
| `NFT_NAT` | - | y | y | y | - |
| `NFT_MASQ` | m | n | n | n | - |
| `NFT_REJECT` | m | n | n | n | - |
| `NFT_COMPAT` | m | y | y | y | - |
| `NFT_CT` | m | y | y | y | - |
| `NF_NAT` | y | y | y | y | - |
| `NF_NAT_MASQUERADE` | y | y | y | y | - |
| `NF_CONNTRACK` | y | y | y | y | - |
| `IP_NF_IPTABLES` | y | y | y | y | - |
| `IP_NF_NAT` | y | y | y | y | - |
| `IP_NF_FILTER` | y | y | y | y | - |
| `IP_NF_TARGET_MASQUERADE` | y | y | y | y | - |
| `NETFILTER_XT_MATCH_ADDRTYPE` | y | y | y | y | - |
| `NETFILTER_XT_MATCH_CONNTRACK` | y | y | y | y | - |
| `NETFILTER_XT_MATCH_COMMENT` | y | n | n | n | - |
| `USER_NS` | y | y | y | y | y |
| `SECCOMP` | y | y | y | y | y |
| `KEYS` | y | y | y | y | y |
| `POSIX_MQUEUE` | y | y | y | y | y |
| `TUN` | y | y | n | y | y |
| `VXLAN` | m | y | n | y | n |
| `EROFS_FS` | n | y | y | n | y |
| `MODULES` | y | n | n | n | n |
| `NFT_COUNTER` | m | - | n | - | - |
| `NFT_FIB_IPV4` | - | n | n | n | - |
| `NETFILTER_XT_MARK` | y | y | n | y | - |

The legacy image: `flintlock-kernel:5.10.77` has `NF_TABLES`, `NFT_COMPAT` and `NFT_CT` as
modules, in an image whose modules a flintlock guest cannot load. nf_tables is therefore
missing in practice, and the image has no EROFS. It is not a candidate.

The published Firecracker kernels: the 6.1 kernel has every option Docker needs built in.
The options that are not set (`NFT_MASQ`, `NFT_REJECT`, `NF_TABLES_INET`, `NFT_FIB_IPV4`,
`xt_comment`) did not affect any check. The 5.10 kernel has the same option set plus the
`NFT_COUNTER` gap, and lacks `TUN` and `VXLAN`.

arm64: the Firecracker arm64 6.1 config matches the amd64 one for every option above, except
`EROFS_FS`, which the mikrolite-images fragment adds on amd64. An arm64 boot was not
possible. No liquidmetal arm64 kernel exists and no arm64 host was available.

Cloud Hypervisor: the published kernel has `NETFILTER` and `BRIDGE` not set. It cannot run
Docker as published. Its image also stores `vmlinux.bin` at the image root, not under
`/boot`. A Cloud Hypervisor boot was not run.

## Decision

muster uses the published `ghcr.io/liquidmetal-dev/firecracker-kernel:6.1-20260929-1349`
image as the reference guest kernel for amd64 on Firecracker, with `filename` set to
`boot/vmlinux`. muster does not build a kernel of its own. The muster-kernel build that KRN-1
and the design planned is dropped.

A kernel update is a change of the immutable tag in the pool configuration, after the checks
in this ADR pass on the new tag.

### Config fragment

No option beyond Firecracker's `microvm-kernel-ci-x86_64-6.1.config` was necessary for the
checks to pass. The only fragment mikrolite-images applies on top of that config is
`erofs.config`, which flintlock's overlay volume mode needs:

```
CONFIG_MISC_FILESYSTEMS=y
CONFIG_EROFS_FS=y
CONFIG_EROFS_FS_XATTR=y
CONFIG_EROFS_FS_POSIX_ACL=y
CONFIG_EROFS_FS_SECURITY=y
CONFIG_EROFS_FS_ZIP=y
```

A kernel on another line or architecture is sufficient for the checks when it matches the
`fc-6.1-amd64` column above. Every `y` in that column is necessary. The options that are
`n` there (`NFT_MASQ`, `NFT_REJECT`, `NF_TABLES_INET`, `NFT_FIB_IPV4`,
`NETFILTER_XT_MATCH_COMMENT`) are not. A 5.10 kernel also needs `CONFIG_NFT_COUNTER=y`.

## Consequences

For muster:

- muster has no kernel sources and no kernel CI, and the design drops `images/kernel/`.
- The `minimal` image keeps Docker's default iptables backend, because the native nftables backend was not tested.
- A workload that needs Docker IPv6 or conntrack flushes needs a new mikrolite-images build with `CONFIG_NF_TABLES_IPV6=y` and `CONFIG_NF_CT_NETLINK=y`.

Open work, all in mikrolite-images:

- The arm64 kernel for v0.2.0 is [mikrolite-images#41](https://github.com/liquidmetal-dev/mikrolite-images/issues/41), which must add the EROFS fragment that the arm64 base config lacks.
- Cloud Hypervisor stays unsupported until a published kernel has netfilter and bridge enabled and a boot test passes on it.
- The published 5.10 kernel is not usable for Docker, and no build has yet verified the probable fix `CONFIG_NFT_COUNTER=y`.
- Firecracker publishes 6.18 guest configs, and 6.18 is the next line to check when mikrolite-images builds it.

## How to reproduce

The spike ran on an x86_64 host with `/dev/kvm`, Docker 29, `mkfs.ext4` from e2fsprogs
1.47.4 and iptables. No flintlock was installed. The files below were kept out of the
repository on purpose. The `minimal` image work supersedes them.

The guest rootfs is built with Docker from this Dockerfile. Docker bind-mounts
`/etc/resolv.conf` during a build, so the file is shipped beside it and a tmpfiles rule
links it at boot. No getty runs on the serial line, because a getty hangs up the console
and the check output is lost.

```dockerfile
FROM ubuntu:24.04
ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends \
      systemd systemd-sysv dbus ca-certificates curl gnupg iproute2 iputils-ping \
      kmod udev && rm -rf /var/lib/apt/lists/*
RUN install -m 0755 -d /etc/apt/keyrings \
 && curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc \
 && echo "deb [arch=amd64 signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/ubuntu noble stable" > /etc/apt/sources.list.d/docker.list \
 && apt-get update && apt-get install -y --no-install-recommends \
      docker-ce docker-ce-cli containerd.io docker-buildx-plugin \
 && rm -rf /var/lib/apt/lists/*
RUN systemctl enable docker containerd \
 && systemctl mask systemd-resolved systemd-networkd-wait-online serial-getty@ttyS0 console-getty getty@tty1 \
 && passwd -d root \
 && printf 'nameserver 1.1.1.1\n' > /etc/resolv.conf.spike \
 && printf 'L+ /etc/resolv.conf - - - - /etc/resolv.conf.spike\n' > /etc/tmpfiles.d/spike-resolv.conf \
 && printf '[Match]\nName=eth0\n[Network]\nAddress=172.16.0.2/30\nGateway=172.16.0.1\n' > /etc/systemd/network/10-eth0.network \
 && systemctl enable systemd-networkd \
 && echo spike > /etc/hostname
COPY spike-check.sh /usr/local/bin/spike-check.sh
COPY spike-check.service /etc/systemd/system/spike-check.service
RUN chmod +x /usr/local/bin/spike-check.sh && systemctl enable spike-check
```

The unit runs the checks after dockerd and sends the output to the serial console:

```ini
[Unit]
Description=muster kernel spike checks
After=docker.service network-online.target
Wants=docker.service

[Service]
Type=oneshot
ExecStart=/usr/local/bin/spike-check.sh
StandardOutput=tty
StandardError=tty
TTYPath=/dev/ttyS0

[Install]
WantedBy=multi-user.target
```

The check script prints one `SPIKE:` line per check, then `docker info`, the dockerd journal
and the kernel warnings, then `SPIKE: END`. It ends with `systemctl reboot`, because a
`poweroff` only halts a Firecracker guest and Firecracker does not exit. With `reboot=k` on
the kernel command line, the reboot makes Firecracker exit.

```bash
#!/usr/bin/env bash
set -u
export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin

pass() { echo "SPIKE: $1 PASS${2:+ $2}"; }
fail() { echo "SPIKE: $1 FAIL ${2:-}"; }
skip() { fail "$1" "skipped"; }

check_kernel() { pass kernel "$(uname -r)"; }

check_cgroup2() {
  local t; t=$(stat -fc %T /sys/fs/cgroup 2>&1)
  [ "$t" = cgroup2fs ] && pass cgroup2 || fail cgroup2 "fs type is $t"
}

check_dns() {
  getent hosts registry-1.docker.io >/dev/null 2>&1 && pass dns || fail dns "getent hosts registry-1.docker.io failed"
}

check_dockerd() {
  local st cg
  st=$(systemctl is-active docker 2>&1)
  if [ "$st" != active ]; then fail dockerd "docker.service is $st"; return 1; fi
  cg=$(docker info --format '{{.CgroupVersion}}' 2>&1)
  if [ "$cg" != 2 ]; then fail dockerd "CgroupVersion is $cg"; return 1; fi
  pass dockerd
}

check_hello() {
  local out; out=$(docker run --rm hello-world 2>&1)
  [ $? -eq 0 ] && pass hello || fail hello "$(echo "$out" | tail -n 1)"
}

check_portmap() {
  local out i
  out=$(docker run -d --name web -p 8080:80 nginx:alpine 2>&1) || { fail portmap "$(echo "$out" | tail -n 1)"; return; }
  for i in $(seq 1 10); do
    if curl -fsS http://127.0.0.1:8080/ 2>/dev/null | grep -q nginx; then pass portmap; return; fi
    sleep 1
  done
  fail portmap "curl 127.0.0.1:8080 did not return nginx after 10 tries"
}

check_usernet() {
  local out
  docker network create spike >/dev/null 2>&1 || { fail usernet "network create failed"; return; }
  out=$(docker run -d --name web2 --network spike nginx:alpine 2>&1) || { fail usernet "$(echo "$out" | tail -n 1)"; return; }
  sleep 2
  out=$(docker run --rm --network spike alpine:3 wget -qO- http://web2/ 2>&1)
  echo "$out" | grep -q nginx && pass usernet || fail usernet "$(echo "$out" | tail -n 1)"
}

check_build() {
  local out
  out=$(printf 'FROM alpine:3\nRUN echo built > /built\n' | docker build -q -t spike-build - 2>&1) || { fail build "$(echo "$out" | tail -n 1)"; return; }
  out=$(docker run --rm spike-build cat /built 2>&1)
  [ "$out" = built ] && pass build || fail build "$(echo "$out" | tail -n 1)"
}

check_bindexec() {
  local out
  mkdir -p /tmp/bm
  out=$(docker run -d --name bm -v /tmp/bm:/data alpine:3 sleep 300 2>&1) || { fail bindexec "$(echo "$out" | tail -n 1)"; return; }
  out=$(docker exec bm sh -c 'echo hi > /data/x' 2>&1) || { fail bindexec "exec: $(echo "$out" | tail -n 1)"; return; }
  [ "$(cat /tmp/bm/x 2>/dev/null)" = hi ] && pass bindexec || fail bindexec "/tmp/bm/x does not contain hi"
}

info() {
  echo "SPIKE: INFO $1"
  shift
  "$@" 2>&1 | sed 's/^/  /'
}

check_kernel
check_cgroup2
check_dns
if check_dockerd; then
  check_hello
  check_portmap
  check_usernet
  check_build
  check_bindexec
else
  for c in hello portmap usernet build bindexec; do skip "$c"; done
fi
info "docker info" docker info
info "journal" bash -c 'journalctl -u docker --no-pager -o cat | tail -n 40'
info "dmesg" bash -c 'dmesg --level=err,warn | tail -n 40'
echo "SPIKE: END"
sync
systemctl reboot
```

The image is flattened to ext4 without a mount and without root. `mkfs.ext4 -d` reads the
tar and keeps the owner and mode of each file:

```bash
docker build -t muster-spike-rootfs rootfs/
cid=$(docker create muster-spike-rootfs true)
docker export "$cid" -o rootfs.tar
docker container rm "$cid"
truncate -s 2G rootfs.ext4
mkfs.ext4 -q -F -d rootfs.tar rootfs.ext4
```

The host needs a tap owned by the user who runs Firecracker, and NAT for the guest. The
host's own Docker sets the FORWARD policy to DROP, so the two ACCEPT rules are necessary:

```bash
ip tuntap add dev fctap0 mode tap user "$USER"
ip addr replace 172.16.0.1/30 dev fctap0
ip link set fctap0 up
iptables -I FORWARD -i fctap0 -j ACCEPT
iptables -I FORWARD -o fctap0 -j ACCEPT
iptables -t nat -A POSTROUTING -s 172.16.0.0/30 ! -o fctap0 -j MASQUERADE
```

The kernel comes from the image, and the boot uses flintlock's default kernel command line
with the root device and the cgroup option added:

```bash
cid=$(docker create ghcr.io/liquidmetal-dev/firecracker-kernel:6.1-20260929-1349 true)
docker cp "$cid:/boot/vmlinux" vmlinux
docker container rm "$cid"
```

```json
{
  "boot-source": {
    "kernel_image_path": "vmlinux",
    "boot_args": "console=ttyS0 reboot=k panic=1 pci=off i8042.noaux i8042.nomux i8042.nopnp i8042.dumbkbd root=/dev/vda rw systemd.unified_cgroup_hierarchy=1"
  },
  "drives": [
    {"drive_id": "rootfs", "path_on_host": "rootfs.ext4", "is_root_device": true, "is_read_only": false}
  ],
  "network-interfaces": [
    {"iface_id": "eth0", "guest_mac": "06:00:AC:10:00:02", "host_dev_name": "fctap0"}
  ],
  "machine-config": {"vcpu_count": 2, "mem_size_mib": 2048}
}
```

```bash
timeout 900 ./firecracker --no-api --config-file vm.json > console.log 2>&1
grep '^SPIKE: ' console.log
```

Firecracker v1.17.0 came from the GitHub release tarball with its published sha256. The
`SPIKE:` lines in `console.log` are the result table above.
