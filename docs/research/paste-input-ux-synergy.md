# Paste-input UX synergy: lookahead debounce vs. backslash continuation

**Status:** research-only. Follow-up to QUM-430/432/449/451 and the prior
architectural diagnosis at `docs/research/paste-pipeline-architecture.md`.
**Investigated by:** ghost (research agent), 2026-05-04.
**Branch:** `dmotles/paste-input-ux-synergy-research`.

---

## TL;DR

The two candidates are **orthogonal** and should be shipped as **two separate
features**:

1. **Lookahead debounce on Enter (option "F")** — robustly fixes multi-line
   paste in environments that strip bracketed-paste markers. This is the only
   real fix for the actual failure mode dmotles is hitting. Pay 30–50 ms of
   submit latency only when the user actually presses Enter. **Ship as the
   primary fix.**

2. **Trailing-backslash line continuation** — a manual-typing UX convenience
   that lets a user pre-empt the Enter-submit by ending a line with `\`. It
   does **nothing** for paste (pasted text doesn't end lines with `\`), and
   carries a small but real cost: it claims the literal sequence `\<Enter>`
   for our app, which means it can change behavior for someone pasting code
   that happens to end a line with a literal backslash. **Ship as a polish
   feature, distinct from F.**

There is **no clever hybrid**. The backslash convention is a typed-character
UX; paste-stream classification is a timing problem. They live at different
layers and don't compose into a smarter solution.

If we have to ship only one, **ship F**. dmotles's reported failure
("Test 123\nAlso another test." splits into two submissions) is solved by F
and not by backslash, regardless of how the backslash convention is wired up.

---

## 1. What does Claude Code actually do with backslash?

Claude Code (Ink/TypeScript) supports four ways to enter a newline without
submitting (see [Interactive Mode reference][cc-interactive]):

1. **Backslash continuation**: end a line with `\`, press Enter → newline,
   no submit.
2. **Shift+Enter**: newline.
3. **Option+Enter** (macOS): newline.
4. **Ctrl+J**: newline.
5. **Paste mode**: multi-line paste auto-detected via bracketed paste.

The QUM-432 ticket description (which dmotles populated from a g2 of the
Claude Code source) cites the relevant call sites:

- `src/hooks/useTextInput.ts:247, :366` — `handleEnter` is the only place
  Enter→submit is decided. Plain Enter submits; Shift+Enter / Meta+Enter
  insert a newline.
- `src/hooks/useTextInput.ts:391, :485` — the **paste-fallback heuristic**:
  inside a single stdin chunk, embedded `\r` is treated as a pasted newline
  (insert), and a trailing lone `\r` is treated as a coalesced Enter
  (submit). This is **chunk-position based**, not timer based.
- `src/components/BaseTextInput.tsx:57` — defense-in-depth: while a paste is
  "in progress" (paste buffer non-empty within a 100 ms window),
  Enter-submit is suppressed unconditionally.

**The backslash convention is not part of paste handling at all.** It lives
purely in `handleEnter`: if `value.endsWith('\\')`, strip the trailing `\`
and replace with `\n` instead of submitting. It's a manual-typing UX
affordance for users who didn't reach for Shift+Enter / Meta+Enter — and
specifically a rescue path for terminals where Shift+Enter doesn't reach
the app cleanly (VS Code's integrated terminal, Tabby, WSL/PowerShell —
[#31904][cc-bug-31904], [#8056][cc-bug-8056]).

> **Verdict:** the backslash convention has zero interaction with paste in
> Claude Code. Pasted text doesn't end lines with `\`, so the convention
> never fires for paste. It is a parallel, orthogonal multi-line-typing
> affordance.

## 2. Does crush do backslash?

Yes. **Crush ships the same convention**, in
`internal/ui/model/ui.go:1823–1833` (crush@v0.64.0):

```go
case key.Matches(msg, m.keyMap.Editor.SendMessage):
    prevHeight := m.textarea.Height()
    value := m.textarea.Value()
    if before, ok := strings.CutSuffix(value, "\\"); ok {
        // If the last character is a backslash, remove it and add a newline.
        m.textarea.SetValue(before)
        if cmd := m.handleTextareaHeightChange(prevHeight); cmd != nil {
            cmds = append(cmds, cmd)
        }
        break
    }
    // Otherwise, send the message
    ...
