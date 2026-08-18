.PHONY: lint-cache-dir test-lint-pin validate build hooks-armed proto-check proto-gen proto-gen-web hub-web fmt-check lint test clean install fmt hooks leak-scan test-notify-tui-e2e test-handoff-e2e test-bridge-lifecycle-e2e test-exit-code-preservation test-parallel-agent-viewport-e2e test-tui-e2e test-leak-resistance-e2e test-merge-reuse-e2e test-ask-user-question-e2e test-drain-row-inject-e2e test-wake-live-e2e test-paste-coalesce-e2e test-e2e-matrix test-e2e-matrix-unit test-hooks-e2e test-hub-bootstrap test-hub-e2e test-wirelog-helpers-unit test-e2e-lockwait-unit test-gitignore-classes test-race test-race-gate always-loaded-budget test-always-loaded-budget-unit

# Default target — full quality gauntlet
validate: build hooks-armed proto-check fmt-check lint test-lint-pin test-race-gate test-race test-wirelog-helpers-unit test-e2e-lockwait-unit test-e2e-matrix-unit test-always-loaded-budget-unit always-loaded-budget test-gitignore-classes leak-scan

BUF ?= buf

# proto-check gates the wire contract: lint, format (check-only, no writes), and
# breaking-change detection against the main HEAD baseline (QUM-875; see
# proto/README.md for the baseline rationale). Additive-only field policy.
proto-check:
	@# Whole recipe runs in ONE shell so the buf-absent guard can early-exit. buf
	@# gates the wire contract but is NOT installed by worktree.setup, and the
	@# pre-commit hook runs `make validate` on every worktree — so if buf is
	@# absent, skip with a loud notice rather than hard-breaking every commit
	@# (install buf to re-enable: https://buf.build/docs/installation).
	@# buf breaking additionally needs a baseline carrying the root buf.yaml so
	@# module scoping matches (else buf's config-less default scan of the baseline
	@# reaches into deploy/hub/spike). Until this slice lands on main there is no
	@# product-proto baseline, so that step self-skips; it self-heals on merge.
	@set -e; \
	if ! command -v $(BUF) >/dev/null 2>&1; then \
		echo "proto-check: SKIPPED — '$(BUF)' not on PATH. Install buf to lint/format/breaking-check the hub proto contract."; \
		exit 0; \
	fi; \
	$(BUF) lint; \
	$(BUF) format --diff --exit-code; \
	if git cat-file -e main:buf.yaml 2>/dev/null; then \
		echo "buf breaking: against .git#branch=main"; \
		$(BUF) breaking --against '.git#branch=main'; \
	else \
		echo "buf breaking: SKIPPED — no proto baseline on main yet (first landing; see proto/README.md). Self-heals once merged to main."; \
	fi

# proto-gen regenerates the committed Go (connect-go) bindings.
proto-gen:
	$(BUF) generate

# proto-gen-web regenerates the TypeScript (connect-es) SPA bindings. Opt-in:
# requires the npm protoc-gen-es / protoc-gen-connect-es tools on PATH. NOT part
# of validate so the build never depends on a node toolchain.
proto-gen-web:
	$(BUF) generate --template buf.gen.web.yaml

# hub-web rebuilds the browser SPA end to end: regenerate the connect-es TS
# bindings into web/gen (via the local protoc-gen-es / protoc-gen-connect-es
# from web/node_modules), then vite-build into the go:embed target
# cmd/hubd/web/dist. Requires a node toolchain (node + npm) and buf. It is
# DELIBERATELY NOT part of `make validate`/`make build` so the default flow
# stays node-free — the built web/dist is committed to the tree. See
# web/README.md for the full pipeline.
hub-web:
	cd web && npm ci
	PATH="$(CURDIR)/web/node_modules/.bin:$$PATH" $(BUF) generate --template buf.gen.web.yaml
	cd web && npm run build

