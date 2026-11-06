# End-to-end manual verification runbook

This runbook checks muster against a real flintlock host, a real battery and a real GitHub
repository. The automated tests use fakes; this runbook is the check that the parts work
together. Each scenario has its steps, the expected result and a box to tick.

## Status of this runbook

- musterd startup arrives with [#38](https://github.com/liquidmetal-dev/muster/issues/38).
  Until then, no scenario can run.
- `musterctl drain` arrives with [#24](https://github.com/liquidmetal-dev/muster/issues/24).
  Until then, scenario 3 cannot run.

## Prerequisites

- The [quick start](../quick-start.md), done: a flintlock host registered in battery as
  `host-a`, a GitHub App installed on a test repository, and musterd configured with the pool
  `quick-start` (labels `self-hosted` and `muster`, `size` 1).
- [`gh`](https://cli.github.com), logged in as an admin of the test repository.
- `poolmgrctl`, with `--addr` (and its TLS flags) set for your poolmgrd. The commands below
  leave out these flags.
- `curl`, and a shell on the musterd host with access to `journalctl -u musterd`.

Set these for the commands below:

```sh
export REPO=you/your-repo
export NS=muster          # battery.namespace
export POOL=quick-start
```

Two helpers that the scenarios use:

```sh
# The runners of the repository, by name and status.
runners() { gh api "repos/$REPO/actions/runners" -q '.runners[] | "\(.name) \(.status) busy=\(.busy)"'; }
# The finished sessions, by outcome and reason.
finished() { curl -s localhost:9090/metrics | grep '^muster_sessions_finished_total'; }
```

Add this workflow to the test repository as `.github/workflows/muster-e2e.yml`. Its `sleep`
input sets how long the job runs:

```yaml
name: muster-e2e
on:
  workflow_dispatch:
    inputs:
      sleep:
        default: "0"
jobs:
  check:
    runs-on: [self-hosted, muster]
    steps:
      - run: uname -a && sleep "${{ inputs.sleep }}"
```

## 1. Happy path

**Purpose:** a queued job gets a VM and a runner, runs green, and the session cleans up.

1. Check that musterd is ready and that the pool has a warm VM:

   ```sh
   curl -s localhost:9090/readyz
   poolmgrctl pool get --name "$POOL" --namespace "$NS"
   ```

2. Start a job and watch the log:

   ```sh
   gh workflow run muster-e2e.yml -R "$REPO"
   journalctl -u musterd -f
   ```

3. While the job runs, run `runners`.

**Expected:**

- `/readyz` returns 200, with `started`, `battery` and `github` all `ok`.
- The log shows `runner online`, `runner busy`, then `session ended` with `reason` set to
  `exec_exit` or `job_completed`. Each line has the same `session_id`, `pool` and
  `runner_name`.
- `runners` shows `muster-$NS-$POOL-<session id>` online and busy while the job runs, and no
  such runner after the session ends.
- `gh run list -R "$REPO" -w muster-e2e.yml` shows the run as `completed success`.
- `finished` shows `muster_sessions_finished_total{outcome="ended",...}` 1 higher than before.
- `poolmgrctl pool get` shows the pool back at `size` warm VMs within some minutes.

- [ ] Pass

## 2. A restart adopts a job in flight

**Purpose:** a restart of musterd does not stop a job (REL-6).

1. Start a job that runs for 3 minutes:

   ```sh
   gh workflow run muster-e2e.yml -R "$REPO" -f sleep=180
   ```

2. When the log shows `runner busy`, restart musterd:

   ```sh
   sudo systemctl restart musterd
   journalctl -u musterd -f
   ```

**Expected:**

- At the stop, the log shows `shutdown: grace expired; leaving sessions for adoption` (the job
  runs for longer than `shutdown_grace`).
- At the start, the log shows `reconcile decision` with the action `adopt`, then
  `reconciliation complete`, then `adopted session` with the same `session_id`.
- `curl -s localhost:9090/metrics | grep muster_reconcile_decisions_total` shows
  `action="adopt",outcome="ok"` at 1.
- The run completes with `success`. Then the log shows `session ended` for the session, and
  `runners` no longer lists its runner.

- [ ] Pass

## 3. Drain, then upgrade

**Purpose:** the upgrade procedure in [upgrade](../operations/upgrade.md) starts no new
session during the drain, and keeps the jobs that run. Needs
[#24](https://github.com/liquidmetal-dev/muster/issues/24).

1. Start a job that runs for 2 minutes: `gh workflow run muster-e2e.yml -R "$REPO" -f sleep=120`.
2. When the log shows `runner busy`, start the drain in a second shell:

   ```sh
   musterctl drain --wait
   ```

3. While the drain waits, start a second job: `gh workflow run muster-e2e.yml -R "$REPO"`.
4. When `musterctl drain --wait` returns, restart musterd: `sudo systemctl restart musterd`.

**Expected:**

- The first job completes with `success` while the drain waits.
- The second job stays queued during the drain: the log shows no new `runner online` line.
- `musterctl drain --wait` returns after the first session ends.
- After the restart, the second job gets a runner and completes with `success`.

- [ ] Pass

## 4. End signals and orphans

### 4a. Idle timeout

**Purpose:** a runner that never gets a job ends at the idle timeout.

1. Set `"defaults": {"idle_timeout": "2m"}` in `/etc/muster/musterd.json`, then
   `sudo systemctl restart musterd`.
2. Start a job: `gh workflow run muster-e2e.yml -R "$REPO"`.
3. Before the runner comes online (before the log shows `runner online`), cancel the run:

   ```sh
   gh run cancel -R "$REPO" "$(gh run list -R "$REPO" -w muster-e2e.yml -L 1 --json databaseId -q '.[0].databaseId')"
   ```

**Expected:**

- The log shows `runner online` and no `runner busy` for the session.
- About 2 minutes after `runner online`, the log shows `session ended` with `reason` set to
  `idle_timeout`.
- `finished` shows `reason="idle_timeout"` at 1 or more. `runners` does not list the runner.

If the log shows `runner busy`, the runner took the job before the cancel, and another end
signal ends the session. Do the steps again. Then set `idle_timeout` back and restart musterd.

- [ ] Pass

### 4b. Orphans at startup

**Purpose:** at startup, musterd deletes a runner that has no lease and releases a lease that
has no runner.

**A runner with no lease:**

GitHub does not delete a runner that is busy with a job, so this check uses a runner that is
online without a job.

1. Do steps 2 and 3 of scenario 4a: start a job and cancel it before the runner comes online.
2. When the log shows `runner online` and no `runner busy` for the session, stop musterd
   before the idle timeout: `sudo systemctl stop musterd`.
3. Find the session's lease and release it by hand, so that the runner has no lease:

   ```sh
   poolmgrctl lease list --pool "$POOL" --namespace "$NS"
   poolmgrctl lease release --lease-id <lease id>
   ```

4. Start musterd: `sudo systemctl start musterd`.

**Expected:**

- The log shows `reconcile decision` with the action `delete_runner` for the session's
  runner name.
- `curl -s localhost:9090/metrics | grep muster_reconcile_decisions_total` shows
  `action="delete_runner",outcome="ok"` at 1.
- `runners` no longer lists the runner.

**A lease with no runner:**

1. Stop musterd: `sudo systemctl stop musterd`.
2. Claim a VM of the pool by hand. The lease has no runner on GitHub:

   ```sh
   poolmgrctl lease claim --pool "$POOL" --namespace "$NS" --request-id e2e-orphan
   ```

3. Start musterd: `sudo systemctl start musterd`.

**Expected:**

- The log shows `reconcile decision` with the action `release_lease` for the lease.
- `curl -s localhost:9090/metrics | grep muster_reconcile_decisions_total` shows
  `action="release_lease",outcome="ok"` at 1.
- `poolmgrctl lease list --pool "$POOL" --namespace "$NS"` no longer shows the lease.

- [ ] Pass

### 4c. Polling intake

**Purpose:** the polling intake finds a queued job without a webhook.

1. Change `/etc/muster/musterd.json` to the
   [polling configuration](../quick-start.md#no-inbound-access) of the quick start, then
   `sudo systemctl restart musterd`.
2. Start a job: `gh workflow run muster-e2e.yml -R "$REPO"`.

**Expected:**

- The log shows `runner online` within `github.poll.interval` (default `1m`) of the start,
  and the job completes with `success`.
- `curl -s localhost:9090/metrics | grep muster_poll_duration_seconds_count` shows
  `outcome="ok"` for the repository.
- The session lines in the log have no `job_id`, because only the webhook intake gives it
  ([observability](../operations/observability.md#logs)).

Change the configuration back after the check.

- [ ] Pass

## Clean up

1. Cancel any run that is still queued:

   ```sh
   gh run list -R "$REPO" -s queued --json databaseId -q '.[].databaseId' |
     xargs -r -n1 gh run cancel -R "$REPO"
   ```
2. Check that `runners` lists no `muster-$NS-` runner, and that
   `poolmgrctl lease list --pool "$POOL" --namespace "$NS"` shows no lease.
3. Remove the `muster-e2e.yml` workflow from the test repository if you do not need it again.

## Troubleshooting

| Symptom | Cause and fix |
| --- | --- |
| musterd exits at startup with `host "host-a" is not registered in battery` | Register the host. See [hosts](../operations/hosts.md#when-a-host-is-missing). |
| `/readyz` returns 503 with `battery` not `ok` | musterd cannot reach poolmgrd. Check `battery.addr` and `battery.tls`. |
| `/readyz` returns 503 with `github` not `ok` | The App ID, the installation ID or the private key is wrong, or `github.api_base_url` is wrong. |
| `muster_webhook_deliveries_total{outcome="bad_signature"}` grows | The webhook secret on the App and in `github.webhook_secret_file` are different. |
| `muster_webhook_deliveries_total{outcome="ignored"}` grows and no session starts | The job's `runs-on` labels match no pool. See [job matching](../configuration.md#job-matching). |
| Sessions fail with `reason="claim_deadline"` | Battery has no free VM. Check the pool and the host: [capacity](../operations/capacity.md). |
| Sessions fail with `reason="start_timeout"` | The runner did not come online. Set `log.level` to `debug` to see the runner output, and check the image against the [image contract](../image-contract.md). |
