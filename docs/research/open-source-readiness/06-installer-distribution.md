# Installer & Distribution Strategy

**Date:** 2026-04-04
**Original researcher:** marsh
**Status:** Reconstructed from summary — needs agent validation/expansion

## Summary

Research on curl-pipe-bash install pattern and distribution channels for a Go CLI tool.

## Primary Channel: curl | bash Install Script

### install.sh Requirements
- POSIX sh (not bash) for maximum compatibility
- Auto-detect platform (uname -s) and architecture (uname -m)
- Download correct binary from GitHub Releases
- Verify checksum (SHA256)
- Install to `~/.local/bin` by default (configurable)
- Provide PATH guidance if `~/.local/bin` not in PATH
- Show clear progress messages

### Reference Implementations Analyzed
- starship — excellent install.sh
- rustup — comprehensive but complex
- nvm — POSIX sh, well-tested
- go-task — simple and clean
- gh CLI — system package managers
- lazygit — GoReleaser + GitHub Releases
- fzf — multi-channel
- age — minimal

## Secondary Channel: go install

```
go install github.com/<org>/<name>@latest
```

Works automatically once tags exist on GitHub. No extra config needed. Good for Go developers who already have the toolchain.

## Future: Homebrew Tap

GoReleaser can auto-generate Homebrew tap config. Create a separate repo (`<org>/homebrew-tap`) and GoReleaser publishes formula automatically on release.

```
brew tap <org>/tap
brew install <name>
```

## Not Recommended (For Now)
- **APT/RPM repos** — high maintenance, low payoff for a new project
- **Signing** — GPG signing adds complexity; checksums are sufficient initially
- **Windows** — WSL is the path; no native Windows support (tmux dependency)

## Distribution Architecture

```
GitHub Release (GoReleaser creates):
├── <name>_v0.1.0_linux_amd64.tar.gz
├── <name>_v0.1.0_linux_arm64.tar.gz
├── <name>_v0.1.0_darwin_amd64.tar.gz
├── <name>_v0.1.0_darwin_arm64.tar.gz
├── checksums.txt (SHA256)
└── install.sh (hosted in repo, URL in README)
```

## Install UX

```bash
curl -fsSL https://<domain>/install.sh | bash
```

Or with version pinning:
```bash
curl -fsSL https://<domain>/install.sh | bash -s -- --version v0.1.0
```

## Open Questions
- Final module path / org name (depends on naming decision)
- Custom domain for install URL
- Update mechanism (check for new versions on launch?)
- Should install.sh check for runtime dependencies (tmux, git, claude)?
