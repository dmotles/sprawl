.PHONY: lint-cache-dir test-lint-pin validate build hooks-armed proto-check proto-gen proto-gen-web hub-web fmt-check lint test clean install fmt hooks leak-scan test-handoff-e2e test-exit-code-preservation test-parallel-agent-viewport-e2e test-tui-e2e test-leak-resistance-e2e test-e2e-matrix test-e2e-matrix-unit test-hooks-e2e test-hub-bootstrap test-hub-e2e test-store-pg test-wirelog-helpers-unit test-e2e-lockwait-unit test-gitignore-classes test-race test-race-gate always-loaded-budget test-always-loaded-budget-unit print-validate-steps test-validate-timing-unit check-validate-baseline validate-baseline print-ldflags test-build-stamp test-doclint test-doclint-split-unit test-check-scope-unit check print-check-steps check-budget-structural check-fmt check-lint check-test-race test-check-budget-unit test-precommit-gate-unit

# THIS_MAKEFILE must be resolved HERE, above any include, where MAKEFILE_LIST's
# last entry is still this file. Files named in the MAKEFILES environment
# variable are PREPENDED (read earlier), so a plain `lastword` is safe at this
# position — unlike GOLANGCI_LINT_CACHE's mid-file assignment below, which needs
# the $(filter %Makefile,...) form. And the filter form must NOT be used here:
# `make -f Makefile-norace` (scripts/test-race-gate.sh's RACE_GATE_MAKEFILE seam)
# names a file that does not match %Makefile, which would resolve to `-f ''`.
THIS_MAKEFILE := $(abspath $(lastword $(MAKEFILE_LIST)))

# QUM-1286: validate's step list, and the ONLY copy of it. The timed driver
# receives it as argv, `print-validate-steps` exposes it for introspection, and
# scripts/testdata/validate-baseline.observed is checked against it — so a step
# added or removed here propagates everywhere instead of leaving a second,
# hand-maintained list to rot.
VALIDATE_STEPS := build test-build-stamp hooks-armed proto-check fmt-check \
	lint test-lint-pin \
	test-race-gate test-race test-doclint test-doclint-split-unit \
	test-check-scope-unit test-check-budget-unit test-precommit-gate-unit \
	test-wirelog-helpers-unit test-e2e-lockwait-unit \
	test-e2e-matrix-unit test-always-loaded-budget-unit always-loaded-budget \
	test-gitignore-classes test-validate-timing-unit check-validate-baseline \
	leak-scan

# ============================================================================
# THE COMMIT GATE (QUM-1289)
#
# WHICH GATE DOES A NEW CHECK BELONG IN? This rule is the deliverable, not the
# list below — a list rots, a rule is decidable by whoever reads it next.
#
# A check goes in `check` (the COMMIT gate) only if ALL FIVE hold:
#
#   1. LOCAL AND HERMETIC. No network, no claude, no tmux, no Docker, no
#      sandbox, no toolchain a fresh agent worktree is not guaranteed to have.
#   2. ITS VERDICT IS A FUNCTION OF THE STAGED DIFF. If the same tree can flip
#      the verdict because of the calendar, the host, another agent's worktree,
#      or an untracked file, it is not a commit gate.
#   3. IT DECLARES A BUDGET in scripts/testdata/check-budget.conf, and the sum
#      of all declared budgets stays within the ceiling. A step with no
#      declaration FAILS `check` — that is a mechanism (check-budget.sh), not a
#      convention, and it is what stops this gate growing back into the merge
#      gate one addition at a time. That is exactly how the current state arose.
#   4. ITS FAILURE NAMES A FILE THE AUTHOR CAN FIX NOW, in this commit.
#   5. IT IS NOT A MECHANISM GUARD. A suite whose subject is another gate's
#      plumbing (test-lint-pin, test-e2e-matrix-unit, test-doclint-split-unit,
#      test-check-scope-unit, test-build-stamp, ...) can only regress when
#      someone edits that plumbing, and editing it is itself a merge-gated
#      event. Merge-only.
#
# Otherwise it stays in `validate` (the MERGE gate). `validate` has no budget and
# is NEVER weakened to make room in `check`.
#
# TWO NAMED EXCEPTIONS to rules 2 and 5, and they are exhaustive. A gate must not
# be able to silently disarm ITSELF, so the guards of the commit path run in the
# commit path:
#   * hooks-armed    — the guard chain must be armed at the moment of committing;
#                      this cannot live inside a hook.
#   * test-race-gate — proves check's own go-test invocation still carries -race.
# Adding a third exception requires the same written argument these two carry.
#
# WHEN IN DOUBT, `validate`. A check in the wrong direction costs a merge slot;
# in the other direction it ships the defect and comes back green.
#
# NOTE ON SCOPE: check-test-race runs -race over the DEPENDENCY CLOSURE of the
# change (scripts/check-scope.sh), not over ./... . -race is included on
# dmotles's decision — races are the defect class this codebase actually
# produces, and deferring all race detection to merge means finding them after
# other work is layered on top.
# ============================================================================

