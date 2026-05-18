.PHONY: validate build fmt-check lint test clean install fmt hooks test-notify-tui-e2e test-handoff-e2e test-bridge-lifecycle-e2e test-exit-code-preservation test-parallel-agent-viewport-e2e test-tui-e2e test-leak-resistance-e2e test-merge-reuse-e2e test-ask-user-question-e2e test-drain-row-inject-e2e

# Default target — full quality gauntlet
validate: build fmt-check lint test

VERSION ?= $(shell git describe --tags --always 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse HEAD 2>/dev/null || echo none)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w \
	-X main.version=$(VERSION) \
	-X main.commit=$(COMMIT) \
	-X main.date=$(DATE)

build:
	go build -ldflags "$(LDFLAGS)" -o sprawl .

fmt:
	golangci-lint fmt ./...

fmt-check:
	@echo "Checking formatting..."
	@test -z "$$(golangci-lint fmt --diff ./...)" || (echo "Files need formatting. Run 'make fmt' to fix." && exit 1)

lint:
	golangci-lint run ./...

test:
	go test ./...

GOBIN ?= $(HOME)/.local/bin

install:
	GOBIN=$(GOBIN) go install -ldflags "$(LDFLAGS)" .

clean:
	rm -f sprawl

hooks:
	ln -sf ../../scripts/pre-commit .git/hooks/pre-commit
	chmod +x .git/hooks/pre-commit
	@echo "Pre-commit hook installed."

# Opt-in end-to-end smoke test for the TUI-mode parent-notification path
# (QUM-312). Simulates a child agent by writing a state.json (state=complete,
# last_report_message set) and a maildir envelope addressed to weave directly
# into the sandbox state tree, then asserts that the `sprawl enter` TUI
# surfaces an 'inbox: N new message(s) for weave' viewport banner and a '(N)'
# unread badge on the synthesized weave row. Not part of `make validate`
# — runs real subprocesses, launches a real claude, and interacts with
# tmux. See scripts/test-notify-tui-e2e.sh. Mandatory before merging
# any change to the TUI-notifier path: cmd/enter.go, cmd/enter_notify.go,
# internal/tui/app.go, internal/tui/messages.go, or internal/tui/tree.go.
test-notify-tui-e2e: build
	bash scripts/test-notify-tui-e2e.sh; rc=$$?; ./sprawl sandbox-gc --max-age=10m || true; exit $$rc

# Opt-in end-to-end regression guard for QUM-329: TUI handoff restart
# must fire when weave calls `handoff` via MCP. Spins up an
# isolated /tmp sandbox, launches `sprawl enter` in a detached tmux
# pane, attaches a phantom client (QUM-327 workaround), drives weave
# to call the MCP tool, and asserts handoff-signal fires, the old
# claude pid dies, a new claude pid spawns with a different
# --session-id, last-session-id changes, and the TUI shows the
# "Session restarting (handoff)" banner. Not part of `make validate` —
# runs real subprocesses, launches real claude, interacts with tmux.
# See scripts/test-handoff-e2e.sh. Mandatory before merging any change
# to cmd/enter.go, internal/supervisor/*.go, internal/sprawlmcp/*.go,
# internal/rootinit/postrun.go, or internal/tui/app.go's
# HandoffRequestedMsg/SessionRestartingMsg/RestartSessionMsg handlers.
test-handoff-e2e: build
	bash scripts/test-handoff-e2e.sh; rc=$$?; ./sprawl sandbox-gc --max-age=10m || true; exit $$rc

# Opt-in end-to-end regression guard for QUM-467: child agents must NOT
# lose MCP connectivity when weave's claude subprocess is restarted.
# Spins up an isolated /tmp sandbox, launches `sprawl enter`, plants a
# synthetic child, has the child send a message to weave (asserts it
# lands), drives weave to call mcp__sprawl__handoff (restart), then has
# the SAME child send another message and asserts it ALSO lands. Pre-fix
# the post-restart send fails with "stream closed" or the message
# silently doesn't land in weave's maildir. Not part of `make validate`
# — runs real subprocesses, launches real claude, interacts with tmux.
# See scripts/test-bridge-lifecycle-e2e.sh. Mandatory before merging any
# change to cmd/enter.go's bridge wiring or
# internal/supervisor/runtime_launcher*.go's InitSpec capture.
test-bridge-lifecycle-e2e: build
	bash scripts/test-bridge-lifecycle-e2e.sh; rc=$$?; ./sprawl sandbox-gc --max-age=10m || true; exit $$rc

