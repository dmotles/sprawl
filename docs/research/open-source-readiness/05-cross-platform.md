# Cross-Platform Build Strategy

**Date:** 2026-04-04
**Original researcher:** lake
**Status:** Reconstructed from summary — needs agent validation/expansion

## Summary

All 4 target platforms cross-compile cleanly with `CGO_ENABLED=0`.

## Target Platforms

| Platform | Status |
|---|---|
| macOS amd64 | ✅ Compiles cleanly |
| macOS arm64 (Apple Silicon) | ✅ Compiles cleanly |
| Linux amd64 | ✅ Compiles cleanly |
| Linux arm64 | ✅ Compiles cleanly |

## Key Findings

### No CGO Dependencies
- Pure Go codebase
- `CGO_ENABLED=0` works for all targets
- No platform-specific build tags needed
- No platform-specific source files

### Platform-Specific Syscall Usage
Two locations use Unix-specific syscalls:
1. `syscall.Flock` in `internal/merge/git.go` — file locking during merge
2. `syscall.Exec` in `internal/tmux/` — exec into tmux

Both are Unix-only but work on all 4 target platforms (all Unix-based). No Windows support needed (tmux dependency).

### gofrs/flock Library
- Fully supports all 4 target platforms
- Uses platform-appropriate locking mechanism on each OS

### Runtime Dependencies
- **tmux** — required, must be installed by user
- **git** — required
- **Claude Code CLI** — required

## Recommendations

1. **GoReleaser config** for multi-platform release builds
2. **GitHub Actions CI matrix** testing all 4 platforms
3. **Version injection via ldflags** (`-X main.version=...`)
4. **Consolidate dual flock implementations** — noted there are two flock usages that could share code

## Notes
- No Windows support planned (tmux is the hard dependency)
- WSL is the Windows path if users ask
