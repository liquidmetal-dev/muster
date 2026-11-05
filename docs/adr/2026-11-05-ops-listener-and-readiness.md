# ADR: health endpoints share the metrics listener, and readiness serves cached probes

- Status: accepted
- Date: 2026-11-05
- Requirements: OBS-1, OBS-3

## Context

Muster has two HTTP listeners. The webhook listener (`listen`) is optional: a deployment
that uses only the polling intake has no webhook listener. The metrics listener
(`metrics_addr`) is always present.

OBS-3 asks for `/readyz` to report that the configuration is loaded, battery is reachable
and GitHub authentication works. A live probe on each request costs one GitHub API call per
probe. Load balancers and kubelets probe often, so live probes use the rate limit that
sessions and the polling intake need.

## Decision

The metrics listener serves `/metrics`, `/healthz` and `/readyz`. The webhook listener
serves only `POST /webhook`.

`/healthz` returns 200 while the process can serve HTTP.

A background loop probes battery (`ListHosts`) and GitHub (`Ping`) at a fixed interval, each
with a timeout. `/readyz` returns the latest results. It returns 503 until musterd marks its
startup complete, and 503 after any probe fails. The next successful probe makes it ready
again. The response body names each check and its last error.

## Consequences

- Health endpoints are available in every deployment, including polling-only deployments.
- A load balancer in front of the webhook listener must probe the metrics address.
- `/readyz` can report a state that is up to one probe interval old.
- One failed probe makes musterd not ready. A short battery or GitHub outage shows as a
  short readiness failure.