# QUM-386: E2E test for parallel Agent tool call rendering in the TUI
# viewport. Uses a fake claude binary (no real claude needed) to emit
# parallel Agent tool_use blocks and verifies the TUI renders two
# independent Agent containers. Mandatory before merging any change to
# internal/tui/viewport.go's Agent container rendering or bridge.go's
# AssistantContentMsg batching.
test-parallel-agent-viewport-e2e: build
	bash scripts/test-parallel-agent-viewport-e2e.sh; rc=$$?; ./sprawl sandbox-gc --max-age=10m || true; exit $$rc

# QUM-458: end-to-end gate for the broader TUI smoke harness, plus the
# leak-resistance harness that SIGKILLs the e2e drivers and asserts no
# orphan claude/tmux/dir residue.
test-tui-e2e: build
	bash scripts/test-tui-e2e.sh; rc=$$?; ./sprawl sandbox-gc --max-age=10m || true; exit $$rc

test-leak-resistance-e2e: build
	./sprawl sandbox-gc --max-age=10m || true; bash scripts/test-leak-resistance-e2e.sh; rc=$$?; ./sprawl sandbox-gc --max-age=10m || true; exit $$rc

# QUM-328: regression guard — verifies E2E scripts preserve exit codes
# across cleanup traps. Lightweight (no claude/tmux/spawl needed).
test-exit-code-preservation:
	bash scripts/test-exit-code-preservation.sh

# QUM-511 / QUM-489: end-to-end regression guard. After a delegate-style
# branch swap (agent's worktree HEAD moves to a new branch but state.json
# still records the spawn-time branch), `sprawl merge` must follow the
# worktree's actual current branch. Pre-fix it silently no-ops because it
# reads stale agentState.Branch. Pure shell — no claude required. See
# scripts/test-merge-reuse-e2e.sh. Mandatory before merging any change to
# internal/agentops/merge.go, internal/sprawlmcp/server.go (toolMerge),
# cmd/merge.go, internal/supervisor/supervisor.go (Merge), or
# internal/supervisor/real.go (Real.Merge / mergeFn).
test-merge-reuse-e2e: build
	bash scripts/test-merge-reuse-e2e.sh

# QUM-527: end-to-end gate for the mcp__sprawl__ask_user_question
# round-trip. Spins up an isolated /tmp sandbox, launches `sprawl enter`
# in a detached tmux pane, drives root weave to call the MCP tool with
# a single-select payload, asserts the modal indicator appears in the
# status bar, sends Down+Enter to select option 2, and asserts the
# viewport surfaces AUQ-ANSWER=<beta-sentinel> (proving the
# QuestionResponse reached claude). Not part of `make validate` — runs
# a real claude subprocess. See scripts/test-ask-user-question-e2e.sh.
# Mandatory before merging any change to the ask-user-question path:
# internal/supervisor/question.go, internal/supervisor/question_real.go,
# internal/sprawlmcp/server.go (toolAskUserQuestion + eligibility gate),
# internal/sprawlmcp/tools.go (ask_user_question schema),
# internal/tui/question.go, internal/tui/app.go (modal+keys+View),
# internal/tui/statusbar.go (SetPendingQuestions), or cmd/enter.go
# (consumer registration + forwarder).
test-ask-user-question-e2e: build
	bash scripts/test-ask-user-question-e2e.sh; rc=$$?; ./sprawl sandbox-gc --max-age=10m || true; exit $$rc

# Opt-in end-to-end smoke test for the drain-row prompt-inject path
# (QUM-569). Drives a real claude child to call `mcp__sprawl__messages_send`
# to weave, then asserts that weave's TUI pane renders the drain-row
# citation `From <child> — mcp__sprawl__messages_read(id=...)` within a
# bounded timeout. Restores the e2e regression guard for the
# Send → defaultNotifier → supervisor.WakeForDelivery → claude
# prompt-inject pipeline that QUM-565 stripped from test-notify-tui-e2e
# when it migrated off the deprecated CLI surface. Mandatory before
# merging any change to the drain pipeline: internal/messages/messages.go,
# internal/runtime/unified.go, internal/runtime/queue.go,
# internal/supervisor/weave_handle.go, internal/supervisor/runtime.go,
# internal/supervisor/runtime_launcher.go, internal/supervisor/real.go,
# internal/inboxprompt/inboxprompt.go, internal/tui/messages.go,
# internal/tui/viewport.go, or cmd/enter.go.
test-drain-row-inject-e2e: build
	bash scripts/test-drain-row-inject-e2e.sh; rc=$$?; ./sprawl sandbox-gc --max-age=10m || true; exit $$rc
