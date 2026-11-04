# Runner image contract

A runner image is the root volume that a session's VM boots. muster relies on the image
contract below, and on nothing more (IMG-4). Any image that meets it can serve a pool. The
reference image variants under `images/runner/` meet it.

## What muster relies on

- **guest-agent.** The liquidmetal guest-agent is enabled at boot and listens on vsock.
  flintlock uses it to run commands in the VM.
- **Entrypoint.** `/usr/local/bin/muster-runner` is executable. Its behaviour:
  - With no arguments, it reads the encoded JIT configuration from stdin and removes all
    whitespace. Then it runs `/opt/actions-runner/run.sh --jitconfig <configuration>` with
    `exec`, as user `runner`, from `/opt/actions-runner`.
  - If root starts it, it starts itself again as `runner`, because `run.sh` does not run
    as root.
  - The exit code is the exit code of `run.sh`.
  - Empty stdin gives exit code 2 and the message `muster-runner: no JIT configuration on
    stdin`. `run.sh` does not start.
  - `--check` prints `ok <check>` or `fail <check>: <reason>` for each check, and gives exit
    code 1 if a check fails. The checks are `user`, `runner_dir`, `run_sh` and `docker`.
  - Any other argument gives exit code 2 and a usage message.
- **User.** User `runner` has uid 1000, passwordless sudo and membership of group `docker`.
  It owns `/opt/actions-runner`.
- **Runner.** The GitHub Actions runner is in `/opt/actions-runner`, and `run.sh` is
  executable.
- **Docker.** dockerd and containerd start at boot, and `runner` can use dockerd.
- **Network.** The VM gets its address from DHCP. The image contains no MAC address, no IP
  address and no machine-id, so each VM has its own identity. The VM can connect to GitHub
  with HTTPS.

## How muster calls the entrypoint

When a session starts, muster opens a flintlock exec stream in the VM as user `runner`, with
`has_stdin=true`. It writes the encoded JIT configuration to stdin and then closes stdin.
The runner registers, takes one job, removes its registration and exits. The exit code of
the exec stream is the first end signal of the session.

## Kernel

The reference guest kernel is the one that
[the guest-kernel ADR](adr/2026-10-27-guest-kernel.md) names:
`ghcr.io/liquidmetal-dev/firecracker-kernel:6.1-20260929-1349`, with `filename` set to
`boot/vmlinux`. An image that needs other kernel options needs a different kernel.

## Bringing your own image

Build your image, and then run the reference checks against it:

```bash
IMAGE=<your image> bash images/runner/smoke/entrypoint-test.sh
IMAGE=<your image> bash images/runner/smoke.sh container
IMAGE=<your image> bash images/runner/smoke.sh vm
```

`smoke.sh container` boots the image under systemd in a privileged container. `smoke.sh vm`
boots it in Firecracker with the reference kernel, DHCP and outbound network access. The vm
mode needs an image with one layer. `images/runner/README.md` lists what each mode needs on
the host.
