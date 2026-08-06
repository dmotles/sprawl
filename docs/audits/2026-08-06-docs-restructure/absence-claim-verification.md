# Absence-claim verification — adversarial QA pass over the docs-restructure audit

**Author:** sentry (QA) · **Date:** 2026-08-06 · **Tree:** `3d92e2c`
**Method:** read-only. `git`, `grep`, `rg`, `find`, file reads. No builds, no tests, no e2e, no sandboxes.
**Remit:** falsify absence-claims only. Presence-claims and cut/keep judgements were out of scope and are not assessed here.

**Operating rule, applied to every conclusion below:**

> Before trusting a negative result, prove the probe can produce a positive one.

Every entry in §2 and §3 states its **positive control** — the run of the *same* probe against a case
known to exist, with its output. A conclusion here without a stated control is a bug in this document.

---

## 1. Verdict

| | count |
|---|---:|
| Absence-claims extracted across the six documents | **~210** (≈50 part A/B · ≈107 e2e + docs/ · ≈55 skills, after removing self-limitation and duplicate restatements) |
| Claims verified by hand | **62** |
| **Falsified — the claim is materially wrong** | **4** |
| **Materially true but literally wrong** (the audit's own §2 failure mode, recurring) | **5** |
| Confirmed absent, with control | **44** |
| Unverifiable as written (no symbol named) | **9** |

**Lead with the bad news — the four that are false:**

1. **§3.1's replacement claim reproduces the very error §3.1 withdraws.** "The entire genuine residue is
   `internal/runtimecfg/`" is the `drain.go` mistake again, one paragraph after diagnosing it.
   `runtimecfg`'s behaviour **is** tested — from `cmd/color_test.go`.
2. **`agent.ValidateName` is *not* reachable from every caller-supplied-name boundary.** `Real.Delegate`
   is one, and it does not call it. The QUM-1128 cancellation was right about the *finding*; the
   coverage assumption attached to it is wrong.
3. **The P3 withdrawal was not propagated.** D4 still instructs "triage the security finding before it
   moves," and the source audit (`docs-directory.md`) still asserts the withdrawn CRITICAL twice and
   says "Promote to Linear before archiving." A reader who acts on either re-files the canceled issue.
4. **A weave-memory claim relayed as fact is false:** `messages_list` *does* have a working unread
   filter. The parameter is `filter:"unread"`, not `unread_only`.

**The good news, and it is a real result:** the **"deleted — verified absent, do not grep for this"
lists hold up.** 21 of 22 spot-checked symbols have zero non-test code hits; the single hit is a
comment that itself says "retired." The audit's editorial rule — *documentation may safely say what is
gone; it may not say what is there* — survives adversarial checking. See §3.1.

---

## 2. Falsified claims

### F1 — "The entire genuine residue is `internal/runtimecfg/` (2 files)" — FALSE

| | |
|---|---|
| **Source** | `DECISION.md` §3.1 (forge). Explicitly flagged to me for verification. |
| **Claim** | After withdrawing P4, the only package in the tree with genuinely no test coverage is `internal/runtimecfg/`. |
| **What actually exists** | `runtimecfg`'s exported behaviour is exercised by **`cmd/color_test.go`**, through `cmd/color.go`. `TestColorSet_ByAlias` drives `FindAccentColor`'s alias path (`"cyan"` → `colour39`); `TestColorSet_Invalid` drives its not-found branch (asserts `unknown color`); `TestColorRotate_PersistsWithoutApplyingLiveTmux` drives `PickAccentColorExcluding` and asserts its actual contract (result `!= "colour39"`, the prior value); `TestColorList_MarksCurrent` iterates `runtimecfg.AccentColors`. `internal/agentops/spawn_test.go` and `internal/tui/app_resync_test.go` additionally consume `DefaultRootName` / `TreePathSeparator`. |
| **Residue after correction** | One symbol: `PickAccentColor` (the un-excluding variant) has no test I could find. That is a one-function gap, not a two-file package with no coverage. |
| **Evidence** | `grep -rln 'runtimecfg' --include='*_test.go' .` → `internal/agentops/spawn_test.go`, `cmd/color_test.go`, `internal/tui/app_resync_test.go`. Then `grep -n 'runtimecfg\.' cmd/color.go` → 5 call sites at :133 :151 :169 :186 :189. |
| **Positive control** | Same probe, package known to be tested: `grep -rln 'inboxprompt' --include='*_test.go' .` → `internal/inboxprompt/inboxprompt_test.go`, `inboxprompt_displaysubject_test.go`. The probe finds test references where they exist. |
| **Decision changed?** | Yes — this is the sentence that closes P4. The withdrawal of P4 stands; its **replacement claim does not**. |

**Why this matters more than the finding itself.** §3.1 diagnoses the failure as *"a countable proxy
(does a file with this name exist?) substituted for the property you care about (is this behaviour
tested?)"* — and then reaches for the same proxy one paragraph later. `runtimecfg` was selected by
"no `_test.go` file inside the package directory," which is the identical predicate that produced
"36 of 216." The correct predicate is a call-graph question, and it has a different answer.
**This is the third instance of this class in one document.**

### F2 — "`agent.ValidateName` covers every entry point" — GAP: `Real.Delegate` does not call it

| | |
|---|---|
| **Source** | `DECISION.md` §3.2 + the QUM-1128 cancellation. Forge explicitly asked me to test "the function exists" vs "every entry point calls it." |
| **Claim under test** | That `ValidateName` is reachable from every boundary where a caller-supplied agent name enters the system. |
| **What I found** | `ValidateName` is the **sole** name-validation implementation in the tree — it is not present in `internal/sprawlmcp/` or `internal/state/` at all. Of the `Real` methods that accept a caller-supplied `agentName`, **`Real.Delegate` (`internal/supervisor/real.go:577`) has no `ValidateName` call on any path.** The MCP layer does not compensate: `toolDelegate` takes `agent` / `agent_name` straight from JSON and passes it through `resolveAgentTarget`, which only picks between the two spellings and validates nothing. |
| **Consequence** | `Delegate` reaches `state.EnqueueTask` → `TasksDir` = `filepath.Join(sprawlRoot, ".sprawl", "agents", agentName, "tasks")` → `os.MkdirAll`. An unvalidated name is joined into a path and a directory is created. |
| **Honest severity** | **Hardening gap, not a live exploit.** `Delegate` opens with `state.LoadAgent`, which reads `agents/<name>.json` and returns early on failure, so a traversal name is rejected *incidentally* before reaching `EnqueueTask`. The defence is a side effect of a lookup, not a boundary check — it is exactly the kind of protection that a refactor removes without anyone noticing, and it is the only thing standing between `Delegate` and the path join. |
| **Evidence** | `grep -n 'ValidateName' internal/supervisor/real.go` → 7 sites: `:847` `:921` `:1045` `:1133` `:1725` `:1775` `:1984` (Kill, Pause, Wake, InduceTerminalFault, PeekActivity, SendMessage, Peek). `Delegate` at `:577` and its whole body to `:626` — absent. `grep -rn 'ValidateName' --include='*.go' internal/sprawlmcp internal/state` → zero. |
| **Positive control** | The identical probe **does** find the check where it exists — 7 hits in the same file, and `grep -rn 'ValidateName' --include='*.go' . \| grep -v _test` → 13 hits across 6 files. The probe is not blind. |
| **Also checked, and NOT a gap** | `Real.ReportStatus` also lacks `ValidateName`, but its name comes from `backendpkg.CallerIdentity(ctx)`, not from caller JSON — trusted identity, not a boundary. `Real.Spawn` allocates names internally and rejects unsupported types before any path join. `cmd/logs.go`, `agentops/{kill,retire,merge}.go` all validate. |
| **Decision changed?** | The QUM-1128 **cancellation is still correct** — the archived finding described "no validation anywhere," which is false. But the coverage claim attached to it should not be relied on. Recommend a follow-up to add `ValidateName` to `Delegate` (one line, same shape as `SendMessage` at `:1775`). |

### F3 — The P3 withdrawal was not propagated; two live artifacts still carry it

| | |
|---|---|
| **Source** | `DECISION.md` §5 **D4**, and `docs-directory.md` (cipher) at D:80 and D:320. |
| **What is still live** | (a) D4 reads: *"**Do not archive P3 first** — triage the security finding before it moves."* P3 was withdrawn in §3.2 of the same document; D4 is an actionable instruction predicated on a finding that no longer exists. (b) `docs-directory.md` — the *source* audit, unamended on the `cipher` branch — still states at D:80: *"`open-source-readiness/03-security-audit.md` filed a **CRITICAL: agent-name path traversal, no validation anywhere**. I verified: `rg 'func [Vv]alidateAgentName'` returns **zero hits**. That finding has been sitting unfixed in an unread archive for four months. … **This should be a Linear issue before anything is moved.**"* and repeats it in the Appendix-B row at D:320 as *"CONTAINS AN UNFIXED CRITICAL … Promote to Linear before archiving."* |
| **Why it is a finding** | §3.2's correction lives 60 lines above D4 and on a different branch from `docs-directory.md`. Anyone who reads the decisions section without the corrections section, or who opens the source audit, re-files QUM-1128. That is not hypothetical — it is how the issue got filed the first time. |
| **Evidence** | Read of `DECISION.md:146` and `cipher/…/docs-directory.md` at the quoted lines. |
| **Positive control** | Not a search-based claim — it is a read of text that is present. Control is the inverse: `grep -n 'WITHDRAWN' DECISION.md` → `:70`, `:71` only, i.e. the withdrawal markers exist in §3 and appear nowhere in §5. |
| **Decision changed?** | Yes — D4's sub-clause should be struck, and `docs-directory.md` needs the same withdrawal banner §3.1/§3.2 got. |

### F4 — "`messages_list` has no working `unread_only` filter" — FALSE

| | |
|---|---|
| **Source** | `skills-and-agent-behavior.md` (prism) §2, line 304 — relayed from weave's private memory. Prism explicitly recorded "no probe stated" for it. |
| **What actually exists** | `internal/sprawlmcp/tools.go:268-271`: `messages_list` takes a **`filter`** parameter, `enum: ["all", "unread", "read", "archived", "status"]`, default `"all"`. The tool's own description gives the worked example `{"filter":"unread","limit":20}`. |
| **The failure shape** | Textbook P3. The remembered claim names a parameter (`unread_only`) that does not exist, and concludes the *capability* is missing. The capability exists under a different name. This is the fifth instance of that class the audit has now produced, and the first one nobody probed at all. |
| **Evidence** | `grep -n -A12 'messages_list' internal/sprawlmcp/tools.go` → the `filter` property block. |
| **Positive control** | The same read of the same file surfaces `report_status`'s `state` enum at `:186`, proving the extraction reaches parameter definitions and would have shown `unread_only` had it been there. |
| **Decision changed?** | Affects **D6**, which proposes publishing weave's private memory to the durable layer. **Publishing it verbatim would ship this error to every agent.** The memory needs the same per-claim verification the tree got — prism's §2.3 already found one other false memory claim (`retire` `RemoveAll`), so the base rate is at least 2. |

---

## 2b. Materially true, literally wrong — the §2 pattern, still recurring

Not falsifications: each of these is *right about the behaviour* and *wrong as written*. They are listed
because the audit's own thesis is that this exact gap is where rot enters, and because a reader
reproducing the stated probe gets a different answer than the document reports.

| # | claim | as written | as measured | note |
|---|---|---|---|---|
| M1 | `internal/shlint` "absent from the working tree **and from all git history**" (`DECISION.md` §4(c)) | absent from both | The **path** never existed — `git log --all --diff-filter=A --name-only \| grep -i shlint` → empty. But the **string** is in the working tree at `.claude/skills/testing-practices/SKILL.md:163`, and `git log --all -S'shlint'` returns **5 commits** (`c5115ff`, `654ea0c`, `d680db1` + 2 audit commits). | Prism's original probe (`git log --all -- internal/shlint` → empty) is **precise and correct**. `DECISION.md` widened it into a claim about the string, which is false. A reader who reproduces the naive probe concludes the audit is wrong. |
| M2 | `TurnTimeout` "has zero occurrences" (`docs-directory.md` D:347) | zero | **1** — `internal/backend/session_perturnctx_test.go:12`, a comment. | No production referent; the sentence's point holds. |
| M3 | `readTurn` "was renamed `runReader`" (D:329) | gone | **10** hits, all test comments in `internal/backend/session_test.go` and `session_async_dispatch_test.go`. | Production symbol is gone; the identifier is very much alive in prose. |
| M4 | `selectionMode` toggle "retired" (D:346) | gone | **1** — `internal/tui/app_test.go:1903`, a comment. | Holds. |
| M5 | P5: "**5** orphan e2e rows" (`DECISION.md` §3) | 5 | **5 against the table**, **4 against all of `CLAUDE.md`**. `liveness-transitions` has no table row but *is* named in the surrounding prose (the partial-`SKIP:` paragraph). | The number is right under the intended reading; state the reading. Orphans: `ask-user-question-idle`, `attach-blocks`, `blurb-live-gate`, `liveness-transitions`, `qum903-false-thinking`. |

**Also resolved — an apparent conflict that is not one.** Scout writes `ForceInterruptDelivery` (E:74);
cipher writes `ForceInterruptForDelivery` (D:33, D:348). **Both spellings existed**, both were deleted by
QUM-821 in `a0ee58a`, and `CLAUDE.md:744` correctly names both (the handle-interface method and the
`UnifiedRuntime` method respectively). Neither auditor erred. `git log --all -S'<name>' -- '*.go'` for
each returns `a0ee58a`.

---

## 3. Confirmed absent

### 3.1 The "deleted — do not grep for this" lists: **the 100%-accurate verdict holds**

This was priority (c) and it is the most useful confirmable result in the pass.

**Probe.** For each symbol: `grep -rn --include='*.go' -w '<sym>' . | grep -v '_test.go'`.
**Symbols checked (22),** drawn from all three clusters CLAUDE.md labels as deleted — the
`viewport-resync` watchdog cluster, the `idle-continuation` auto-continue cluster, and the
`busy-queue-typing` / `notif-stacked-restart` retired-handler cluster:

`TurnWatchdogTickMsg` · `runTurnWatchdog` · `noteBusActivityIfApplicable` · `watchdogTimeoutDefault` ·
`SPRAWL_TUI_WATCHDOG_TIMEOUT_MS` · `SPRAWL_DEBUG_DROP_NEXT_TERMINAL_MSG` · `AutoContinueMsg` ·
`continuationPrompt` · `servicedTaskSet` · `ForceInterruptForDelivery` · `ForceInterruptDelivery` ·
`pendingTrigger` · `autonomousFrameHandler` · `SetQueuedCount` · `queuedCount` ·
`pendingQueuedIndicator` · `InterruptAndSend` · `SetPendingPreview` · `pendingPreview` ·
`pendingSubmit` · `syncQueuedIndicator` · `queuedUser`

**Result: 21 of 22 return zero non-test hits.** The single hit is `queuedUser` →
`internal/tui/app.go:323`, a comment reading *"QUM-833: the former queuedUser/queuedText maps are
retired."* — i.e. precisely the "deleted-context hits … were all comments or test-file prose" that
scout reported at E:94. **Scout's verdict is confirmed, not merely restated.**

**Positive control (the load-bearing part).** The identical probe against symbols known live:
`ValidateName` → 13 · `anyModalUp` → 9 · `drainPolicy` → 11 · `runDrain` → 4 non-test hits.
The probe finds live symbols in the same packages the deleted ones lived in. The zeros are real zeros.

### 3.2 Everything else confirmed, terse

Format: **claim** — evidence → *control*.

**From `DECISION.md` §3:**

- **P1 — researcher and QA receive no safety guidance.** Reproduced prism's matrix over
  `internal/agent/testdata/{researcher,qa,engineer,manager}_tui.golden`:
  `Executing actions` 0/0/1/1 · `# System` 0/0/1/1 · `Tone and style` 0/0/1/1 · `Destructive-var`
  0/0/1/1 · `not the only agent` 0/0/1/1 · `# Environment` 0/**1**/1/1.
  → *Control:* `report_status` in the same four files → 4/5/6/8. The goldens are readable and the
  probe finds strings in them. **P1 confirmed**, and prism's narrower claim that *researcher is the
  only role with no `# Environment` block* is confirmed too (QA has one; I have one).
  The destructive-var rule really does live only in Go: `internal/agent/prompt_child_sections.go:130`
  (engineer) and `:414` (manager); zero hits in `CLAUDE.md`. *(Caveat: it is not quite "nowhere else in
  the repo" — `.claude/skills/e2e-testing-sandboxing/SKILL.md:14,85,180` states an equivalent
  `rm -rf "$SPRAWL_ROOT"` prohibition with the 2026-04-21 incident that motivated it. The rule's
  general form is engineer/manager-only; a concrete instance is documented in one skill.)*
- **P2 — `DESCRIPTION.md` asserts a safety property the code lacks.** `grep -rn 'no more agents can be
  spawned' .` → one hit, `DESCRIPTION.md:29` itself. Behaviour probe, not string probe:
  `internal/agent/names.go:71` `AllocateName` falls through the pool into `for i := 1; ; i++` with no
  bound and no error return — it only `log.Printf`s a warning past `2*len(pool)`. There is no ceiling.
  → *Control:* the same grep locates the string in `DESCRIPTION.md`, proving it reaches tracked files.
  **Confirmed, and strengthened** — the earlier version rested on a string absence; the loop is the
  behavioural proof.
- **P5 — orphan e2e rows.** Confirmed at 5; see M5 for the reading. → *Control:* the same loop returns
  1–2 table hits for `handoff`, `usage`, `notif-stacked-restart`.
- **D5 — `linear-issues` is silently broken.** `.claude/skills/linear-issues/SKILL.md` documents
  `send_async` (:202), `send_interrupt` (:205, :210) and `message` (:211); `grep -c 'send_message'` on
  that file → **0**. `internal/sprawlmcp/tool_description_sync_test.go:70` bans the first two by name;
  `server_sendmessage_test.go:254` asserts `send_async` is absent from `tools/list`. → *Control:* the
  same recursive grep finds `send_message` in `.claude/skills/testing-practices/SKILL.md`, so it is
  not a scoping artifact. **Confirmed — highest-blast-radius live defect in the audit.**

**Paths and symbols (existence-checked; control = the same check on a sibling that exists):**

- Absent: `cmd/retire.go` · `cmd/spawn.go` · `cmd/messages.go` · `cmd/report.go` · `cmd/status.go` ·
  `cmd/kill.go` · `cmd/rootloop.go` (deleted by QUM-346, `git log --all` confirms it existed) ·
  `internal/tui/bridge.go` · `internal/tui/tuiadapter_test.go` · `internal/runtime/turnloop.go` ·
  `internal/tmux/` · `internal/tuichat/` · `docs/README.md`. → *Control:* `cmd/root.go`, `cmd/enter.go`
  present; `git log --all -- cmd/enter.go` returns commits.
- Zero Go hits (word-bounded, whole tree incl. tests): `messagesDeps` · `defaultRetireDeps` ·
  `defaultMessagesDeps` · `TestRetire_HappyPathDeletesState` · `TestMessagesSend_HappyPath` ·
  `NextTask` · `claudeMdExcludes`. → *Control:* same probe, `readTurn` → 10, `TurnTimeout` → 1,
  proving the probe does surface comment-only hits rather than silently missing them.
- `SPRAWL_TMUX_SOCKET` / `_stmux` absent from `.claude/skills/` — `grep -rn` over the directory → zero.
  → *Control:* `grep -rln 'sandbox' .claude/skills/` → 2 files. **Recon's #39 concern that the grep
  lacked `-r` is resolved: with `-r`, still zero.**
- `internal/agent/retire.go` contains no `not found` string (87 lines). **Extending query's finding:**
  the canonical error does exist, at `internal/agentops/retire.go:59` and `internal/supervisor/real.go:580`
  — a *different package*. CLAUDE.md's `internal/agent/retire.go:82` cite points at the `state.DeleteAgent`
  call, i.e. the cause. Query's reading is right and the correct cite is one directory over.
  → *Control:* the same `grep -rn 'not found'` returns 10 hits repo-wide.
- `docs/design/` has **only ever** contained `hub/` —
  `git log --all --diff-filter=A --name-only -- 'docs/design/*' | grep -v '^docs/design/hub/'` → empty.
  → *Control:* the identical probe on `docs/designs/*` returns `agent-teardown.md`,
  `agent-wrapper-loop.md`, `chatlist-invariants.md`, `merge-engine.md`. **D4's merge rationale holds.**
- `README.md` never links the `docs/` directory. My first probe (`grep -c 'docs/'`) returned **2**, both
  `docs.anthropic.com` URLs — a false positive I am recording because a less careful reader would have
  scored cipher wrong here. **Cipher confirmed.**
- No `--json` flag on any command (`grep -rn '"json"' cmd/*.go` → zero). → *Control:* `"no-validate"`
  and `"dry-run"` found in `cmd/merge.go:34,35`. `merge` carries only `-m` / `--no-validate` /
  `--dry-run` — no `--force`.
- `validate-popup` is **not** a member of `anyModalUp()` (`internal/tui/app.go:3153-3155`:
  `showHelp || showConfirm || showError || showQuestion || showUsage || showTree`), and the mouse-wheel
  gate at `:573` calls exactly `anyModalUp()`. Since `validatePopup` is a separate field (`:258`), the
  wheel is **not** suppressed while the validate popup is open — CLAUDE.md's sentence is wrong about
  behaviour, not just membership. `palette` was retired by QUM-864; no `showPalette` field exists.
  → *Control:* the same grep finds `validatePopup` at 12+ sites, so the field-name probe is not blind.
- Cipher's counter-claims against `open-source-readiness/` are all correct: `LICENSE`,
  `.goreleaser.yaml`, `install.sh`, `.github/workflows/release.yml` all **exist**, so those docs'
  absence-claims are the false ones.

**Upgraded from "unverifiable" to settled:**

- Prism §9 left open whether `tester` / `code-merger` in `ValidTypes` are reachable, saying it "needs
  the spawn path traced." **They are unreachable.** `internal/agentops/spawn.go:93` gates on
  `SupportedTypes` — `{engineer, researcher, manager, qa}` — immediately after `IsValidType`, and
  `internal/sprawlmcp/tools.go:77` restricts the MCP `type` enum to the same four. Both names are dead
  except as `NamePools` / `FallbackPrefix` keys (`internal/agent/names.go:41,42,51,52`).
- Prism §1.4's "prose forbids, code permits" for QA is **confirmed and applies to me**: my own RULES say
  *"Do NOT spawn sprawl children — you are a leaf verifier,"* while
  `internal/agentops/spawn.go` `AgentTypesAllowedToSpawnSubAgents` contains `"qa": true`. Under
  CLAUDE.md's own terminology a sub-agent *is* a sprawl-spawned child, so this is a genuine contradiction,
  not a vocabulary artifact. I complied with the prose.

---

## 4. Unverifiable

Nine of cipher's Appendix-B rows assert that something was deleted **without naming it**, so there is
nothing to probe. I am recording them as unsettled rather than guessing, because each is currently
carrying a DELETE recommendation on evidence a reader cannot check:

| doc row | the unnamed thing |
|---|---|
| `parallel-agent-viewport-containers.md` (D:281) | "the bug, the field, and the renderer were all deleted" — no field or renderer named |
| `viewport-yank.md` (D:288) | "every symbol gone" — no symbols listed |
| `m13-phase1-validation` (D:309) | "every CLI command it gates is deleted" — no commands listed |
| `manager-wake-loss-2026-05-07.md` (D:311) | "a race between two symbols that no longer exist" — neither named |
| `qum-462-live-verify.md` (D:337) | "a code path that no longer exists" — not named |
| `qum-570-startturn-caller-map.md` (D:341) | "sole production caller is a deleted file" — file not named |
| `tui-input-disappears-with-tall-tree.md` (D:363) | "a deleted env var" — not named |
| `tui-parity-audit` (D:364) | "a deleted command in a deleted mode" — neither named |
| `m4-manager-smoke-test.md` (D:369) | "every referenced test file is gone" — none listed |
| `agent-wrapper-loop.md` (D:277) | "banner links to a deleted file" — file not named |

Cipher self-declares at D:239 that the 55 archive-bound docs were **not** verified as rigorously as the
KEEP set, which is honest and correctly scopes these. They are fine as DELETE/ARCHIVE inputs; they are
not usable as evidence for anything else. Two further items I could not settle:

- **Cipher D:241** — "a sample suggested symbol-level dangling is materially worse than the 22% path
  rate." No sample size, no method. Unfalsifiable as written; the 22% path figure is unaffected.
- **Prism's private-memory claims generally.** I verified the two that name a checkable referent (§2.3's
  `RemoveAll` — prism's correction is right, the only `RemoveAll` in the retire path targets
  `…/logs` — and F4 above). The remaining memory items describe host-specific operational history I
  cannot reach read-only.

