// Package config reads musterd's single JSON configuration file
// (requirement OPS-2), applies defaults and validates it (REL-5).
//
// Three rules shape the package:
//
//   - Decoding is strict. Unknown or duplicate keys are errors, and so are
//     unknown fields inside a pool's microvm_template. Every decode or
//     validation failure names the JSON path of the field it concerns, and
//     Parse reports all of them at once.
//   - Secrets (the GitHub App key, the token, the webhook secret, TLS keys)
//     come only from files or environment variables, never inline
//     (SEC-3). They are held in Secret, which prints as [redacted].
//   - Parse touches no file beyond the one it reads, so it works offline.
//     Load adds the file-touching steps: it resolves secrets and proves the
//     TLS files parse. Live checks (battery hosts, GitHub credential reach,
//     GH-3) are injected through Checker by the packages that own the
//     clients, so this package imports neither.
package config
