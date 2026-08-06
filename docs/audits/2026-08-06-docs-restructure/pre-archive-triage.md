# Pre-archive triage — unactioned obligations in the 118 non-KEEP `docs/` files

**Author:** pulse (researcher) · **Date:** 2026-08-06 · **Branch:** `dmotles/docs-triage-unactioned`
**Tree state:** all verification below was run against `3d92e2c` ("QUM-1093: scope the cost figure in status to the current session"). Every count and every "still live" claim is *as measured at that commit* and will decay the moment someone complies with it.
**Input:** the classification in `docs/audits/2026-08-06-docs-restructure/docs-directory.md` (branch `dmotles/docs-audit-docs-dir`, commit `b75df17`). This document extends that audit; it does not redo it.
**Method:** read-only. No build, no `make`, no e2e, no sandbox. `git`, `rg`, `find`, file reads only.

---

## 0. The method rule this sweep had to adopt mid-flight

The brief handed me a verified premise: `docs/research/open-source-readiness/03-security-audit.md`
filed a CRITICAL agent-name path traversal, and `rg 'func [Vv]alidateAgentName'` returns
**zero hits**, so nothing was ever done. That premise is false, and the way it is false is the
most transferable thing in this report.

The function exists. It is called **`ValidateName`**, not `ValidateAgentName`:

```
internal/agent/validate.go:15  func ValidateName(name string) error
                              regexp `^[a-zA-Z0-9][a-zA-Z0-9_-]*$`, 64-char cap
```

It rejects `/`, `\`, `..`, and a leading `.` — exactly the fix the audit's Priority Action #1
asked for — and it is called from 11 non-test sites (`internal/supervisor/real.go` ×7,
`internal/agentops/{kill,retire,merge}.go`, `cmd/logs.go`). It landed in `30fd7fe` on
**2026-04-06**, two days after the audit was written, and the commit body names the ticket:
*"QUM-161: Added agent name validation to prevent path traversal (8 entry points covered)."*
**QUM-161 is Done in Linear.** QUM-1128 was filed on the false premise and has since been
canceled and linked to QUM-161.

The grep was not careless. It was *structurally incapable of returning a positive*, because it
searched for an identifier the doc proposed rather than the behaviour the doc demanded. Zero and
cannot-return-nonzero are indistinguishable from the output alone. So:

> **Before trusting a negative result, prove the probe can produce a positive one.**
> Run it against a case you know exists, and record what it printed.

Every survivor below carries the positive control I ran. The rule changed at least one answer in
both directions: it killed a candidate (see *Cleared* → "control-request entry logging") and it
confirmed the top finding. Where I could not construct a positive control, I say so and lower the
confidence rather than reporting the negative as fact.

---

## 1. The list — live, unfiled, unfixed

Nine items. Ranked by severity. Each is (still real) **and** (not covered by a Linear issue)
**and** (not already fixed), each checked against the tree at `3d92e2c` and against Linear.

---

### 1. Child inbox drain is unserialised — a concurrent poke can double-inject a message

| | |
|---|---|
| **Severity** | **High** |
| **Source doc** | `docs/research/qum-1061-child-drain-inflight-asymmetry.md:172-177` ("asymmetry 2") — **ARCHIVE** set |
| **Confidence** | **High** |

**The defect.** The child drain does a read-then-write with no mutex. A poke arriving on the MCP
handler goroutine (`Real.SendMessage` / `Real.ReportStatus`) can interleave with `PostTurnSweep`
on the backend reader goroutine; both read the in-flight set before either writes, and the same
inbox entry is injected into the child's stdin twice. The child's guarantee is "written once per
*sequential* drain", not "once under concurrent drains".

**Evidence it is still live.** The code says so itself, in a comment that declares the residual
and declines to close it:

```
internal/supervisor/drain.go:159       mu: nil,
internal/supervisor/drain.go:150-158   "Left nil DELIBERATELY: adding a mutex here would be a
                                        behaviour change, and QUM-1062's contract is that
                                        unification changes nothing. Closing it is its own issue."
