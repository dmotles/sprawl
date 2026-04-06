# Open Source Licensing Research

**Date:** 2026-04-04
**Original researcher:** brook
**Status:** Reconstructed from full read — high confidence

## Current State

- **No LICENSE file exists.** README references `See [LICENSE](LICENSE) for details` (line 154), but no LICENSE file exists.
- **No copyright headers** in any `.go` source files.
- **No NOTICE file.**
- **No SPDX identifiers** anywhere in the codebase.
- Module path: `github.com/dmotles/sprawl` (per go.mod).

## Dependency Audit

All dependencies use permissive licenses. Zero copyleft or incompatible licenses.

| Dependency | Type | License | Notes |
|---|---|---|---|
| `github.com/spf13/cobra` | Direct | Apache-2.0 | CLI framework |
| `github.com/gofrs/flock` | Direct | BSD-3-Clause | File locking |
| `github.com/inconshreveable/mousetrap` | Indirect | Apache-2.0 | Cobra dep (Windows) |
| `github.com/spf13/pflag` | Indirect | BSD-3-Clause | Cobra dep |
| `golang.org/x/sys` | Indirect | BSD-3-Clause | Go stdlib extension |
| `github.com/stretchr/testify` | Test-only | MIT | Not in release binary |
| `github.com/davecgh/go-spew` | Test-only | ISC | Not in release binary |
| `github.com/pmezard/go-difflib` | Test-only | BSD-3-Clause | Not in release binary |
| `gopkg.in/yaml.v3` | Test-only | MIT + Apache-2.0 | Not in release binary |

**Bottom line:** Fully compatible with any permissive open source license.

## Recommendation: Apache-2.0

**Decision: Apache-2.0** (confirmed by user, CLA skipped)

Rationale:
1. **Patent protection** — explicit patent grant protects contributors and users
2. **Patent retaliation** — suing contributors over patents terminates license
3. **Corporate-friendly** — companies can use and contribute freely
4. **Ecosystem alignment** — Cobra (Apache-2.0), Kubernetes (Apache-2.0), aider (Apache-2.0)
5. **Dependency compatibility** — all deps are BSD-3/Apache-2.0/MIT/ISC

## Action Items

- [ ] Add LICENSE file (Apache-2.0 full text)
- [ ] Add NOTICE file with copyright attribution
- [ ] Add SPDX copyright headers to all `.go` source files: `// Copyright 2025-2026 <copyright holder>` / `// SPDX-License-Identifier: Apache-2.0`
- [ ] Update README License section
- [ ] Copyright holder: David Motles (confirmed by user)

## Similar Projects for Reference

| Project | License |
|---|---|
| cobra (spf13/cobra) | Apache-2.0 |
| gh (cli/cli) | MIT |
| lazygit | MIT |
| Kubernetes | Apache-2.0 |
| aider | Apache-2.0 |
| continue.dev | Apache-2.0 |
