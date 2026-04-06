# Security Audit

**Date:** 2026-04-04
**Original researcher:** delta
**Validated by:** brook (2026-04-05) — all claims verified against codebase with file paths/line numbers, additional findings added
**Status:** Validated ✓

## Summary

Security audit of the sprawl codebase for vulnerabilities relevant to publishing as open source. All original findings confirmed. Path traversal is the most actionable issue — no agent name validation exists anywhere in the codebase.

## Findings

### Excellent: Command Injection Defenses

- All `exec.Command` calls use argument arrays (not shell strings) — **CONFIRMED**
- Proper shell quoting for tmux commands
- One `bash -c` usage exists but is safe:

```go
// internal/merge/git.go:126
cmd := exec.Command("bash", "-c", "make validate") //nolint:gosec // command is not user-controlled
```

This is a hardcoded command string, not user-controlled. The `//nolint` comment acknowledges the pattern.

### CRITICAL: Agent Name Path Traversal

**CONFIRMED — No validation exists anywhere.** Searched for `ValidateAgentName`, `validateAgentName`, `isValidName`, `nameRegex` — zero results. Agent names from CLI args flow directly into `filepath.Join` unsanitized in **8+ locations**.

A name like `../../etc` could escape the `.sprawl/` directory for reads, writes, and deletes.

#### Affected Code Paths

| File | Line | Operation | Code |
|------|------|-----------|------|
| `cmd/poke.go` | 56 | Write poke file | `filepath.Join(sprawlRoot, ".sprawl", "agents", agentName+".poke")` |
| `cmd/logs.go` | 59 | Read logs dir | `filepath.Join(sprawlRoot, ".sprawl", "agents", agentName, "logs")` |
| `internal/state/state.go` | 51 | Write state | `filepath.Join(dir, agent.Name+".json")` |
| `internal/state/state.go` | 60 | Read state | `filepath.Join(AgentsDir(sprawlRoot), name+".json")` |
| `internal/state/state.go` | 101 | Delete state | `filepath.Join(AgentsDir(sprawlRoot), name+".json")` |
| `internal/messages/messages.go` | 70 | Create message dirs | `filepath.Join(MessagesDir(sprawlRoot), to)` |
| `internal/messages/messages.go` | 129 | Write wake file | `filepath.Join(sprawlRoot, ".sprawl", "agents", to+".wake")` |
| `internal/worktree/worktree.go` | 27–29 | Create git worktree | `filepath.Join(worktreesDir, agentName)` |
| `internal/merge/git.go` | 134 | Write poke file | `filepath.Join(sprawlRoot, ".sprawl", "agents", agentName+".poke")` |
| `internal/agent/retire.go` | 31 | Write kill sentinel | `filepath.Join(sprawlRoot, ".sprawl", "agents", agentState.Name+".kill")` |

**Fix:** Add a centralized `ValidateAgentName()` function rejecting slashes, dots-prefix, and other path-unsafe characters. Call it at every CLI entry point before the name reaches any file operation.

### CRITICAL: Agent Identity Spoofing

**CONFIRMED.** `SPRAWL_AGENT_IDENTITY` is set in one place and checked only for presence:

- **Set:** `internal/agentloop/real_starter.go:31` — `env = append(env, fmt.Sprintf("SPRAWL_AGENT_IDENTITY=%s", config.AgentName))`
- **Checked:** `cmd/handoff.go:62`, `cmd/retire.go:131` — only checks non-empty, no cryptographic verification
- **Used in prompt:** `internal/agent/prompt.go:471` — mentioned in prompt text for the agent's self-awareness

Messages have no sender verification — the `from` field in `messages.Send()` is caller-supplied with no validation against identity. Any process can send messages claiming to be any agent.

**Note:** This is inherent to the trust model — agents run locally as the same OS user. Worth documenting but may not need fixing for v1.

### HIGH: Prompt Injection

**CONFIRMED — 4 distinct injection vectors identified.**

#### A. Poke file content → Claude prompt

```go
// cmd/agentloop.go:498-514
if content, readErr := deps.readFile(pokePath); readErr == nil {
    pendingPoke = strings.TrimSpace(string(content))  // unvalidated
}
// ... later:
_, pokeContent, sendErr := sendWithInterrupt(prompt)  // sent to Claude
```

#### B. Wake file content → Claude prompt

