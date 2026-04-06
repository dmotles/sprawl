# CLI UX Best Practices for Sprawl

**Load this skill before adding or modifying any CLI command's behavior.**

Sprawl's CLI is consumed almost exclusively by AI agents (Claude Code), not humans at a TTY. Every design decision should optimize for non-interactive, agent-driven usage. No TUIs, no interactive prompts, no spinners, no color-dependent output.

---

## Core Principle: Every Command Tells the Agent What To Do Next

The single most important pattern in this CLI: **after every command — success or failure — tell the caller what action to take next.** The agent reading your output has no institutional knowledge. Treat it like a new hire on their first day.

### The "Next Action Hint" Pattern

This is inspired by git, which is famously good at this:

```
$ git checkout -b feature
Switched to a new branch 'feature'
```

```
$ git push
fatal: The current branch feature has no upstream branch.
To push the current branch and set the remote as upstream, use:
    git push --set-upstream origin feature
```

**Apply this to every sprawl command:**

```go
// GOOD: spawn tells the agent what happens next
fmt.Fprintf(os.Stderr, "Spawned engineer %s (branch: %s)\n", name, branch)
fmt.Fprintf(os.Stderr, "Agent will message you when done — no need to poll.\n")

// GOOD: handoff tells the agent what to communicate
fmt.Fprintf(os.Stderr, "Handoff complete. Session %s is ready for the next agent.\n", session)
fmt.Fprintf(os.Stderr, "Tell the user it is now safe to terminate this session (Ctrl+C) if they wish.\n")

// GOOD: merge tells the agent the state of the world
fmt.Fprintf(os.Stderr, "Merged agent %q (branch %s) into %s\n", name, branch, target)
fmt.Fprintf(os.Stderr, "  Squash commit: %s %q\n", hash, summary)
fmt.Fprintf(os.Stderr, "  Branch %s deleted\n", branch)
fmt.Fprintf(os.Stderr, "  Agent %s retired\n", name)
fmt.Fprintf(os.Stderr, "You can now review the changes with: git log --oneline -5\n")

// GOOD: error with recovery suggestion
return fmt.Errorf("agent %q is not in 'done' state (current: %s) — wait for it to finish or use --force to merge anyway", name, status)

// BAD: bare success with no guidance
fmt.Fprintf(os.Stderr, "Done.\n")

// BAD: error with no recovery path
return fmt.Errorf("merge failed")
```

### What Hints Should Include

| Scenario | Hint should say |
|---|---|
| Command succeeded | What the agent should do or communicate next |
| Command succeeded (async) | That no polling is needed, or how to check status |
| Command failed (recoverable) | The exact command or action to fix it |
| Command failed (precondition) | What state needs to change first |
| Command failed (permanent) | That it's not retryable and what alternative exists |
| Destructive action completed | What was changed and what cannot be undone |

---

## Output Design for Agent Consumers

### stderr vs stdout

Follow the Unix convention strictly — this matters for agents that parse output:

- **stdout**: Machine-parseable data (JSON, agent names, branch names, IDs). This is the "return value."
- **stderr**: Human/agent-readable status messages, hints, warnings. This is the "log."

An agent piping output will get clean data on stdout and context on stderr.

### Output Format

Keep output **plain text on stderr, structured on stdout** when data is involved:

```go
// Status/progress → stderr
fmt.Fprintf(os.Stderr, "Spawned engineer %s (branch: %s)\n", name, branch)

// Data the caller needs to parse → stdout
fmt.Fprintf(os.Stdout, "%s\n", agentName)  // bare value, easy to capture
```

For commands that return structured data (status, tree, list), support a `--json` flag:

```go
// Human-readable default
fmt.Fprintf(os.Stdout, "%-12s %-10s %s\n", name, status, branch)

// --json for programmatic access
json.NewEncoder(os.Stdout).Encode(agent)
```

### Keep Output Scannable

- Lead with the most important information
- Use `key: value` format for details
- Indent subordinate details with 2 spaces
- One logical action per line

```
Retired agent "cedar" (branch dmotles/qum-42 preserved)
  Worktree cleaned up
  Messages archived (3 messages)
  Tell the user: agent cedar has completed its work and been retired.
```

---

## Error Messages That Enable Recovery

Every error message must answer three questions:

1. **What happened?** — the immediate failure
2. **Why?** — the precondition or context that caused it
3. **What should I do?** — the recovery action

### Pattern: Include Bad Value + Valid Options

```go
// GOOD
return fmt.Errorf("invalid agent type %q for family %q; valid types: %v", agentType, family, validTypes)

// BAD
return fmt.Errorf("invalid agent type")
```

### Pattern: Suggest the Fix Command

```go
// GOOD — agent can copy-paste the fix
return fmt.Errorf("agent %q has no worktree; create one first with: sprawl spawn --family engineering --type engineer --prompt '...'")

// GOOD — explains the precondition
return fmt.Errorf("cannot merge %q: agent status is %q, expected \"done\". Wait for it to finish, or use: sprawl merge --force %s", name, status, name)
```

### Pattern: Distinguish Retryable vs Permanent Failures

