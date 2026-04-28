<p align="center">
  <img src="assets/banner.jpg" alt="Sprawl" width="100%">
</p>

# Sprawl

Self-organizing AI agent orchestration on [Claude Code](https://docs.anthropic.com/en/docs/claude-code). Designed to force-multiply solo agentic engineers.

Give it a goal. It figures out how to organize agents to achieve it.

## Install

```bash
curl -fsSL https://raw.githubusercontent.com/dmotles/sprawl/main/install.sh | sh
```

Or with Go:

```bash
go install github.com/dmotles/sprawl@latest
```

Build from source:

```bash
git clone https://github.com/dmotles/sprawl.git
cd sprawl
make build
```

## Quick Start

```bash
cd your-repo
sprawl enter
sprawl config set validate "make test"
sprawl config set worktree.setup 'npm install && cp $SPRAWL_ROOT/.env .env'
```

Use `sprawl config show` to view current settings, or `sprawl config --help` for all options.

This launches **weave** (the root agent) inside the sprawl TUI. Give it a goal and it self-organizes from there — decomposing work, spawning agents, and managing everything autonomously.

## Usage

- **Navigate agents**: Use the TUI tree panel to watch agents work; child agents still run in tmux windows you can attach to.
- **Context handoff**: When weave's context window fills up, ask weave to call the `sprawl_handoff` MCP tool. The TUI auto-restarts the session with memories of what happened.
- **Shut down**: Ask weave to make sure no agents are running, then exit the TUI.

## State

All of sprawl's state lives in `.sprawl/` (gitignored), except for `.sprawl/config.yaml` which is tracked — it holds project-level settings like the validation command. Back up this directory to migrate between hosts.

## Prerequisites

- [Claude Code](https://docs.anthropic.com/en/docs/claude-code)
- [tmux](https://github.com/tmux/tmux)
- [Go](https://go.dev/) 1.25+
- Git

## More

- [DESCRIPTION.md](DESCRIPTION.md) — architecture and design
- [CONTRIBUTING.md](CONTRIBUTING.md) — how to contribute
- [LICENSE](LICENSE)
