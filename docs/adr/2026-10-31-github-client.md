# ADR: GitHub client owns App auth and rate limiting in its transport

- Status: accepted
- Date: 2026-10-31
- Requirements: GH-1, GH-2, GH-3, GH-4, GH-5, SESSION-4, REL-4

## Context

`internal/github` is the only code in muster that calls the GitHub REST API. It must authenticate
as a GitHub App or with a personal access token, refresh installation tokens before they expire,
honour GitHub's rate limits, and never register a runner twice. go-github provides the endpoint
types but not token refresh, and its own rate limit handling fails calls early, without a retry.
Third-party libraries (ghinstallation, golang-jwt) cover auth but hide the token cache and the
clock, which the tests need to control.

## Decision

1. The App JWT is RS256, signed with the standard library: `iat` is 60 seconds in the past and
   `exp` is 9 minutes ahead. A JWT is minted for each request that needs one.
2. The installation token is cached and replaced 5 minutes before `expires_at`, one refresh at a
   time. If a refresh fails while the old token is still valid, the old token is used. After a
   401 on an installation token the client drops the token and replays the request once.
3. Credentials are added only to requests for the origin of the configured base URL.
4. Rate limiting and retries live in an `http.RoundTripper` under go-github, and go-github's own
   precheck is disabled (`WithDisableRateLimitCheck`), so there is one policy:
   - 429, and 403 with `Retry-After`, `X-RateLimit-Remaining: 0`, or a body whose
     `documentation_url` names the secondary rate limits, are retried for every method, because
     GitHub did not process the request. The wait is `Retry-After`, else the reset time, else one
     minute for a secondary limit named only in the body (GitHub's guidance), else jittered
     exponential backoff. A `Retry-After` that does not parse uses the backoff.
   - 5xx and network errors are retried only for GET, HEAD and DELETE. A JIT configuration POST
     that may have succeeded is never sent again.
   - A wait longer than 60 seconds returns `RateLimitedError` with the reset time, so the caller
     backs off instead of blocking a reconcile loop for up to an hour.
   - Each attempt has a 30 second timeout; a call makes at most 5 attempts.
5. Calls and errors go to a `Metrics` interface, labelled by route template. The Prometheus
   binding belongs to `internal/metrics`.
6. `Checker` implements `config.Checker`. For an App it compares the installation account with
   every pool scope owner and polled repository owner. For both credential types it then probes
   every pool scope (runner list) and polled repository (queued run list) with one call each.

## Consequences

- Session, poller and reconcile code receive typed errors (`ErrRunnerExists`, `ErrRunnerBusy`,
  `ErrRateLimited`, ...) and do not need their own rate limit handling.
- A duplicate runner name after a lost JIT response surfaces as `ErrRunnerExists`; the session
  decides how to recover. The client never guesses.
- Validation verifies access, not names, so App installations limited to selected repositories
  and personal access tokens are both checked. The probes need the runner and Actions read
  permissions muster needs at run time anyway.
- The documentation of App permissions and token scopes (GH-6) is separate work.