VERSION ?= $(shell git describe --tags --always 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse HEAD 2>/dev/null || echo none)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w \
	-X main.version=$(VERSION) \
	-X main.commit=$(COMMIT) \
	-X main.date=$(DATE)

build:
	go build -ldflags "$(LDFLAGS)" -o sprawl .
	go build ./cmd/hubd

# QUM-1223: golangci-lint is PINNED. A bare `golangci-lint` resolves via PATH,
# which made `make validate` — the repo's gate, and what the pre-commit hook
# runs — a function of whichever version the host happened to have installed.
# That drift is only loud in one direction: the 2026-08-13 host migration made
# the gate STRICTER and failed visibly, but the same drift the other way makes
# it silently weaker and nobody notices. Measured, it is worse than version
# drift: any executable named `golangci-lint` earlier on PATH that exits 0
# takes over the gate entirely (a decoy printing nothing made `make lint` exit 0
# over 6 real findings).
#
# `go run <pkg>@<version>` resolves in module-agnostic mode, so this pins the
# tool WITHOUT adding it to the main module's graph. That matters: a `tool`
# directive in go.mod makes MVS bump shipped product dependencies — measured,
# it raised charm.land/lipgloss/v2 v2.0.2->v2.0.3 and
# github.com/charmbracelet/x/ansi v0.11.6->v0.11.7, both TUI renderers. Do NOT
# "simplify" this back to `go get -tool`; pinning a linter must not upgrade the
# renderer inside the binary we ship.
#
# `golangci-lint fmt` reaches gofumpt and goimports as LIBRARIES compiled into
# the binary, so this pins formatting behaviour too — confirmed by running
# `fmt --diff` with gofumpt, goimports and golangci-lint all absent from PATH.
# Guarded by scripts/test-lint-pin.sh, which runs inside `validate`.
GOLANGCI_LINT_VERSION ?= v2.12.2
GOLANGCI_LINT ?= go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

# QUM-1232: ONE CACHE PER WORKTREE. The linter's default cache is
# os.UserCacheDir()/golangci-lint — a single namespace shared by every worktree
# and every agent on this host. Two worktrees of this repo with identical content
# hash to IDENTICAL cache keys (paths in the key are relativized to the module
# path) while the cached issue keeps the ABSOLUTE filename of whichever worktree
# produced it. Measured consequence: your own real finding comes back as
# `../<other-agent>/main.go:3:6` and appears ZERO times at any path in your tree.
# You cannot fix it, and nobody can see that it happened — that is a false green
# wearing a false red's clothes. Deleting the sibling worktree makes it worse:
# the entries outlive the files, and post-cache exclusion processing then
# degrades silently.
#
# Derived from $(MAKEFILE_LIST), NOT from $(CURDIR): the value must key on the
# tree being linted, not on the directory make was invoked from. $(abspath) is
# required — a relative value is fatal ("build cache is required, but could not
# be located: GOLANGCI_LINT_CACHE is not an absolute path"), not a fallback to
# the default.
#
# `lastword` of the `%Makefile` entries, not `firstword` of everything. Two
# measured reasons, both of which move the cache OUT of the worktree and silently
# re-share it: GNU make PREPENDS every file named in the `MAKEFILES` environment
# variable to MAKEFILE_LIST (measured: MAKEFILES=/tmp/x/extra.mk made a
# firstword form resolve to /tmp/x/.golangci-cache), and an `include` added above
# this line would make a plain `lastword` resolve to the included file's
# directory. The filter survives both, including a MAKEFILES entry itself named
# `Makefile` — verified. A8/A8c pin it.
#
# `:=` and not `?=`: isolation is a safety property, not an operator preference.
# A stale GOLANGCI_LINT_CACHE inherited from a parent process or an old
# experiment must not be able to silently un-isolate every target here, and a
# make assignment beats the environment. `export` is what carries it to the
# linter child at all — without it this variable is decoration.
#
# Reaped for free: the dir lives inside the worktree and is gitignored, and
# `git worktree remove` deletes a worktree containing a populated ignored
# directory (measured, with and without --force). So every teardown path already
# reaps it and `worktree.teardown` stays empty.
#
# Cost, MEASURED on this host rather than estimated: each active worktree now
# warms its own cache instead of sharing one. First `make lint` in a fresh
# worktree is cold — 18.6s wall, against 1.6s warm — and the cache settles at
# 33M (the old shared one measured 34M/8142 files). The linter self-trims entries
# unused for >5 days on close, so this is self-bounding, and a removed worktree
# takes its cache with it. Guarded by A8/A8b/A9/A10/A11 in
# scripts/test-lint-pin.sh.
GOLANGCI_LINT_CACHE := $(abspath $(dir $(lastword $(filter %Makefile,$(MAKEFILE_LIST)))))/.golangci-cache
export GOLANGCI_LINT_CACHE

