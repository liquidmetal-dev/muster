# ADR: battery is the v1 VM source, behind a replaceable interface

- Status: accepted
- Date: 2026-10-27
- Requirements: GOAL-1, GOAL-3, POOL-2, POOL-8, FUT-1, FUT-2

## Context

A GitHub Actions job should start within seconds of being queued. Booting a microVM from
scratch through flintlock takes long enough to matter, and flintlock has no snapshot/restore
today; only a requirements proposal exists
(`docs/proposals/26-09-25-snapshot-restore/` in liquidmetal-dev/flintlock, issue #204).

battery (liquidmetal-dev/battery) keeps pools of pre-booted microVMs on flintlock hosts and
hands them out through a lease API. Releasing a lease deletes the VM, which is exactly the
one-job-per-VM semantics muster wants.

## Decision

Muster obtains VMs from battery in v1. All battery-specific code lives behind
`internal/vmsource.Source` and `internal/vmsource.VM` (acquire, exec, keepalive, release,
leases, events). Session logic never sees battery or flintlock types.

Muster owns its pools: it declares them in its configuration and applies them to battery
through `PoolAdmin` on startup. It never manages flintlock hosts in battery.

Muster starts the runner through flintlock's `MicroVMExec` on the host address battery
returns with the claim, which requires `flintlockd --enable-exec-api` on every host.

## Consequences

- Operators run battery as well as flintlock. The quick start covers both.
- VM boot cost is paid ahead of time by battery's replenishment, not by the job.
- When flintlock ships snapshots, a `snapshot` implementation of `Source` restores from a
  per-pool snapshot package. The interface already carries what it needs; guest-side
  network re-identification is the main new work.
- Pool capacity is bounded by `size + max_concurrent` per pool because the default
  strategy is `IMMEDIATE_ON_LEASE`.
