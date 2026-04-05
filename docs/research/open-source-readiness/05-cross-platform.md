# 05 — Cross-Platform Support

> **Validated by:** creek (research agent), 2026-04-05
> Verified by reading actual source code in the dendra codebase, checking
> gofrs/flock v0.13.0 platform support, and auditing all syscall usage.

## Summary

Dendra is currently a **Unix-only** tool. It depends on tmux for session
management, uses POSIX syscalls directly, and assumes a Unix-like environment
throughout. This document catalogs every platform-specific dependency and
assesses the work needed for cross-platform support.

## CGO Status

**No CGO usage.** Verified by searching the entire codebase:

- No `import "C"` statements
- No `CGO_ENABLED` references in build configs
- No C source files or cgo directives
- All dependencies are pure Go

This means `CGO_ENABLED=0` cross-compilation works out of the box. GoReleaser
can produce binaries for any GOOS/GOARCH combination without a cross-compiler
toolchain.

## Platform-Specific Code Audit

### 1. `syscall.Exec` — Process Replacement (Unix-only)

**File:** `internal/tmux/tmux.go` (lines 217–223)

```go
func (r *RealRunner) Attach(name string) error {
    if IsInsideTmux() {
        args := []string{"tmux", "switch-client", "-t", exactTarget(name)}
        return syscall.Exec(r.TmuxPath, args, os.Environ())
    }
    args := []string{"tmux", "attach-session", "-t", exactTarget(name)}
    return syscall.Exec(r.TmuxPath, args, os.Environ())
}
```

**Platform impact:** `syscall.Exec` replaces the current process (Unix
`execve`). This syscall does not exist on Windows. However, this is part of the
tmux integration which is inherently Unix-only, so the platform constraint is
already implied.

### 2. `syscall.Flock` — File Locking (Unix-only)

**File:** `internal/merge/git.go` (lines 12–30)

```go
func RealLockAcquire(lockPath string) (func(), error) {
    // ...
    f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
    // ...
    if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
        // ...
    }
    return func() {
        _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
        _ = f.Close()
    }, nil
}
```

**Platform impact:** `syscall.Flock` is a POSIX-only call (`flock(2)`). This
will not compile on Windows. This is used for merge operation locking.

**Fix:** Replace with `gofrs/flock`, which is already a dependency and provides
cross-platform file locking (see below). The `agentloop.go` file already uses
`gofrs/flock` for the same purpose — these implementations should be unified.

### 3. `syscall.SIGTERM` / `syscall.SIGINT` — Signal Handling

**File:** `cmd/agentloop.go` (lines 250–251)

```go
sigCh := make(chan os.Signal, 1)
signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
```

**File:** `cmd/senseiloop.go` (lines 106–107)

```go
sigCh := make(chan os.Signal, 1)
signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
```

**Platform impact:** `SIGTERM` and `SIGINT` are POSIX signals. On Windows,
`SIGINT` is partially supported (via Ctrl+C), but `SIGTERM` does not exist.

**Fix:** Use `os.Interrupt` instead of `syscall.SIGINT` (cross-platform). For
graceful shutdown on Windows, use `os.Interrupt` alone — it maps to `CTRL_C_EVENT`.
The `signal.Notify(sigCh, os.Interrupt)` pattern works on both Unix and Windows.

### 4. tmux Dependency — Core Architecture (Unix-only)

**File:** `internal/tmux/tmux.go` (entire file, 250 lines)

The `tmux.Runner` interface and `RealRunner` implementation form a core part of
dendra's architecture. Every agent runs in a tmux window. Key operations:

| Method | What it does |
|---|---|
| `HasSession()` | Checks if tmux session exists |
| `NewSession()` | Creates a tmux session |
| `NewWindow()` | Creates a window in a session |
| `KillWindow()` | Kills a tmux window |
| `ListWindowPIDs()` | Lists PIDs of tmux window processes |
| `ListSessionNames()` | Lists all tmux sessions |
| `SendKeys()` | Sends keystrokes to a tmux pane |
| `Attach()` | Attaches to / switches to a tmux session |
| `IsInsideTmux()` | Checks `TMUX` env var |

**Platform impact:** tmux is not available on Windows natively. This is the
**single largest barrier** to Windows support. The entire agent orchestration
model depends on tmux for:

- Process isolation (each agent in its own window)
- Session management (attach/detach/switch)
- Process lifecycle (kill windows, list PIDs)

### 5. Shell Assumptions

**File:** `internal/tmux/tmux.go` (lines 231–250)

`BuildShellCmd()` and `ShellQuote()` produce shell-escaped command strings for
tmux's `new-session` command. These assume a POSIX shell.

**File:** Various test scripts in `scripts/` use `/tmp/`, `bash`, and other
Unix conventions.

