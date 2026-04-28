.PHONY: validate build fmt-check lint test clean install fmt hooks test-notify-tui-e2e test-handoff-e2e

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
# (QUM-312). Asserts that a child agent running `sprawl report done` or
# `sprawl messages send weave` causes the `sprawl enter` TUI to surface
# an 'inbox: N new message(s) for weave' viewport banner and a '(N)'
# unread badge on the synthesized weave row. Not part of `make validate`
# — runs real subprocesses, launches a real claude, and interacts with
# tmux. See scripts/test-notify-tui-e2e.sh. Mandatory before merging
# any change to the TUI-notifier path: cmd/enter.go, cmd/enter_notify.go,
# internal/tui/app.go, internal/tui/messages.go, or internal/tui/tree.go.
test-notify-tui-e2e:
	bash scripts/test-notify-tui-e2e.sh

# Opt-in end-to-end regression guard for QUM-329: TUI handoff restart
# must fire when weave calls `sprawl_handoff` via MCP. Spins up an
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
test-handoff-e2e:
	bash scripts/test-handoff-e2e.sh
