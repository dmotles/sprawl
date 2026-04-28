# Sprawl

**Sprawl** — named for the interconnected urban megastructure in William Gibson's *Sprawl trilogy* — is a self-organizing AI agent orchestration system built on top of Claude Code. The CLI command is `sprawl`.

## Why

Today's AI coding agents are powerful but singular. You talk to one agent, it does one thing, and complex work requires you to manually decompose problems, manage context, and coordinate efforts. This doesn't scale.

What if you could give a high-level goal to an AI system and it would figure out how to organize itself to achieve it? Not through a rigid, predefined pipeline, but through a simple set of rules that allow agents to self-organize, spawn sub-agents, decompose work, and converge toward a completed goal.

The inspiration comes from Conway's Game of Life — not because Sprawl is a cellular automaton, but because of the core insight: **simple rules can produce complex, emergent behavior**. Unlike Conway's Game of Life, which runs indefinitely, Sprawl is goal-directed. The system converges. Tasks get decomposed until they become atomic, the atomic work gets done, results flow back up, and the system resolves.

## What

Sprawl is a terminal-first CLI tool that orchestrates multiple Claude Code instances. You run `sprawl enter`, it spawns the **root** agent inside the sprawl TUI, and you **seed** it with a goal. From there the system self-organizes to accomplish it.

### Seeding

Seeding is the act of giving the root agent its initial goal — the prompt that starts everything. A seed is a high-level objective ("build a SaaS billing system", "migrate the API from REST to GraphQL") that the root will decompose, organize around, and drive to completion. The seed is the input; the fully realized project is the output.

### The Root

The root is the top-level agent and the only one the user interacts with directly. It cannot make code changes itself — its job is to understand the seed, decide how to organize work, and spawn the right agents to get it done. It can read code, execute commands, check on the status of its children, send them messages, and report back to the user. Everything grows from the root.

### Agent Identity

Every agent in the system has a unique name drawn from a pre-set pool of ~50 names. When an agent is spawned, it is assigned the next available name. The name is set as the `SPRAWL_AGENT_IDENTITY` environment variable in the agent's environment, so every agent always knows who it is. This means commands like `sprawl spawn` don't need a `--parent` flag — the system knows who's spawning based on the caller's identity.

If the name pool is exhausted, the system errors: "no more agents can be spawned." This acts as a natural ceiling on system complexity. Future versions may generate additional names dynamically.

### Agent Lifecycle

The root agent is an **interactive Claude Code session** — the user talks to it directly.

Every other agent runs in a **loop**:

1. **Wake** — Claude Code is launched (or resumed) with a prompt, e.g., "You have new messages. Check your inbox."
2. **Work** — The agent runs until it hits a stop state (task complete, waiting for input, nothing left to do).
3. **Sleep** — The loop takes over. It watches for incoming messages, new task assignments, or signals from the system.
4. **Wake again** — When something arrives, the loop resumes the Claude Code session using the **same session ID**, preserving the agent's full conversation history and context.

