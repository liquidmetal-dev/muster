# ADR: muster owns the specs of its pools, and never forces a prune

- Status: accepted
- Date: 2026-11-04
- Requirements: POOL-2, POOL-3, POOL-4, POOL-7

## Context

Battery stores a full `PoolSpec` for each pool. An operator can change a pool directly in
battery, for example with `poolmgr` or a script. Muster's configuration also declares the
pool. At startup the two can disagree, and some pools in muster's namespace can be absent
from the configuration.

Muster could compare only the fields that it sets, and keep the other fields as battery has
them. But `UpdatePool` replaces the whole spec, so a field that muster does not send is
cleared on the next update anyway.

## Decision

The configuration is the only source of a pool's spec. At startup, pool reconciliation
builds the full desired `PoolSpec` and compares it with battery's spec. When any field is
different, muster sends `UpdatePool` with the full desired spec. A change made directly in
battery is overwritten.

An orphan pool is logged. Muster prunes it only when `--prune-pools` is set, and always
without `force`. A pool that has leased VMs stays until a later start.

Muster reads hosts from `HostAdmin` and never calls a `HostAdmin` mutation.

## Consequences

- To change a pool, operators change muster's configuration and restart musterd. A manual
  change in battery lasts only until the next start.
- Muster sets every spec field, including fields that it does not otherwise use
  (`hook_failure_policy` is always `DELETE_AND_REPLACE`, `pre_lease_commands` is always
  empty).
- A prune never ends a running job. Removing a pool that is in use needs a drain first
  (`docs/operations/upgrade.md`).
