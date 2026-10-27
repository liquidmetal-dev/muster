# Agent Instructions

## Commit messages

- All commits must follow [Conventional Commits](https://www.conventionalcommits.org/) (e.g. `feat: add x`, `fix: correct y`, `chore: update z`).
- All commits must be signed (`git commit -S`, or `commit.gpgsign = true`).
- Do not add agent footers (e.g. "Co-Authored-By", "Generated with", session links) to commit messages or pull request descriptions.

## Pull requests

- Do not add agent footers to PR descriptions.

## API changes

- If a change modifies anything under `api/proto/muster/v1alpha1/` (a new or changed service, RPC, message or field), update `musterctl` (`internal/musterctl/`) in the same change so the CLI does not drift from the API it wraps.

## Toolchain

- The project uses the latest stable Go release. Bump `mise.toml` and `go.mod` within a release cycle of each new Go minor (requirement PROJ-1).
- `mise.toml` pins go, golangci-lint, buf and goreleaser. Run `mise install` once; CI installs the same versions.

## Decisions

- Record decisions with lasting consequence as ADRs in `docs/adr/`, following the format of the existing files there (requirement PROJ-6).

## Build and test

    go build ./... && go vet ./... && go test -race ./... && golangci-lint run