```

Two callers, asymmetric by construction — `internal/supervisor/runtime_launcher.go:598`
(`childDrainPolicy()`, unserialised) vs `internal/supervisor/weave_handle.go:249`
(`weaveDrainPolicy(&h.drainMu)`, serialised).

**Positive control.** `rg -n 'mu:' internal/supervisor/drain.go` returns **two** lines — `:112 mu: mu`
and `:159 mu: nil`. The probe can distinguish a populated mutex field from a nil one, and does.

**Not filed.** QUM-1061 (Done), QUM-1062 (Done), QUM-1066 (Done), QUM-1072 (Done) are the
surrounding arc; none of them is this. A keyword search over the project for child-drain
serialisation / concurrent duplicate / TOCTOU returns no matching open issue.

**Why it ranks first.** It is the only finding where a live code comment says "this is its own
issue" and no issue exists — the exact shape the archive is about to bury, in the subsystem
CLAUDE.md already flags as having a total e2e coverage gap for async delivery to a busy child
(QUM-1073).

---

### 2. Nothing runs the test suite automatically, on any event, in a public repo

| | |
|---|---|
| **Severity** | **High** |
| **Source doc** | `docs/research/open-source-readiness/07-unknown-unknowns.md:124, 252` — **DELETE** set |
| **Confidence** | **High** |

**The defect.** The doc's readiness table records *"CI pipeline exists — **No** — P1 — `make validate`
exists but no automation."* That is still true. `make validate` is enforced **only** by a
locally-installed pre-commit hook, which `git commit --no-verify` skips and which a fresh clone
does not have until someone runs `make hooks` or creates a worktree.

**Evidence it is still live.** Listing the directory rather than grepping for a filename:

```
$ find .github -type f -o -type l
.github/workflows/release.yml          # ← the entire contents
```

`release.yml` is `on: push: tags: - "v*"`, and its only job runs
`goreleaser/goreleaser-action` with `args: release --clean`. There is **no test step, no lint
step, no `make validate` step** — so the release path itself ships a binary without running the
suite. No other CI system is configured anywhere in the tracked tree (checked for GitLab CI,
CircleCI, Azure Pipelines, Jenkins, Travis, Drone, Buildkite, Woodpecker).

**Positive controls.** `find .github -type f | wc -l` → `1`, so the probe returns non-empty and is
live. The other-CI pattern was re-run with a substitution known to exist (`.goreleaser.yaml`,
`Makefile`) and hit both — so its empty result is a true negative, not a broken regex.

**Not filed.** QUM-167 ("CI/CD: GoReleaser + GitHub Actions release workflow") is Done and is the
release workflow above. Nothing covers validate-on-push/PR.

---

### 3. `install.sh` accepts an unverified binary when no sha256 tool is present

| | |
|---|---|
| **Severity** | **Medium-High** (supply chain; low reachability — see below) |
| **Source doc** | `docs/research/open-source-readiness/06-installer-distribution.md:283-286` — **DELETE** set |
| **Confidence** | **High** on the control flow; the severity is bounded by reachability, stated below |
| **Provenance** | **This is not an obligation the doc raised. It is a defect the doc *authored*.** See below — it matters for how you file it. |

**The defect.** `verify_checksum` returns success when neither `sha256sum` nor `shasum` is on the
host. It warns to stderr and `return 0`, which the caller cannot distinguish from a verified
archive; `set -eu` does not help, because the return status is zero.

```
install.sh:85    if command -v sha256sum >/dev/null 2>&1; then
install.sh:86      actual="$(sha256sum "$archive" | cut -d' ' -f1)"
install.sh:87    elif command -v shasum >/dev/null 2>&1; then
install.sh:88      actual="$(shasum -a 256 "$archive" | cut -d' ' -f1)"
install.sh:89    else
install.sh:90      echo "Warning: no sha256 tool found, skipping verification" >&2
install.sh:91      return 0
install.sh:92    fi
```

Single call site, on the install path, between download and extract:
`install.sh:152  verify_checksum "${WORK_DIR}/${ARCHIVE}" "${WORK_DIR}/${CHECKSUM_FILE}"`.

**This is the more serious of the two possible readings, not the milder one.** A checksum
*exists*: `install.sh:82-84` hard-exits 1 if the archive is absent from the checksums file. So the
skipped case is "a valid expected checksum was fetched and then not compared", **not** "there was
nothing to verify".

**Reachability, stated honestly.** The `else` arm fires only on a host with neither `sha256sum`
(GNU coreutils, also busybox) nor `shasum` (ships with perl; present on stock macOS). That is
unusual — a minimal container with none of the three. This is what bounds the severity, and it
should be in the issue so nobody over-reads it. The tell that the behaviour is nonetheless wrong:
`scripts/test-install.sh:189` treats the same condition as **fatal** (`FATAL: no sha256 tool
available for tests; exit 1`) while production treats it as a pass.

**Positive control.** `command -v sha256sum && command -v shasum` both resolve on this host, so
the skip arm is confirmed to be the *unusual* branch rather than the normal one — the probe
distinguishes the two states.

**Provenance — read this before filing.** The identical eight lines, including
`echo "Warning: no sha256 tool found, skipping verification"` and `return 0`, are in the
archive-bound doc at `06-installer-distribution.md:283-286`, as the proposed installer skeleton.
The doc is the **origin** of the shape, not its reporter. So this does not fit the
"finding raised and never actioned" frame; it is a defect that entered the tree *through* a doc
we are about to delete. Per forge's scope guard I flag it rather than generalising: it is the one
item on this list I would route as "found in passing" rather than as archive-triage output. It
also happens to be the shape CLAUDE.md § QUM-997 names — a fallback branch that can neither
count a failure nor fail the run — sitting in a security control.

**Not filed.** QUM-961 ("Fix two security guards that pass vacuously: test-install checksum
rejection and test-hub-bootstrap secret-leak scan", Todo) is adjacent but **different**: I read its
body in full, and part A is about `scripts/test-install.sh`'s `assert_exit_nonzero` accepting any
nonzero cause — i.e. the *test* of the rejection path. It does not touch `install.sh`'s own
skip-and-return-0. Fixing QUM-961 as written leaves this open.

---

### 4. Deleting these docs deletes the only written record of the security/trust model

| | |
|---|---|
| **Severity** | **Medium** |
| **Source docs** | `07-unknown-unknowns.md:126` (SECURITY.md, P2) and `03-security-audit.md:167-169` (Priority Actions #2/#3/#4) — **DELETE** and **ARCHIVE** respectively |
| **Confidence** | **High** |

**The defect.** Three distinct Priority Actions from the security audit were never discharged, and
they share a consequence: after the cut, nothing in the tree records them.

- **#2 — document the trust model.** The audit asks for a top-level `docs/security-model.md`
  recording that agents trust each other, that the filesystem is the trust boundary, and that
  identity spoofing (`messages.Send`'s caller-supplied `from`) and prompt injection are *accepted*
  risks rather than unnoticed ones. No such doc exists.
- **#3 — file permissions.** Agent state, messages, and worktree dirs are still `0o644`/`0o755`,
  world-readable, each with a `//nolint:gosec // … is intentional` annotation. The audit's ask was
  "tighten, **or** document the single-user assumption prominently." Neither happened.
