# Upgrading musterd

musterd keeps no state of its own (REL-1). Battery leases and GitHub runners are the record of
every session. A restart does not stop jobs in flight (REL-6): the next start finds them and
adopts them.

## What SIGTERM does

1. musterd stops making new sessions.
2. It waits for busy sessions (sessions whose runner has a job) to end, for at most
   `shutdown_grace`. The default is `30s`. A session that ends during the wait is cleaned up as
   usual: its VM is released and its runner is deleted.
3. When no session is busy, or when the grace ends, musterd exits. It leaves every session that
   is still active as it is. It does not release their VMs and it does not delete their
   runners.

Set `shutdown_grace` lower than the stop timeout of your process supervisor, for example the
systemd `TimeoutStopSec` or the Kubernetes `terminationGracePeriodSeconds`. If the supervisor
kills musterd first, the result is the same as the end of the grace.

## What the next start does

Before musterd accepts any demand, it reconciles (REL-2). It lists the battery leases in its
namespace and the GitHub runners whose names start with `muster-<namespace>-`, in the scope of
each configured pool.

- A lease and the runner named for the lease's pool and session id become an adopted session.
  musterd tracks it in RUNNING until an end signal: the job completes, the runner disappears
  from GitHub, the idle timeout, or the maximum session duration.
- A lease with no such runner is released.
- A runner with no such lease is deleted, if its name is `muster-<namespace>-<pool>-<session id>`
  for a configured pool or for a pool that one of the leases names. Other runners are left
  alone. This keeps musterd away from the runners of a second instance whose namespace starts
  with this namespace and a hyphen (for example `muster` and `muster-x`).

If musterd cannot read the lease or runner lists within 2 minutes, it exits with an error, so
the supervisor can restart it. If one release or delete still fails after 3 tries, musterd logs
it at `WARN` and continues. The next restart tries again. An orphan lease also expires on its
own, because nothing sends it heartbeats.

## Upgrade procedure

1. Run `musterctl drain --wait`. musterd stops making new sessions and waits until the busy
   sessions end. (`musterctl drain` comes with the gRPC API.)
2. Stop musterd.
3. Replace the binary or the image.
4. Start musterd. It adopts the sessions that are still in flight.

A plain restart without step 1 is also safe: the sessions that are still in flight are adopted.
The drain only makes sure that no new session starts during the upgrade.

## Removing a pool or changing its scope

Drain the pool first, and wait until its sessions have ended. musterd lists runners only in the
scopes of the pools that are configured now, and it deletes a runner of a removed pool only
when a lease of that pool is still there. Other runners of a removed pool stay until you delete
them.

## Two instances

Two musterd instances that share a battery and a GitHub scope must use different namespaces
(`battery.namespace`). Each instance reconciles its own namespace only.
