# End-to-end manual verification runbook

This runbook checks muster against a real flintlock host, a real battery and a real GitHub
repository. The unit tests use fakes. The [e2e suite](e2e-suite.md) runs the real flintlock and
battery with the fake GitHub. This runbook is the check that the parts work together with
the real GitHub. Each scenario has its steps, the expected result and a box to tick.

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

## 3. Drain on stop, then upgrade

**Purpose:** the [upgrade procedure](../operations/upgrade.md#upgrade-procedure) keeps a busy
job that ends within `shutdown_grace`, and starts no new session during the stop.

This scenario needs the webhook intake, as the quick start configures it. The `in_progress`
delivery marks the session busy at once. With the polling intake only, musterd learns that a
runner is busy from the runner poll, which runs every 60 seconds, so the 25-second job can end
before the log shows `runner busy`.

1. Start a job that runs for 25 seconds, which is shorter than the default `shutdown_grace` of
   30 seconds: `gh workflow run muster-e2e.yml -R "$REPO" -f sleep=25`.
2. As soon as the log shows `runner busy`, stop musterd and time the stop:
   `time sudo systemctl stop musterd`.
3. While the stop waits, start a second job: `gh workflow run muster-e2e.yml -R "$REPO"`.
4. When `systemctl stop` returns, replace the binary or the package with the build under test.
   Then start musterd: `sudo systemctl start musterd`.
5. In the GitHub App settings, on the **Advanced** page, redeliver the `workflow_job` delivery
   with the action `queued` for the second job
   ([jobs queued during the upgrade](../operations/upgrade.md#jobs-queued-during-the-upgrade)).
   If the polling intake is also on and the second job already has a runner, skip this step.

**Expected:**

- The first job completes with `success`. The log shows `shutdown: busy sessions finished`
  before musterd exits.
- `time` shows that the stop took more than 10 seconds. This proves that musterd waited for a
  busy session, because the log line also appears when no session is busy. If the stop took
  less than 10 seconds, the first job ended before the stop: do the scenario again.
- During the stop, the log shows no new `runner online` line. On the **Advanced** page of the
  GitHub App settings, the `queued` delivery of the second job failed, at a time before
  `systemctl stop` returned. This proves that the second job was queued during the stop.
- After the start and the redelivery, the second job gets a runner and completes with
  `success`.

> From v0.2.0 ([#24](https://github.com/liquidmetal-dev/muster/issues/24)), a variant of this
> scenario runs `musterctl drain --wait` before the stop.

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

## 5. Remove a pool

**Purpose:** the
[pool removal procedure](../operations/upgrade.md#removing-a-pool-or-changing-its-scope) works
with the v0.1.0 binaries.

1. In `/etc/muster/musterd.json`, add a copy of the `quick-start` pool entry, so that it keeps
   the `flintlock_hosts` and the `microvm_template`. Set its name to `e2e-remove`, its labels
   to `self-hosted` and `muster-remove`, and its `size` to 1. Then
   `sudo systemctl restart musterd`.
2. Add a workflow `muster-e2e-remove.yml` to the test repository: a copy of `muster-e2e.yml`
   with the name `muster-e2e-remove` and `runs-on: [self-hosted, muster-remove]`. Run it once:
   `gh workflow run muster-e2e-remove.yml -R "$REPO"`. The job completes with `success`.
3. Remove `muster-e2e-remove.yml` from the test repository. This moves the workflows off the
   pool's labels.
4. Wait until every `state` of the pool shows 0:

   ```sh
   curl -s localhost:9090/metrics | grep 'muster_sessions{pool="e2e-remove"'
   ```

5. Stop musterd with `sudo systemctl stop musterd`. Then check that battery has no lease of the
   pool: `poolmgrctl lease list --pool e2e-remove --namespace "$NS"`.
6. Remove the `e2e-remove` entry from `/etc/muster/musterd.json`.
7. Start musterd once with `-prune-pools`, with the `prune-pools.conf` drop-in file of the
   procedure. Then remove the file and run `sudo systemctl daemon-reload`.

**Expected:**

- The log shows `orphan pool pruned` with `pool=e2e-remove`.
- `poolmgrctl pool get --name e2e-remove --namespace "$NS"` reports that the pool does not
  exist.
- `systemctl cat musterd` shows no `prune-pools.conf` after you remove it.
- `runners` lists no `muster-$NS-e2e-remove-` runner.

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
