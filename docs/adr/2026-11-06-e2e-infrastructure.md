# ADR: the e2e suite runs real flintlockd and poolmgrd as root on an Ubuntu host

- Status: accepted
- Date: 2026-11-06
- Requirements: PROJ-4

## Context

The e2e suite (`test/e2e`, build tag `e2e`) must prove that muster claims a VM from battery,
runs the runner in a Firecracker microVM through flintlock's exec API, and ends the session.
The unit tests already cover muster against in-process fakes of battery and flintlock. Only
the real daemons show that the microVM template, the runner image and the exec stream work
together.

The real daemons need `/dev/kvm`, containerd with a devmapper thin-pool, a bridge and root.
GitHub's `ubuntu-latest` runners have KVM, and flintlock's own hosted e2e workflow runs there
with `sudo`. Muster does not publish runner images to a registry yet.

## Decision

- The suite runs the real flintlockd and poolmgrd. A provision script downloads release
  binaries at versions pinned in one file and checks their SHA256. The poolmgrd version
  matches the battery module version in `go.mod`.
- The `e2e` runner image is built locally and pushed to a `registry:2` container on
  `localhost:5000`. containerd reaches it over plain HTTP through a `hosts.toml` entry. The
  image does not go to ghcr.io and is not imported with `ctr`.
- Provisioning and `go test` run as root on Ubuntu 24.04. Other hosts are not supported.
- flintlockd, poolmgrd and the registry start once per run. Each e2e scenario starts its own
  fake GitHub and musterd and uses its own battery namespace. The scenarios run one at a time.
- The suite does not prove automatically that each e2e scenario fails when its feature is
  disabled. That check is done by hand with a temporary patch, and the PR records it.

## Consequences

- A new flintlock or battery release needs a change to the pinned versions and checksums.
- The local registry uses the same pull path as production hosts. A pull problem shows in
  e2e, and no registry credentials are necessary in CI.
- Running the suite needs root on an Ubuntu host or VM. Developers on other systems run it in
  a VM.
- No production code has switches that exist only to disable features for tests.
- A change that breaks a feature without breaking its assertion is not caught until someone
  repeats the manual check.