```go
// cmd/agentloop.go:613-622
wakeContent, readErr := deps.readFile(wakeFilePath)
if readErr == nil {
    wakePrompt := string(wakeContent)  // unvalidated
    _, pokeContent, sendErr := sendWithInterrupt(wakePrompt)  // sent to Claude
}
```

#### C. Message subject → prompt instruction

```go
// cmd/agentloop.go:581-582
cmdLines = append(cmdLines, fmt.Sprintf(
    "Run `sprawl messages read %s` to read a message from %s (subject: %q)",
    msg.ID, msg.From, msg.Subject,  // user-controlled msg.Subject
))
```

#### D. Wake file composed from untrusted subject

```go
// internal/messages/messages.go:129-131
wakePath := filepath.Join(sprawlRoot, ".sprawl", "agents", to+".wake")
wakeMsg := fmt.Sprintf("New message from %s: %s", from, subject)
_ = os.WriteFile(wakePath, []byte(wakeMsg), 0o644)
```

**Note:** Same trust boundary as identity spoofing — local filesystem access implies full control anyway. Document this as an accepted risk in the trust model.

### HIGH: File Permissions

**CONFIRMED — All files 0o644, all directories 0o755, with intentional `//nolint:gosec` annotations.**

#### File permissions (0o644)

| File | Line | What |
|------|------|------|
| `internal/state/state.go` | 52 | Agent state JSON |
| `internal/messages/messages.go` | 111 | Message tmp files |
| `internal/messages/messages.go` | 125 | Sent message copies |
| `internal/messages/messages.go` | 131 | Wake files |
| `internal/merge/git.go` | 135 | Poke files |
| `cmd/poke.go` | 57 | Poke files |

#### Directory permissions (0o755)

| File | Line | What |
|------|------|------|
| `internal/state/state.go` | 42, 119 | Agents dir, .sprawl dir |
| `internal/messages/messages.go` | 72, 124 | Message subdirs, sent dir |
| `internal/worktree/worktree.go` | 23 | Worktrees dir |

All are annotated with `//nolint:gosec // G301/G306: world-readable X is intentional`.

**Fix:** Consider 0o600 for files and 0o700 for directories containing agent state and messages, or document the single-user assumption prominently.

### MEDIUM: Sandbox is Advisory-Only

**CONFIRMED — purely a prompt-level warning with no OS enforcement.**

- **Detection:** `cmd/rootloop.go:173` reads `SPRAWL_TEST_MODE` env var
- **Implementation:** `internal/agent/prompt.go` lines 278–286, 309–310, 460–461, 512–513, 758–759 — appends a text warning to the system prompt:

```
# TEST SANDBOX MODE

You are operating in a testing sandbox for sprawl. Take care to:
- Avoid taking any action outside of $SPRAWL_ROOT
- ONLY execute sprawl using $SPRAWL_BIN (do not use bare 'sprawl' from PATH)
- Do not interact with production systems, push to remote repositories,
  or modify files outside the test directory
- This environment will be torn down after testing
```

No filesystem restrictions, no process isolation, no capability restrictions. The LLM agent can ignore this warning entirely. **Acceptable for current use case but should be documented as a limitation.**

### LOW: Dependencies Clean

**CONFIRMED.** From `go.mod`:

- **Direct:** `github.com/gofrs/flock v0.13.0` (file locking), `github.com/spf13/cobra v1.10.2` (CLI framework)
- **Indirect:** `github.com/inconshreveable/mousetrap v1.1.0`, `github.com/spf13/pflag v1.0.9`, `golang.org/x/sys v0.37.0`

Minimal attack surface. Only 2 direct dependencies + 3 transitives. No known vulnerabilities at time of audit.

## Priority Actions

1. **Agent name validation** — highest bang-for-buck. Single centralized function rejecting names containing `/`, `\`, `..`, or starting with `.`. Closes all 8+ path traversal vectors.
2. **Document trust model** — agents trust each other, filesystem is the trust boundary. Identity spoofing and prompt injection are accepted risks within this model. This should be a top-level doc (e.g., `docs/security-model.md`).
3. **Consider tighter file permissions** — 0o600/0o700 instead of 0o644/0o755 for state files and directories. The `//nolint` annotations show this was a conscious decision, but it should be revisited for shared systems.
4. **Document sandbox limitations** — `SPRAWL_TEST_MODE` is prompt-level only. Add a note to README or security docs.
