# Claude Code Capabilities Research: Agents, Skills, Plugins

**Date:** 2026-03-31
**Author:** dave (dendra agent)
**Purpose:** Research how Claude Code handles skills, plugins, and the `--agents` flag to inform Dendrarchy's design for TDD workflows and sub-agent orchestration.

---

## 1. CLI Flags Related to Agents, Skills, Plugins, and Prompting

### Agent-Related Flags

| Flag | Description |
|------|-------------|
| `--agent <agent>` | Select a specific agent for the session. Overrides the 'agent' setting. |
| `--agents <json>` | JSON object defining custom agents (e.g., `'{"reviewer": {"description": "Reviews code", "prompt": "You are a code reviewer"}}'`) |

### Prompting Flags

| Flag | Description |
|------|-------------|
| `--system-prompt <prompt>` | System prompt to use for the session (replaces default) |
| `--append-system-prompt <prompt>` | Append to the default system prompt |
| `--bare` | Minimal mode: skip hooks, LSP, plugin sync, attribution, auto-memory, background prefetches, keychain reads, CLAUDE.md auto-discovery. Skills still resolve via `/skill-name`. Context must be explicitly provided. |

### Plugin/Skill Flags

| Flag | Description |
|------|-------------|
| `--plugin-dir <path>` | Load plugins from a directory (repeatable: `--plugin-dir A --plugin-dir B`) |
| `--disable-slash-commands` | Disable all skills |

### Tool Control Flags

| Flag | Description |
|------|-------------|
| `--allowedTools <tools...>` | Comma/space-separated list of tool names to allow (e.g., `"Bash(git:*) Edit"`) |
| `--disallowedTools <tools...>` | Comma/space-separated list of tool names to deny |
| `--tools <tools...>` | Specify list of available tools. Use `""` for none, `"default"` for all, or specific names. |

### Session Control

| Flag | Description |
|------|-------------|
| `--model <model>` | Model for the session (alias like `sonnet`/`opus` or full name) |
| `--effort <level>` | Effort level: low, medium, high, max |
| `--permission-mode <mode>` | Choices: acceptEdits, bypassPermissions, default, dontAsk, plan, auto |
| `--worktree [name]` | Create a new git worktree for this session |
| `--mcp-config <configs...>` | Load MCP servers from JSON files or strings |
| `--settings <file-or-json>` | Additional settings from file or JSON string |

### Subcommands

```
claude agents          # List configured agents
claude plugin          # Manage plugins (install, uninstall, enable, disable, list, update, validate)
claude plugin marketplace  # Manage marketplaces
```

**`claude agents` output (current):**
```
5 active agents

Built-in agents:
  claude-code-guide · haiku
  Explore · haiku
  general-purpose · inherit
  Plan · inherit
  statusline-setup · sonnet
```

---

## 2. Directory Structure: `.claude/`

### User-Level (`~/.claude/`)

```
~/.claude/
├── CLAUDE.md                  # User's global instructions for all projects
├── settings.json              # User settings (e.g., autoUpdatesChannel)
├── sessions/                  # Persisted session data (JSON files by ID)
├── paste-cache/               # Cached paste data
├── backups/                   # Backup files
└── plugins/
    ├── blocklist.json         # Plugin blocklist
    └── marketplaces/
        └── claude-plugins-official/
            ├── .gcs-sha
            ├── .claude-plugin/
            │   └── marketplace.json
            └── plugins/
                ├── feature-dev/       # Example: Feature development plugin
                │   ├── .claude-plugin/
                │   │   └── plugin.json
                │   ├── agents/        # Agent definitions (markdown files)
                │   │   ├── code-architect.md
                │   │   ├── code-explorer.md
                │   │   └── code-reviewer.md
                │   ├── commands/      # Slash commands (markdown files)
                │   │   └── feature-dev.md
                │   └── README.md
                └── skill-creator/     # Example: Skill creator plugin
                    ├── .claude-plugin/
                    │   └── plugin.json
                    └── skills/
                        └── skill-creator/
                            ├── SKILL.md           # Main skill definition
                            ├── agents/            # Sub-agent definitions
                            │   ├── grader.md
                            │   ├── analyzer.md
                            │   └── comparator.md
                            ├── scripts/           # Helper scripts
                            ├── references/        # Reference docs
                            ├── assets/            # Templates, HTML
                            └── eval-viewer/       # Evaluation tooling
```

### Project-Level (`.claude/` in repo root)

```
.claude/
├── commands/      # Custom slash commands (markdown files)
├── skills/        # Project-specific skills
└── settings.json  # Project-level settings (not typically committed)
```

---

## 3. The `--agents` JSON Format

### Format

The `--agents` flag accepts a JSON object where keys are agent names and values define the agent:

