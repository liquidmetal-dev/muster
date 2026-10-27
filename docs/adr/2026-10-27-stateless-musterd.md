# ADR: musterd keeps no persistent state

- Status: accepted
- Date: 2026-10-27
- Requirements: REL-1, REL-2, REL-6, SESSION-2, SESSION-4

## Context

An early draft gave musterd a SQLite database for sessions, queued jobs and webhook
delivery ids. On inspection, every durable fact muster needs already lives elsewhere:
battery holds leases (and the `request_id` muster set on each one), and GitHub holds
runners (whose names muster chooses). The only things a database would add are audit
history and a persistent retry queue for releases.

## Decision

musterd holds no persistent state. Sessions live in memory. On startup musterd reconciles
battery's leases against GitHub's runners:

- A lease whose `request_id` matches a runner named for the same session id is adopted as a
  RUNNING session and tracked to completion through GitHub state and webhooks.
- A lease with no matching runner is released.
- A runner with the muster prefix and no matching lease is deleted.

The session id is embedded in both the battery `request_id` and the runner name
(`muster-<namespace>-<pool>-<short id>`) to make this join possible. Webhook delivery
deduplication is an in-memory set with a 24 hour TTL.

## Consequences

- There is no "one writer per database" rule, no migrations, and nothing to back up.
- A restart or upgrade does not abort in-flight jobs. `musterctl drain --wait` followed by a
  restart is the documented upgrade procedure.
- A crash between `ClaimVM` and the JIT request wastes one VM, which reconciliation releases
  on the next start.
- Two musterd instances must use different battery namespaces, since the namespace is part
  of the runner-name prefix each one reconciles.
- Audit history, if ever wanted, is an additive feature and does not change this decision.