CHECK_STEPS := check-budget-structural build hooks-armed check-fmt check-lint \
	always-loaded-budget test-race-gate check-test-race

CHECK_BUDGET_S ?= 60
CHECK_CEILING_S ?= 45
CHECK_TIMINGS := .check-timings

# The scoper, as a variable so scripts/test-check-scope-unit.sh can substitute a
# stub that returns a chosen exit code. That is the only way to assert these
# recipes HONOUR check-scope's 0/77/1 distinction — and they did not: check-lint
# read `$?` after a pipeline and so saw sed's status, turning an uncomputable
# scope into a silent "nothing to lint".
CHECK_SCOPE ?= bash scripts/check-scope.sh

# The fast commit gate. Runs the steps through the SAME timed driver validate
# uses (so test-validate-timing-unit guards this too), then reports the budget.
#
# SPRAWL_VALIDATE_TIMING_OUT is not optional: without it this would overwrite
# validate's own recorded observation on every commit and corrupt
# `make validate-baseline`.
#
# NO per-package capture is requested, deliberately, by naming a step that does
# not exist. The driver rightly FAILS when a captured step yields no package
# result lines — an empty top-5 table reads exactly like a fast run — but that
# guard is calibrated for `validate`, where `./...` always contains tests. For a
# CHANGE-SCOPED gate, "no package in scope has test files" is a legitimate
# outcome, and so is "no Go packages at all".
#
# I first tried to gate the capture on the scope being non-empty. That was a
# partial fix and it still produced a false red: a fresh worktree committing a
# single .txt file scoped to exactly 1 package, that package had NO test files,
# zero package lines came back, and the commit was refused with
# "captured step check-test-race.txt produced NO package result lines". Gating on
# emptiness cannot fix that, because the scope was not empty. Dropping the
# request is the correct fix, and it costs only check's top-5 table — the budget
# warning reads `step` lines, not `pkg` lines, so it still names the dominant
# step. The guard stays fully armed where it belongs, in validate.
check:
	@start=$$(date +%s); \
	SPRAWL_VALIDATE_TIMING_OUT=$(CHECK_TIMINGS) \
	SPRAWL_VALIDATE_CAPTURE_STEPS=__check_requests_no_per_package_capture__ \
	bash scripts/validate-timed.sh $(CHECK_STEPS); rc=$$?; \
	elapsed=$$(( $$(date +%s) - start )); \
	if [ "$$rc" -eq 0 ]; then \
	  bash scripts/check-budget.sh --elapsed $$elapsed --budget $(CHECK_BUDGET_S) \
	    --timings $(CHECK_TIMINGS) || true; \
	else \
	  echo "check: FAILED in $${elapsed}s — fix the failure above; the time budget is not the issue." >&2; \
	fi; \
	exit $$rc

# Introspection seam, mirroring print-validate-steps. Deliberately a bare printf
# with no $(MAKE) in it, so `make -n print-check-steps` executes nothing — that
# is what lets scripts/pre-commit probe cheaply for this target's existence.
print-check-steps:
	@printf '%s\n' $(CHECK_STEPS)

check-budget-structural:
	@bash scripts/check-budget.sh --structural --steps "$(CHECK_STEPS)" \
		--conf scripts/testdata/check-budget.conf --ceiling $(CHECK_CEILING_S)

