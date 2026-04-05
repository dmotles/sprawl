# Security Audit

**Date:** 2026-04-04
**Original researcher:** delta
**Status:** Reconstructed from summary — needs agent validation/expansion

## Summary

Security audit of the dendra codebase for vulnerabilities relevant to publishing as open source.

## Findings

### Excellent: Command Injection Defenses
- All `exec.Command` calls use argument arrays (not shell strings)
- Proper shell quoting for tmux commands
- No `os/exec` with `sh -c` patterns

### CRITICAL: Agent Name Path Traversal
- Agent names from CLI args are passed to `filepath.Join` unsanitized
- Affects: state, messages, locks, worktree code
- A name like `../../etc` could escape the `.dendra/` directory
- **Fix:** Add centralized agent name validator rejecting slashes, dots, and other path-unsafe characters

### CRITICAL: Agent Identity Spoofing
- `DENDRA_AGENT_IDENTITY` env var is trivially spoofable
- Messages have no sender verification
- No access controls on agent state/messages
- **Note:** This is inherent to the trust model — agents run locally as the same user. Worth documenting but may not need fixing.

### HIGH: Prompt Injection
- Message subjects, poke files, and wake files feed directly into Claude prompts
- An attacker with filesystem access could inject instructions into agent prompts
- **Note:** Same trust boundary as identity spoofing — local filesystem access implies full control anyway.

### HIGH: File Permissions
- All files created 0o644 (world-readable)
- Includes agent state, prompts, and messages
- On shared systems, other users could read agent state
- **Fix:** Consider 0o600 for sensitive files, or document the single-user assumption

### MEDIUM: Sandbox is Advisory-Only
- `DENDRA_TEST_MODE=1` just injects text into system prompt
- No OS-level enforcement (no chroot, no namespace isolation)
- **Note:** Acceptable for current use case. Document the limitation.

### LOW: Dependencies Clean
- Only 2 direct deps (cobra + flock)
- No known vulnerabilities
- Minimal attack surface

## Priority Actions

1. **Agent name validation** — highest bang-for-buck. Single function, closes all path traversal.
2. **Document trust model** — agents trust each other, filesystem is the trust boundary. This is by design.
3. **Consider tighter file permissions** — 0o600 instead of 0o644 for state files.
