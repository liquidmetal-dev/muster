# Observability

musterd writes structured logs (OBS-2), serves Prometheus metrics (OBS-1), and reports its
health and readiness (OBS-3).

## Logs

musterd writes logs with `log/slog`. The optional `log` block of the configuration sets the
format and the level:

```json
"log": {"format": "json", "level": "info"}
```

| Field | Values | Default |
| --- | --- | --- |
| `format` | `json`, `text` | `json` |
| `level` | `debug`, `info`, `warn`, `error` | `info` |

The values are case-sensitive. An other value stops musterd at startup with an error that
names the field.

Every package uses the same keys for the same things:

| Key | Meaning |
| --- | --- |
| `session_id` | The session id. |
| `pool` | The pool name. |
| `runner_name` | The GitHub runner name of the session. |
| `job_id` | The GitHub job that runs on the session's runner. |
| `vm_id` | The VM that the session claimed. |
| `lease_id` | The battery lease of a VM. |

Each line about a session has `session_id`, `pool` and `runner_name`, and `vm_id` after the
claim. A session gets `job_id` when the webhook intake receives the `in_progress` delivery
for its runner. The GitHub runner list has no job id, so a deployment that uses only the
polling intake has no `job_id` on session lines.

Runner output (stdout and stderr) is logged at `debug` level.

## Metrics

musterd serves Prometheus metrics at `GET /metrics` on `metrics_addr` (default `:9090`). Each
muster metric name starts with `muster_`. The Go runtime and process metrics (`go_*`,
`process_*`) are served too.

| Name | Type | Labels | Meaning |
| --- | --- | --- | --- |
| `muster_sessions` | gauge | `pool`, `state` | Live sessions. Every configured pool has a series, 0 or more, for `PENDING`, `CLAIMING`, `STARTING` and `RUNNING`. `ENDED` and `FAILED` appear only while a finished session is in cleanup. |
| `muster_claim_attempts_total` | counter | `pool` | VM claim calls to battery. |
| `muster_claim_exhaustions_total` | counter | `pool` | Claim calls that found the pool without a free VM. |
| `muster_claim_duration_seconds` | histogram | `pool` | Time from CLAIMING to a claimed VM, with the backoff while the pool is exhausted. |
| `muster_queued_to_online_seconds` | histogram | `pool` | Time from the creation of a session to its runner coming online. |
| `muster_session_duration_seconds` | histogram | `pool`, `outcome` | Time from the creation of a session until it is finished. |
| `muster_sessions_finished_total` | counter | `pool`, `outcome`, `reason` | Finished sessions. `outcome` is `ended` or `failed`. `reason` is the end or failure reason, for example `exec_exit` or `claim_deadline`. |
| `muster_vm_releases_total` | counter | `pool`, `outcome`, `reason`, `result` | VM release calls during cleanup, by the reason the session finished. `result` is `ok` or `error`. Cleanup tries a failed release again until it succeeds. |
| `muster_github_requests_total` | counter | `endpoint`, `status` | GitHub API HTTP attempts, retries included. `endpoint` is a route template such as `GET /orgs/{org}/actions/runners`. `status` is `2xx`, `3xx`, `4xx`, `5xx`, or `error` when no response arrived. |
| `muster_github_errors_total` | counter | `endpoint`, `kind` | Failed GitHub API attempts. `kind` is `rate_limited`, `server`, `network`, `timeout` or `client`. |
| `muster_webhook_deliveries_total` | counter | `action`, `outcome` | Webhook deliveries. `outcome` is `handled`, `duplicate`, `ignored`, `bad_signature`, `bad_request` or `too_large`. |
| `muster_poll_duration_seconds` | histogram | `repo`, `outcome` | Time to list the queued jobs of one repository. `outcome` is `ok`, `error` or `rate_limited`. |
| `muster_reconcile_decisions_total` | counter | `action`, `outcome` | Startup session reconciliation decisions: `adopt`, `release_lease` or `delete_runner`, with `ok` or `failed`. |
| `muster_ready` | gauge | `check` | 1 when the readiness check passes, 0 when it fails. See [Health](#health). |
| `muster_build_info` | gauge | `version`, `commit` | Always 1. |

Histogram buckets, in seconds:

| Histograms | Buckets |
| --- | --- |
| `muster_claim_duration_seconds`, `muster_queued_to_online_seconds` | 1, 2, 5, 10, 20, 30, 60, 120, 300, 600 |
| `muster_session_duration_seconds` | 30, 60, 300, 600, 1800, 3600, 7200, 14400, 28800 |
| `muster_poll_duration_seconds` | 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10 |

The latency spans start and stop at times that muster records for the session, not for a
job (`docs/adr/2026-11-05-session-centric-latency.md`). They do not include the time that
GitHub takes to deliver or list a job. A session that musterd adopted at startup has no
creation time in that process. It is counted in `muster_sessions` and
`muster_sessions_finished_total`, but not in the histograms.

## Health

musterd serves two health endpoints on `metrics_addr`, next to `/metrics`
(`docs/adr/2026-11-05-ops-listener-and-readiness.md`). The webhook listener serves only
`POST /webhook`, so a load balancer in front of the webhook listener must probe
`metrics_addr`.

- `GET /healthz` returns `200` and `ok` while the process serves HTTP. Use it as a liveness
  probe.
- `GET /readyz` returns `200` when musterd is ready and `503` when it is not. Use it as a
  readiness probe.

`/readyz` has four checks:

| Check | Passes when |
| --- | --- |
| `started` | musterd has completed startup: the configuration is loaded, pools and sessions are reconciled, and the intakes run. |
| `stopping` | musterd has not begun shutdown. It fails from the first SIGTERM or SIGINT until musterd exits. |
| `battery` | The latest battery `ListHosts` call succeeded. |
| `github` | The latest GitHub credential check succeeded. |

A background loop runs the `battery` and `github` checks every 15s, each with a 5s timeout.
`/readyz` returns the latest results and makes no call itself, so it uses no GitHub rate
limit. One failed check makes musterd not ready. The next check that passes makes it ready
again. A result can be up to one interval old.

The body names each check, its result and the time of the last check:

```json
{"ready":false,"checks":[
  {"name":"started","ok":true},
  {"name":"stopping","ok":true},
  {"name":"battery","ok":false,"error":"battery: ListHosts: unavailable","at":"2026-11-05T12:00:15Z"},
  {"name":"github","ok":true,"at":"2026-11-05T12:00:15Z"}
]}
```

`muster_ready{check}` has the same results as a metric.
