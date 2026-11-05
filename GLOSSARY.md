# Muster

Muster turns queued GitHub Actions jobs into one-shot microVM runners. These terms name the
things muster tracks and the events that move them.

## Sessions

**Session**:
Muster's unit of work: one claimed VM, one just-in-time runner, at most one job.
_Avoid_: Lease, job, runner (each names only one part of a session)

**Session id**:
The short random identifier of a session. It is both the claim's request id and the suffix
of the runner name, so a lease and a runner that share it belong to the same session.
_Avoid_: Short id, request id

**Runner name**:
The GitHub runner name of a session: `muster-<namespace>-<pool>-<session id>`.

**Active session**:
A session in PENDING, CLAIMING, STARTING or RUNNING.

**Busy**:
A session whose runner GitHub has reported running a job at least once. A busy session
stays busy until it ends.
_Avoid_: In use, assigned

**End signal**:
An observation that a session's runner is finished: exec exit, job completed plus grace,
runner absent from GitHub, idle timeout, or maximum session duration. The first end signal
ends the session.

**Finished session**:
A session that has reached ENDED, after an end signal, or FAILED, for a failure reason. Its
outcome is that state and its reason says why.
_Avoid_: Done, completed, ended (for a failed session)

**Adopted session**:
A session muster finds in flight at startup and resumes in RUNNING without an exec stream.

## Intake

**Intake**:
A source from which musterd learns which GitHub jobs are queued.
_Avoid_: Source, feed, ingest

**Webhook intake**:
The intake that receives the `workflow_job` deliveries that GitHub sends to musterd.
_Avoid_: Listener, receiver

**Polling intake**:
The intake that asks GitHub at an interval for the queued jobs of each configured repository.
_Avoid_: Poller, reconciler, sync

**Poll pass**:
One run of the polling intake over all its configured repositories.
_Avoid_: Poll cycle, sweep, tick

**Listing**:
The set of queued jobs that one repository returned during a poll pass, with the time musterd
asked for it.
_Avoid_: Snapshot, poll result

## Pools

**Pool**:
A named set of warm VMs in battery, on named flintlock hosts, that muster keeps for the jobs
of one pool scope whose labels the pool has.
_Avoid_: Runner group, fleet

**Pool scope**:
The GitHub organization or repository whose jobs a pool serves and where its runners register.
_Avoid_: Target, owner

**Namespace**:
The battery namespace that holds the pools and leases of one musterd instance. Two instances
that share a battery use different namespaces.
_Avoid_: Tenant, prefix

## Startup and shutdown

**Pool reconciliation**:
The startup pass that makes the pools in muster's namespace equal the configured pools, before
session reconciliation runs.
_Avoid_: Pool sync, apply

**Orphan pool**:
A pool in muster's namespace that the configuration does not name.
Pool reconciliation logs it, and prunes it only when the operator asks.

**Prune**:
To delete an orphan pool. A prune never forces: a pool with leased VMs stays until a later start.
_Avoid_: Clean up, garbage-collect

**Session reconciliation**:
The startup pass that pairs each of muster's leases with the runner named for the lease's
pool and session id, before any demand is taken: pairs become adopted sessions, and orphans
are removed.
_Avoid_: Reconciliation (unqualified), sync, recovery

**Orphan lease**:
A lease in muster's namespace with no listed runner named for its pool and session id, or a
lease muster cannot track (its pool is no longer configured, or its request id is not a
session id).
Session reconciliation releases it.

**Orphan runner**:
A runner named for muster's namespace, one of its pools and a session id, that is not the
runner named for any lease's pool and session id. A pool is muster's when it is configured or
when one of muster's leases names it.
Session reconciliation deletes it.

**Ready**:
musterd has completed startup, and its latest battery and GitHub probes succeeded. A
musterd that is alive is not necessarily ready.
_Avoid_: Healthy, up

**Shutdown grace**:
How long muster waits after SIGTERM for busy sessions to finish before it exits. Sessions
still active at exit are left running, to be adopted by the next start.
_Avoid_: Drain timeout

## Images

**Runner image**:
The root volume that a session's VM boots, holding the runner and the services it needs.
_Avoid_: Rootfs, template, base image

**Image contract**:
The paths, user, entrypoint and services that muster relies on in a runner image, and
nothing more. Any image that meets it can serve a pool.
_Avoid_: Image spec, image requirements

**Image variant**:
One of the reference runner images that the repository builds: `minimal`, `full` or `e2e`.
_Avoid_: Flavour, edition
