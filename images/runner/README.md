# Runner images

The reference runner images for muster. Each image variant has its own directory. The
[image contract](../../docs/image-contract.md) says what muster relies on in an image.

| Variant | Directory | Status |
| --- | --- | --- |
| `minimal` | `minimal/` | amd64 |

## Build

```bash
bash images/runner/build.sh minimal
```

The script builds the image for `ARCH` (default `amd64`) and tags it locally as
`ghcr.io/liquidmetal-dev/muster-runner:minimal-24.04-<runner version>` and
`ghcr.io/liquidmetal-dev/muster-runner:minimal-latest`. It does not push. The runner version
and its sha256 values are the `ARG` defaults in `minimal/Dockerfile`.

## Test

Each script uses the image in `IMAGE` (default `...:minimal-latest`).

| Command | What it checks | Host needs |
| --- | --- | --- |
| `bash images/runner/smoke/image-test.sh` | the image contents: one layer, the user, the units, the packages | docker, jq |
| `bash images/runner/smoke/entrypoint-test.sh` | `muster-runner` with a stub `run.sh` | docker |
| `bash images/runner/smoke.sh container` | a boot under systemd in a privileged container: guest-agent, dockerd, containerd and `muster-runner --check` | docker |
| `bash images/runner/smoke.sh vm` | a boot in Firecracker with the reference kernel: the container checks, plus DHCP, machine-id, DNS and HTTPS to GitHub | `/dev/kvm`, sudo, docker, `mkfs.ext4`, iptables, `ip`, jq |

The Images workflow runs the first three on each change under `images/`. The vm mode runs
on a developer's host only. It downloads Firecracker v1.17.0 to
`~/.cache/muster-smoke/`. It adds the TAP device `mstap0` (172.16.0.1/24), iptables rules
and a dnsmasq container for the duration of the run, and removes them when it ends.