This means agents are **dormant but reusable**, not disposable. An engineer named Frank does a task, goes dormant, and can be woken up for follow-up work with full memory of what it already did. The manager decides whether to reuse Frank (who has context from related work) or spawn a fresh agent (because Frank is busy or context doesn't matter).

Claude Code's `--resume <session-id>` flag and `--json` output mode are the mechanisms that make this work. The session ID is defined when the agent is first created and reused for every subsequent wake cycle.

### Agent Types

Agents come in four types. The system's philosophy is to keep rules simple and let agents — especially managers — exercise judgment about how to accomplish their goals.

| Type | Can edit code | Can spawn agents | Worktree | Lifecycle |
|---|---|---|---|---|
| **Root** | No | Yes | Main | Persistent interactive session |
| **Manager** | No | Yes | Own worktree + integration branch | Dormant between tasks, reusable, lives until goal complete |
| **Engineer** | Yes | No | Own worktree + branch | Dormant between tasks, reusable for follow-up work |
| **Researcher** | No | No | Own worktree | Dormant between tasks, reusable for follow-up work |

#### Root

The root cannot edit code. It can read code, execute commands, and spawn agents. It is the only agent the user interacts with directly. Its prompt is kept intentionally simple: your job is to take the wishes of the user and execute them into changes. Your tools are spawning agents and messaging agents.

#### Manager

A manager receives a task from its parent and decides how to execute it. Its core responsibilities:

1. **Decompose** — Break the task into 3-10 subtasks. No more. If a subtask is still too big, spawn a sub-manager for it. If it's small enough (a few hundred lines, one module, one commit's worth of changes), spawn an engineer.
2. **Dispatch** — Spawn the right agents for each subtask. Managers can spawn engineers, researchers, and other managers.
3. **Wait and respond** — Sit and wait for agents to report back. When work comes in, decide what to do with it.
4. **Integrate** — When an engineer reports done, the manager evaluates the work. If it's good (possibly after having a researcher or QA agent review it), it uses `sprawl merge` to squash-merge the engineer's branch into the manager's integration branch. If the work is bad, the manager has two choices:
   - **Abandon and respawn**: scrap the work and spawn a new engineer with corrected instructions based on what went wrong.
   - **Spawn forward**: if it's close but needs tweaks, send follow-up work to the same agent (who has context) or spawn a new one to fix the issues from where the previous one left off.
5. **Manage agents** — Managers can reuse idle agents for follow-up work (the agent retains its session context) or kill unresponsive agents (`sprawl kill <agent>`).
6. **Report up** — When all subtasks are complete and merged into the manager's integration branch, report to the parent that the branch is ready to be merged up.

The key design principle: **the manager decides how to handle every situation.** Your objective is X; figure out how to make it happen. These are the tools you have. This keeps the rules simple and lets emergent behavior handle the complexity.

A big part of the manager's job is understanding **parallelism and dependencies**. Some subtasks are independent and can be worked on simultaneously by different engineers in separate worktrees. Others have dependencies and must be sequenced. Some resources (like a shared dev environment) can only be used by one agent at a time. The manager must reason about this when dispatching work.

#### Engineer (IC)

The hands-on builder. Engineers have full, unfettered access to edit code, create files, run commands, and make changes within their own worktree. They are leaf nodes — they cannot spawn other agents. When they finish their task, they report done. If they discover additional work is needed beyond their scope, they report the problem back to their manager, who decides how to handle it.

When an engineer is spawned, the system creates a new git worktree and branch for the agent to work in.

#### Researcher (IC)

An individual contributor without code editing permissions. They can read code, execute commands, and search the web. Useful for investigation, research, documentation, review, and analysis tasks. Like engineers, they are leaf nodes and cannot spawn agents.


### Agent Families

Agents also belong to one of three families, which represent the agent's orientation and the kind of prompting/expertise it brings:

1. **Product** — Concerned with the *why* and the *what*. These agents define, research, document, and shape product direction. They care about business outcomes, user experience, and product specifications. They may make technical decisions at a high level but never go deep into implementation.

2. **Engineering** — Concerned with the *how*. These agents decompose product requirements into executable work, make architectural decisions, and — at the leaf level — write the actual code.

3. **Quality Assurance** — Concerned with *correctness*. These agents test, verify, and ensure that the work meets the quality bar and specifications. They mechanically validate all aspects of the product.

### The Rules

The system operates on a small set of simple rules:

1. **The root grows the initial network.** Based on the seed, the root creates managers in whatever family makes sense — product managers, engineering managers, QA managers — with guidelines but also freedom to decide the right structure.

2. **Managers decompose and delegate.** When a manager receives a task, it decides: is this big enough to warrant sub-managers, or can I hand this directly to an IC? This decision is made autonomously by each manager. A manager should own no more than 3-10 subtasks at a time.

3. **Managers own integration.** Each engineering manager has its own integration branch. When an IC's work is deemed ready, the manager uses `sprawl merge` to squash-merge it in. When all subtasks are integrated, the manager reports up that its branch is ready.

4. **Managers handle failure.** When work comes back wrong, the manager decides: abandon and respawn with better instructions, or spawn forward to fix from where it is. The manager exercises judgment.

