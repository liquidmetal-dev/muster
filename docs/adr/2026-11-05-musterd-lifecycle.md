# ADR: musterd fails fast at startup and drains on a signal

- Status: accepted
- Date: 2026-11-05
- Requirements: REL-1, INTAKE-1, INTAKE-4

## Context

musterd runs under systemd with `Restart=on-failure`. Startup has steps that can fail
(GitHub authentication, battery, host checks, pool and session reconciliation). Shutdown
must not abort in-flight jobs, and musterd keeps no state, so a session that is still busy
at exit is adopted by the next start.

## Decision

- The metrics listener starts first, straight after the configuration loads. `/readyz`
  returns 503 while startup runs.
- A failed startup step stops musterd with exit code 1. musterd does not retry the step;
  systemd restarts the process.
- A SIGTERM or SIGINT during startup cancels the current step. musterd stops what has started
  and exits with code 0.
- After startup, a SIGTERM or SIGINT makes musterd not ready, stops the intakes, and drains:
  it waits up to the shutdown grace for busy sessions to finish. A second signal ends the
  wait at once.
- musterd exits with code 0 when the shutdown grace expires with sessions still busy. Those
  sessions are left for adoption.
- When a long-running part fails after startup (for example, the webhook listener cannot
  serve), musterd drains the same way and exits with code 1.

## Consequences

- A persistent fault, such as a revoked GitHub key, shows as a systemd restart loop and not as
  a process that stays not ready.
- Exit code 0 does not mean that all sessions ended. Operators find sessions left running in
  the WARN log line and in the next start's adoption count.
- `TimeoutStopSec` in the unit must stay above the shutdown grace, so that systemd does not
  kill musterd during the drain.
