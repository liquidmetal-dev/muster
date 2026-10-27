# Muster requirements

- Status: proposed
- Date: 2026-10-27
- Companion: [design](2026-10-27-muster-design.md)

Muster runs GitHub Actions jobs in microVMs. Each job gets a dedicated, pre-booted
microVM from a warm pool managed by [battery](https://github.com/liquidmetal-dev/battery)
on top of [flintlock](https://github.com/liquidmetal-dev/flintlock). When the job ends the
microVM is deleted. Muster is a showcase project for the liquidmetal-dev family: it must be
portable, documented, and look like a sibling of battery.

The key words MUST, MUST NOT, REQUIRED, SHALL, SHALL NOT, SHOULD, SHOULD NOT,
RECOMMENDED, MAY and OPTIONAL in this document are to be interpreted as described in
[RFC 2119](https://www.rfc-editor.org/rfc/rfc2119). Each requirement has a stable
identifier so that issues, tests and ADRs can cite it.

## 1. Scope and goals

- **GOAL-1** Muster SHALL run each GitHub Actions job in a dedicated microVM obtained from a
  warm pool, and SHALL destroy that microVM when the job ends.
- **GOAL-2** Muster SHALL be usable by a third party with a flintlock host, a battery
  deployment and a GitHub organization or repository, following only this repository's
  documentation.
- **GOAL-3** Muster SHALL isolate "where VMs come from" behind one internal interface so that
  a flintlock snapshot-restore source can be added without changing GitHub-facing behaviour.
- **GOAL-4** Muster SHALL be exercised end to end on real infrastructure by a suite in
  liquidmetal-dev/acceptance-tests.
- **NONGOAL-1** The following are out of scope for v1. Each MAY be a tracked follow-up:
  enterprise-scope runners; Windows and macOS guests; multi-tenant quotas or accounting;
  multiple `musterd` replicas (high availability); multiple GitHub installations per
  instance; a Kubernetes operator, CRDs or Helm chart; managing flintlock hosts in battery;
  Actions cache proxies; autoscaling battery pool sizes from queue depth; organization
  repository discovery for polling; keeping failed VMs for debugging; hot configuration
  reload.

## 2. Job intake

- **INTAKE-1** Muster MUST accept GitHub `workflow_job` webhook deliveries and MUST verify
  `X-Hub-Signature-256` against a configured secret before processing. Unverified deliveries
  MUST be rejected with HTTP 401 and MUST NOT be processed.
- **INTAKE-2** Muster MUST treat `workflow_job.queued` as evidence of a queued job and MUST
  use `in_progress` and `completed` (with `runner_name`) as signals that a job is no longer
  queued and that a session's runner is busy or finished.
- **INTAKE-3** Muster MUST ignore redelivered deliveries it has processed within the last
  24 hours (an in-memory delivery id set with TTL).
- **INTAKE-4** Muster MUST respond to webhooks within 10 seconds and MUST NOT block the
  response on VM acquisition.
- **INTAKE-5** Muster MUST implement a polling fallback that lists queued jobs for an
  explicit list of repositories at a configured interval (default 60 seconds) and merges the
  result with webhook-sourced knowledge so that one queued job is never counted twice.
  Muster MUST NOT discover repositories automatically in v1.
- **INTAKE-6** Polling and the webhook listener MUST each be disable-able; at least one MUST
  be enabled.
- **INTAKE-7** Muster MUST only act on jobs whose labels match a configured pool and whose
  repository is within that pool's scope; all other jobs MUST be ignored silently (debug log
  only).
- **INTAKE-8** The webhook listener MUST support optional TLS from configured certificate
  and key files. Without them it MUST listen on plain HTTP, and the documentation MUST say
  to terminate TLS in front of it.

## 3. Pools and label matching

- **POOL-1** Configuration MUST declare pools. Each pool MUST specify a name, the runner
  labels it serves, a flintlock `MicroVMSpec` template (protojson), the warm `size`, the
  eligible flintlock host names, and a GitHub scope (an organization with an optional runner
  group name, or an `owner/repo`).
- **POOL-2** On startup muster MUST create or update the matching battery pool through
  `PoolAdmin` so that battery's desired state equals the configuration. Muster MUST set
  `allow_guest_agent=true` on the template, MUST default `replenishment_strategy` to
  `IMMEDIATE_ON_LEASE`, MUST pass through optional `create_commands`, and MUST place its
  pools in a configurable battery namespace (default `muster`).
- **POOL-3** Muster MUST NOT add, update, cordon or remove flintlock hosts in battery. Host
  registration is the operator's job and MUST be documented. Muster MUST fail validation if
  a pool references a host that battery does not know.
- **POOL-4** Every flintlock host used by a pool MUST run flintlock v0.15.2 or later started
  with `--enable-exec-api`. Muster MUST check `ServerInfo.exec.enabled` on each host at
  startup and MUST refuse to serve a pool whose hosts lack it, with a clear error.
- **POOL-5** A queued job matches a pool when every label in the job's `labels` is in the
  pool's label set and the job's repository is within the pool's scope. When several pools
  match, muster MUST pick the first in configuration order. Each pool's labels MUST include
  `self-hosted`; documentation RECOMMENDS a `muster` discriminator label.
- **POOL-6** Each pool MUST have a configurable `max_concurrent` (default twice `size`);
  muster MUST NOT hold more active sessions for a pool than that.
- **POOL-7** Muster MUST NOT delete a battery pool that is absent from its configuration
  unless `--prune-pools` is set; it MUST log the orphan instead.
- **POOL-8** Muster MUST NOT reuse a VM for a second job and MUST rely on battery's
  release-deletes semantics.

## 4. Runner session lifecycle

A *session* is muster's unit of work: one claimed VM, one just-in-time (JIT) runner, at
most one job.

- **SESSION-0** Per pool, muster MUST keep the number of active, not-yet-busy sessions equal
  to the number of queued matching jobs (bounded by POOL-6), and MUST NOT create sessions for
  jobs outside the pool's scope.
- **SESSION-1** Each session MUST have a unique id and MUST move through the states
  PENDING → CLAIMING → STARTING → RUNNING → ENDED → RELEASED, with FAILED reachable from any
  active state.
- **SESSION-2** Muster MUST claim with `ClaimVM(pool, request_id=<session id>)` so that a
  retried claim returns the same lease.
- **SESSION-3** On `RESOURCE_EXHAUSTED` muster MUST retry with bounded exponential backoff
  while demand still exceeds active sessions, up to a claim deadline (default 10 minutes).
  On deadline the session MUST end FAILED.
- **SESSION-4** Muster MUST request the JIT runner configuration only after a VM is claimed,
  with: runner name `muster-<namespace>-<pool>-<short id>` (at most 64 characters);
  `labels` equal to the pool's label set; `runner_group_id` resolved from the pool's runner
  group name (organization scope) or the default group (repository scope); and
  `work_folder` from pool configuration (default `_work`).
- **SESSION-5** Muster MUST start the runner by executing `/usr/local/bin/muster-runner` in
  the guest through flintlock's `MicroVMExec.ExecCommand` on the host address returned by
  the claim, as the image's runner user, passing the encoded JIT configuration on stdin and
  never in arguments or environment.
- **SESSION-6** Muster MUST heartbeat the lease at no more than half the pool's
  `heartbeat_interval` while the session is active, and MUST stop after release.
- **SESSION-7** A session MUST be ENDED and then released when any of the following happens
  first: the exec stream returns an exit code; a `workflow_job.completed` naming its runner
  is received and a grace period (default 30 seconds) passes; the runner is no longer listed
  by GitHub (checked with one scoped `ListRunners` call per poll interval, not one request
  per session); the runner has been online but not busy for the idle timeout (default
  5 minutes); or the maximum session duration (default 6 hours, configurable per pool)
  elapses.
- **SESSION-8** On release muster MUST call `ReleaseVM` and, if the runner still exists in
  GitHub, MUST delete it through the runners API. Both MUST be retried with backoff until
  success or process exit.
- **SESSION-9** The encoded JIT configuration MUST be treated as a secret: never logged,
  dropped from memory once the runner has started, and redacted from error messages.
- **SESSION-10** Muster SHOULD forward the runner's stdout and stderr to its own log at debug
  level and MUST NOT fail a session because of log volume.
- **SESSION-11** Muster MUST support a drain mode in which no new sessions are created while
  existing ones run to completion, and an undrain to resume.

## 5. GitHub integration

- **GH-1** Muster MUST authenticate as a GitHub App (app id, installation id, private key)
  or with a personal access token. Installation tokens MUST be refreshed before expiry.
- **GH-2** Muster MUST support organization and repository runner scopes, selectable per
  pool.
- **GH-3** A `musterd` instance MUST hold exactly one GitHub credential. Validation MUST
  reject pool scopes and polled repositories that the credential cannot reach.
- **GH-4** Muster MUST support a configurable API base URL for GitHub Enterprise Server.
- **GH-5** Muster MUST honour GitHub rate limiting (HTTP 403 and 429 with `Retry-After`) and
  SHOULD expose API error counts as metrics.
- **GH-6** Documentation MUST list the GitHub App permissions and webhook events required,
  and the token scopes for the personal access token path.

## 6. Runner microVM images and kernel

- **IMG-1** The repository MUST build two reference root-volume images for amd64 and arm64,
  published to `ghcr.io/liquidmetal-dev/muster-runner` with tags
  `<variant>-<base>-<runner version>` and `<variant>-latest`: `minimal` and `full`, where
  `full` is layered on `minimal`.
- **IMG-2** `minimal` MUST contain: Ubuntu 24.04 with systemd as init; the liquidmetal
  guest-agent enabled at boot; the GitHub Actions runner at `/opt/actions-runner` owned by
  user `runner` (uid 1000) with passwordless sudo; `/usr/local/bin/muster-runner`, which
  reads the encoded JIT configuration from stdin and `exec`s `run.sh --jitconfig`; dockerd
  and containerd started at boot with `runner` in the `docker` group; git, curl, jq, tar,
  zstd, build-essential and ca-certificates; DHCP networking with no baked MAC or IP
  address.
- **IMG-3** `full` MUST provide parity with GitHub's hosted `ubuntu-latest` toolset, built
  from `ghcr.io/catthehacker/ubuntu:act-24.04` plus the `minimal` layer, and MUST document
  the delta from hosted runners.
- **IMG-4** The image contract (paths, user, entrypoint behaviour, required services) MUST
  be documented. Muster MUST NOT depend on anything beyond that contract, so that users can
  bring their own image.
- **IMG-5** Image CI MUST rebuild and publish on source changes and on new `actions/runner`
  releases (a scheduled check at least weekly).
- **IMG-6** Image builds SHOULD be smoke-testable without flintlock (boot under systemd in a
  container or qemu; assert that guest-agent and dockerd start and `muster-runner --check`
  succeeds).
- **KRN-1** Muster MUST provide a verified reference guest kernel for amd64 and arm64, for
  Firecracker and for Cloud Hypervisor. Each one MUST be a kernel that
  liquidmetal-dev/mikrolite-images publishes, pinned by its immutable tag. It MUST have the
  options Docker needs (cgroup v2, overlayfs, bridge, veth, netfilter, nf_tables) and the
  hypervisor's guest requirements built in, laid out as flintlock expects. Muster MUST NOT
  build a kernel of its own. The ADR `docs/adr/2026-10-27-guest-kernel.md` records which
  tags are verified. For a combination with no verified tag, the ADR MUST list the open
  work and the issue that tracks it.
- **KRN-2** A spike MUST first establish whether the published liquidmetal kernels already
  satisfy KRN-1. Its outcome MUST be recorded as an ADR.

## 7. Reliability and recovery

- **REL-1** Muster MUST hold no persistent state of its own. Battery leases and GitHub
  runners are the sources of truth.
- **REL-2** On startup muster MUST reconcile: for each lease in its namespace whose
  `request_id` matches a runner named for the same session that GitHub still lists, muster
  MUST adopt the session as RUNNING and track it to completion through GitHub state and
  webhooks; leases with no such runner MUST be released; runners with the muster prefix and
  no lease MUST be deleted.
- **REL-3** Muster MUST subscribe to battery `Events` for its pools and MUST treat VM
  deletion events for a session's VM as session failure.
- **REL-4** All outbound calls MUST have timeouts and MUST be retried with jittered
  exponential backoff where idempotent.
- **REL-5** Configuration MUST be validated at startup (schema, pool uniqueness, labels,
  host existence and exec enablement, GitHub credential reach). `musterd` MUST exit non-zero
  with a clear message on failure, and `musterctl config validate` MUST perform the same
  checks offline where possible.
- **REL-6** A restart MUST NOT abort in-flight jobs (a consequence of REL-2). Documentation
  MUST describe `drain --wait` followed by a restart as the upgrade procedure.

## 8. Security

- **SEC-1** Webhook signatures MUST be verified (INTAKE-1).
- **SEC-2** Connections to battery, to flintlock and to the muster API MUST support TLS and
  mutual TLS, configured globally with optional per-host overrides keyed by battery host
  name. Plaintext MUST require an explicit `insecure: true`.
- **SEC-3** Secrets (App private key, token, webhook secret, TLS keys) MUST be loadable from
  files or environment variables and MUST NOT be logged.
- **SEC-4** The microVM is the isolation boundary: one job per VM, no reuse, deleted after
  use. Muster MUST NOT expose any guest-to-muster path.
- **SEC-5** `musterd` SHOULD run as a non-root user. The shipped systemd unit MUST set
  hardening options appropriate for a network service.

## 9. API, CLI and observability

- **API-1** Muster MUST expose a gRPC API defined with buf under
  `api/proto/muster/v1alpha1`, published to the Buf Schema Registry on release, with
  services for sessions (list, get, release), pools (status: desired, active, busy, queued,
  warm) and admin (drain with optional wait, undrain).
- **API-2** `musterctl` MUST wrap that API with `session list|get|release`, `pool status`,
  `drain [--wait]`, `undrain` and `config validate`. Any API change MUST update `musterctl`
  in the same change.
- **OBS-1** Muster MUST expose Prometheus metrics: sessions by state and pool; claim
  attempts and exhaustions; claim latency; queued-to-online latency; session duration;
  releases and failures by reason; GitHub API calls and errors; webhook deliveries by
  action.
- **OBS-2** Muster MUST emit structured logs (`log/slog`) with session id, pool, job id and
  runner name as fields.
- **OBS-3** Muster MUST expose `/healthz` (process alive) and `/readyz` (configuration
  loaded, battery reachable, GitHub authentication works).

## 10. Packaging and operations

- **OPS-1** Releases MUST publish `musterd` and `musterctl` binaries for linux/amd64 and
  linux/arm64, a multi-arch container image `ghcr.io/liquidmetal-dev/musterd`, and `.deb`
  and `.rpm` packages carrying the systemd unit and default configuration path, through
  goreleaser on semver tags.
- **OPS-2** Configuration MUST be a single JSON file (`-config`). Changes take effect on
  restart.
- **OPS-3** The repository MUST include a quick start (flintlock, battery, muster and one
  repository), a configuration reference, the image contract, kernel notes, and a manual
  end-to-end runbook.

## 11. Project conventions and testing

- **PROJ-1** The project uses the latest stable Go release (1.27 at the time of writing).
  `mise.toml` and `go.mod` MUST be bumped within a release cycle of each new Go minor.
  `mise.toml` pins go, golangci-lint, buf and goreleaser. Commits follow Conventional
  Commits. Commits and pull requests MUST NOT carry agent footers. CI MUST run build, vet,
  lint, test and proto checks on every pull request with SHA-pinned actions.
- **PROJ-2** Unit tests MUST use in-process fakes for battery, flintlock exec and GitHub.
- **PROJ-3** The repository MUST contain a fake GitHub server (`internal/fakegithub`) that
  implements every endpoint muster uses (App installation token, JIT configuration for
  organization and repository, runner list and delete, runner groups, queued workflow runs
  and jobs), signs and delivers `workflow_job` webhooks, and simulates runner lifecycle
  (online, busy, completed, deregistered) on configurable timers. It MUST be usable from
  unit tests and from the e2e suite.
- **PROJ-4** The repository MUST contain an e2e suite (`test/e2e`, build tag `e2e`) that
  runs a real `musterd` against a real `poolmgrd` and a real `flintlockd` with Firecracker,
  using the fake GitHub and an `e2e` image variant whose `run.sh` is a stub, and asserts at
  least: a queued job produces a claim, an exec and a runner registration; completion
  releases the lease and deletes the VM; `RESOURCE_EXHAUSTED` backs off and recovers; a
  restart adopts an in-flight session; drain stops new sessions. It MUST run in
  GitHub-hosted CI on a schedule and on demand.
- **PROJ-5** Real-GitHub end-to-end coverage MUST be a `tests/muster/` suite in
  liquidmetal-dev/acceptance-tests that provisions flintlock hosts, poolmgrd and musterd,
  registers against a GitHub test repository, dispatches a workflow, and asserts that the
  job ran inside a microVM and that the VM was deleted.
- **PROJ-6** Decisions with lasting consequence MUST be recorded as ADRs in `docs/adr/`.

## 12. Future: snapshot-restore source

- **FUT-1** The VM source interface MUST be sufficient for a flintlock snapshot-restore
  implementation (acquire, exec, keepalive, release, events) and MUST NOT leak battery types
  into session logic.
- **FUT-2** When flintlock ships snapshots, a `snapshot` source SHOULD restore from a
  per-pool snapshot package and MUST handle guest-side network re-identification as the
  flintlock proposal requires. This is tracked, not built, in v1.

## References

- battery API: `api/proto/poolmgr/v1alpha1` in liquidmetal-dev/battery (`Lease`,
  `PoolAdmin`, `Events`, `HostAdmin`).
- flintlock exec: `api/services/microvmexec/v1alpha1` in liquidmetal-dev/flintlock, gated
  by `flintlockd --enable-exec-api`.
- flintlock snapshot proposal: `docs/proposals/26-09-25-snapshot-restore/` in
  liquidmetal-dev/flintlock (issue #204).
- GitHub: [Create configuration for a just-in-time runner](https://docs.github.com/en/rest/actions/self-hosted-runners),
  [Autoscaling with self-hosted runners](https://docs.github.com/en/actions/hosting-your-own-runners/managing-self-hosted-runners/autoscaling-with-self-hosted-runners).
