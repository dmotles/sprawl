# Contributing to Sprawl

Thanks for your interest in contributing!

## Reporting Bugs

Open an issue on [GitHub Issues](https://github.com/dmotles/sprawl/issues) with:

- What you expected to happen
- What actually happened
- Steps to reproduce
- Go version and OS

## Development Setup

1. Install [Go 1.25+](https://go.dev/dl/)
2. Clone the repo and cd into it
3. Install the pre-commit hook: `make hooks`
4. Run the full validation suite: `make validate`

`make validate` runs build, format checking, linting, and tests in sequence. Get this passing before submitting any changes.

## Pull Requests

1. Fork the repo and create a branch from `main`
2. Make your changes
3. Run `make validate` and ensure it passes
4. Open a pull request against `main`

Keep PRs focused — one logical change per PR.

## Code Style

Formatting and linting are handled by the toolchain:

- `make fmt` — auto-fix formatting (uses [gofumpt](https://github.com/mvdan/gofumpt) via golangci-lint)
- `make lint` — run [golangci-lint](https://golangci-lint.run/)

The pre-commit hook runs `make validate` automatically, so issues are caught before they reach CI.