# Scoped fmt/lint. These do NOT recurse into fmt-check/lint with an override:
# a command-line variable assignment propagates through MAKEFLAGS into any
# nested make, which would silently narrow the MERGE gate too. Separate recipes
# with their own variables is what keeps the two gates' scopes independent
# (scripts/test-lint-pin.sh asserts this).
check-fmt:
	@files=$$(git diff --cached --name-only -M --diff-filter=d -- '*.go'; \
	          git diff --name-only -M --diff-filter=d -- '*.go'; \
	          git ls-files --others --exclude-standard -- '*.go'); \
	files=$$(printf '%s\n' $$files | sort -u | grep -v '^$$'); \
	present=""; for f in $$files; do [ -f "$$f" ] && present="$$present $$f"; done; \
	files=$$(printf '%s\n' $$present | grep -v '^$$'); \
	if [ -z "$$files" ]; then echo "check-fmt: no existing Go files changed — nothing to format-check"; exit 0; fi; \
	out=$$($(GOLANGCI_LINT) fmt --diff $$files 2>&1); rc=$$?; \
	if [ "$$rc" -gt 1 ]; then \
	  printf '%s\n' "$$out"; \
	  echo "check-fmt: the pinned formatter exited $$rc: the check DID NOT RUN. That is a tool failure, not a dirty tree." >&2; \
	  exit $$rc; fi; \
	if [ -n "$$out" ]; then printf '%s\n' "$$out"; \
	  echo "check-fmt: files need formatting. Run 'make fmt'."; exit 1; fi; \
	if [ "$$rc" -ne 0 ]; then \
	  echo "check-fmt: the formatter exited $$rc without printing a diff: the check DID NOT RUN." >&2; \
	  exit $$rc; fi; \
	echo "check-fmt: OK ($$(printf '%s\n' $$files | grep -c .) file(s))"

# NOTE the two-step scope capture, and do not collapse it back into one.
# `rc=$$?` after a PIPELINE is the exit status of the LAST command, so
#     scope=$$($(CHECK_SCOPE) | sed ...); rc=$$?
# captured sed's status — always 0 — and check-scope's rc=1 ("I could not work
# out what to test") became rc=0 with empty output, matched the skip arm, and
# exited 0. A scope-computation failure silently became "nothing to lint". The
# transform happens AFTER rc is read. test-check-scope-unit.sh section [8] holds
# this shut for both consumers.
check-lint:
	@scope=$$($(CHECK_SCOPE) 2>/dev/null); rc=$$?; \
	if [ "$$rc" = "77" ]; then \
	  echo "check-lint: no Go packages in scope — skipping (gated at merge by 'make lint')"; exit 0; fi; \
	if [ "$$rc" != "0" ]; then \
	  echo "check-lint: could not compute a scope (rc=$$rc) — refusing to report that as a pass." >&2; exit 1; fi; \
	if [ -z "$$scope" ]; then \
	  echo "check-lint: check-scope exited 0 but printed nothing — refusing to treat that as 'nothing to lint'." >&2; exit 1; fi; \
	pkgs=$$(printf '%s\n' $$scope | sed 's|^github.com/dmotles/sprawl|.|' | tr '\n' ' '); \
	$(GOLANGCI_LINT) run $$pkgs

# The scoped -race run. A scope of "no Go packages" is a SKIP with a reason, not
# a silent pass: check-scope.sh exits 77 for that, and 1 for a scope it could
# not compute, which must never read as "nothing to test".
check-test-race:
	@$(CHECK_SCOPE) --report >/dev/null; \
	scope=$$($(CHECK_SCOPE) 2>/dev/null); rc=$$?; \
	if [ "$$rc" = "77" ]; then \
	  echo "check-test-race: SKIPPED — this change has no Go bearing. Gated at merge by 'make test-race' over ./... ."; \
	  exit 0; \
	fi; \
	if [ "$$rc" != "0" ] || [ -z "$$scope" ]; then \
	  echo "check-test-race: could not compute a scope (rc=$$rc) — refusing to report that as a pass." >&2; \
	  exit 1; \
	fi; \
	pkgs=$$(printf '%s\n' $$scope | sed 's|^github.com/dmotles/sprawl|.|' | tr '\n' ' '); \
	echo "check-test-race: $$(printf '%s\n' $$scope | grep -c .) package(s) in the dependency closure"; \
	go test -race -count=1 $$pkgs

