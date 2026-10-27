# Muster design

- Status: proposed
- Date: 2026-10-27
- Requirements: [2026-10-27-muster-requirements.md](2026-10-27-muster-requirements.md)
- ADRs: [battery as the VM source](../adr/2026-10-27-battery-as-vm-source.md),
  [stateless musterd](../adr/2026-10-27-stateless-musterd.md),
  [guest kernel](../adr/2026-10-27-guest-kernel.md)

## 1. Overview

Muster turns queued GitHub Actions jobs into one-shot microVMs. Two binaries:

- `musterd`: the daemon. It learns about queued jobs from GitHub (webhooks, with polling
  as a fallback), claims pre-booted microVMs from battery, starts a just-in-time (JIT)
  GitHub runner inside each one through flintlock's exec API, and releases the VM when the
  runner exits. Battery deletes released VMs and keeps the pool warm.
- `musterctl`: a CLI over musterd's gRPC API for inspecting sessions and pools and for
  draining before an upgrade.

Muster keeps no database. Battery holds the leases and GitHub holds the runners; on startup
muster rebuilds its view from those two and adopts sessions that were in flight.

```
GitHub ──webhook/poll──▶ intake ──queued jobs──▶ dispatcher ──▶ session ──▶ vmsource (battery)
                                                     │              │           ├─ poolmgrd  Lease / PoolAdmin / Events
                                                     ▼              ▼           └─ flintlockd MicroVMExec (per host)
                                              in-memory registry  github client
                                                     ▲
musterctl ──gRPC──▶ api (Sessions, Pools, Admin) ────┘
```

Inside the VM: the image's `muster-runner` entrypoint reads the encoded JIT configuration
from stdin and execs `run.sh --jitconfig`. The runner registers, takes one job, deregisters
and exits. The exec stream's exit code is muster's primary end-of-session signal.

## 2. Packages

| Package | Responsibility |
|---|---|
| `cmd/musterd`, `cmd/musterctl` | flags, wiring, signals (SIGTERM drains, then exits after a bounded wait) |
| `api/proto/muster/v1alpha1` | buf-managed protos: `Sessions`, `Pools`, `Admin` |
| `internal/config` | JSON schema, defaults, validation, protojson decode of `MicroVMSpec` templates |
| `internal/github` | App and token auth, JIT config, runners list/delete, runner groups, queued-job listing; rate-limit aware client built on `google/go-github` |
| `internal/intake/webhook` | HTTP(S) handler, HMAC verification, delivery-id TTL set, `workflow_job` parsing |
| `internal/intake/poller` | periodic queued-job listing per configured repository |
| `internal/demand` | per-pool registry of queued jobs fed by both intakes; computes `desired(pool)` |
| `internal/dispatch` | level-triggered loop: `desired - active_not_busy`, `max_concurrent`, drain flag |
| `internal/session` | per-session state machine and in-memory registry |
| `internal/vmsource` | `Source` and `VM` interfaces |
| `internal/vmsource/battery` | `Lease` and `Events` clients plus flintlock `MicroVMExec` with per-host TLS |
| `internal/pools` | reconciles configured pools into battery `PoolAdmin`; host exec-enabled check |
| `internal/reconcile` | startup adoption and cleanup (REL-2) |
| `internal/api` | gRPC service implementations |
| `internal/musterctl` | CLI commands and output |
| `internal/metrics`, `internal/server` | Prometheus registry, `/healthz`, `/readyz`, webhook mux |
| `internal/fakegithub` | fake GitHub server for unit and e2e tests (section 10) |
| `test/e2e` | build-tagged suite against real flintlockd and poolmgrd with the fake GitHub |

## 3. The `vmsource` seam

This is the interface that lets a future flintlock snapshot-restore source replace battery
without touching GitHub-facing code (GOAL-3, FUT-1).