---

## 5. Coverage

**Extracted:** ~210 absence-claims across the six documents, via three independent full reads
(part A + B; e2e-matrix + docs-directory; skills-and-agent-behavior). The count excludes the ~15
self-limitation statements ("I did not measure X"), which are absence-of-evidence claims about the
auditors' own method rather than about the tree, and excludes duplicate restatements of the same claim
across sections.

**Checked by hand: 62**, allocated by the brief's priority order:

| priority | scope | checked |
|---|---|---:|
| (a) claims behind a filed issue or a §3 product defect | P1–P5 + both of forge's own corrections | 7 / 7 — **all** |
| (b) identifiers that look guessed from prose | the 8 flagged high-risk items in part A/B + 6 from prism | 14 / 14 — **all** |
| (c) "deleted, do not grep" lists | 22 symbols across all three clusters | 22 — **verdict confirmed** |
| (d) everything else | paths, flags, MCP verbs, skills, D4/D5 inputs | 19, chosen by blast radius |

**Skipped, and why:**

- **Every presence-claim** (the 315-of-399 symbol verification, the 22% dangling rate, the 14
  duplications, the row-fan-out measurements). Out of remit. Note the audit's own §2 conclusion is that
  presence-claims are the ones that rot — **so the surface I was pointed at is the surface least likely
  to be wrong, and the numbers driving D1/D2/D4 have had no adversarial pass at all.** See §6.