# Default target — full quality gauntlet.
#
# QUM-1286: this is a RECIPE rather than a prerequisite list so each step can be
# timed and attributed. Three consequences, all deliberate, none of them free:
#
#  1. The literal `$(MAKE)` is LOAD-BEARING and must not be "simplified" to
#     `make`. GNU make executes a recipe line containing that literal even under
#     -n (handing `n` down via MAKEFLAGS), which is the only reason `make -n
#     validate` still expands the steps — and therefore the only reason
#     scripts/test-race-gate.sh's wiring assertions can still see `go test
#     -race ./...` at all. With a bare `make`, -n prints this one line, the race
#     gate goes blind, and it stays GREEN while blind. Guarded by
#     scripts/test-validate-timing-unit.sh section [8], which watches the
#     control fire.
#  2. `make -j validate` no longer interleaves the steps; the driver runs them
#     serially. That is required for a per-step number to mean anything, and it
#     is why the `hooks-armed: build` ordering dependency below is now belt and
#     braces rather than the mechanism.
#  3. Each step is its own make process, so `build` runs twice — once as a step,
#     once as `hooks-armed`'s prerequisite. Measured cost is one warm `go build`;
#     it is reported in the baseline's driver overhead rather than papered over.
#  4. The driver is resolved from $(CURDIR), NOT from $(dir $(THIS_MAKEFILE)).
#     Those differ exactly when someone runs `make -f <copy> validate`, which is
#     what scripts/test-race-gate.sh's RACE_GATE_MAKEFILE seam does to
#     DEMONSTRATE that the race gate can fail. Keyed to the makefile's own
#     directory, a copy in /tmp looked for /tmp/scripts/validate-timed.sh, the
#     expansion died at rc 127 with no `go test` line in it, and every group-[1]
#     leg failed — for the wrong reason. A reader would have seen red and
#     concluded the gate was armed. THIS_MAKEFILE is still what the sub-makes
#     re-enter, which is the half that must follow the copy.
#
# VALIDATE_TOLERATE is empty for every ordinary run; see validate-baseline.
VALIDATE_TOLERATE ?=

validate:
	@MAKE_CMD='$(MAKE) --no-print-directory' \
	SPRAWL_VALIDATE_MAKE_F='$(THIS_MAKEFILE)' \
	SPRAWL_VALIDATE_MAKE_C='$(CURDIR)' \
	SPRAWL_VALIDATE_TOLERATE_STEPS='$(VALIDATE_TOLERATE)' \
	bash '$(CURDIR)/scripts/validate-timed.sh' $(VALIDATE_STEPS)

# Introspection seam, in the same spirit as lint-cache-dir below: one step per
# line, so scripts/check-validate-baseline.sh and
# scripts/test-validate-timing-unit.sh can compare sets without re-parsing the
# Makefile. The one-per-line shape is asserted, not assumed — a space-separated
# single line would silently turn the baseline's step-set legs into no-ops.
print-validate-steps:
	@printf '%s\n' $(VALIDATE_STEPS)

# QUM-1286: unit suite for the timing driver itself. Pure bash + make, no Go
# build, no claude, no tmux. In `validate` for the same reason
# test-e2e-matrix-unit is: a regression test guarding a false-green is worthless
# if it only runs when somebody remembers.
test-validate-timing-unit:
	bash scripts/test-validate-timing-unit.sh

# QUM-1286: the recorded baseline is a CHECKED artifact, not a remembered one.
# The previous baseline was prose in this file (see test-race below), undated and
# unchecked, and nobody could tell whether it still described the tree. Do not
# quote a drift magnitude for it: that comparison is cross-host and the "~25s"
# figure this comment used to carry was confounded — see the CONFOUND note in
# scripts/validate-timed.sh, which resolves it with matched-core-count numbers.
# This asserts the recorded step set still equals VALIDATE_STEPS in both content
# and ORDER, that every step passed, that the measurement is dated and in-window
# and non-degenerate, and that no checkout path leaked into a PUBLIC repo.
check-validate-baseline:
	bash scripts/check-validate-baseline.sh scripts/testdata/validate-baseline.observed

