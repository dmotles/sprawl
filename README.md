# Sprawl

**Sprawl** — named for the interconnected urban megastructure in William Gibson's *Sprawl trilogy* — is a self-organizing AI agent orchestration system built on top of [Claude Code](https://docs.anthropic.com/en/docs/claude-code). The CLI command is `sprawl`.

Give it a high-level goal. It figures out how to organize itself to achieve it.

## Why

Today's AI coding agents are powerful but singular — one agent, one task, manual coordination. Sprawl asks: what if a simple set of rules could let agents self-organize, decompose work, and converge toward a completed goal?

Inspired by Conway's Game of Life — not as a cellular automaton, but by the core insight that **simple rules produce complex, emergent behavior**. Unlike Conway's system, Sprawl is goal-directed. The network expands outward so it can collapse back inward with a completed result.

## How It Works

You run `sprawl init`, which spawns the **root** agent in a tmux session. You give it a **seed** — a high-level objective like "build a SaaS billing system" or "migrate the API from REST to GraphQL." From there, the system self-organizes: the root spawns managers, managers decompose and delegate, engineers write code, and results flow back up the network.

### Agent Types

| Type | Can Edit Code | Can Spawn Agents | Worktree | Lifecycle |
|---|---|---|---|---|
| **Root** | No | Yes | Main | Persistent interactive session |
| **Manager** | No | Yes | Own worktree + integration branch | Dormant between tasks, reusable |
| **Engineer** | Yes | No | Own worktree + branch | Dormant between tasks, reusable |
| **Researcher** | No | No | Own worktree | Dormant between tasks, reusable |
| **Code Merger** | Merge only | No | Parent manager's worktree | Ephemeral — one merge, then done |

- **Root** — The only agent the user interacts with directly. Cannot edit code. Understands the seed, decides how to organize work, and spawns agents.
- **Manager** — Receives a task, decomposes it into 3-10 subtasks, dispatches agents, integrates completed work, and handles failures. Managers own an integration branch and reason about parallelism and dependencies.
- **Engineer** — The hands-on builder. Full access to edit code, create files, and run commands in their own worktree. Leaf node — cannot spawn agents.
- **Researcher** — An IC without code editing permissions. Reads code, runs commands, searches the web. Used for investigation, review, and analysis.
- **Code Merger** — Merges a completed branch into a manager's integration branch. Spawned on demand, operates in the parent manager's worktree, then dies.

### Agent Families

Each agent belongs to a family that shapes its orientation:

1. **Product** — Concerned with the *why* and the *what*. Defines, researches, and shapes product direction.
2. **Engineering** — Concerned with the *how*. Decomposes requirements, makes architectural decisions, writes code.
3. **Quality Assurance** — Concerned with *correctness*. Tests, verifies, and validates.

### Agent Lifecycle

The root agent is an interactive Claude Code session. Every other agent runs in a wake/sleep loop:

1. **Wake** — Claude Code is launched (or resumed) with a prompt.
2. **Work** — The agent runs until it hits a stop state.
3. **Sleep** — The loop watches for incoming messages, new tasks, or system signals.
4. **Wake again** — The loop resumes the session using the same session ID, preserving full context.

Agents are dormant but reusable, not disposable. A manager can reuse an idle agent for follow-up work (with full memory of prior tasks) or spawn a fresh one.

### Agent Identity

Every agent gets a unique name from a pool of ~50 names, set as `SPRAWL_AGENT_IDENTITY` in its environment. Commands like `sprawl spawn` infer the caller's identity automatically — no `--parent` flag needed.

### The Rules

1. **The root grows the initial network.** Based on the seed, it creates managers with the right families and structure.
2. **Managers decompose and delegate.** Each manager decides: sub-managers for big tasks, ICs for small ones. 3-10 subtasks max.
3. **Managers own integration.** Each manager has an integration branch. Completed IC work gets merged in via Code Mergers.
4. **Managers handle failure.** Bad work? Abandon and respawn with better instructions, or spawn forward to fix from where it is.
5. **ICs do the work but cannot spawn.** Engineers and researchers are leaf nodes. They report problems back to their manager.
6. **Completion flows upward.** Done managers report to their parent, branches merge up, until the root reports success to the user.

### The Forcing Function

The system doesn't spiral into infinity because **task decomposition bottoms out**. Tasks eventually become atomic — make a code change, write a document, run a test — and atomic work gets done by ICs. The branching converges.

## CLI Reference

### Initialize

```
sprawl init                          # Launch the root agent
```

### Spawn and Manage Agents

```
sprawl spawn \
  --family <product|engineering|qa> \
  --type <manager|engineer|researcher|merger> \
  --branch <branch-name> \
  --prompt "<task description>"

sprawl kill <agent-name>             # Kill an unresponsive agent
```

### Messaging

Agents communicate via a mailbox-style system:

```
sprawl messages send <agent> "<subject>" "<message>"
sprawl messages broadcast "<subject>" "<message>"
sprawl messages inbox
sprawl messages list [all|sent|read|unread|archived]
sprawl messages read <msg-id>
sprawl messages unread <msg-id>
sprawl messages archive <msg-id>
```

### Reporting

Agents report status to their parent in the network:

```
sprawl report status "<status>"      # Report current status
sprawl report done "<result>"        # Report successful completion
sprawl report problem "<problem>"    # Escalate an issue
```

### Signaling

Messages and status changes trigger Claude Code hooks that nudge the receiving agent — no polling required.

## Getting Started

### Prerequisites

- [Claude Code](https://docs.anthropic.com/en/docs/claude-code) installed and configured
- [tmux](https://github.com/tmux/tmux) installed
- [Go](https://go.dev/) (for building from source)
- Git

### Install

```bash
git clone <repo-url>
cd sprawl
make build
```

### Usage

```bash
# Initialize the system — launches the root agent in tmux
sprawl init

# In the root agent session, give it a seed:
# "Build a REST API for a todo app with authentication"

# The root will self-organize from there.
```

## Platform

- **Runtime:** Orchestrates Claude Code instances via tmux sessions
- **Git strategy:** Each agent operates in its own git worktree. Uses [beads](BEADS.md) (`bd`) for issue tracking per worktree.
- **Language:** Go

## License

See [LICENSE](LICENSE) for details.
