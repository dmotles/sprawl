# 04 — Release Mechanism

> **Validated by:** creek (research agent), 2026-04-05
> Verified against GoReleaser v2 docs, gh CLI (cli/cli), lazygit (jesseduffield/lazygit), and current sprawl codebase.

## Summary

Sprawl needs a release pipeline to produce versioned binaries for distribution.
This document covers GoReleaser v2 configuration, GitHub Actions integration,
and version injection via ldflags.

## Current State

Sprawl currently has no release automation. The Makefile builds a local binary
with a simple `go build -o sprawl .` (no ldflags, no version embedding). There
is no `.goreleaser.yaml`, no release workflow, and no version variable in
`main.go` or elsewhere.

**Relevant files:**

- `Makefile` — line 7: `go build -o sprawl .`
- `main.go` — calls `cmd.Execute()`, no version variables
- `go.mod` — module `github.com/dmotles/sprawl`, Go 1.25.0

## GoReleaser v2

GoReleaser v2 is the current major version (v2 launched as essentially v1.26.2
with all deprecated options removed). The key migration changes from v1:

- Config must include `version: 2` at the top
- `archives.format` became `archives.formats`
- `snapshot.name_template` became `snapshot.version_template`
- `builds.gobinary` became `builds.tool`
- CLI flags: `--rm-dist` became `--clean`, `--debug` became `--verbose`

### Recommended .goreleaser.yaml for Sprawl

Based on lazygit's config (the cleanest reference among popular Go CLIs):

```yaml
version: 2

builds:
  - env:
      - CGO_ENABLED=0
    goos:
      - linux
      - darwin
      - windows
    goarch:
      - amd64
      - arm64
    ldflags:
      - >-
        -s -w
        -X main.version={{.Version}}
        -X main.commit={{.Commit}}
        -X main.date={{.Date}}

archives:
  - name_template: >-
      {{- .ProjectName }}_
      {{- .Version }}_
      {{- .Os }}_
      {{- if eq .Arch "amd64" }}x86_64
      {{- else }}{{ .Arch }}{{ end }}
    format_overrides:
      - goos: windows
        formats: [zip]

checksum:
  name_template: 'checksums.txt'

snapshot:
  version_template: '{{ .Tag }}-next'

changelog:
  use: github-native
```

**Notes:**

- `CGO_ENABLED=0` is appropriate since sprawl has no CGO dependencies (verified
  — no `import "C"` or CGO references anywhere in the codebase).
- Windows builds will compile but will **not work** without significant code
  changes (see [05-cross-platform.md](05-cross-platform.md)). Consider
  excluding `windows` initially.
- `-s -w` strips debug info and DWARF symbols to reduce binary size.

## Version Injection via ldflags

### Go Code Pattern

Add version variables to `main.go` (or a dedicated `internal/build` package):

```go
package main

import "github.com/dmotles/sprawl/cmd"

var (
    version = "dev"
    commit  = "none"
    date    = "unknown"
)

func main() {
    cmd.Execute()
}
```

Then wire these into a `sprawl version` command or the root command's version
field. GoReleaser automatically populates `main.version`, `main.commit`, and
`main.date` at build time.

### Manual Build with ldflags

For local development or CI without GoReleaser:

```bash
go build -ldflags "-s -w \
  -X main.version=$(git describe --tags --always) \
  -X main.commit=$(git rev-parse HEAD) \
  -X main.date=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  -o sprawl .
```

### Non-main Package Alternative

If version info belongs in a library package (like gh CLI uses
`internal/build.Version`):

```yaml
ldflags:
  - -X github.com/dmotles/sprawl/internal/build.Version={{.Version}}
```

**Recommendation:** Start with `main.version` for simplicity. Move to a
dedicated package only if multiple commands or packages need version info.

## GitHub Actions Release Workflow

### Standard Tag-Push Trigger (Recommended)

```yaml
name: release

on:
  push:
    tags:
      - "v*"

permissions:
  contents: write

jobs:
  goreleaser:
    runs-on: ubuntu-latest
    steps:
      - name: Checkout
        uses: actions/checkout@v4
        with:
          fetch-depth: 0

      - name: Set up Go
        uses: actions/setup-go@v5
        with:
          go-version-file: go.mod

      - name: Run GoReleaser
        uses: goreleaser/goreleaser-action@v7
        with:
          distribution: goreleaser
          version: "~> v2"
          args: release --clean
        env:
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
```

**Critical details:**

- `fetch-depth: 0` is **required** — GoReleaser needs full git history for
  changelog generation and tag detection.
- `permissions: contents: write` is needed to create GitHub releases.
- `GITHUB_TOKEN` is automatically provided by GitHub Actions; no custom secret
  needed for basic releases.
- `go-version-file: go.mod` reads the Go version from the module file, keeping
  it in sync.
- Tag pattern `v*` fires only on semver-style tags (e.g., `v1.0.0`).

### Release Process

```bash
git tag v1.0.0
git push origin v1.0.0
# GitHub Actions triggers automatically, GoReleaser builds and publishes
```

## Reference Projects

### lazygit (jesseduffield/lazygit) — Recommended Reference

- GoReleaser v2 with a clean, minimal config
- `CGO_ENABLED=0`, targets linux/darwin/windows/freebsd on amd64/arm/arm64/386
- Standard ldflags: `-X main.version={{.Version}} -X main.commit={{.Commit}}`
- Release workflow uses `workflow_dispatch` with a version bump selector
  (minor/patch) — calculates next semver, creates tag, runs goreleaser
- Uses `goreleaser/goreleaser-action@v7` with `version: v2`
- `changelog: use: github-native`

### gh CLI (cli/cli) — Enterprise Reference (Too Complex to Copy)

- GoReleaser v2 but heavily customized: separate build IDs per OS, custom
  release script wrapper, code signing on macOS/Windows, MSI building, RPM/DEB
  signing, Apple notarization, Homebrew tap bumping
- Version injection: `-X github.com/cli/cli/v2/internal/build.Version={{.Version}}`
- Uses `workflow_dispatch` (not tag-push) with multi-runner builds
- **Not a good template for sprawl** — far more complexity than needed

## Recommended Implementation Plan

1. **Add version variables** to `main.go` (`version`, `commit`, `date`)
2. **Add a `sprawl version` command** that prints them
3. **Create `.goreleaser.yaml`** using the config above (initially linux +
   darwin only, add windows later after cross-platform work)
4. **Add `.github/workflows/release.yml`** with the tag-push workflow
5. **Update Makefile** to inject ldflags in the local `build` target
6. **Tag and release** — `git tag v0.1.0 && git push origin v0.1.0`

## Open Questions

- Should sprawl publish to Homebrew? If so, a separate tap repo and a PAT
  secret are needed.
- Should there be a `sprawl self-update` mechanism, or rely on package managers?
- What's the minimum supported Go version? Currently building with Go 1.25.0.