# QUM-1286: re-measure the baseline. Runs the real gauntlet and promotes the
# driver's own machine-readable observation, so the committed file is never
# hand-edited. Not part of `validate` (it would be circular).
# TEST_RACE_FLAGS=-count=1 is not optional here and is not "changing what
# validate runs": a WARM run serves ~43 of 44 packages from the package cache,
# so it can report a duration for ONE package and a per-package baseline built
# from it would be a table of absences. Recording therefore bypasses the cache so
# every package is really measured, and the baseline records go_cache=cold to say
# so. A command-line variable assignment propagates through MAKEFLAGS to the
# driver's sub-makes, which is why this works without threading it by hand.
#
# VALIDATE_TOLERATE names the two SELF-REFERENTIAL steps. Both fail when the step
# set drifts — which is exactly when a fresh baseline is needed — so with plain
# fail-fast `make validate` AND `make validate-baseline` both failed and there
# was no supported way to record one, while the baseline file itself says DO NOT
# HAND-EDIT. Tolerating them here unwedges that recovery path without softening
# either gate: they still run, still report, and still make the run exit
# non-zero. The leading `-` accepts that non-zero so the promotion below is
# reached; the driver refuses to write an artifact unless every step ran, so a
# genuinely broken run cannot be promoted.
# TWO PASSES, and the second one is the point. Pass 1 tolerates the two
# self-referential steps so a DRIFTED step set can be bootstrapped at all; its
# artifact therefore legitimately contains a failing step, whose duration is a
# time-to-failure rather than the cost of the work. Promoting that would record a
# wrong number, so pass 1 exists only to make the gate self-consistent. Pass 2 is
# a plain, fully-gated run: it must be green, and the file that actually gets
# committed is always its artifact, with rc=0 on every step. Costs two validate
# runs; re-recording is rare and a baseline nobody can trust is worse.
validate-baseline:
	@echo "validate-baseline: pass 1/2 — bootstrapping (the two baseline-checking steps are tolerated)"
	-@$(MAKE) validate TEST_RACE_FLAGS=-count=1 \
		VALIDATE_TOLERATE='test-validate-timing-unit check-validate-baseline'
	@bash scripts/check-validate-baseline.sh .validate-timings/baseline.observed || { \
		echo "validate-baseline: REFUSING to promote. Pass 1 produced no complete, cache-bypassed observation, so there is nothing honest to record — and note this checks the ARTIFACT, not merely its existence: an earlier run's file sitting in .validate-timings/ is a stale measurement, which is worse than a missing one. Fix the failure above and re-run." >&2; \
		exit 1; \
	}
	@cp .validate-timings/baseline.observed scripts/testdata/validate-baseline.observed
	@echo "validate-baseline: pass 2/2 — confirming on a fully-gated run (nothing tolerated)"
	@$(MAKE) validate TEST_RACE_FLAGS=-count=1
	@bash scripts/check-validate-baseline.sh --final .validate-timings/baseline.observed || { \
		echo "validate-baseline: pass 2 did not produce a promotable observation. The tree is left with pass 1's bootstrap baseline, which records a tolerated failure — do NOT commit it; fix the failure above and re-run." >&2; \
		exit 1; \
	}
	@cp .validate-timings/baseline.observed scripts/testdata/validate-baseline.observed
	@echo "Recorded a fresh baseline in scripts/testdata/validate-baseline.observed — review the diff and commit it."

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
	@# module scoping matches. Until this slice lands on main there is no
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
# QUM-1287: the stamp is HEAD's COMMITTER DATE, not wall-clock build time (the
# reproducible-builds SOURCE_DATE_EPOCH convention). It was `date -u`, which
# changes on every invocation and made the link output unreusable BY
# CONSTRUCTION: every `make validate` — so every pre-commit hook, for every
# agent — relinked both binaries even when nothing had changed. Measured: two
# consecutive `make build` runs on an unchanged tree produced different hashes,
# and a changed stamp costs ~1.3s of relink against ~0.2s for a cached one.
#
# `format-local:` with TZ=UTC0, NOT `format:`. `--date=format:` renders the
# committer's own recorded offset and ignores TZ entirely, so on a commit made at
# a non-UTC offset it yields a local wall-clock time suffixed with a literal `Z`
# — wrong by the offset, yet self-consistent.
#
# BE PRECISE ABOUT WHAT IS ASSERTED, because the first draft of this comment
# claimed more than the test delivers. scripts/test-build-stamp.sh B2 recomputes
# the stamp with the SAME expression, so it catches an empty stamp, a wall-clock
# stamp and the author date — but not `format:` vs `format-local:`, which only
# diverge on a commit whose committer offset is non-UTC, and every commit in this
# repo is +00:00. B2t is therefore a TEXTUAL pin on this line: it is what can
# actually fire here.
#
# $(or ...) rather than a `||` fallback inside $(shell): outside a checkout
# `git log` exits 128 and a `||` still lets the EMPTY success through, stamping
# `-X main.date=` with nothing. buildinfo's own default for an unstamped build is
# "unknown", so that is the word used here too. The fallback is applied again at
# the LDFLAGS use site because `?=` lets an explicitly empty command-line
# `DATE=` win before $(or ...) is ever evaluated — measured: `make print-ldflags
# DATE=` stamped `-X main.date=` with nothing. Two ways in, both closed.
#
# `built:` therefore under-reports on a dirty tree: a binary built from
# uncommitted work claims its parent commit's time. That is inherent to making
# the link a function of the tree, and nothing branches on the value — `sprawl
# version` prints it and that is its only surface.
DATE    ?= $(or $(shell TZ=UTC0 git log -1 --format=%cd --date=format-local:%Y-%m-%dT%H:%M:%SZ 2>/dev/null),unknown)
# QUM-1286: DEFERRED (`=`), not simply-expanded. VERSION/COMMIT/DATE each fork a
# subprocess, and validate now runs 17 sub-makes; a simply-expanded LDFLAGS made
# every one of them fork `git describe`, `git rev-parse` and `date` at PARSE
# time, whether or not it was going to build anything. Deferred confines those
# forks to the recipes that actually reference LDFLAGS (build, install).
LDFLAGS = -s -w \
	-X main.version=$(VERSION) \
	-X main.commit=$(COMMIT) \
	-X main.date=$(or $(strip $(DATE)),unknown)

