# Runner images

The reference runner images for muster. Each image variant has its own directory. The
[image contract](../../docs/image-contract.md) says what muster relies on in an image.

| Variant | Directory | Status |
| --- | --- | --- |
| `minimal` | `minimal/` | amd64 |
| `e2e` | `e2e/` | amd64; `minimal` with a stub `run.sh`, for the e2e suite only |

## Build

```bash
bash images/runner/build.sh minimal
```

The script builds the image for `ARCH` (default `amd64`) and tags it locally as
`ghcr.io/liquidmetal-dev/muster-runner:minimal-24.04-<runner version>` and
`ghcr.io/liquidmetal-dev/muster-runner:minimal-latest`. It does not push. The runner version
and its sha256 values are the `ARG` defaults in `minimal/Dockerfile`.

## Publish

Run the Images workflow by hand on `main` (`gh workflow run images.yml --ref main`). After the
tests pass, the `publish minimal (amd64)` job builds `minimal` again and pushes both tags to
GHCR. A run from any other ref skips the publish job. The `e2e` variant is never published.

The `e2e` variant builds on the local `minimal-latest` image, so build `minimal` first. Its
`run.sh` decodes the fake GitHub's JIT configuration, sleeps for `sleep_seconds` and exits
with `exit_code`. It takes the runner version of `minimal/Dockerfile` for its tag.

## Test

Each script uses the image in `IMAGE` (default `...:minimal-latest`).

| Command | What it checks | Host needs |
| --- | --- | --- |
| `bash images/runner/smoke/image-test.sh` | the image contents: one layer, the user, the units, the packages | docker, jq |
| `bash images/runner/smoke/entrypoint-test.sh` | `muster-runner` with a stub `run.sh` | docker |
| `bash images/runner/smoke/e2e-stub-test.sh` | the `e2e` variant's `run.sh` through `muster-runner` (`IMAGE` default `...:e2e-latest`) | docker, jq |
| `bash images/runner/smoke.sh container` | a boot under systemd in a privileged container: guest-agent, dockerd, containerd and `muster-runner --check` | docker |
| `bash images/runner/smoke.sh vm` | a boot in Firecracker with the reference kernel: the container checks, plus DHCP, machine-id, DNS, HTTPS to GitHub, and a vsock connection from the host to guest-agent | `/dev/kvm`, sudo, docker, `mkfs.ext4`, iptables, `ip`, jq, python3 |

The Images workflow runs all but the vm mode on each change under `images/`. guest-agent stops
when it has no vsock device. Where the container has no `/dev/vsock`, as on GitHub-hosted
runners, the container mode checks only that guest-agent is enabled and that its binary
runs. The vm mode is then the check that guest-agent starts and answers on vsock (IMG-6). The vm mode runs
on a developer's host only. It downloads Firecracker v1.17.0 to
`~/.cache/muster-smoke/`. It adds the TAP device `mstap0` (172.16.0.1/24), iptables rules
and a dnsmasq container for the duration of the run, and removes them when it ends.
