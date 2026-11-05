# ADR: latency and duration metrics measure sessions, not jobs

- Status: accepted
- Date: 2026-11-05
- Requirements: OBS-1

## Context

OBS-1 asks for claim latency, queued-to-online latency and session duration. A session is
not bound to one job: demand counts queued jobs per pool, and GitHub gives the job to any
idle runner that matches. The time between a job's queue time and a particular runner
coming online is therefore only an estimate.

An adopted session has no creation, claim or online time in the process that adopted it.

## Decision

Each span starts and stops at times that muster records for the session:

- Claim latency is the time from CLAIMING to the claimed VM, including backoff while the
  pool is exhausted.
- Queued-to-online latency is the time from the creation of the session to the runner
  coming online.
- Session duration is the time from the creation of the session until it is finished.

Adopted sessions are not observed in these histograms. They are counted in the session
gauge and in the finished-session counter.

## Consequences

- The histograms do not include the time that GitHub takes to deliver or list a job.
- After a restart, the histograms do not include the sessions that were adopted.
- A change to job-based latency needs a new metric name, because dashboards depend on the
  meaning of the current names.