### 6. `os/exec` Usage — Generally Cross-Platform

Files using `os/exec.Command()`:

- `internal/tmux/tmux.go` — tmux commands (Unix-only regardless)
- `internal/merge/git.go` — git commands (cross-platform if git is installed)
- `internal/worktree/worktree.go` — git commands (cross-platform)
- `internal/agent/claude.go` — claude CLI (cross-platform)
- `internal/memory/oneshot.go` — claude CLI (cross-platform)
- `internal/agentloop/real_starter.go` — process starting (cross-platform)
- `cmd/spawn.go`, `cmd/merge.go`, `cmd/retire.go`, `cmd/cleanup_branches.go` —
  git commands (cross-platform)

**Platform impact:** `os/exec` itself is cross-platform. The git and claude CLI
calls would work on any platform where those tools are installed.

## gofrs/flock Platform Support

**Version in use:** v0.13.0 (latest as of October 2025)

**Platform-specific implementations:**

| File | Platform |
|---|---|
| `flock_unix.go` | Linux, macOS, BSDs |
| `flock_unix_fcntl.go` | Unix systems using fcntl |
| `flock_windows.go` | Windows |
| `flock_others.go` | Fallback for other platforms |

**Assessment:** gofrs/flock fully supports Windows. It is already used in
`cmd/agentloop.go` for work locking. The direct `syscall.Flock` call in
`internal/merge/git.go` should be replaced with `gofrs/flock` to eliminate one
platform-specific dependency and unify the locking approach.

## Two Inconsistent Locking Implementations

The codebase has **two different file-locking approaches** for the same concept:

1. **`cmd/agentloop.go`** (lines 106–114) — Uses `gofrs/flock`:
   ```go
   fl := flock.New(lockPath)
   return &workLock{
       Acquire: func() error { return fl.Lock() },
       Release: func() error { return fl.Unlock() },
   }
   ```

2. **`internal/merge/git.go`** (lines 12–30) — Uses raw `syscall.Flock`:
   ```go
   syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
   ```

**Recommendation:** Unify on `gofrs/flock` everywhere. This eliminates the
`syscall` import in `internal/merge/git.go` and makes the merge locking
cross-platform.

## Platform Support Matrix

| Component | Linux | macOS | Windows | Fix Needed |
|---|---|---|---|---|
| Go build (`CGO_ENABLED=0`) | Yes | Yes | Yes | None |
| `gofrs/flock` file locking | Yes | Yes | Yes | None |
| `syscall.Flock` (merge) | Yes | Yes | No | Replace with gofrs/flock |
| `syscall.Exec` (tmux attach) | Yes | Yes | No | Part of tmux — N/A |
| `syscall.SIGTERM/SIGINT` | Yes | Yes | Partial | Use `os.Interrupt` |
| tmux session management | Yes | Yes | No | Major architecture change |
| git commands (`os/exec`) | Yes | Yes | Yes | None (if git installed) |
| claude CLI (`os/exec`) | Yes | Yes | Yes | None (if claude installed) |
| Shell quoting | Yes | Yes | No | Needs Windows cmd/PowerShell support |

## Realistic Assessment

### What's Easy (Quick Wins)

1. **Replace `syscall.Flock` with `gofrs/flock`** in `internal/merge/git.go` —
   already have the dependency, just need to swap the implementation.
2. **Replace `syscall.SIGTERM/SIGINT` with `os.Interrupt`** in both loop
   commands — one-line change per file.
3. **Build for macOS** — already works, just need GoReleaser config.

### What's Hard (Architectural)

1. **Windows support** requires replacing tmux with an alternative process
   management approach. Options:
   - Windows Terminal tabs/panes
   - A custom process supervisor (no terminal multiplexer)
   - WSL (just run the Linux binary)
2. **Shell quoting** needs a Windows-aware implementation for cmd.exe /
   PowerShell.

### Recommendation

**Target linux/amd64, linux/arm64, darwin/amd64, darwin/arm64 for initial
release.** These four targets work today with minimal changes (just the
GoReleaser config). Windows support is a significant project that should be
tracked separately.

The quick wins (unify flock, fix signal handling) should be done regardless —
they improve code quality even without Windows as a target.

## Build Tags (None Currently Used)

The codebase has no platform-specific build tags (`//go:build`) in production
code. The only build tag is `//go:build integration` in
`internal/protocol/protocol_integration_test.go`, which gates integration tests.

If Windows support is pursued, the tmux package would need build tags to
provide platform-specific implementations behind a common interface.

## Open Questions

- Is Windows support actually desired, or is linux + macOS sufficient?
- If Windows is needed, is WSL an acceptable target (avoids the tmux problem)?
- Should the two locking implementations be unified now as a code quality
  improvement, independent of cross-platform goals?
