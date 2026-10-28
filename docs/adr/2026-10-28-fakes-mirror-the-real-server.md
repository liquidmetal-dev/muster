# ADR: Fakes mirror the real server

- Status: accepted
- Date: 2026-10-28
- Requirements: PROJ-2, PROJ-3

## Context

Unit tests use in-process fakes for battery, flintlock exec and GitHub (PROJ-2). The first
fake, `internal/vmsource/battery/fakepoolmgr`, answers with the status codes of the real
poolmgrd for every case muster handles and serialises with battery's generated protobuf types.
The fake GitHub server (PROJ-3) raised the same two questions again: which types carry the
wire format, and how faithful the error behaviour must be. A fake that answers with a
convenient subset of codes, or with hand-written JSON, passes its own tests and lets the
client it serves drift from the real server.

## Decision

Every fake in this repository follows two rules.

1. A fake serialises with the client library's own types where one exists: go-github for
   GitHub, battery's generated protos for poolmgrd, flintlock's generated protos for exec. The
   fake's tests drive it through that client library, so wire compatibility is proven by the
   fake's own tests.
2. A fake returns the real server's status codes for every case muster handles (404 for an
   unknown object, 422 for a busy runner, 401 for bad credentials, `RESOURCE_EXHAUSTED` for an
   empty pool, and so on), not a convenient subset. The package comment lists the codes.

## Consequences

- A fake may depend on the client library before the client package that uses it exists.
  `internal/fakegithub` adds `google/go-github` to `go.mod` before `internal/github` does.
- The fake poolmgrd already follows rule 2. The flintlock exec fake (#5) and later fakes
  follow both rules.
- A fake implements the endpoints that muster calls or that an open muster issue plans to
  call (for `internal/fakegithub`: `GET /app/installations/{id}` and `GET /user` for the
  credential reach check in #7), and nothing else.
- A client bug that depends on an exact status code or field name shows up in the client's
  tests against the fake, not in production.