- **#4 — sandbox limitation.** `SPRAWL_TEST_MODE` is still prompt-level only, with no OS
  enforcement, and is not mentioned in `README.md`.

Separately, `07-unknown-unknowns.md` asks for a `SECURITY.md` vulnerability-reporting process.

**Evidence it is still live.**

```
$ git ls-files | grep -i security
docs/design/hub/security-privacy.md
docs/research/open-source-readiness/03-security-audit.md     # ← being archived
$ find . -iname 'SECURITY*' -not -path './.git/*' -not -path './.sprawl/*'
./docs/design/hub/security-privacy.md
```

No `SECURITY.md` at the repo root, under `.github/`, or at any other path, tracked or untracked.
No `docs/security-model.md`. `rg -ni 'trust model|trust boundary|threat model'` over the tracked
tree excluding `docs/` returns nothing. `rg -n 'SPRAWL_TEST_MODE' README.md DESCRIPTION.md`
returns nothing. Permissions confirmed by reading the call sites in
`internal/state/state.go`, `internal/messages/messages.go`, `internal/worktree/worktree.go`.

**Positive controls.** The same `git ls-files | grep -i` probe run for `contributing` returns
`CONTRIBUTING.md`, so the probe finds root-level docs when they exist. The permissions grep
returned 20+ real hits, so it is live.

**Not filed.** No issue covers SECURITY.md or a trust-model doc. (`QUM-165 Add CONTRIBUTING.md`
is Done and is the neighbouring item that *was* actioned.)

**Why this one is specifically an archive-safety finding.** The two files that state the trust
model are the two being cut. Archiving #2–#4 without recording them anywhere live is the precise
failure the cut is supposed to prevent.

---

### 5. `ask_user_question` silently ignores a `multiSelect` parameter

| | |
|---|---|
| **Severity** | **Medium** |
| **Source doc** | `docs/research/ask-user-question-mcp-design.md:570-572, 736` (recommendation R7) — **ARCHIVE** set |
| **Confidence** | **High** on the mechanism, **Medium** on impact |

**The defect.** R7 asked for a lenient decoder accepting **both** `multi_select` and `multiSelect`,
plus an integration test asserting both forms work, precisely because a caller using camelCase
would otherwise get a silently wrong modal rather than an error. Only the snake_case form exists,
and there is no custom unmarshaller, so `multiSelect: true` decodes to `MultiSelect == false` and
the user gets a single-select modal with no diagnostic anywhere.

**Evidence it is still live.**

```
internal/supervisor/question.go:41   MultiSelect bool `json:"multi_select,omitempty"`
internal/sprawlmcp/tools.go:343      "multi_select": map[string]any{ … }
```

`rg -n 'multiSelect|UnmarshalJSON' internal/supervisor/question.go internal/sprawlmcp/{server,tools}.go`
→ no hits.

**Positive control.** The same command *did* return the two `multi_select` lines above, so the
probe reaches these files and matches this shape; the absence of `multiSelect` / `UnmarshalJSON`
is a true negative.

**Not filed.** Twelve `ask_user_question` issues exist (QUM-527/535/536/538/553/558/611/635/759…);
none is R7.

**Impact caveat, stated because it cuts against me:** the MCP schema advertises only
`multi_select`, so a schema-conforming client cannot trip this. The exposure is a hand-written or
remembered call — which is exactly the "weave's muscle memory" case R7 named. That is why I rate
confidence high and impact medium.

---

### 6. Activity-ring writes to `activity.ndjson` are unbounded and discard errors

| | |
|---|---|
| **Severity** | **Medium** |
| **Source doc** | `docs/research/permission-hang-forensic-2026-05-19.md:270-279` (F3) — **ARCHIVE** set |
| **Confidence** | **High** |

