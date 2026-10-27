# muster

[![CI](https://github.com/liquidmetal-dev/muster/actions/workflows/ci.yml/badge.svg)](https://github.com/liquidmetal-dev/muster/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/liquidmetal-dev/muster.svg)](https://pkg.go.dev/github.com/liquidmetal-dev/muster)
[![Go Version](https://img.shields.io/badge/go-1.27-00ADD8?logo=go)](go.mod)
[![License](https://img.shields.io/github/license/liquidmetal-dev/muster)](LICENSE)

**muster** runs each GitHub Actions job in a fresh microVM. It takes a pre-booted VM from a
[battery](https://github.com/liquidmetal-dev/battery) warm pool on
[flintlock](https://github.com/liquidmetal-dev/flintlock), starts a GitHub runner inside it,
and deletes the VM when the job ends. It is part of the
[liquidmetal-dev](https://github.com/liquidmetal-dev) family of projects.

> **Status:** pre-alpha. Nothing runs yet; this repository holds the scaffold and the design.

## Design

- [Requirements](docs/design/2026-10-27-muster-requirements.md)
- [Design](docs/design/2026-10-27-muster-design.md)
- ADR: [stateless musterd](docs/adr/2026-10-27-stateless-musterd.md)
- ADR: [battery as the VM source](docs/adr/2026-10-27-battery-as-vm-source.md)

## Build and test

Install the pinned toolchain with [mise](https://mise.jdx.dev):

```sh
mise install
```

The tools are on your `PATH` only when mise is activated in your shell
(`mise activate`, see the mise docs) or when you run them through `mise exec -- <command>`.

```sh
go build ./...
go vet ./...
go test -race ./...
golangci-lint run
```

## License

See [LICENSE](LICENSE).
