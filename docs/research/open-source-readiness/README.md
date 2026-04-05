# Open Source Readiness Research

Research documents for preparing dendra for open source publication. These are living documents — update them as decisions are made and work is completed.

## Documents

| # | Document | Topic | Confidence |
|---|----------|-------|------------|
| 01 | [Licensing](01-licensing.md) | Copyright, license choice, dependency audit | High (full report preserved) |
| 02 | [Secrets Scan](02-secrets-scan.md) | Credentials, proprietary info, PII | Medium (reconstructed from summary) |
| 03 | [Security Audit](03-security-audit.md) | Vulnerabilities, trust model, attack surface | Medium (reconstructed from summary) |
| 04 | [Release Mechanism](04-release-mechanism.md) | Versioning, GoReleaser, GitHub Actions | Medium (reconstructed from summary) |
| 05 | [Cross-Platform Build](05-cross-platform.md) | macOS/Linux × amd64/arm64 | Medium (reconstructed from summary) |
| 06 | [Installer & Distribution](06-installer-distribution.md) | curl\|bash, Homebrew, go install | Medium (reconstructed from summary) |
| 07 | [Unknown Unknowns](07-unknown-unknowns.md) | Gaps, meta-concerns, community readiness | Medium (reconstructed from summary) |

## Key Decisions Made

- **License:** Apache-2.0 (no CLA)
- **Copyright holder:** David Motles
- **Platforms:** macOS + Linux, amd64 + arm64 (no Windows)
- **Internal refs:** Move Qumulo/Linear config to CLAUDE.local.md + .gitignore

## Key Decisions Pending

- **Project name** — see `docs/research/naming-all-candidates.md`
- **GitHub org / module path** — depends on name
- **Public identity / email** — domain + email or GitHub noreply
- **Agent name path traversal fix** — pre-publish or fast-follow?
- **Scope of first publish** — incremental or all-at-once?