**The defect.** F3 asked for a bounded write deadline and a drop counter, because a bare blocking
write on the activity path can stall its caller indefinitely. The write is still bare:

```
internal/agentloop/activity.go:81    _, _ = w.Write(b)
```

No deadline, no drop counter, both return values discarded.

**Partially mitigated, which is why it is Medium and not High.** F2 of the same doc *did* land —
`internal/backend/session.go` now has `observerCh` + `runObserverDrain` — so a stalled write no
longer wedges the protocol reader; it stalls the observer drain goroutine instead. The blast
radius shrank; the unbounded write did not go away.

**Positive control.** `rg -n 'writeTimeout' --type go` returns the QUM-1072 bounded-write
machinery in `internal/supervisor/drain.go:88,92,135,186` — so the repo does have this pattern,
the probe finds it where it exists, and there is a known-good shape to copy.

**Not filed.** QUM-547 ("Audit runtime/supervisor teardown paths for other unbounded waits") is
Done and is about teardown waits, not this write path. No issue covers it.

---

### 7. Two probe call sites apply *opposite* defaults for a missing capability

| | |
|---|---|
| **Severity** | **Low-Medium** (latent; not reachable in production today) |
| **Source doc** | `docs/research/architecture-simplification-audit-2026-05-20.md:16, 539` (S1 §3d) — **ARCHIVE** set |
| **Confidence** | **High** on the divergence, **Low** on exploitability |

**The defect.** S1 asked that the optional duck-typed capabilities become *required* on
`RuntimeHandle` so the defensive-default branches could be deleted, calling out that the branches
default in **opposite directions**, "which is itself a correctness hazard". Both branches are
still there and still opposite:

```
internal/supervisor/runtime.go:302-311   AgentRuntime.IsTerminallyFaulted:
                                          probe absent → return false   (healthy)
internal/supervisor/runtime.go:~819-826  AgentRuntime.Wake:
                                          probe absent → faulted = true (faulted)
```

**Exploitability, stated against my own finding.** QUM-613 (Done) named these as sub-interfaces in
`internal/supervisor/handle_probes.go` with compile-time `var _ =` assertions, and both production
handles (`*unifiedHandle`, `*WeaveRuntimeHandle`) satisfy `terminalFaultProbe`. So the divergent
branches are **provably unreachable in production today** — the audit's own open question #2
("can the no-probe default ever be exercised in production? If not it's dead-code-in-prod") is
still unanswered and the answer is now "no". This is a latent trap for the next handle
implementation, not a live bug. File it as cleanup, or close it by answering the open question in
a comment; do not staff it as a defect.

**Not filed.** QUM-613 (Done) covered the *naming* half of S1. Nothing covers making them required
or reconciling the defaults.

---

### 8. `searchOverlay` is unclamped and can re-create the fixed input-overflow class

| | |
|---|---|
| **Severity** | **Low** |
| **Source doc** | `docs/research/input-panel-overflow.md:240-243, 256` — **ARCHIVE** set |
| **Confidence** | **Medium** — mechanism read, **not reproduced** |

**The defect.** The doc's main fix landed (`internal/tui/app.go:557-562` and `:3192-3196` both
re-run `resizePanels()` when input height changes), but it flagged a follow-up it never resolved:
*"does `searchOverlay()` suffer the same drift? Suspect yes — flag for follow-up."* `searchOverlay`
is still composed into `content` (`app.go:2677`, `:2684`), is still not sized by `resizePanels`,
and `app.go:3501-3506` builds `"(reverse-i-search)`" + query + "': " + input` with no width clamp,
so a long query plus a long input wraps to N rows.

**Confidence is Medium and I am not raising it.** I read the composition path; I did not run the
TUI (out of scope for this sweep) and did not reproduce the overflow. **Verify before filing.**
The sibling follow-up in the same doc — clamp `maxInputHeight` to a fraction of terminal height —
is also undone (`internal/tui/layout.go:11 maxInputHeight = 12`, a bare constant with no
terminal-height term), and is the cheaper of the two to check.

**Not filed.** No issue matches.

---

### 9. `NOTICE` and SPDX headers were checked off as actions and never done

| | |
|---|---|
| **Severity** | **Low** (housekeeping) |
| **Source doc** | `docs/research/open-source-readiness/01-licensing.md:47-48` — **DELETE** set |
| **Confidence** | **High** |

`LICENSE` exists (Apache-2.0) and the README links it — those two action items shipped. The other
two did not: there is no `NOTICE` file (`git ls-files | grep -i '^NOTICE'` → empty), and
`rg -l 'SPDX-License-Identifier' --type go` matches **zero** `.go` files. Positive control: the
same SPDX probe run without the `--type go` filter matches exactly one file — the doc that
proposes the header — so the probe works and the Go-file result is a true zero.