# Introspection seam, in the same spirit as print-validate-steps and
# lint-cache-dir: it lets scripts/test-build-stamp.sh assert the stamp's
# stability and provenance WITHOUT paying a compile.
print-ldflags:
	@printf '%s\n' '$(LDFLAGS)'

# QUM-1287: the byte-identity gate for the stamp above. In `validate` because a
# regression here is silent — a needlessly relinked binary still works.
test-build-stamp:
	bash scripts/test-build-stamp.sh

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

# FMT_SCOPE exists for the same reason LINT_SCOPE does (see below): the pin
# assertions in scripts/test-lint-pin.sh ask WHICH BINARY ran, not what it found,
# so they have no need to walk ./... — and linting or formatting the whole tree
# three extra times per `make validate` widens the window in which every OTHER
# agent's validate hits golangci-lint's machine-wide lock. Measured: scoping the
# suite's fmt-check legs to one package took ~6.5s off `make test-lint-pin`.
# validate's own fmt-check step still walks ./..., which is the run that matters.
FMT_SCOPE ?= ./...

# QUM-1287: this recipe was `@test -z "$$($(GOLANGCI_LINT) fmt --diff ./...)"`,
# which captured stdout and DISCARDED the exit status — so a formatter that
# failed without printing to stdout made the formatting gate pass. Measured on
# the parent commit: `make fmt-check GOLANGCI_LINT=false` and a variant exiting 7
# with output on stderr BOTH returned 0. That is a non-asserting fallback in the
# gate itself; scripts/test-lint-pin.sh F1/F2 hold it shut.
#
# Both conditions are reported separately and neither can be silent: a non-empty
# diff means the tree needs formatting, and a non-zero status with no diff means
# the check DID NOT RUN, which must never read as a clean tree. The diff is
# printed (it was previously swallowed) with the actionable line last.
#
# Output is checked BEFORE status on purpose: `fmt --diff` exits 1 when it finds
# a diff, so a non-zero status is the normal case there. The rc is reported
# alongside the diff anyway, because a formatter that crashes AFTER writing to
# stdout would otherwise be diagnosed as "needs formatting" — a loud red either
# way, but a misleading one.
#
# `exit $$rc` propagates the value no further than this recipe: make maps any
# recipe failure to its own exit 2, so a caller sees 2, never 1 or 7. Only the
# non-zero-NESS crosses the boundary, which is all F1/F2 assert.
#
# Note this is NOT redundant with `lint`, despite `golangci-lint run` also
# reporting formatter findings in v2 — measured, "File is not properly formatted
# (gofumpt)". `run` only loads files satisfying the host's build constraints,
# while `fmt` walks the source, and this tree has 17 such files in 6 packages
# (`go list -f '{{.IgnoredGoFiles}}' ./...`). Collapsing the two would silently
# stop checking all of them. The premise is asserted, not assumed:
# scripts/test-lint-pin.sh C1 goes red if those files ever disappear, so the
# collapse gets re-evaluated instead of being forgotten.
fmt-check:
	@echo "Checking formatting..."
	@out=$$($(GOLANGCI_LINT) fmt --diff $(FMT_SCOPE)); rc=$$?; \
	if [ -n "$$out" ]; then \
		printf '%s\n' "$$out"; \
		if [ "$$rc" -gt 1 ]; then \
			echo "NOTE: the formatter also exited $$rc, so the output above may be a crash rather than a diff."; \
		fi; \
		echo "Files need formatting. Run 'make fmt' to fix."; \
		exit 1; \
	fi; \
	if [ "$$rc" -ne 0 ]; then \
		echo "the pinned formatter exited $$rc without printing a diff: the formatting check DID NOT RUN. This is a tool or config failure, not a clean tree."; \
		exit $$rc; \
	fi

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
# NO MEASUREMENTS HERE. The numbers that used to sit in this comment (a "4
# cores" host, `go test ./...` 99.0s vs `-race` 122.2s, "internal/supervisor
# alone is 75s of the 122s") were undated and unchecked, and this host is a
# different, 8-core machine — so they could not be compared against a run here at
# all, in either direction. QUM-1286 settled it by pinning this host to 4 cores
# (`taskset -c 0-3`): internal/supervisor measured 100.09s against the recorded
# 75s at the same core count, so the package grew by about a third. Re-adding a
# number to this comment would recreate exactly the artifact that made that
# question unanswerable for months. The live, dated, cache-annotated
# baseline lives in scripts/testdata/validate-baseline.observed and is checked by
# `make check-validate-baseline`; `make validate-baseline` re-measures it.
#
# One piece of the old REASONING survives and one piece of it was WRONG, and the
# distinction is now measured rather than asserted:
#
#   * `internal/supervisor`, the dominant package, really is sleep/timeout-bound
#     and barely responds to core count — 100.1s on 4 cores vs 103.6s on 8.
#   * the SUITE TOTAL is not. It parallelises across 44 packages and is ~24%
#     faster on 8 cores (164.4s vs ~125s). The old comment's "the suite is
#     sleep/timeout-bound, not CPU-bound" elided that, which is exactly why its
#     core-count-blind totals could not be compared against anything.
#
# Numbers here only because they are the *subject* of that correction; the live
# ones live in the checked baseline. The other half of the reasoning stands: a
# targeted "concurrency-heavy packages" subset was rejected — it covers a handful
# of ~40 packages and needs a hand-maintained list that silently stops covering
# any newly-concurrent package.
#
# -race requires cgo and a C toolchain. That fails LOUDLY (the build is refused)
# rather than silently skipping, so it cannot become a false green — and
# test-race-gate re-proves detection actually works on every run anyway.
# TEST_RACE_FLAGS is the seam `make validate-baseline` uses to bypass the package
# cache (-count=1) when RECORDING a baseline. Empty by default, so a plain
# `make validate`/`make test-race` is byte-identical to before QUM-1286 — and
# note the deliberate absence of -count=1 here is what makes an unchanged-tree
# re-run cheap, which is also what makes duration bimodal and unusable as a gate.
TEST_RACE_FLAGS ?=