```json
{
  "agent-name": {
    "description": "Short description of what this agent does",
    "prompt": "You are a specialized agent that..."
  }
}
```

### How It Works

When you pass `--agents`, these agents become available via the **Agent tool** within the session. The main Claude session can spawn them using `Agent(subagent_type="agent-name")`.

### Agent Markdown File Format (Plugin-Based)

Agents defined as `.md` files in a plugin's `agents/` directory use YAML frontmatter:

```markdown
---
name: code-reviewer
description: Reviews code for bugs, logic errors, security vulnerabilities...
tools: Glob, Grep, LS, Read, NotebookRead, WebFetch, TodoWrite, WebSearch, KillShell, BashOutput
model: sonnet
color: red
---

You are an expert code reviewer...

## Core Review Responsibilities
...
```

**Available frontmatter fields:**
- `name` — Agent identifier
- `description` — What the agent does (shown when listing agents)
- `tools` — Comma-separated list of tools the agent has access to
- `model` — Model to use (e.g., `sonnet`, `opus`, `haiku`, or `inherit`)
- `color` — Display color for the agent

---

## 4. Can We Define Sub-Agents via `--agents` When Spawning?

### Yes — With Caveats

**Direct approach with `--agents`:**
```bash
claude --agents '{
  "oracle": {"description": "Research expert", "prompt": "You research codebases..."},
  "test-writer": {"description": "Writes tests", "prompt": "You write comprehensive tests..."},
  "test-critic": {"description": "Reviews tests", "prompt": "You critically review test quality..."},
  "implementer": {"description": "Implements code", "prompt": "You implement features..."},
  "code-reviewer": {"description": "Reviews code", "prompt": "You review code for quality..."},
  "qa-validator": {"description": "Validates quality", "prompt": "You validate software quality..."}
}'
```

This makes these 6 agents available as sub-agent types for the session.

**How Dendrarchy currently spawns agents:**
Dendrarchy uses `claude -p` with system prompts. If we want a spawned engineer agent to have access to sub-agents, we need to:

1. Pass `--agents <json>` when spawning the engineer via `claude -p`
2. The engineer's system prompt should reference the available sub-agents
3. The sub-agents will be available via the Agent tool within that session

**Limitation:** The `--agents` JSON format appears to support `description` and `prompt` fields but may not support `tools` or `model` restrictions. The plugin-based `.md` format (frontmatter) supports the full range of options (`tools`, `model`, `color`).

### Better Approach: Plugin Directory

Using `--plugin-dir` gives more control:

```bash
claude -p --plugin-dir ./tdd-agents/ "Implement feature X using TDD"
```

Where `./tdd-agents/` contains:
```
tdd-agents/
├── .claude-plugin/
│   └── plugin.json
├── agents/
│   ├── oracle.md
│   ├── test-writer.md
│   ├── test-critic.md
│   ├── implementer.md
│   ├── code-reviewer.md
│   └── qa-validator.md
└── commands/
    └── tdd.md          # Optional: /tdd slash command
```

Each agent `.md` file can specify its own model, tools, and detailed prompts.

---

## 5. How Skills Work

### Skill Structure

Skills are markdown files with YAML frontmatter that Claude can dynamically load:

```
skill-name/
├── SKILL.md              # Required: Main skill definition
│   ├── YAML frontmatter  # name, description (required)
│   └── Markdown body     # Instructions
└── Bundled Resources/    # Optional
    ├── scripts/          # Executable code for deterministic tasks
    ├── references/       # Docs loaded as needed
    └── assets/           # Templates, icons, fonts
```

### Progressive Disclosure (Three-Level Loading)

1. **Metadata** (name + description) — Always in context (~100 words). This is how Claude decides whether to invoke a skill.
2. **SKILL.md body** — Loaded when skill triggers (<500 lines ideal).
3. **Bundled resources** — Loaded on demand (scripts can execute without loading into context).

### Triggering Mechanism

Skills appear in Claude's `available_skills` system-reminder with their name + description. Claude decides whether to consult a skill based on:
- The description matching the user's request
- The task being complex enough that Claude can't handle it with basic tools alone
- Skills are invoked via the `Skill` tool: `Skill(skill="skill-name")`

**Key insight:** Skills are "paged in" dynamically. Only the name + description is always in context. The full body loads only when triggered. This is why skill descriptions should be somewhat "pushy" — to avoid under-triggering.

### Skills vs Slash Commands

- **Slash commands** (`/command-name`): Located in `commands/` directory. Explicitly invoked by the user. Markdown files with frontmatter.
- **Skills** (`SKILL.md` in `skills/` directory): Can be auto-triggered based on description matching. More autonomous.

Both can reference sub-agents defined in the same plugin's `agents/` directory.

---

## 6. Design Recommendation: TDD Workflow Implementation

### Options Analysis