5. **ICs do the work but cannot spawn agents.** Engineers and researchers are leaf nodes. They execute their assigned task. If they discover additional work is needed, they report the problem back to their manager.

6. **Completion flows upward.** When a manager's entire scope of work is done and integrated, it reports completion to its parent. The parent merges the manager's branch up. This cascades until the root can report to the user that the goal is achieved.

### The Forcing Function

The system doesn't spiral into infinity because of a natural forcing function: **task decomposition bottoms out**. At some point, a task becomes simple enough that the only thing left to do is make the code change, write the document, or run the test. The recursive spawning of managers converges toward atomic units of work, and atomic work gets done by ICs.

This is what distinguishes Sprawl from Conway's Game of Life. Conway's system is aimless — patterns emerge and evolve but go nowhere in particular. Sprawl is goal-directed. The expansion is in service of convergence. The network grows outward so it can collapse back inward with a completed result.

## CLI

The `sprawl` CLI is how agents interact with the system. Rather than providing tools via MCP servers, the system's capabilities are exposed as CLI commands. This keeps agents loosely coupled to Claude Code specifically — the CLI is the interface, not the model.

### Core Commands

```
sprawl enter                         Launch the root agent (TUI)
```

### Spawning & Agent Management

```
sprawl spawn \
  --family <product|engineering|qa> \
  --type <manager|engineer|researcher> \
  --branch <branch-name> \
  --prompt "<task description>"

sprawl kill <agent-name>             Kill an unresponsive agent
```

The calling agent's identity is inferred from `SPRAWL_AGENT_IDENTITY` — no `--parent` flag needed.

### Messaging

Agents communicate via a mailbox-style messaging system.

```
sprawl messages send <agent-id> "<subject>" "<message>"
sprawl messages broadcast "<subject>" "<message>"
sprawl messages inbox
sprawl messages list [all|sent|read|unread|archived]
sprawl messages read <msg-id>
sprawl messages unread <msg-id>
sprawl messages archive <msg-id>
```

Broadcast sends a message to all agents. Intended primarily for the root.

### Reporting

Agents report status to their parent (superior) in the network.

```
sprawl report status "<status>"      Report current status
sprawl report done "<result>"        Report successful completion
sprawl report problem "<problem>"    Escalate an issue
```

### Signaling

Underlying the messaging and reporting system is a signaling mechanism integrated with Claude Code hooks. When a message arrives or a child agent reports a status change, a hook fires to nudge the receiving agent — prompting it to check its inbox or handle the notification. This avoids polling and keeps agents responsive without building a custom communication protocol.

## Name

The name **Sprawl** is taken from William Gibson's *Sprawl trilogy* (*Neuromancer*, *Count Zero*, *Mona Lisa Overdrive*) — the Boston-Atlanta Metropolitan Axis, a vast interconnected urban megastructure where networks overlap, evolve, and self-organize. Like Gibson's Sprawl, this system is an organic network of autonomous agents that grows, adapts, and connects to accomplish complex goals. The CLI command `sprawl` is the entry point into the network.

## Platform

- **CLI entry point:** `sprawl enter` launches the root agent inside the TUI
- **Runtime:** Orchestrates Claude Code instances; child agents run in tmux sessions managed by sprawl
- **Git strategy:** Each agent operates in its own git worktree. Issue tracking is external (users bring their own — Linear, GitHub Issues, etc.).

## Future / Potential Enhancements

The following features are planned but not yet implemented:

- **Tester agent type** — A quality-focused individual contributor that writes and runs tests, verifies correctness, and validates that work meets specifications. Would have code editing permissions (for writing test code) and its own worktree.
- **Code Merger agent type** — A specialized agent whose sole job is to merge a completed branch into a manager's integration branch. Currently, merging is done via the `sprawl merge` command by managers directly.
- **Automatic .env copying** — Copying/initializing secrets (e.g., `.env` from the root) when spawning engineer agents.
- **Web UI** — A web-based interface for visualization and monitoring of the agent network.