fmt:
	$(GOLANGCI_LINT) fmt ./...

fmt-check:
	@echo "Checking formatting..."
	@test -z "$$($(GOLANGCI_LINT) fmt --diff ./...)" || (echo "Files need formatting. Run 'make fmt' to fix." && exit 1)

# LINT_SCOPE exists so scripts/test-lint-pin.sh can exercise WHICH BINARY runs
# without linting ./... three extra times per validate — golangci-lint's lock is
# machine-wide across all worktrees on this host, so widening that window
# manufactures false-reds for other agents. Default is the full tree.
LINT_SCOPE ?= ./...

lint:
	$(GOLANGCI_LINT) run $(LINT_SCOPE)

# Introspection seam for scripts/test-lint-pin.sh A8, in the same spirit as
# LINT_SCOPE above. Prints the SHELL value ($$GOLANGCI_LINT_CACHE), not the make
# value, so the assertion sees what a recipe's child process actually gets —
# printing $(GOLANGCI_LINT_CACHE) would look identical with the `export` above
# deleted, which is exactly the regression A8 exists to catch.
lint-cache-dir:
	@printf '%s\n' "$$GOLANGCI_LINT_CACHE"

# QUM-1223: proves the pin above actually BINDS rather than merely being
# written down. Pure bash + go, no claude/tmux. Its A2 leg puts a decoy
# `golangci-lint` earlier on PATH and asserts the pinned binary still runs —
# a green `make lint` alone cannot distinguish a working pin from no pin.
test-lint-pin:
	bash scripts/test-lint-pin.sh

# The non-race convenience run. NOT what `validate` uses — see test-race.
test:
	go test ./...

# QUM-972: THE enforced race gate. `validate` depends on this INSTEAD of `test`;
# running both would double the suite for no extra coverage, since the race build
# runs every assertion the plain build does.
#
# Measured on this host (4 cores, warm build caches, -count=1):
#   go test ./...          99.0s
#   go test -race ./...   122.2s   (+23%)
# Cheap because the suite is sleep/timeout-bound, not CPU-bound —
# internal/supervisor alone is 75s of the 122s and barely moves under
# instrumentation. A targeted "concurrency-heavy packages" subset was measured
# at 76.0s: it saves 46s while covering 4 of ~40 packages, and needs a
# hand-maintained list that silently stops covering any newly-concurrent
# package. Not worth it.
#
# -race requires cgo and a C toolchain. That fails LOUDLY (the build is refused)
# rather than silently skipping, so it cannot become a false green — and
# test-race-gate re-proves detection actually works on every run anyway.
test-race:
	go test -race ./...

# Guards the gate above. Dropping -race from `validate` is a SILENT regression:
# nothing fails, races just stop being detected. So is landing in an environment
# where -race is inert (CGO_ENABLED=0, no gcc, a hostile GOFLAGS). This asserts
# the wiring from `make -n validate` and re-runs validate's own flags against a
# planted race plus a clean control. Pure-local: bash + go.
test-race-gate:
	bash scripts/test-race-gate.sh