Apache-2.0 does not *require* a `NOTICE` absent third-party attribution, so this is genuinely
optional; it is on the list only because it was written down as an action and never closed or
declined. File it or decline it in writing — either discharges it.

---

## 2. Cleared — candidates investigated and dismissed

This section is the evidence the sweep was real. Every entry was a plausible unactioned obligation
that survived first reading and died on contact with the tree or with Linear.

### Killed by the positive-control rule (both directions)

| candidate | why it died |
|---|---|
| **Agent-name path traversal — the brief's own premise** | **Fixed 2026-04-06.** `internal/agent/validate.go:15 func ValidateName`, 11 non-test call sites, landed in `30fd7fe`, ticket **QUM-161 (Done)**. The zero-hit grep searched for `ValidateAgentName`, an identifier the doc *proposed*. QUM-1128 canceled. |
| **No entry logging on `handleInlineControlRequest`** (`permission-hang-forensic:307-308`, D2) | Candidate said the handler logs nothing on entry, so a future forensic can't separate "never sent" from "handler hung". True — but the positive control killed it: `rg -c 'slog\.' internal/backend/session.go` → **zero**, and the file does not import `log/slog` at all. "No log here" is the file's uniform convention, not an oversight in this handler. Reporting it would have been a false positive. |
| **`tab-cycling-audit.md`'s banner is itself stale** (claimed by the input audit) | The counter-claim is what's wrong. `rg -n 'activePanel' --type go -g '!*_test.go'` → **one hit, and it is a comment** (`internal/tui/app.go:715`). `activePanel` is not a live identifier; the banner's QUM-695 claim is correct. |
| **Spawn advertises unimplemented agent types** (`mcp-surface-audit` P1 #7) | `ValidTypes` really does still list `tester` and `code-merger` while `SupportedTypes` has four. But reading the *behaviour* rather than the vars: `internal/agentops/spawn.go:93` rejects them with `"agent type %q is not yet supported; currently supported: engineer, researcher, manager, qa"`. That message is **QUM-45, "Fix spawn error message listing unsupported agent types" — Done.** The user-facing behaviour is correct; the residue is cosmetic. |

### Already filed in Linear

| candidate | issue |
|---|---|
| Input-history cap 10k + mode `0600` (`m15-phase-relevance:239` — the security-flavoured one, `internal/tui/history.go:22 historyFilePerm = 0o644`) | **QUM-696** (Backlog) |
| Richer consolidation progress output / `sprawl memory status` (`memory-consolidation-perf` item **F**) | **QUM-287** (Backlog) |
| Input-hash short-circuit for consolidation (item **G**) | **QUM-288** (Backlog) |
| Cap/budget the consolidation prompt (item **D**) | **QUM-285** (Done) |
| Consolidation off the critical path (item **A**) | **QUM-282** (Done) |
| Unbounded `cancel_async_message` ack wait / Ctrl+U hang (`qum-1000-local-command-strand-design:204-215`) | **QUM-1005** (Backlog) |
| `UserMessageConsumedMsg` `TurnIdle→TurnThinking` spurious spinner (`qum-1000:216-221`) | **QUM-1014** (Todo) and **QUM-1075** (Backlog) |
| MCP `retire` lacks a `force` flag (`mcp-surface-audit` P0 #3) | **QUM-853** (Backlog), **QUM-811** (Todo) |
| Wedged child / repeated bounded drain writes | **QUM-1076** (Backlog) |
| Stale `docs/design/hub/12` watchdog reference | **QUM-1004** (Backlog) |

### Fixed in the tree (no issue needed)

Grouped by source doc, with the mechanism that discharged them — recorded so nobody re-opens them.

- **`memory-consolidation-perf.md`** — items **A** (off critical path: `internal/rootinit/bgconsolidate.go`),
  **B** (parallelised under `errgroup` in `postrun.go`), **C** (`DefaultMemoryModel = "sonnet"`,
  `internal/memory/budget.go:33`), **E** (per-phase `context.WithTimeout`, `DefaultInvokeTimeout = 120s`).
- **`architecture-simplification-audit-2026-05-20.md`** — S3 (report_status → maildir), S4 (`RuntimeStarter.Start`
  takes no ctx, QUM-612), S5 (six e2e harnesses → one matrix driver). S2 is *partial*: `internal/supervisor/liveness/`
  exists but is a **projection** over the five sources, not a collapse of them — I am not listing it as a finding
  because the doc's goal was arguably met in spirit and adjudicating that is a design call, not a triage call.
- **`mcp-hang-observability-design.md`** — angles A1–A5 and C all shipped (`internal/sprawlmcp/calllog/`,
  `.sprawl/runtime/in-flight.json`, SIGUSR1 dump, pprof via QUM-678/934, `RealRunTestsStreaming`, `SetActiveOps`).
- **`notification-injection-race-2026-05-14.md`** — recommendations (c) and (d) both discharged by the one
  QUM-580 mechanism, `internal/supervisor/sweep_coordinator.go`.
- **`qum-458-e2e-leak-analysis.md`** — all four layers shipped (`internal/procutil/pdeathsig_*`,
  `installOrphanWatchdog`, `cmd/sandbox_gc.go`, `trap … EXIT INT TERM HUP`).
- **`qum-606-recover-zombie-2026-05-20.md`** — R1–R5 all shipped.
- **`qum-611-ask-question-wedge-2026-05-21.md`** — F1–F4 shipped (`DismissQuestionMsg.Hard`, status-bar badge,
  `cancelByAgent`, ctx-respecting waits). Only R7-adjacent leftovers remain; the one that matters is finding **5**.
- **`qum-615-agent-liveness-spec`**, **`qum-371-scope-update`**, **`qum-727-design`**, **`qum-618-wedge-rootcause`**,
  **`agent-resume-after-restart`** (gaps 1–5), **`child-viewport-missing-tool-results`**, **`input-panel-overflow`**
  (main fix), **`context-token-counter`** (items 1–5), **`token-usage-tracking`** (gap 5 fixed at
  `internal/tui/statusbar.go:185-189` by QUM-1093, i.e. by HEAD itself), **`tui-input-disappears-with-tall-tree`**,
  **`tui-parity-audit`** (all S1/S2/S3), **`weave-session-cycling`**, **`beads-worktree-integration`** (both main
  recommendations), **`cli-deletion-deadcode-audit`** (every named deletion), **`mcp-manager-callsite-bugs`** (bug B),
  **`branch-hygiene-root-cause`**, **`paste-input-ux-synergy`** (both issues, QUM-455/456),
  **`paste-render-cadence`** (shipped as `internal/inputcoalesce/`), **`qum-334-bridge-bleed`** (per-agent viewport
  + the buffer-leak caveat), **`qum-386-regression`** (`ParentToolID`), **`qum-488-delegate-wake`** (`feedTasks`),
  **`qum-549-send-interrupt`** (QUM-552 + both doc caveats), **`qum-570-startturn-caller-map`**,
  **`qum-685-bench-investigation`**, **`lost-commits-2026-04-21`** (recs 1 and 4),
  **`tui-weave-wedge-2026-05-05`**, **`unify-tui-weave-init`**, **`qum-552-sandbox-transcript`**,
  **`unified-runtime-messaging-audit`** (§B.2, §B.3, §F.5.4), **`m15-phase-relevance`** (toast subsystem),
  **`04-release-mechanism`** (all five: version vars, `sprawl version`, `.goreleaser.yaml`, release workflow,
  Makefile ldflags), **`06-installer-distribution`** (install.sh with checksum verification — modulo finding 3),
  **`02-secrets-scan`** (`CLAUDE.local.md` gitignored; hardcoded test path generalised),
  **`01-licensing`** (LICENSE + README link), **`07-unknown-unknowns`** (CONTRIBUTING.md → QUM-165, CHANGELOG.md,
  version string).

### Moot — the subject no longer exists

Whole docs whose recommendations target deleted code, and which therefore carry **no** obligation
into the archive: `realtime-message-injection.md`, `sandbox-notifier-leak-2026-04-22.md`,
`tmux-elimination-research.md`, `qum-432-stripped-bracketed-paste-plan.md`,
`qum-462-live-verify.md`, `qum-619-idle-interrupt-race-2026-05-21.md`,
`qum-617-text-selection-2026-05-21.md` (the selection-mode toggle was built and *deliberately*
retired — CLAUDE.md § QUM-653/731), `qum-670-baseline.md`, `config-load-bug-merge-retire.md`,
`agent-wrapper-loop.md`, `parallel-agent-viewport-containers.md`, `viewport-yank.md`,
`design-notes/tab-cycling-audit.md`, `manager-wake-loss-2026-05-07.md`,
`messaging-delivery-architecture-2026-05-12.md` (the `send_async` doc-vs-reality bug — tool
deleted by QUM-550), `05-cross-platform.md` (its subject is `internal/tmux/`, which does not
exist), and the `.beads` half of `07-unknown-unknowns.md`.

Two residues noticed in passing, both trivial and neither worth an issue on its own:
`internal/worktree/worktree.go:87 SetupBeadsRedirect` is dead by construction now that no `.beads/`
exists (it no-ops), and `internal/merge/git.go:134` still *writes* a `.poke` file for which no
reader was found.

### Judged not to be obligations

- **`docs/design/hub/{02,05,06,08,12}`** — I checked the shipped hub code rather than treating
  unbuilt design as debt. Every security/correctness control these docs specify is *implemented*:
  bearer-token auth covering streaming handlers (`internal/hub/auth.go:26,48-51`), `/debug/state`
  gated default-off (`cmd/hubd/main.go:173`), `/healthz` dep-free vs `/readyz` gated on `Store.Ping`,
  and no hardcoded endpoint (`hub.ResolveHubURL`). The rest is design for unbuilt code, which the
  input audit already classifies as a labelling problem, not an obligation.
- **`_test_sleep` shipping in the production binary** (`qum-552-sandbox-transcript:97`) — env-gated
  at *both* the tool-list and the dispatch site on `SPRAWL_ENABLE_TEST_TOOLS`, which is the
  defence-in-depth the doc asked for.
- Assorted explicitly-deferred design options (`broadcast`, `messages_mark_unread`, `sprawl_tree`,
  `resume_mode`, an `ask_user_question` timeout, `OutcomeUserDismissed`, hub memory sync) — deferred
  *by the doc that proposed them*, which is a decision, not an unactioned obligation.
- **`messaging-overhaul.md` §8.5** — the claim that interrupt direction is "documentation, not
  enforcement" is **wrong**. `internal/supervisor/real.go:1827` calls `isAncestor(sprawlRoot, caller, to)`
  and returns `"send_message: %q is not an ancestor of %q (parent→descendants only per §8.5)"`.
  The gate is enforced in code. (Its *reachability* against weave is a separate filed issue,
  QUM-1046.) §8.6's back-pressure cap genuinely is absent — `internal/inboxprompt/inboxprompt.go:75`
  truncates the 80-byte *subject* only — but the doc rates it an improvement, not a defect, and I
  found no incident, so I am recording it here rather than on the list.

---

## 3. Coverage statement

**What I examined.** All **118** files I could identify as non-KEEP, computed as
`find docs -type f` (144 files at `3d92e2c`) minus a 26-file KEEP set I reconstructed from the
input audit's Appendix B verdicts (`LIVE`, `LIVE-PARTIAL`, `UPDATE`, `REWRITE`, `DESIGN-ONLY`).

**The 118/120 discrepancy is real and I did not paper over it.** The brief says 120. My
reconstruction yields 26 KEEP, not 24. The likely cause: the input audit's §5 routes the three
`qum-991/repro-*.sh` scripts and the two hub `evidence/*.txt` files to "moved out of `docs/`"
rather than to KEEP, so different readings of "non-KEEP" differ by a handful. **The two files I
may have wrongly excluded are somewhere in that five-file boundary set** — all of which are
executable scripts or raw test transcripts, i.e. the lowest-yield class in the corpus. I flag it
rather than asserting 120, because a count I cannot reproduce is exactly the enumeration rot this
audit exists to stop. Re-derive from the classification table, not from this paragraph.

**By what method.**

1. **Mechanical sweep, whole corpus.** Two `rg` passes over all of `docs/`: one for explicit
   severity markers (`CRITICAL`, `SEVERE`, `P0`, `P1`, `must fix`, `blocker`, `security …`,
   `vulnerab`, `path traversal`, `no validation`, `not yet implemented`, `known bug`, `unfixed`,
   `should be filed`), one for obligation language (`TODO`, `follow-up`, `next steps`,
   `open questions`, `we should`, `should be fixed/added/done`, `recommend`). The second pass
   returned 71 distinct files; every one of them is inside the read set below.
2. **Full reads.** All 90 prose files (`.md`) in the non-KEEP set were read **in full** — not
   skimmed, not sampled — across five parallel readers, each required to check every candidate
   against the tree with `rg`/`find` before reporting it.
3. **My own re-verification of every survivor.** No item reached §1 on a reader's word. For each
   I re-ran the check myself, read the actual code (not a name grep), constructed a positive
   control, and queried Linear.

**What I did not examine in full, and why.** The **28 raw-capture files** — 26 under
`docs/research/m13-phase1-evidence/` (tmux pane captures, one stderr log), the 2 hub
`evidence/qum-911/*.txt` (a `go test` transcript and a mutation-run record), and
`docs/research/tui-render-corruption-2026-04-22.txt` (a 45-line pane capture) — were **not read
line by line.** They were swept mechanically: `rg -ni 'TODO|FIXME|BUG|must fix|not implemented|
known bug|should'` over the whole directory returned **zero** hits. `ec7-weave-system-prompt.md`,
the one prose-shaped file in that directory, *was* read in full and contains no obligations
(it is a frozen copy of generated prompt output).

**This is the one place I sampled, and here is the honest risk.** A defect stated in a terminal
capture without any of those words — a visible stack trace, a wrong-looking rendered value — would
not be caught by that sweep. I judged that acceptable because the input audit establishes these
files have only 11 distinct contents across 26 files, and because a raw capture is evidence *for*
a finding rather than a finding itself. If you want that closed, it is a bounded 11-file read.

**Not examined by design:** the 26 KEEP files (out of scope — they are not being buried), and
`CLAUDE.md` (explicitly excluded by the brief; three-way contended).

**What I could not verify.** Two items are marked Medium/Low confidence *because* I could not run
anything: finding 8 (`searchOverlay`, mechanism read but not reproduced — the TUI needs a live
run), and `m13-phase1-validation-2026-04-22.md:242`'s four pre-existing `test-tui-e2e.sh` failures,
whose status is **unknown** — the script still exists, no issue references those failures, and
nothing in the tree records them as resolved. I did not run it (needs `claude` + tmux, out of
scope). It is not on the list because "unknown" is not "live and unfiled"; it is a loose end.

---

## 4. Verdict on the DELETE set specifically

**Widening the sweep to the DELETE set was justified, and it was not a close call. The delete set
was the higher-yield half.**

Three of the nine findings — and the two most actionable ones after the drain race — come from
files classified **DELETE**:

| finding | source doc | classification |
|---|---|---|
| 2 — no CI runs the test suite | `07-unknown-unknowns.md` | **DELETE** |
| 3 — `install.sh` skips checksum verification | `06-installer-distribution.md` | **DELETE** |
| 9 — NOTICE + SPDX never done | `01-licensing.md` | **DELETE** |
| 4 — trust model / SECURITY.md (half) | `07-unknown-unknowns.md` | **DELETE** |

The concentration has a cause worth naming, because it generalises past this cut.
`docs/research/open-source-readiness/` was classified DELETE largely on the strength of its
**false** claims — "No LICENSE file exists", "no release automation", "no `.github/`", "no
install.sh" — all of which are now wrong, and correctly so, because those items shipped. But the
classifier's reasoning ("this file asserts things that are no longer true, therefore it is dead")
does not distinguish *the checkboxes that got ticked* from *the ones in the same list that did
not*. **A readiness checklist rots into a DELETE verdict precisely because most of it succeeded,
and that verdict then discards the minority that failed.** Finding 2 sits four lines below
finding 9's already-done sibling in the same table.

That is a stronger argument for triage-before-cut than the ARCHIVE case the input audit made. An
archived doc can still be grepped; a deleted one leaves only `git log`, and nobody greps `git log`
for an unactioned P1.

Finding 3 is a different and sharper form of the same point, and it inverts the framing. That doc
does not contain an unactioned finding — it contains the **provenance** of a live defect in
`install.sh`. Deleting it removes the only record of where a security control's
skip-and-return-zero shape came from. The defect stays either way; only the explanation dies.

**The ARCHIVE set was not inert either** — findings 1, 5, 6, 7, 8 come from it, and finding 1 is
the most serious item in this report. But if you were forced to triage only one half, the
evidence at `3d92e2c` says triage the half you are about to make unrecoverable.

---

## Appendix — reflections

**Most surprising.** That the sweep's flagship example was backwards. The brief handed me a
CRITICAL security finding described as verified-unfixed, and it had been fixed within 48 hours of
being written, four months ago, by a ticket that is Done. What made it invisible was not
negligence but a **name**: the doc proposed `ValidateAgentName()`, the implementer wrote
`ValidateName()`, and the probe everyone reached for searched the proposal. Finding this took
about four minutes and it inverted the premise of the task. The general form is worse than a
missed fix — a doc's *proposed* identifier is the most natural thing to grep for and the least
likely to exist, so this failure mode is biased toward false alarm exactly where alarm is loudest.

**Second surprise, running the other way.** The corpus is in far better shape than the framing
suggested. I expected the archive to be full of buried bugs. Instead the dominant pattern is
recommendations that *shipped*, often under a different name, at a different layer, or via a
mechanism the doc did not anticipate — `paste-render-cadence`'s coalescing landed as
`internal/inputcoalesce/`; `mcp-hang-observability`'s unix-socket pprof shipped as an HTTP
listener with a SIGUSR2 toggle; the memory-perf doc's A–E all have closed or filed tickets. The
docs rotted because the *tree moved past them*, which is the healthy version of this problem. The
archive-safety risk turned out to be concentrated almost entirely in one subtree
(`open-source-readiness/`, a checklist whose successes obscured its failures) and in one
self-declared code comment.

**Open questions I could not close.**
(a) The four `test-tui-e2e.sh` failures from April are still unaccounted for — not fixed on the
record, not filed, not reproduced. That is the single loosest thread I am leaving.
(b) Whether finding 8's `searchOverlay` overflow actually reproduces; it needs a live TUI.
(c) Whether finding 1's concurrent duplicate has ever *occurred* in a real session. The wire logs
would settle it and I did not look — the code comment establishes the window exists, not that it
has been hit.
(d) The exact non-KEEP denominator (118 vs 120) — see §3.

**What I would do next, in order.** First, run the mechanical obligation-grep over
`.claude/skills/` and `CLAUDE.md`, which no audit in this restructure has swept and which are
paid for on every turn rather than only on a grep — the same class of buried obligation would be
far more expensive there. Second, grep the wire logs for a repeated `EntryID` on a child stdin
write, which would upgrade finding 1 from "the window exists" to "it has happened N times".
Third — and this is the one I would actually argue for — the misnamed-symbol failure that opened
this report is not a docs problem and will not be fixed by the docs cut. It is a *verification*
problem, and the referential-integrity check the input audit proposes in its §5 cannot catch it:
a doc citing a function that never existed under that name dangles at the *symbol* level, which
that check explicitly does not measure. The input audit's own "what I'd do next" flags symbol-level
dangling as unmeasured and suspected worse. This sweep is one data point that it is worse, and
that its failures are the expensive kind.