test-race:
	go test -race $(TEST_RACE_FLAGS) ./...

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
# docs/archive/budget-resolver.md (archived).
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

# QUM-1289: the doc-lint tests, which are deliberately NOT in the default ./cmd
# test binary. They read every tracked .go file and regex-scan every SKILL.md,
# which the race detector instruments for nothing: 32.57s vs 1.63s for
# TestSkillsGoSymbolBanListIsDead alone. ./cmd is reverse-reachable from ~46 of
# 51 packages, so that cost landed in nearly every change closure and dominated
# the commit gate.
#
# Deliberately WITHOUT -race, and that is a structural claim rather than a
# preference: cmd/skills_doclint_test.go has no goroutines, channels, sync or
# t.Parallel, so there is nothing for the detector to observe.
# test-doclint-split-unit asserts BOTH that this step really executes both tests
# AND that the file still has no concurrency primitives — so if concurrency
# appears here, -race is demanded again instead of the justification rotting.
#
# -count=1 because a cached PASS would make this step a no-op that still looks
# green, and this is the ONLY place these two tests run.
test-doclint:
	go test -tags doclint -count=1 -run '^(TestSkillsGoSymbolBanListIsDead|TestSkillsDoNotNameDeadGoSymbols)$$' ./cmd/

# Guards the split above: that the moved tests are absent from the default
# binary, present in the tagged one, reachable from VALIDATE_STEPS, and actually
# EXECUTED. Moving a test behind a build tag nothing runs deletes coverage while
# looking like an optimisation.
test-doclint-split-unit:
	bash scripts/test-doclint-split-unit.sh

