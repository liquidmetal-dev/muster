# ADR: the polling intake merges one repository at a time and never closes what it did not list

- Status: accepted
- Date: 2026-11-03
- Requirements: INTAKE-5, INTAKE-6, INTAKE-7, GH-5, OBS-1

## Context

The polling intake recovers webhook deliveries that GitHub did not send or musterd did not
receive, and it is the only intake when musterd cannot accept inbound connections. A
listing that is wrong in the closing direction is worse than one that is late: the demand
registry closes an open record that a listing omits, and it then ignores that job id for
24 h, so the job never gets a runner. GitHub has no endpoint that lists queued jobs. It
lists workflow runs by status, and then each run's jobs. A run is `in_progress` as soon as
its first job starts, and it can still hold queued jobs (`needs:`, matrix `max-parallel`).
All GitHub calls share one credential and its rate limit.

## Decision

1. `internal/intake/poller` lists the configured repositories one after another, never in
   parallel. A poll pass starts when `Run` starts. The next pass starts `interval` after
   the previous one started, or when the previous one ends if it took longer, so two
   passes never overlap. This is the loop of `dispatch.Dispatcher.Run`.
2. Each repository is merged on its own: `Registry.Polled([repo], listedAt, jobs)`, with
   `listedAt` read from the clock before that repository's listing starts. A repository
   whose listing fails is not merged, so its open records stay open until a later pass,
   another intake, or the 24 h expiry closes them.
3. A rate-limit error stops the pass. The repositories after it are not listed and not
   merged, and the next pass starts at the usual interval.
4. `github.Client.ListQueuedJobs` lists `queued` runs first and `in_progress` runs second,
   keeps the jobs whose status is `queued`, and drops duplicate job ids. Runs only move
   forward, so a run that changes status between the two lists is in at least one of them.
5. The listing carries each job's `created_at`, so a polled job and a webhook job expire
   at the same time.
6. When `poll.enabled` is false, `Run` returns at once and makes no GitHub calls.
7. The poller records the duration and outcome (`ok`, `error`, `rate_limited`) of each
   repository it listed. GitHub API errors by endpoint stay in the client's metrics.

## Consequences

- A pass costs 2 run-list calls per repository plus one job-list call per queued or
  running run. A busy repository costs more API calls than an idle one.
- Pagination can still miss a run. When one repository has more than 100 queued or
  running runs, a run that leaves page 1 while the pages are read moves another run off
  page 2, and the pass closes that run's queued jobs. v1 accepts this limit.
- A repository that fails on every pass keeps its records open for up to 24 h, so muster
  can keep runners up for jobs that already ended.
- Wiring the poller into `cmd/musterd` and the Prometheus binding are #38 and the metrics
  work.