```go
type Source interface {
    // Acquire returns a booted VM for the pool. requestID makes retries idempotent.
    // Returns ErrExhausted when nothing is available right now.
    Acquire(ctx context.Context, pool, requestID string) (VM, error)
    Release(ctx context.Context, vm VM) error
    // Leases lists what the source currently holds for this muster instance (startup reconciliation).
    Leases(ctx context.Context) ([]LeaseInfo, error)
    // Events reports VM loss or impending expiry for a pool.
    Events(ctx context.Context, pool string) (<-chan Event, error)
}

type VM interface {
    ID() string
    Exec(ctx context.Context, spec ExecSpec) (ExecSession, error) // stdin writer, stdout/stderr readers, Wait() exit code
    KeepAlive(ctx context.Context) error
    KeepAliveInterval() time.Duration
}
```

Battery implementation: `Acquire` is `ClaimVM`; `Exec` dials the flintlock host from the
claim's `host.address` using the global or per-host TLS settings and runs `ExecCommand`;
`KeepAlive` is `Heartbeat`; `Release` is `ReleaseVM`; `Leases` is `ListLeases`; `Events` is
`Events.Subscribe` filtered to lease-affecting types.

Future snapshot implementation: `Acquire` is `CreateMicroVM` from the pool's snapshot
package followed by waiting for `CREATED` and guest-agent readiness; `Release` is
`DeleteMicroVM`; `KeepAlive` is a no-op; `Events` is derived from `ListMicroVMsStream`.

## 4. Session state machine

```
PENDING ─dispatch─▶ CLAIMING ─Acquire ok─▶ STARTING ─exec open, JIT sent, runner online─▶ RUNNING ─end signal─▶ ENDED ─Release + DeleteRunner─▶ RELEASED
                        │ exhausted: back off while desired > active; claim deadline → FAILED
                        └── from any active state: VM lost event or fatal error ─▶ FAILED ─cleanup─▶ RELEASED
```

Order of operations in STARTING: claim first, then request the JIT configuration, so an
exhausted pool never burns runner registrations. Open the exec stream with `has_stdin=true`
as the image's runner user, write the encoded configuration, send EOF. The session is RUNNING
once GitHub lists the runner as online or an `in_progress` event names it, within the start
timeout (default 2 minutes); otherwise it is FAILED and cleaned up.

End signals (SESSION-7), first one wins: exec exit code; `workflow_job.completed` naming the
runner plus a grace period; runner missing from the scoped `ListRunners`; idle timeout
(online but never busy); maximum session duration. Adopted sessions (section 9) enter
RUNNING with no exec stream and rely on the other signals.

Cleanup is idempotent: `ReleaseVM`, then delete the runner if GitHub still lists it. Both
retry with backoff until they succeed or the process exits; a process exit leaves the
startup reconciliation to finish the job.

## 5. Demand model

A JIT runner is not bound to the job that triggered it: any queued job whose labels are a
subset of the runner's labels may land on it. Muster therefore does not map jobs to sessions
one to one. Per pool, on every intake event and on a timer:

- `desired(pool)` = queued jobs known from webhooks or polling whose labels match the pool
  (POOL-5) and whose repository is within the pool's scope.
- `active(pool)` = sessions in PENDING, CLAIMING, STARTING or RUNNING whose runner has not
  yet been seen busy.
- If `desired > active` and `max_concurrent` allows, create `desired - active` sessions.
  Never destroy a session because `desired` dropped; the idle timeout reclaims runners that
  never receive a job.