# QUM-1289: guards scripts/check-scope.sh, the dependency-aware change-scoper
# that will drive `make check`'s test step. Its live positive control watches
# BOTH plausible wrong scopers (dirs-of-edited-files, and reverse closure over
# .Deps) miss a pinned test-only dependent — because a gate that silently
# narrows its own scope reports green over a package it never built.
test-check-scope-unit:
	bash scripts/test-check-scope-unit.sh

# QUM-1289: guards scripts/check-budget.sh — the STRUCTURAL half is what stops
# `check` growing back into the merge gate one addition at a time, and the
# wall-clock half must warn loudly without ever blocking (dmotles's call).
test-check-budget-unit:
	bash scripts/test-check-budget-unit.sh

# QUM-1289: guards WHICH GATE scripts/pre-commit runs, and its fallback. The hook
# every agent runs is MAIN's copy, so a missing `check` target on a pre-1289
# branch would block every commit for every agent. Asserts both arms, that the
# fallback is loud, that no-gate-available REFUSES, and that the scope paths are
# captured before the GIT_* unset (a partial commit otherwise reads the wrong index).
test-precommit-gate-unit:
	bash scripts/test-precommit-gate-unit.sh

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

# QUM-1249 (M1a) event-log store and QUM-1252 (M3a) workflow-engine Postgres
# integration suites — both behind the store_pg tag. Docker-dependent,
# so it is NOT in `validate` — validate stays Docker-free. `-count=1` is
# load-bearing: a Docker-down run t.Skip's, Go caches that as a passing package,
# and without the bypass the skip replays as green once Docker is back.
# For the exit-77-on-no-Docker contract, run it through its matrix row instead:
# `make test-e2e-matrix-store-pg-integration`.
test-store-pg:
	go test -tags store_pg -count=1 -v ./internal/store/ ./internal/engine/

# QUM-616 matrix-driven e2e harness foundation. Wave 1 — runs alongside
# the per-test test-*-e2e targets. See scripts/e2e-matrix.sh.
test-e2e-matrix: build
	bash scripts/e2e-matrix.sh all; rc=$$?; ./sprawl sandbox-gc --max-age=10m || true; exit $$rc

# QUM-947: unit tests for the driver itself — arg parsing, fail-fast row-name
# validation, and the summary's passed/requested arithmetic. Pure shell, no
# claude and no tmux, so it runs inside `make validate`: a regression test
# guarding a false-green is worthless if it only runs when someone remembers.
# This explicit rule takes precedence over the test-e2e-matrix-% pattern rule
# below, so `unit` is never mistaken for a row name.
#
# QUM-1303: this comment said "~0.4s" while the step measured ~97s in validate —
# the THIRD place that one figure had rotted (with SKILL.md's runtime paragraph
# and its matrix-table row). No duration is quoted here on purpose: the recorded
# per-step figure lives in scripts/testdata/validate-baseline.observed, which a
# check reads. Do not paste a number back in.
test-e2e-matrix-unit:
	bash scripts/test-e2e-matrix-unit.sh

# Pattern target: `make test-e2e-matrix-merge-reuse` runs only that row.
# For several rows in one driver invocation (with an honest denominator), call
# the driver directly: `bash scripts/e2e-matrix.sh row-a row-b row-c`.
test-e2e-matrix-%: build
	bash scripts/e2e-matrix.sh $*; rc=$$?; ./sprawl sandbox-gc --max-age=10m || true; exit $$rc