```go
// Retryable — agent knows it can retry
return fmt.Errorf("tmux session %q not responding (may be starting up) — retry in a few seconds", session)

// Permanent — agent knows to try something else
return fmt.Errorf("branch %q has already been deleted — nothing to merge. The work may already be integrated", branch)
```

### Exit Codes

Use semantic exit codes so agents can branch without parsing error text:

| Code | Meaning | Agent action |
|---|---|---|
| 0 | Success | Proceed to next step |
| 1 | General failure | Read stderr for recovery hint |
| 2 | Usage error (bad args/flags) | Fix the command invocation |

Cobra handles exit code 2 for usage errors automatically. For domain errors, return errors from `RunE` (exits 1).

---

## Idempotency and Safety

Agents retry. Network glitches happen. Commands get called twice. **Design for it.**

### Idempotent Operations: Warn, Don't Error

```go
// GOOD — safe to retry
if agentState.Status == "killed" {
    fmt.Fprintf(os.Stderr, "Warning: agent %q is already killed — no action needed.\n", name)
    return nil
}

// GOOD — already in desired state
if agentState.Status == "retired" {
    fmt.Fprintf(os.Stderr, "Agent %q is already retired — nothing to do.\n", name)
    return nil
}
```

### Destructive Operations: Require Explicit Intent

For operations that can't be undone, require a `--force` flag or explicit confirmation via flag:

```go
// Non-interactive confirmation via flag
if !forceFlag {
    return fmt.Errorf("refusing to merge agent %q with uncommitted changes — use --force to override", name)
}
```

**Never prompt for interactive confirmation** — agents can't answer `y/n` prompts.

---

## Command Naming and Structure

### Verb-Based Commands (Sprawl's Pattern)

Sprawl uses direct verbs: `spawn`, `kill`, `retire`, `merge`, `status`, `handoff`. This is good for a focused tool. Keep it.

### Naming Conventions

- Use **lowercase, single-word** commands when possible: `spawn`, `kill`, `merge`
- Use **hyphenated** names for multi-word: `cleanup-branches`
- Flag names: `--family`, `--force`, `--json` (GNU long-form)
- Required flags: mark with `MarkFlagRequired` — cobra gives good errors for free

### Help Text

Write `Short` descriptions that complete the sentence "sprawl [command] will...":

```go
Short: "Spawn a new agent"           // good
Short: "This command spawns agents"   // bad — redundant phrasing
Short: "Agent spawning"               // bad — noun phrase, not actionable
```

Write `Long` descriptions that explain **when and why** to use the command, not just what it does:

```go
Long: "Spawn a new agent with the given family and type. " +
      "The agent runs in its own tmux window and git worktree. " +
      "It will message you when its task is complete.",
```

---

## Patterns from Famously Good CLIs

### git: "Did you mean...?" and Next-Step Suggestions

Git never leaves you at a dead end. Every error suggests a path forward. Every successful state-change tells you what's different now and what you might want to do next. **This is the #1 pattern to emulate.**

### gh (GitHub CLI): Progressive Disclosure and `--json`

`gh` keeps default output human-scannable but offers `--json field1,field2` for machine parsing. Great for a CLI consumed by both agents and humans during debugging.

### kubectl: Declarative Idempotency

`kubectl apply` can be run 10 times with the same result. When state already matches, it says "unchanged" — it doesn't error. **Sprawl should treat already-in-desired-state as success, not failure.**

### terraform: Plan Before Apply

For destructive operations, show what will change before doing it. Terraform's `plan` → `apply` pattern maps to sprawl's `--dry-run` potential for risky commands.

### docker: Noun-Verb Hierarchy for Discovery

`docker container ls`, `docker image rm` — the command tree is self-documenting. Agents can explore via `--help` at each level. Sprawl's flat verb structure works fine at current scale, but if it grows, consider grouping.

---

## Checklist: Before Shipping a New Command

Use this checklist when adding or modifying any sprawl command:

- [ ] **Success output includes a next-action hint** — what should the agent do/communicate after this succeeds?
- [ ] **Error messages include recovery suggestions** — every error says what to do about it
- [ ] **Idempotent where possible** — calling it twice doesn't break things
- [ ] **No interactive prompts** — all input comes via flags and args
- [ ] **stderr for status, stdout for data** — output streams are correct
- [ ] **Help text is actionable** — Short completes "sprawl X will...", Long explains when/why
- [ ] **Validation errors include bad value + valid options** — the agent can self-correct
- [ ] **Destructive actions require --force** — no silent data loss
- [ ] **`--json` flag if the command returns structured data** — for programmatic consumption

---

## Quick Reference: Output Templates

### Success with next step
```
[Action completed]: [key details]
[What the agent should do or communicate next]
```

### Success with async follow-up
```
[Action started]: [key details]
[How the agent will be notified, or that no polling is needed]
```

### Recoverable error
```
[What failed]: [specific context]
[Exact command or action to fix it]
```

### Idempotent no-op
```
Warning: [resource] is already [desired state] — no action needed.
```

### Multi-step result
```
[Primary action]: [key details]
  [Detail 1]
  [Detail 2]
  [Detail 3]
[Next step hint]
```