- **All cut/keep judgements** on the 144 `docs/` files.
- **The ~30 counted-quantity claims** in prism §6.1 ("115 assertions → 114", "8 scenarios → 9",
  "75 shell files → 72"). These are recount claims, not absence-claims, and re-deriving each needs the
  harness runs my constraints forbid.
- **Anything requiring a build, a test run, or an e2e row** — barred by the brief. In particular I could
  not execute a single e2e row, so every statement here about the matrix is a statement about *scripts
  and table text*, never about behaviour under test.

---

## 6. Reflections — gaps, residual risk, and what I would do next

**Gaps in the acceptance criteria as briefed.**

1. **"Absence-claim" is not a well-defined predicate, and the boundary is where the errors live.**
   Both P4 and F1 are absence-claims *about a proxy* ("no file named X") standing in for absence-claims
   *about a property* ("this behaviour is untested"). Under a strict reading, "36 of 216 files have no
   `_test.go`" is a **presence**-claim about a count and outside my remit; under the useful reading it
   is the single most damaging absence-claim in the audit. I used the useful reading. A future brief
   should say **"claims whose truth would be established by a search returning zero"** — that is the
   class the operating rule actually governs.
2. **Verifying corrections was in scope; verifying the *scope* of corrections was not stated.** F3 is
   not a wrong claim — it is a right claim that did not propagate. I found it only because I read D4
   after §3.2. Nothing in the brief asked me to check whether a withdrawal reached every place the
   finding appears, and that is where the residual QUM-1128 risk actually sits.
