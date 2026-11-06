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

1. Stop musterd: `sudo systemctl stop musterd`, or `docker stop -t 60 musterd` for a container.
   SIGTERM drains musterd, as [What SIGTERM does](#what-sigterm-does) describes: it starts no
   new session and waits for busy sessions for at most `shutdown_grace`. The `-t` value must be
   greater than `shutdown_grace`, because the default `docker stop` timeout is 10 seconds.
2. Replace the binary or the package. For a container, remove it with `docker rm musterd`: a
   container keeps the image that it was made from.
3. Start musterd: `sudo systemctl start musterd`, or for a container, `docker run` with the new
   image tag, as in [install.md](install.md#in-a-container). musterd adopts the sessions that
   are still in flight.

> From v0.2.0 ([#24](https://github.com/liquidmetal-dev/muster/issues/24)), `musterctl drain
> --wait` stops new sessions and waits for the busy sessions before you stop musterd, with no
> limit from `shutdown_grace`.

### Jobs queued during the upgrade

musterd starts no session for a job that GitHub queues during the drain or while musterd is
stopped. The intakes stop at SIGTERM. A webhook delivery in that time fails, or, if it arrives
while the webhook intake stops, musterd accepts it but makes no session. GitHub has no
endpoint that lists queued jobs, so the next start does not find these jobs by itself.

- With the polling intake on, the next start finds the queued jobs of the repositories in
  `github.poll.repos`.
- With the webhook intake only, or for a repository that `github.poll.repos` does not list,
  redeliver the `workflow_job` delivery with the action `queued` of each such job. Use the
  **Advanced** page of the GitHub App settings. You can also turn on the polling intake next to
  the webhook intake.

[#57](https://github.com/liquidmetal-dev/muster/issues/57) tracks a fix that needs no manual
step.

## Removing a pool or changing its scope

musterd reads its configuration only at startup, and v0.1.0 has no drain for one pool. When
musterd starts and a lease names a pool that is no longer configured, it releases the lease.
If the job of that session still runs, the job loses its VM
([#56](https://github.com/liquidmetal-dev/muster/issues/56)). So make sure that the pool has no
session before you change the configuration:

1. Move the workflows that use the pool's labels to another pool, or pause them. Cancel their
   runs that are still queued: a moved workflow does not move a job that is already queued.
2. Wait until the metric `muster_sessions{pool="<pool>"}` is 0 for every `state`
   ([metrics](observability.md#metrics)).
3. Stop musterd.
4. While musterd is stopped, check that battery has no lease of the pool:

   ```sh
   poolmgrctl lease list --pool <pool> --namespace <namespace>
   ```

   musterd cannot make a session while it is stopped, so this check is final. If a lease is
   there, a job got a session after step 2. Start musterd with the configuration unchanged (it
   adopts the session), and do steps 2 to 4 again.
5. Remove the pool from the configuration, or change its scope.
6. Start musterd.
   - **Removal only.** Start musterd once with `-prune-pools` to delete the battery pool,
     which is now an orphan pool. Use a drop-in file of its own, and then delete only that
     file:

     ```sh
     sudo mkdir -p /etc/systemd/system/musterd.service.d
     printf '[Service]\nExecStart=\nExecStart=/usr/bin/musterd -config /etc/muster/musterd.json -prune-pools\n' |
       sudo tee /etc/systemd/system/musterd.service.d/prune-pools.conf
     sudo systemctl daemon-reload
     sudo systemctl start musterd
     journalctl -u musterd | grep 'orphan pool'
     sudo rm /etc/systemd/system/musterd.service.d/prune-pools.conf
     sudo systemctl daemon-reload
     ```

     The empty `ExecStart=` line is necessary: without it, systemd adds a second command. Do
     not use `systemctl revert`, because it also deletes your own drop-in files.
     `-prune-pools` acts only at startup, so the next start, without the file, does not prune.
     For a container, add `-prune-pools` after the image name for one start.

     `-prune-pools` deletes every orphan pool in the namespace, not only this one. A pool that
     still has leased VMs stays, and the log shows
     `orphan pool still has leased VMs; a later start prunes it`. Do this step again later for
     that pool.
   - **Scope change.** A plain start. The pool is still configured, so it is not an orphan.
7. In the scope that the pool used (its old scope after a scope change), delete the runners of
   the pool that are left. Do this also when another pool shares that scope. musterd lists
   runners only in the scopes of the configured pools, and it deletes a runner only for a
   configured pool or for a pool that a lease names.

   A runner name of the pool is `muster-<namespace>-<pool>-` and a 12-character session id of
   the characters `a-z` and `2-7`. Match the full name, not only its start: the start alone also
   matches the runners of a pool such as `<pool>-x`. For a repository scope (use
   `orgs/<org>` for an organization scope):

   ```sh
   gh api --paginate repos/<owner>/<repo>/actions/runners \
     -q '.runners[] | select(.name | test("^muster-<namespace>-<pool>-[a-z2-7]{12}$")) | "\(.id) \(.name)"'
   gh api -X DELETE repos/<owner>/<repo>/actions/runners/<id>
   ```

## Two instances

Two musterd instances that share a battery and a GitHub scope must use different namespaces
(`battery.namespace`). Each instance reconciles its own namespace only.