# Unit tests for the hand-rolled wire-log counter/ordering helpers inside the
# e2e row scripts (scripts/e2e-tests/*.sh). Those helpers gate the rows'
# non-vacuity aborts, so a helper returning a non-integer makes a row pass
# while measuring nothing. Pure-local: bash + jq, no claude/tmux/sandbox.
test-wirelog-helpers-unit:
	bash scripts/test-wirelog-helpers-unit.sh

# The LIVE always-loaded instruction-budget gate: resolves what every agent
# unavoidably loads and fails over the ceiling in
# scripts/always-loaded-budget.conf. See
# docs/archive/budget-resolver.md.
#
# IN `validate` since QUM-1155. It was held out for two reasons and both are now
# discharged: (1) it FAILED on the tree — 938 in-tree lines against a 250 ceiling
# plus the CLAUDE.md:3 mandated read of DESCRIPTION.md — and wiring a
# known-failing gate into validate just teaches people to bypass validate;
# (2) CLAUDE.md was contended by another writer. The cut landed, DESCRIPTION.md
# is an allowlisted on-demand pointer, and the tree now measures 74 against 250.
#
# TRACKED-ONLY, AND THAT IS THE POLICY — NOT A LIMITATION TO LIFT. The enforced
# set is the injected files that are git-tracked in the checkout they were
# injected from (a worktree's own branch, or the sprawl root — asking only the
# root index would leave a file an agent added on its branch unenforced in the
# worktree that added it).
# CLAUDE.local.md is gitignored and per-user: it loads on the machine that has it
# and does not exist in a fresh clone. Enforcing it would make `make validate`
# pass or fail as a function of whose checkout ran it, and would make the
# recorded manifest list entries a clean clone cannot derive — measured: rc=1 on
# a --depth 1 clone of a correct tree, landing on the one person who cannot fix
# it by changing a tracked file. Untracked always-loaded files are still REPORTED
# by the script, with their sizes, and counted in the verdict line's `untracked=`
# field; they are excluded from the total, never hidden. Folding them back into
# the enforced total is a REGRESSION, not an improvement.
#
# Its unit suite (below) is in validate too — the same split as `test-race-gate`
# guarding `test-race`: the mechanism's own guard runs even when the live gate is
# trivially green, and a broken MECHANISM is diagnosed before a failing
# MEASUREMENT.
always-loaded-budget:
	bash scripts/always-loaded-budget.sh --check-manifest scripts/testdata/always-loaded-manifest.observed

# Fixture-only unit suite for the resolver: pure bash + git, ~6s, reads NOTHING
# from the real tree, so an unrelated CLAUDE.md edit can never fail it.
test-always-loaded-budget-unit:
	bash scripts/test-always-loaded-budget.sh

# Unit tests for the e2e harness' weave.lock release wait (QUM-948). Guards a
# lock-release race: `tmux kill-session` does not close the dying weave's flock
# fd, so a kill-then-relaunch path that slept a fixed 2s failed under load with
# "another weave session is already running". Pins that the replacement retry
# waits past the old sleep, backs off, and still FAILS on a genuinely leaked
# lock rather than hanging or passing. Pure-local: bash + coreutils + flock(1),
# no claude/tmux/sandbox.
test-e2e-lockwait-unit:
	bash scripts/test-e2e-lockwait-unit.sh

GOBIN ?= $(HOME)/.local/bin

install:
	GOBIN=$(GOBIN) go install -ldflags "$(LDFLAGS)" .

clean:
	rm -f sprawl

# QUM-872: whole-tree employer/cloud-leak scan. Enabled by default now that the
# tree has been scrubbed (QUM-873). The scan is a no-op when the gitignored
# forbidden-terms list is absent, so it is safe in any checkout. The per-commit
# staged scan runs independently via scripts/pre-commit.
leak-scan:
	@echo "leak-scan (whole-tree): scanning tracked tree..."
	@scripts/guard-employer-leak --all

