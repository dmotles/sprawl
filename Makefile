.PHONY: validate build fmt-check lint test clean install fmt hooks test-init-e2e test-notify-e2e

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

# Opt-in end-to-end smoke test for `sprawl init --detached` (tmux mode).
# Requires a real `claude` binary on PATH. Not part of `make validate` —
# runs a real subprocess and interacts with tmux. See scripts/test-init-e2e.sh.
# Mandatory before merging any change to cmd/rootloop.go or internal/claude/.
test-init-e2e:
	bash scripts/test-init-e2e.sh

# Opt-in end-to-end smoke test for the parent-notification contract (QUM-310).
# Asserts that a child agent running `sprawl report done` causes an
# `[inbox] New message from <child>` line to appear in the root weave tmux
# pane when SPRAWL_MESSAGING=legacy. Not part of `make validate` — runs real
# subprocesses and interacts with tmux. See scripts/test-notify-e2e.sh.
# Mandatory before merging any change to cmd/messages.go, cmd/report.go,
# internal/messages/, internal/agentops/report.go, or internal/supervisor/real.go.
test-notify-e2e:
	bash scripts/test-notify-e2e.sh
