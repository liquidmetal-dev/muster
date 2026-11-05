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