# QUM-989: empirically stage fixtures against the real .gitignore to prove the
# terraform-plan and *.log ignore classes still match. Pure shell + git, ~0.3s.
# The regression it guards is silent: a tidy-up stops a pattern matching and
# nothing fails until an infra artifact is staged into this PUBLIC repo.
test-gitignore-classes:
	bash scripts/test-gitignore-classes.sh

# QUM-951: assert the guard stack is actually ARMED for this working tree before
# anything else in validate has a chance to look green. `git -c
# core.hooksPath=<nonexistent> commit` runs NO hooks and exits 0 — silently
# voiding the QUM-808 pre-commit guard, the QUM-837 reference-transaction
# backstop, and this very validate gate at once — and a dangling symlink, a lost
# executable bit or a deleted guard helper disarm the same way, with no warning
# from git in any case.
#
# This CANNOT live in a hook: hooks are exactly what is being bypassed. It fails
# hard, deliberately. Cf. the always-loaded-budget gate below, which is
# tracked-only precisely so it cannot fail as a function of whose checkout ran
# it — the distinction is that a clone with no hooks is not a valid state to be
# committing from, and the remedy is one tracked, documented command that the
# failure message names.
#
# Declared `: build` explicitly rather than relying on validate's prerequisite
# order, so `make -j` cannot race it ahead of the binary it runs.
hooks-armed: build
	@./sprawl hooks verify

hooks:
	ln -sf ../../scripts/pre-commit .git/hooks/pre-commit
	chmod +x .git/hooks/pre-commit
	ln -sf ../../scripts/guard-main-ref .git/hooks/reference-transaction
	chmod +x .git/hooks/reference-transaction
	@echo "Pre-commit and main-ref guard hooks installed."

# SUPERSEDED — this target no longer tests anything. QUM-1186 deleted the
# `report_status` tool and the state fields the script probed with, so
# scripts/test-notify-tui-e2e.sh now exits 77 (skip) at the top rather than
# false-greening on assertions whose subject is gone. The TUI-mode
# parent-notification coverage (QUM-312) lives in matrix row `notify-tui`:
#
#     make test-e2e-matrix-notify-tui
#
# Deleting this target and its script is tracked separately (QUM-1186 lane 5
# hand-off). Until then it is kept so an existing invocation gets the rc-77
# skip and the pointer above rather than a "no rule to make target" error.
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

# QUM-511 / QUM-489: end-to-end regression guard. When an agent's worktree
# HEAD moves to a new branch but state.json still records the spawn-time
# branch, `sprawl merge` must follow the worktree's actual current branch.
# QUM-1186: this comment previously cited a "delegate-style branch swap".
# That mechanism is deleted; the field still goes stale for the more general
# reason above (any agent may check out or create a branch in its own
# worktree), so the guard stays. This mirrors the note on the
# `deps.CurrentBranch(agentState.Worktree)` resolution in
# internal/agentops/merge.go. Pre-fix it silently no-ops because it
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
# (QUM-569). Drives a real claude child to call `mcp__sprawl__send_message`
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

