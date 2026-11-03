# ADR: the webhook intake verifies first, remembers last, and never waits

- Status: accepted
- Date: 2026-11-03
- Requirements: INTAKE-1, INTAKE-2, INTAKE-3, INTAKE-4, INTAKE-7, INTAKE-8, SEC-1, SEC-3

## Context

GitHub sends `workflow_job` deliveries to musterd and waits at most 10 s for an answer. It
does not retry a failed delivery on its own; an operator or the polling intake recovers it.
GitHub can send the same delivery twice, and anyone who can reach the listener can send a
forged one. musterd keeps no state across restarts (`2026-10-27-stateless-musterd.md`), and
the demand registry already ignores a job id it has closed within 24 h.

## Decision

1. `internal/intake/webhook` checks a delivery in a fixed order: body size (1 MiB, 413),
   the `X-Hub-Signature-256` HMAC (401), the event and delivery headers and the payload
   (400), the pool match, and last the delivery id. Only a delivery that passes every check
   records its id. A forged delivery cannot fill the id set, and a delivery that failed can
   still succeed when it is sent again.
2. The signature check is written in the package: `sha256=` and lower-case hex only,
   compared with `hmac.Equal`. go-github's `ValidatePayload` also accepts the SHA-1
   header, which INTAKE-1 does not name.
3. Delivery ids live in memory for 24 h. Pruning runs on insert, at most once a minute,
   so the set needs no goroutine. A restart forgets the ids; the registry's tombstones
   stop a job from being counted twice.
4. The handler checks the pool match for `queued`, `in_progress` and `completed`.
   `Registry.Started` and `Completed` take only a job id, so without this check an
   unmatched closing event would tombstone its id.
5. Calls into the registry are synchronous, with no queue and no 503 path. The registry
   is in memory, and its change hook is `dispatch.Dispatcher.Wake`, which never blocks.
   The hook must stay non-blocking, or a slow hook delays every webhook response.
6. A valid delivery of another event (such as `ping`) or another action gets 200 and a
   debug log, so the GitHub delivery page shows success.
7. `runner_name` is logged only. The session state machine (#12) adds the hook that uses
   it, when there is a session store to consume it.
8. `internal/server` owns the listener: the `POST /webhook` route, optional TLS from
   `listen.tls`, timeouts, and graceful shutdown. Health routes (#16) are added through
   `Server.Handle`. Wiring it into `cmd/musterd` is #38.

## Consequences

- A slow `onChange` hook in the registry is a bug in the webhook path, not only in the
  dispatcher.
- A restart within 24 h of a delivery forgets its id. A redelivery after a restart reaches
  the registry, which ignores it when the job is already open or closed.
- Without a `tls` block the listener serves plain HTTP, and the operator must terminate
  TLS in front of it (INTAKE-8). The operator documentation (#18) says so.
- Built-in webhook TLS moves from v0.2.0 to v0.1.0 in the design milestones.
