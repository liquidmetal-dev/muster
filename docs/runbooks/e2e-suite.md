# Running the e2e suite

The e2e suite (`test/e2e`, build tag `e2e`) runs a real musterd against real flintlockd and
poolmgrd processes, with Firecracker microVMs, the fake GitHub and the `e2e` runner image.
The [ADR](../adr/2026-11-06-e2e-infrastructure.md) gives the reasons for this design.

## Prerequisites

- Ubuntu 24.04. `provision.sh` stops on other systems. On another system, use an Ubuntu VM
  with nested KVM.
- `/dev/kvm`.
- Root.
- docker with buildx, jq, curl, dmsetup and losetup.
- go, as `mise.toml` pins it.
- About 50 GB of free disk. The thin-pool files are sparse.

## Run it

```sh
sudo env "PATH=$PATH" hack/e2e/provision.sh
sudo env "PATH=$PATH" GOFLAGS=-buildvcs=false \
  go test -tags e2e -timeout 30m -count=1 -v ./test/e2e
sudo hack/e2e/cleanup.sh
```

- `provision.sh` installs pinned binaries in `/opt/muster-e2e/bin`.
- It starts a containerd of its own, on a loop-device thin-pool.
- It adds the bridge `muster-e2e-br0`.
- It pushes the `e2e` image to a registry container on `localhost:5000`.
- It writes `/run/muster-e2e/env`, which the suite reads.
- It does not touch the host's containerd.

`sudo` gives root its own `HOME`, so root's go caches stay out of yours.

When `/run/muster-e2e/env` is missing, the suite prints a message and passes. Set
`E2E_REQUIRED=1` to make it fail instead. The workflow does this.

To run one e2e scenario, add `-run TestDrainStopsNewSessions` (for example).

## Logs

Each daemon writes to its own file, `<name>-<n>.log`, in `E2E_LOG_DIR`. When that is not set,
the suite uses a temporary directory and prints its path. A failed wait prints the last 50
lines of the musterd and flintlockd logs.

## CI

`.github/workflows/e2e.yml` runs the suite:

- every night;
- on `workflow_dispatch`;
- on pull requests that have the label `run/e2e`;
- on a pull request comment that starts with `/run-e2e`, from someone with write access.

A `/run-e2e` comment gets an 👀 reaction. The run tests the head commit of the pull request at
the time of the comment, and sets the commit status `e2e (comment)` on that commit. GitHub
reads the comment trigger from the workflow on the default branch, so a change to it takes
effect only after it merges.

When the job fails, it uploads the daemon logs as the artifact `e2e-logs`.

## Updating versions

`hack/e2e/versions.env` pins flintlockd, poolmgrd, Firecracker, containerd, the registry
image and the kernel image, with their SHA256 sums. `BATTERY_VERSION` must equal the battery
version in `go.mod`, and `provision.sh` checks this. For a new release, change the version and
its sum together.

## Proving a scenario

Each e2e scenario fails when its feature is disabled. To check this again, patch the feature
out in production code, run the scenario and see it fail, then revert the patch. The pull
request that added the suite lists the patch for each scenario.