# QUM-606/QUM-724: end-to-end gate for `mcp__sprawl__wake` subprocess
# survival. Builds the sprawl binary with `-tags sprawl_test` so the
# `_test_induce_wedge` MCP tool is present, then drives a real claude
# child through fault → wake → post-wake turn in an isolated /tmp
# sandbox. Asserts that a new `claude … --resume` subprocess survives
# the wake return AND that a sentinel sent via send_message lands in
# the child's activity.ndjson — pre-QUM-606-fix the subprocess died
# immediately and no frames arrived. See scripts/test-wake-live-e2e.sh.
# Mandatory before merging any change to the wake path:
# internal/supervisor/runtime.go, internal/supervisor/real.go,
# internal/sprawlmcp/server.go (toolWake),
# internal/backend/claude/adapter.go, internal/runtime/unified.go, or
# internal/runtime/turnloop.go.
test-wake-live-e2e:
	cd $(CURDIR) && go build -tags sprawl_test -o sprawl-wake-e2e ./
	SPRAWL_BIN=$(CURDIR)/sprawl-wake-e2e bash scripts/test-wake-live-e2e.sh; rc=$$?; \
	    [ -x $(CURDIR)/sprawl ] && $(CURDIR)/sprawl sandbox-gc --max-age=10m || true; \
	    rm -f $(CURDIR)/sprawl-wake-e2e; \
	    exit $$rc

# QUM-608 paste-coalescer e2e gate. Launches sprawl enter in an
# isolated /tmp sandbox under tmux, injects a 200-char paste burst via
# `tmux send-keys -l`, asserts the full payload appears in the input
# panel within 5s (well below the typewriter-animation budget the bug
# produces), then SIGINTs and asserts clean shutdown. See
# scripts/test-paste-coalesce-e2e.sh. Mandatory before merging any
# change to internal/inputcoalesce/ or the tea.NewProgram call site in
# cmd/enter.go.
test-paste-coalesce-e2e: build
	bash scripts/test-paste-coalesce-e2e.sh; rc=$$?; ./sprawl sandbox-gc --max-age=10m || true; exit $$rc

# QUM-842: CLI-level round-trip for `sprawl hooks install`/`uninstall`. Needs
# only git + the built binary (no claude, no sandbox). Verifies install,
# non-root --no-verify abort, root/human pass, and surgical uninstall.
test-hooks-e2e: build
	SPRAWL_BIN=$$PWD/sprawl bash scripts/test-hooks-e2e.sh

# QUM-870: exercises deploy/hub/bootstrap/bootstrap.sh against a fake `az` shim
# (no cloud, no binary). Verifies config refusal, hardening flags, mandatory
# tags, create/converge idempotency, no-leak grep, and gitignore coverage.
test-hub-bootstrap:
	bash scripts/test-hub-bootstrap.sh

# QUM-911: Hub Phase 1 capstone e2e — a local hubd process, the real host
# tailer, and a Connect subscriber (browser stand-in) proving live-tail plus
# zero-gap/zero-dupe reconnect across a subscriber blip and a hubd restart.
# Needs only the Go toolchain (behind the hub_e2e build tag; no claude/tmux).
test-hub-e2e:
	go test -tags hub_e2e -count=1 -v ./internal/hub/e2e/

# QUM-616 matrix-driven e2e harness foundation. Wave 1 — runs alongside
# the per-test test-*-e2e targets. See scripts/e2e-matrix.sh.
test-e2e-matrix: build
	bash scripts/e2e-matrix.sh all; rc=$$?; ./sprawl sandbox-gc --max-age=10m || true; exit $$rc

# QUM-947: unit tests for the driver itself — arg parsing, fail-fast row-name
# validation, and the summary's passed/requested arithmetic. Pure shell, ~0.4s,
# no claude and no tmux, so it runs inside `make validate`: a regression test
# guarding a false-green is worthless if it only runs when someone remembers.
# This explicit rule takes precedence over the test-e2e-matrix-% pattern rule
# below, so `unit` is never mistaken for a row name.
test-e2e-matrix-unit:
	bash scripts/test-e2e-matrix-unit.sh

# Pattern target: `make test-e2e-matrix-merge-reuse` runs only that row.
# For several rows in one driver invocation (with an honest denominator), call
# the driver directly: `bash scripts/e2e-matrix.sh row-a row-b row-c`.
test-e2e-matrix-%: build
	bash scripts/e2e-matrix.sh $*; rc=$$?; ./sprawl sandbox-gc --max-age=10m || true; exit $$rc
