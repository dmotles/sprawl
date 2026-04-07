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

## Quick Start

```bash
cd your-repo
sprawl init
```

This launches **neo** (the root agent) in a tmux session. Give it a goal and it self-organizes from there — decomposing work, spawning agents, and managing everything autonomously.

## Usage

- **Navigate agents**: Use tmux to watch agents work. `ctrl+b s` to switch sessions, `ctrl+b w` to switch windows.
- **Context handoff**: When neo's context window fills up, ask neo to run `sprawl handoff`, then `/exit`. The next session picks up with memories of what happened. If you forget, the root loop will attempt to auto-summarize for you.
- **Shut down**: Ask neo to make sure no agents are running, then `ctrl+b x` to kill neo's session.

## State

All of sprawl's state lives in `.sprawl/` (gitignored). Back up this directory to migrate between hosts.

## Prerequisites

- [Claude Code](https://docs.anthropic.com/en/docs/claude-code)
- [tmux](https://github.com/tmux/tmux)
- [Go](https://go.dev/) 1.25+
- Git

## More

- [DESCRIPTION.md](DESCRIPTION.md) — architecture and design
- [CONTRIBUTING.md](CONTRIBUTING.md) — how to contribute
- [LICENSE](LICENSE)