| Approach | Pros | Cons |
|----------|------|------|
| **Skill (SKILL.md)** | Auto-triggers on TDD-related requests; progressive loading; can bundle scripts and reference docs | Requires careful description tuning; may over/under-trigger |
| **`--agents` definitions** | Simple; passed at spawn time; works with Dendrarchy's existing `claude -p` pattern | Limited formatting; prompt length in JSON is awkward; no tool/model restrictions |
| **Plugin with agents + command** | Full control over agents (tools, models); slash command for explicit invocation; agents for sub-tasks | Requires maintaining a plugin directory; more setup |
| **Custom command (.md file)** | Explicit invocation (`/tdd`); clear workflow phases; can reference agents | No auto-triggering; user must know to invoke it |

### Recommended Approach: **Plugin with Agents + Command**

For Dendrarchy's TDD workflow, the best approach is a **plugin directory** because:

1. **Agent definitions need tool restrictions.** A test-writer should have different tools than a code-reviewer. Only the `.md` frontmatter format supports `tools:` restrictions.

2. **Model selection per agent.** The oracle might use `opus` for deep analysis while the test-writer uses `sonnet` for speed. The `--agents` JSON doesn't support model selection.

3. **Dendrarchy spawns via `claude -p`.** We can add `--plugin-dir ./plugins/tdd-workflow/` to the spawn command.

4. **The command provides structure.** A `/tdd` command (or the initial prompt) orchestrates the phases (red-green-refactor) while sub-agents handle specific tasks.

### Proposed Plugin Structure

```
plugins/tdd-workflow/
├── .claude-plugin/
│   └── plugin.json
├── agents/
│   ├── oracle.md           # Research & codebase analysis (model: sonnet, tools: read-only)
│   ├── test-writer.md      # Write failing tests (model: sonnet, tools: Edit, Write, Bash)
│   ├── test-critic.md      # Review test quality (model: opus, tools: Read, Grep)
│   ├── implementer.md      # Write minimal implementation (model: sonnet, tools: Edit, Write, Bash)
│   ├── code-reviewer.md    # Review implementation quality (model: sonnet, tools: Read, Grep)
│   └── qa-validator.md     # Run tests & validate (model: sonnet, tools: Bash, Read)
├── commands/
│   └── tdd.md              # /tdd slash command with the TDD workflow phases
└── references/
    └── tdd-principles.md   # Reference doc on TDD best practices
```

### How to Wire into Dendrarchy

When spawning an engineer agent:

```go
// In LaunchOpts or equivalent:
cmd := exec.Command("claude", "-p",
    "--plugin-dir", "./plugins/tdd-workflow/",
    "--append-system-prompt", engineerSystemPrompt,
    "--model", "sonnet",
)
```

The engineer agent would then have access to:
- Sub-agents: oracle, test-writer, test-critic, implementer, code-reviewer, qa-validator
- Command: `/tdd` to invoke the structured workflow
- Reference docs loaded on demand

### Alternative: Hybrid `--agents` + `--append-system-prompt`

For simpler cases where tool/model restrictions aren't needed:

```go
agentsJSON := `{
  "oracle": {"description": "Researches codebase", "prompt": "..."},
  "test-writer": {"description": "Writes failing tests", "prompt": "..."}
}`

cmd := exec.Command("claude", "-p",
    "--agents", agentsJSON,
    "--append-system-prompt", "Use TDD: write failing test → implement → refactor...",
)
```

This is simpler but less powerful. Good for prototyping; graduate to plugin for production.

---

## 7. Key Takeaways for Dendrarchy

1. **`--agents` JSON is the quick path** — pass agent definitions inline when spawning. Good for simple sub-agents where tool/model restrictions aren't needed.

2. **`--plugin-dir` is the robust path** — a plugin directory with `.md` agent files gives full control over tools, models, colors, and can include commands + skills + scripts.

3. **Skills auto-trigger; commands are explicit.** For a structured workflow like TDD, a command (`/tdd`) is more appropriate than a skill, because you want deterministic invocation, not opportunistic triggering.

4. **Progressive disclosure matters.** Keep agent prompts focused. Use `references/` for detailed docs that agents can read on demand rather than putting everything in the prompt.

5. **The feature-dev plugin is the gold standard example.** It shows exactly how to structure a multi-phase workflow with specialized sub-agents (code-explorer, code-architect, code-reviewer) orchestrated by a command (`/feature-dev`).

6. **Dendrarchy's `--append-system-prompt` is key.** When spawning agents with `claude -p`, use `--append-system-prompt` to add Dendrarchy-specific instructions (reporting, scope rules) without replacing Claude Code's default system prompt.

7. **`--bare` mode is useful for controlled spawning.** It skips auto-discovery and gives us full control over what context the agent sees, but skills still work via `/skill-name`.
