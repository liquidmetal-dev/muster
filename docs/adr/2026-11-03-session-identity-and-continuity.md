# ADR: session identity and continuity across restarts

- Status: accepted
- Date: 2026-11-03
- Requirements: SESSION-1, SESSION-2, SESSION-4, SESSION-7, REL-2, REL-6
- Refines: [stateless musterd](2026-10-27-stateless-musterd.md)

## Context

Startup reconciliation joins battery leases to GitHub runners through the session id
carried in both. The stateless-musterd ADR fixes that join but not the shape of the id.
Runner names are at most 64 characters. A 36-character UUID would leave only 19 characters
for the namespace and pool together. A long id with a short prefix in the runner name would make the join a prefix match
that can collide. Separately, two events could end a session that is still running a job: a
broken exec stream and a process shutdown.

## Decision

- A session id is 12 lowercase base32 characters from a cryptographic random source. The
  same string is the battery `request_id` and the suffix of the runner name
  `muster-<namespace>-<pool>-<session id>`. The join is an exact match.
- A broken exec stream without an exit code is not an end signal. The session stays
  RUNNING and relies on the other end signals, as an adopted session does.
- On process shutdown, active sessions stop without releasing their VM or deleting their
  runner. The next start adopts them.

## Considered options

- UUID session id with an 8-character prefix in the runner name: rejected for the prefix
  join and its collisions.
- Ending the session on a broken exec stream: rejected because releasing the VM kills a
  job that may still be running.
- Releasing every session on shutdown: rejected because every upgrade would abort the jobs
  in flight, against REL-6.

## Consequences

- Changing the session id format strands sessions that are in flight across the upgrade
  that changes it.
- The pool name and namespace must leave room for the 12-character id within 64 characters;
  configuration validation enforces this.
- A host or flintlock restart costs a session its log forwarding, not its job.