3. **No AC covers the audits' *presence*-claims, which is where the audit itself predicts the rot is.**
   §2 conclusion 2 says absence-claims are stable and presence-claims expire silently. My entire remit
   was the stable half. §3.1 confirms this asymmetry is real — 21 of 22 deleted-symbol claims held,
   while the presence-side census (`atomicDuration` 3→4, `needs_claude` 11→32) was 3× stale. **The
   headline numbers in D1/D2/D4 are unaudited.**

**Residual risks the work does not address.**

- **`Real.Delegate` is one refactor away from a real traversal** (F2). Its only defence is an early
  `LoadAgent` that exists for a different reason. One line fixes it.
- **D6 proposes publishing weave's private memory, and the memory has a measured error rate.** Two of
  the checkable claims in it are wrong (F4; prism's §2.3 `RemoveAll`). Publishing it as durable guidance
  ships those errors to every agent, with more authority than they have now.
- **QA is told it may not spawn; the code says it may.** Whichever is intended, one of them is wrong,
  and the prose side is the one with no test.
- **The e2e-matrix table's obligations were never executed.** I verified the table's *text* against
  the tree. Nobody in this audit ran a row. A row that is correctly named, correctly gated, and broken
  looks identical to a working one from here.
- **`liveness-transitions`, `wake-live` S3 and `pause-lifecycle` emit partial `SKIP:` lines and still
  report PASS** (CLAUDE.md's own QUM-970 note). Any future claim that the matrix "passed" needs those
  scanned, and a docs restructure that drops that paragraph removes the only warning.

**What I would check with more time, in order.**

1. **Re-derive the four headline numbers** — 22% dangling, 315/399 symbols, 14/14 duplications, the
   median-14-of-30 fan-out. These carry the decision and have had zero adversarial review. The fan-out
   number is the one I would attack first: it is computed from a glob matcher over a table whose glob
   rows CLAUDE.md itself warns are matched inconsistently, so the denominator is method-dependent.
2. **Audit the remaining `Real` methods and every `cmd/` entry point** for caller-supplied strings that
   reach `filepath.Join` — F2 came out of enumerating one function list; I did not sweep for the
   *class* (branch names, session IDs, message IDs are all joined into paths somewhere).
3. **Verify the rest of weave's private memory claim-by-claim** before D6 moves any of it.
4. **Diff each of the six audit branches against `main`** to catch other unpropagated corrections in the
   F3 shape — I found one by accident and did not look for more.
5. **Run the enumeration grep over the six audit documents themselves.** Forge's §7 recommends running
   it first on the *source*; nobody has run it on the *audit*. §3.1 and F1 are both instances of the
   pattern appearing inside the document that names it, which is the strongest available argument that
   the audit corpus should be subject to its own detector.

**One methodological note, offered because the brief invited it.** Three of my four falsifications came
from probing a *different property* than the document probed — not from probing more carefully. F1
asked "is the behaviour tested?" instead of "does the file exist?"; F2 asked "is the check reached?"
instead of "does the function exist?"; F4 asked "does the capability exist?" instead of "does this
parameter name exist?". The operating rule — *prove the probe can produce a positive* — is necessary
and, on this evidence, **not sufficient**: every one of those original probes could have produced a
positive, and each was pointed at the wrong noun. The companion rule is the one §3.1 already reaches
for and does not quite state as a procedure:

> **Name the property before you name the probe.** Write down the sentence you intend to publish, in
> terms of behaviour, *then* choose the search. If the search's subject and the sentence's subject are
> different nouns, the search cannot settle the sentence — however many controls it passes.
