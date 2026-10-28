# ADR: configuration is strict, secrets are external, validation reports everything

- Status: accepted
- Date: 2026-10-28
- Requirements: OPS-2, REL-5, SEC-2, SEC-3, GH-3

## Context

musterd reads one JSON file that operators edit by hand. A typo in a key, a secret pasted
inline, or a certificate path that is wrong on one host each cost a restart to discover,
and a secret that reaches a log stays there. REL-5 asks that startup fail fast with a
clear message and that `musterctl config validate` run the same checks offline where
possible.

## Decision

`internal/config` follows four rules.

1. Decoding is strict. Unknown and duplicate JSON keys are errors, as are unknown fields
   inside a pool's `microvm_template`, which is decoded as a flintlock `MicroVMSpec` with
   protojson and `DiscardUnknown` off. Fields that muster or battery own (`id`,
   `namespace`, `uid`, the timestamps) are rejected; `kernel` and `root_volume` are
   required.
2. Secrets (the GitHub App key, the token, the webhook secret, TLS private keys) come only
   from files or environment variables, as `*_file` / `*_env` pairs with exactly one set.
   They live in a `Secret` type whose every text form prints `[redacted]`.
3. Validation is split. `Parse` decodes, applies defaults and runs every static rule
   without touching a file. `Load` adds the file-touching steps: it resolves secrets and
   builds every TLS configuration once. Both report every failure at once, each with its
   JSON path, through `FieldError` and `errors.Join`.
4. Live checks (battery host existence and exec enablement, GitHub credential reach) are
   injected through the `Checker` interface by the packages that own the clients.
   `config` imports neither battery nor GitHub.

## Consequences

- A new flintlock `MicroVMSpec` field needs a dependency bump before a template can use
  it.
- `musterctl config validate` can run on a machine without the secrets or certificates
  present, through `Parse`.
- `docs/examples/musterd.json` is tested with `Parse`, so it cannot drift from the schema.
- The three battery checks (`addr`, `namespace`, `tls`) are repeated from
  `battery.Config.Validate` on purpose, with JSON paths attached, because `config` has no
  battery import.
- Errors never carry secret values; environment errors name the variable, file errors
  name the path.
- The shipped example uses `eth1` for the guest interface because flintlock reserves
  `eth0` for Firecracker.
- A `listen` block without `tls` serves plain HTTP, so the operator must terminate TLS in
  front of it (INTAKE-8). The configuration reference (#18) and the `Listen.TLS` field
  comment both say so.