Queued-job records close on `in_progress` or `completed`, on a poll that no longer lists
them, or after 24 hours (GitHub's own queue limit). In webhook-only mode the first two are
the only closing signals. Net effect: the number of not-yet-busy runners tracks the queue
depth per pool with bounded over-provisioning.

## 6. Pool reconciliation

At start: `HostAdmin.ListHosts`, then flintlock `ServerInfo` on each host to confirm
`exec.enabled` (POOL-4). For each configured pool, `GetPool` then `CreatePool` or
`UpdatePool` with: namespace `muster` (configurable), the template with
`allow_guest_agent=true`, `size`, `flintlock_hosts`, strategy `IMMEDIATE_ON_LEASE` unless
overridden, heartbeat settings from defaults, and `create_commands` passed through. Pools
found in the namespace but absent from configuration are logged and deleted only with
`--prune-pools` (without `force`, so leased VMs block deletion).

`max_concurrent` defaults to twice `size`. With `IMMEDIATE_ON_LEASE`, battery keeps `size`
VMs warm regardless of how many are leased, so total VMs per pool is bounded by
`size + max_concurrent`.

## 7. Configuration

A single JSON file. The `microvm_template` is a flintlock `MicroVMSpec` in protojson, so it
can be copied from any flintlock example. Exactly one of `scope.org` or `scope.repo` per
pool. Every pool and every polled repository must be reachable by the single configured
GitHub credential (GH-3).

```json
{
  "listen": {"addr": ":8443", "tls": {"cert_file": "/etc/muster/tls.crt", "key_file": "/etc/muster/tls.key"}},
  "api": {"addr": ":9443", "tls": {"insecure": true}},
  "metrics_addr": ":9090",
  "github": {
    "api_base_url": "https://api.github.com",
    "app": {"app_id": 123, "installation_id": 456, "private_key_file": "/etc/muster/app.pem"},
    "webhook_secret_file": "/etc/muster/webhook.secret",
    "poll": {"enabled": true, "interval": "60s", "repos": ["liquidmetal-dev/muster"]}
  },
  "battery": {"addr": "poolmgrd.example.com:8443", "tls": {"ca_file": "/etc/muster/battery-ca.pem"}, "namespace": "muster"},
  "flintlock": {"tls": {"insecure": true}, "hosts": {"host-a": {"tls": {"ca_file": "/etc/muster/host-a-ca.pem"}}}},
  "defaults": {"claim_deadline": "10m", "start_timeout": "2m", "idle_timeout": "5m", "max_session_duration": "6h"},
  "pools": [
    {
      "name": "ubuntu-small",
      "scope": {"org": "liquidmetal-dev", "runner_group": "Default"},
      "labels": ["self-hosted", "linux", "x64", "muster", "ubuntu-24.04"],
      "size": 3,
      "max_concurrent": 6,
      "flintlock_hosts": ["host-a"],
      "create_commands": ["docker info"],
      "microvm_template": {
        "vcpu": 2,
        "memoryInMb": 4096,
        "kernel": {"image": "ghcr.io/liquidmetal-dev/firecracker-kernel:6.1-20260929-1349", "filename": "boot/vmlinux"},
        "rootVolume": {"id": "root", "source": {"containerSource": "ghcr.io/liquidmetal-dev/muster-runner:minimal-latest"}},
        "interfaces": [{"deviceId": "eth0", "type": "TAP"}]
      }
    }
  ]
}
```

A `scope` of `{"repo": "owner/name"}` registers runners at repository scope. The token path
replaces `github.app` with `github.token_file`.

## 8. Images and kernel

Sources live under `images/runner/`.

- `minimal`: a Dockerfile from `ubuntu:24.04` with systemd as init, guest-agent from the
  liquidmetal apt repository, the Actions runner tarball pinned by a per-architecture
  `RUNNER_VERSION` build argument, docker-ce and containerd, the `runner` user, the
  `muster-runner` entrypoint, and systemd-networkd DHCP. The build flattens the result to a
  rootfs volume image as flintlock expects.
- `full`: `FROM ghcr.io/catthehacker/ubuntu:act-24.04` (pinned by digest) with the
  `minimal` layer applied on top, plus a documented delta from GitHub's hosted runners.
- `e2e`: `minimal` with `/opt/actions-runner/run.sh` replaced by a stub (section 10).
- Kernel: the published `ghcr.io/liquidmetal-dev/firecracker-kernel` image at the immutable
  tag that `docs/adr/2026-10-27-guest-kernel.md` names. Muster builds no kernel.
- CI: an amd64/arm64 build matrix on GitHub-hosted runners, a weekly check for new
  `actions/runner` releases, and a smoke boot test under systemd.

Operator notes to document: hosts pull images through containerd, so private registries
need host-side credentials; the `full` image is large, so size the containerd thin pool
accordingly.

The image contract (IMG-4) is: guest-agent on vsock; `/usr/local/bin/muster-runner` as the
entrypoint reading the encoded JIT configuration from stdin; user `runner` with uid 1000
owning `/opt/actions-runner`; dockerd available to `runner`; outbound HTTPS to GitHub.
Muster relies on nothing else.

## 9. Startup and reconciliation

Startup sequence: load configuration → validate → GitHub authentication check → connect to
battery → host checks → pool reconciliation → session reconciliation → start intakes,
dispatcher and API → ready.

Session reconciliation (REL-2) joins three lists: battery `ListLeases` in the muster
namespace, GitHub `ListRunners` filtered to the `muster-<namespace>-` prefix, and nothing
else (muster has no store). A lease whose `request_id` matches a runner name's session id is
adopted as a RUNNING session. A lease with no matching runner is released. A runner with no
matching lease is deleted. Adoption is what makes `drain --wait` followed by a restart a safe
upgrade (REL-6).

## 10. Fake GitHub and the e2e suite

`internal/fakegithub` is an `httptest`-style server with an in-memory model of
organizations, repositories, runner groups, runners and jobs. It implements:
`POST /app/installations/{id}/access_tokens`; `POST /orgs/{org}/actions/runners/generate-jitconfig`
and the repository form; `GET` and `DELETE` on runners; `GET /orgs/{org}/actions/runner-groups`;
`GET /repos/{owner}/{repo}/actions/runs?status=queued` and `/runs/{id}/jobs`. Test code
calls `QueueJob(repo, labels)`, which stores the job and POSTs a signed
`workflow_job.queued` to musterd's webhook URL. After a JIT configuration is issued, the fake
advances that runner on timers: online, then busy on the oldest queued matching job (emitting
`in_progress`), then `completed`, then deregistered. Per-test overrides hold a runner idle,
make it never register, or complete early. The encoded JIT configuration is a JSON blob that
the e2e stub understands.

`test/e2e` (build tag `e2e`) brings up `flintlockd` (Firecracker, `--enable-exec-api`),
`poolmgrd`, the fake GitHub and `musterd` on one host, using the `e2e` image variant, whose
stub `run.sh` parses `--jitconfig`, sleeps for the encoded duration and exits with the encoded
code. Scenarios assert through battery `ListLeases`, flintlock `ListMicroVMs`, the fake
GitHub's state, and muster's metrics and gRPC API. CI runs the suite nightly and on
`workflow_dispatch` on `ubuntu-latest`, where `/dev/kvm` is available, and on pull requests
that carry an opt-in label.

The real-GitHub end-to-end suite lives in liquidmetal-dev/acceptance-tests (PROJ-5) and is
the project's dogfooding.

## 11. Observability

Prometheus metrics (OBS-1) are registered in `internal/metrics` and served on
`metrics_addr`. Logs use `log/slog` with session id, pool, job id and runner name as fields.
`/healthz` reports process liveness; `/readyz` reports that configuration is loaded, battery
is reachable and GitHub authentication works.

## 12. Risks

- The KRN-2 spike verified Docker on the published 6.1 Firecracker kernel on amd64
  (`docs/adr/2026-10-27-guest-kernel.md`). arm64 and Cloud Hypervisor are not verified.
- `catthehacker/ubuntu` is a community image. It is pinned by digest and the `minimal` path
  does not depend on it.
- arm64 doubles image CI time. GitHub-hosted arm64 runners are free for public
  repositories.
- A muster restart drops exec streams. Adoption covers it, but end detection for adopted
  sessions depends on GitHub state and is slower.
- Runner auto-update at start can add minutes when the image's runner lags GitHub's latest.
  The weekly image rebuild (IMG-5) keeps the gap small.

## 13. Milestones

- `v0.1.0`: first job end to end. musterd with the battery source, webhook and polling
  intake, count-based dispatch, sessions, startup reconciliation, metrics, the `minimal`
  image on amd64, the fake GitHub and the e2e suite, binaries and container, quick start.
- `v0.2.0`: `full` image, arm64 images and an arm64 kernel from mikrolite-images, gRPC API
  and `musterctl`, `.deb` and `.rpm` packages, built-in webhook TLS.
- `v0.3.0`: the acceptance-tests suite.