```

(The comment says "remove it and add a newline" — in practice they only
remove the `\`, relying on the textarea having already inserted the `\n`
from the `Editor.Newline` binding for that key combo. Either way the user
ends up with the trailing-backslash → newline behavior on the SendMessage
key.)

**Crush still has zero stripped-bracketed-paste recovery code** (confirmed
in `paste-pipeline-architecture.md` §3 and re-checked: the only paste path
is the size-based attachment fork in `internal/ui/model/ui.go` `handlePasteMsg`
~ L3411). Crush relies entirely on bracketed paste working end-to-end and
gives the user backslash + Shift+Enter as manual multi-line affordances.
**No synergy between the two in their codebase.**

## 3. Other shell-inspired patterns

| Pattern | Fits our problem? | Notes |
|---|---|---|
| **Backslash line continuation** | Manual typing only | Works as polish; doesn't help paste. |
| **Heredoc `<<EOF` mode** | No | Demands the user remember a sentinel before typing/pasting. Worst-case for paste — user has to wrap the paste manually. Violates the "Enter-as-submit muscle memory" requirement. |
| **Readline `M-Enter` / `Ctrl+_`** | Already present | We already bind Shift+Enter for newline. Bash binds `Ctrl+v Ctrl+j` (literal LF) for the same purpose. Adding Ctrl+J as an alias is cheap and free of side effects. |
| **Vim-style command modes** | No | Adds modal complexity for a single use-case. dmotles wants the input bar to feel like a chat box, not vim. |
| **Bash multi-line via unclosed quote / paren** | No | Implicit. Works in shells because grammar tells you when input is "complete". A free-form chat input has no grammar to lean on. |
| **Trailing `\` continuation** (covered above) | Polish only | Best of the manual-typing options because it's a minimal one-character convention with no chord. |

The only patterns that have any chance of helping paste are the ones that
either (a) wrap paste in explicit markers (= bracketed paste, which is the
problem we already can't rely on), or (b) classify the **stream timing or
shape** of the bytes as paste-vs-typing (= F).

## 4. Synergy analysis

Can the backslash convention extend to handle paste? **No, and here's the
exhaustive argument:**

- **Pasted text does not end lines with `\`.** Backslash-continuation only
  fires on `value.endsWith("\\") && Enter`. A paste like
  `"Test 123\nAlso another test."` contains no backslashes at all; the
  convention is silent for it.
- **Synthesizing trailing `\`** before each pasted `\n` would require us to
  *already know* we're inside a paste — which is the very problem we're
  trying to solve. Circular.
- **Asking the user to wrap a paste in `\`** (e.g. type `\` then paste then
  Enter) defeats Enter-as-submit muscle memory and requires two manual
  actions for what should be a copy-paste-Enter.
- **Treating any embedded `\` as "this might be a paste"** is not even close
  to a reliable signal — most pastes contain zero backslashes, most lines
  with backslashes (Windows paths, escape sequences in code) are typed
  manually.

The backslash convention solves one problem: "user is composing a multi-line
message manually and Shift+Enter doesn't reach our app cleanly in their
terminal." That problem is independent from paste classification, and the
fix shape is independent (textarea-value suffix check vs. inter-key timing
window).

**Recommendation:** treat them as orthogonal. Ship F as the bug fix, ship
backslash as a separate polish PR.

## 5. Edge cases for F (lookahead debounce on Enter)

The F design: when an `Enter` `KeyPressMsg` arrives, **defer** the
submit-vs-newline decision for `pasteLookaheadWindow` (recommend
**40 ms**). If another `KeyPressMsg` arrives in that window → the Enter
was embedded in a paste; insert `\n` and process the next key normally.
If the window elapses with no further key → it was a real submit.

Implementation sketch (replaces the `pasteUntil`/`lastKeyAt` block in
`internal/tui/input.go:79–110`):

```go
// On Enter: schedule a Tick; do NOT submit yet. Stash the textarea value
// (or just rely on it being preserved across messages because we don't
// mutate it here). On either:
//   - a follow-up KeyPressMsg arriving before the deadline → embedded;
//     insert "\n" into the textarea, drop the pending submit.
//   - the pasteLookaheadMsg{seq} arriving with the still-current seq →
//     real submit; emit SubmitMsg.
// Use a monotonically-increasing seq counter so a stale Tick from a
// previous Enter that was reclassified as embedded doesn't fire a submit.
```

### 5.1 Trailing `\r` on a paste

If the paste's last byte is `\r` (i.e. the user copied content that ended
with a newline), F's behavior is: Enter arrives → 40 ms quiet window
elapses → submits. **This is the right behavior** in 99% of cases: the user
copied "complete" content and pressing Enter to dispatch it is the
natural action. The 1% edge case is a user who copied content with a
trailing newline and wanted to keep typing afterwards — they get a
premature submit, recoverable via Up-arrow history. Acceptable.

### 5.2 Deliberate Enter inside a fast paste

If the user genuinely typed Enter within 40 ms of a pasted character, F
will reclassify it as embedded and insert `\n`. **This is unreachable in
practice**: human inter-keystroke times bottom out around 80–100 ms even
for skilled typists, and 40 ms is well below that. The only way to land
inside the window is to be inside an actual paste burst.

### 5.3 Paste with a deliberate intentional newline followed by a long pause

If a paste pauses for >40 ms mid-stream (e.g. terminal flow-control hiccup),
F will misclassify the Enter as a submit and dispatch the partial paste.
This is the same failure mode QUM-432's 10 ms window has, just with a more
generous threshold. Mitigations:

- **40 ms is conservative.** The QUM-432 cliff at 10 ms misfired in
  dmotles's env because the `Test 123\nAlso another test.` pause exceeded
  10 ms. Even a single browser-terminal stall rarely exceeds 40 ms once
  the paste burst has started — empirically, paste inter-byte gaps in
  stripped-bracketed-paste environments cluster <30 ms and human inter-
  keystroke gaps cluster >80 ms. The 40–80 ms zone is mostly empty.
- **A 50 ms window** is also fine; we can pick the upper end of dmotles's
  suggested 30–50 ms range to err on the side of catching more pastes.
- **The visible failure mode is rare and recoverable** (Up-arrow brings
  back the partial input).

### 5.4 Interaction with QUM-432's existing burst window

**F replaces QUM-432 entirely.** Specifically:

- The `lastKeyAt` / `pasteBurstWindow` (10 ms backward look) becomes
  redundant: F's forward look (40 ms after Enter) catches the same cases
  more reliably because it doesn't depend on the timing of the byte
  *before* Enter.
- The `pasteUntil` / `pasteQuietWindow` (50 ms forward look from any
  in-burst printable) was QUM-432's attempt at sticky paste mode. With F,
  the forward-look-on-Enter is the only window we need — pre-Enter
  stickiness adds no information, because if a paste delivers the bytes
  that arrive before Enter, those bytes already insert correctly via the
  normal textarea path.
- Net code change: delete `pasteBurstWindow`, `pasteQuietWindow`,
  `lastKeyAt`, `pasteUntil`, and the timing logic in lines `79–110`. Add
  a `pendingEnter` token + a `pasteLookaheadMsg` handler.

This is a **net simplification**: F is one timer instead of two windows
and a dead-reckoning on past keystrokes.

### 5.5 Latency cost on every submit

The 40 ms cost is a real perceived-latency tax on every Enter submit.
Whether this is noticeable depends on render budget — a Bubble Tea frame
at 60 fps is 16 ms, so a 40 ms wait is ~2.5 frames. For comparison,
typical TUI input-to-render latency over SSH is already ~50–100 ms; a
40 ms additional gate is at the edge of perception but likely below it
for most users.

If we want to **avoid the latency for typed Enter** (vs. paste-Enter),
the only real escape hatch is "Enter is preceded by a printable in the
last X ms" — i.e. the QUM-432 logic, used as a *fast path* before the
deferred decision. Hybrid:

```go
// Fast path: if the previous KeyPressMsg's text was empty (control key)
// or arrived >100 ms ago, this Enter is almost certainly a real submit
// — don't bother deferring.
if !looksLikePasteContext(now) {
    submit(); return
}
// Slow path: defer 40 ms.
schedulePasteLookahead()
```

I'd defer this optimization until after F ships and we measure whether
the 40 ms is actually felt. KISS first.

## 6. Bubble Tea v2 escape hatches we haven't considered

Re-checked against `paste-pipeline-architecture.md` §2. **Nothing new.**

- Bracketed paste, Kitty keyboard, and `modifyOtherKeys` are all enabled
  by default in Bubble Tea v2 (`cursed_renderer.go:134–139, :175`). None
  of them carries a paste signal beyond CSI 200~/201~, which is what gets
  stripped in dmotles's env in the first place.
- `tea.WithFilter` could synthesize a `PasteMsg` from a burst of
  `KeyPressMsg`s, but it has the same heuristic problem as QUM-449 and
  loses key context across panels (rejected as Option D in the prior
  research).
- **Custom message types**: we can introduce our own (`pasteLookaheadMsg`,
  in F's design), but they're not a *signal* from the framework — they're
  a tool we use to schedule our own deferred decisions.
- **No upstream change between v2.0.3 and v2.0.6** affects paste; bumping
  Bubble Tea won't help.

The signal we wish we had — "the last K bytes from the PTY arrived within
single read() syscall" — is only available pre-decode, before the
ultraviolet event scanner splits bytes into `KeyPressMsg`s. Plumbing it
through would mean forking ultraviolet, which is dramatically out of
scope for "fix multi-line paste."

## 7. Recommendation

**Ship two issues:**

### Issue 1 (primary fix, Bug, High): Lookahead debounce on Enter for stripped-bracketed-paste recovery

Replace QUM-432's pre-Enter timing window with a post-Enter 40 ms lookahead
debounce. When Enter arrives, schedule a `tea.Tick(40ms, pasteLookaheadMsg{seq})`
and stash the seq. If another `KeyPressMsg` arrives before that Tick fires,
treat the Enter as embedded (insert `\n`, bump seq so the stale Tick is
ignored) and process the new key normally. If the Tick fires with the
matching seq → real submit; emit `SubmitMsg`.

**Code locus:** `internal/tui/input.go` — replace the `pasteBurstWindow`
/ `pasteQuietWindow` / `lastKeyAt` / `pasteUntil` machinery (lines 23–48
and 79–110) with `pendingEnter` + `pendingEnterSeq` fields and a
`pasteLookaheadMsg` case in `Update`.

**Tests:**
- Existing QUM-432 unit tests are repurposed: synthesize a KeyPressMsg
  stream (`T`, `e`, `s`, `t`, ` `, `1`, `2`, `3`, `Enter`, `A`, `l`, `s`,
  `o`, …), advance fake clock 0 ms, fire `pasteLookaheadMsg` only after
  the trailing `.` settles → assert one `SubmitMsg` containing both lines.
- Real-Enter test: type `hello`, press Enter, advance fake clock 41 ms,
  fire `pasteLookaheadMsg` → assert `SubmitMsg("hello")`.
- Embedded-Enter test: type `a`, press Enter at t=0, type `b` at t=20 ms
  → assert no submit, textarea value is `"a\nb"`, the t=40 ms Tick is a
  no-op (seq mismatch).
- **Mandatory live e2e** in dmotles's env: paste `"Test 123\nAlso another
  test."`, confirm one `SubmitMsg` with both lines.

**Estimate:** 0.5–1 day. **Risk:** low. The state machine is small, the
test surface is the same as QUM-432's (already in place), and the failure
mode (premature submit) is the same one we're already shipping with — so
worst-case we're at parity.

### Issue 2 (polish, Improvement, Medium): Trailing-backslash line continuation

In `InputModel.Update` Enter branch, before classifying as submit-or-embedded,
check `strings.HasSuffix(m.ta.Value(), "\\")`. If true, drop the trailing
`\` and insert `\n` instead. Match Claude Code / crush behavior.

**Code locus:** `internal/tui/input.go:83–91`.

**Tests:** unit test in `input_test.go`: set value to `"foo\\"`, fire
Enter KeyPressMsg, assert no submit and value becomes `"foo\n"`. Confirm
that Shift+Enter still works for the no-trailing-backslash case.

**Estimate:** 0.25 day. **Risk:** very low. Pure additive, gated on a
specific suffix.

**Sequencing:** these are independent. Ship F first because it's the bug
fix; backslash polish can land in the same week or later.

---

## 8. Reflection — what surprised me, what's open

**Surprises:**

- **Crush has the backslash convention too** (line 1826 of `ui.go`). I'd
  assumed it was Claude Code-specific. Charmbracelet has independently
  converged on it as the multi-line-typing affordance for terminals where
  Shift+Enter is unreliable. That's a stronger signal to adopt it than I
  expected going in.
- **The "synergy" thesis falls apart cleanly.** I started this
  investigation assuming there might be some clever wiring where the
  backslash convention gives us a second classification signal for paste.
  The exhaustive argument in §4 is short because the answer is short:
  pasted text doesn't end lines with `\`. The two features just don't
  share a substrate.
- **F's edge cases are mostly non-issues.** I expected the latency cost
  on every submit to be a hard tradeoff; on closer thought, 40 ms is
  below the noise floor of typical SSH/tmux input latency, and the
  failure mode for a paste-with-pauses is the same as today's failure
  mode (rare, recoverable via history). The "F vs. don't-fix" decision
  is overwhelmingly in F's favor.

**Open questions / what I'd do next with more time:**

1. **Measure dmotles's actual paste cadence.** A small instrumentation
   patch (log inter-byte deltas during paste in the dev container's
   browser terminal) would tell us whether 30, 40, or 50 ms is the right
   constant. I picked 40 ms by feel; an empirical p99 for paste gaps
   would let us pick a defensible number with less guesswork.
2. **Profile the latency cost on real submits.** A small TUI benchmark
   that measures wall-time from KeyPressMsg{Enter} to SubmitMsg-handled
   pre- and post-F would confirm the 40 ms is actually 40 ms and not
   "40 ms + a bunch of unrelated render churn that happens to cluster
   there." If the cost is materially worse than 40 ms, the §5.5 fast
   path becomes worth implementing immediately.
3. **Audit other terminals.** I focused on dmotles's code-server browser
   terminal because that's the reproduction. Worth a half-hour of
   testing whether the same paste-without-markers behavior happens in
   Tabby, VS Code's integrated terminal, mosh, and Windows Terminal —
   any of which might have idiosyncrasies F should accommodate.
4. **Revisit QUM-432's classifier deletion** once F lands. The §5.4
   net-simplification only happens if we actually delete the old code;
   leaving it in alongside F would give us two competing classifiers,
   which is the anti-pattern the prior architecture review flagged.

---

## References

- `internal/tui/input.go:23–110` — current QUM-432 classifier.
- `internal/tui/app.go:331–349` — `tea.PasteMsg` handler (QUM-430,
  works when bracketed paste reaches us).
- `docs/research/paste-pipeline-architecture.md` — prior diagnosis;
  rejects QUM-449 buffer-and-flush as an anti-pattern, recommends
  perf optimization (shipped as QUM-451) + environment docs.
- `internal/ui/model/ui.go:1823–1833` (charmbracelet/crush@v0.64.0) —
  trailing-backslash convention reference implementation.
- [Claude Code Interactive Mode reference][cc-interactive] — multi-line
  input methods.
- [#31904][cc-bug-31904], [#8056][cc-bug-8056] — Claude Code GitHub
  issues showing why backslash exists as a fallback for terminals where
  Shift+Enter is mangled.

[cc-interactive]: https://claudefa.st/blog/guide/mechanics/interactive-mode
[cc-bug-31904]: https://github.com/anthropics/claude-code/issues/31904
[cc-bug-8056]: https://github.com/anthropics/claude-code/issues/8056
