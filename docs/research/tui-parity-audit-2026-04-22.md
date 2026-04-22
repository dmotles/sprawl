# TUI vs tmux parity audit — 2026-04-22

Author: ghost (researcher agent, commissioned by weave)
Scope: decide whether `sprawl enter` (TUI/bubbletea mode) is ready to replace
`sprawl init` (tmux + send-keys + legacy messaging) as the daily driver, and
produce a punchlist of what still blocks cutover.

## (a) Executive summary

**Recommendation: NOT READY. Close with a small, focused punchlist.**

The two modes share the same session-init, resume/handoff, and child-agent
queue plumbing (all of `internal/agentloop/`, `internal/rootinit/`,
`internal/messages/`, `internal/claude/` is shared). Baseline launch, render,
and child-agent lifecycle work in TUI mode. Where TUI mode falls short is in
the **root-directed notification path**: when a child sends a message to weave
(e.g. `sprawl report done`), tmux mode pokes the weave pane via `tmux
send-keys`; TUI mode has no equivalent signal wired into the bubbletea event
loop, so weave is silent until the user explicitly switches to the child in
the tree and reads the unread count. This is *the* blocker — without it the
coordinator can't notice child completions without polling.

There are also a small number of UX gaps (no mouse/selection polish for some
terminals, no inbox indicator on the weave root node, FinalizeHandoff blocks
the main goroutine) that are known and individually tracked.

Given that the notification gap is a single, well-scoped problem (and matches
the "weave root-loop doesn't use flushQueue" note in the session timeline),
TUI cutover is **one to three focused tickets away**, not a rearchitecture.

### One-paragraph verdict for the impatient

TUI mode boots, renders, resumes, handoffs, and orchestrates children. It
does not alert weave when children write to its maildir. Fix that plus the
few issues already filed (QUM-205 unread counter on the weave root node,
QUM-235 identity/prompt, QUM-260 FinalizeHandoff off main goroutine), and
TUI is ready to be the daily driver.

## (b) What works in TUI mode

Validated by code review + a minimal manual smoke test (launched
`sprawl enter` under tmux with a 160×45 pane, captured state):

| Area | Status | Evidence |
|---|---|---|
| Launch, alt-screen render, panel layout | ✅ | manual capture below; `cmd/enter.go:439`, `internal/tui/app.go:499` |
| Weave as root in agent tree | ✅ | `internal/tui/app.go:596` (`PrependWeaveRoot`) — **shipped, supersedes QUM-205 title** (its unread-counter subpoint is still open; see gaps) |
| Session-id display, resume-by-default, transcript replay on resume | ✅ | `cmd/enter.go:441-453`, `internal/tui/replay.go` |
| Resume-failure fallback (QUM-261) | ✅ | `cmd/enter.go:279-286,411-470`, `internal/claude/resumewatch.go`; symmetric with tmux path in `cmd/rootloop.go:151` |
| Ctrl+C confirm dialog, clean shutdown without killing children | ✅ | `internal/tui/app.go:160-177,535-554` |
| Ctrl+N / Ctrl+P agent cycling + `/switch` fuzzy palette | ✅ | `internal/tui/app.go:209-217`, `internal/tui/palette.go`, `commands/registry.go` |
| Tab / Shift-Tab panel cycle, F1 / `?` help | ✅ | `internal/tui/app.go:180-184,218-227` |
| Visual-select / yank (v/j/k/g/G/y) → OSC 52 clipboard | ✅ (functional) | `internal/tui/app.go:231-235,667-714`; caveat: QUM-281 still notes raw-markdown copy + requires `tmux set-clipboard on` (QUM-302) |
| Mouse scroll wheel on viewport | ✅ | `internal/tui/app.go:146-158,574-576` |
| Activity panel (wide terminals) | ✅ (wired) | `internal/tui/app.go:770-791` polls `sup.PeekActivity` every 2s |
| `/handoff` palette command → signal → FinalizeHandoff → consolidate → fresh session | ✅ (functional, with UX caveat) | `commands/handoff.go`, `app.go:356-363,379-415`, `cmd/enter.go:83-87` |
| MCP sprawl-ops tools (spawn/status/delegate/message/merge/retire/kill/handoff) available to weave | ✅ | `cmd/enter.go:166-180,314-329`, `internal/sprawlmcp/` |
| Stderr redirect to log file so subprocess noise doesn't corrupt screen (QUM-304) | ✅ | `cmd/enter.go:495-516`, `internal/tui/stderr_redirect.go` |
| Single-weave flock | ✅ | `cmd/enter.go:384-389` |
| Error dialog on session crash + `r` to restart | ✅ | `internal/tui/app.go:331-354,379-415` |

## (c) What's broken / missing / worse than tmux mode

Severity: **S1** blocks cutover; **S2** visible regression but tolerable; **S3** polish.

### S1-1. No child→weave notification surfaces in TUI

**Symptom.** When any child invokes `sprawl report done`, `sprawl messages
send weave …`, or any MCP `sprawl_send_async` targeting weave, the message
lands in `~/.sprawl/messages/weave/new/…`, the `weave.wake` sentinel is
written, and **nothing in the bubbletea UI changes**. The status bar,
viewport, and tree are all silent. The user has no passive signal that a
child needs attention.

**Root cause.** The process-level notifier is registered once in
`cmd/root.go:26-37`:

```go
messages.SetDefaultNotifier(buildLegacyRootNotifier(os.Getenv, runner, sprawlRoot))
```

and `buildLegacyRootNotifier` (`cmd/messages_notify.go:32-54`) gates on
`SPRAWL_MESSAGING == "legacy"` and then does `tmux send-keys` to the root
tmux window. `sprawl init` explicitly sets `SPRAWL_MESSAGING=legacy`
(`cmd/init.go:161,212`); `sprawl enter` intentionally does **not** set it
(queue path is the default post-QUM-292/293/295). So in TUI mode the
notifier is a silent no-op — which is correct for legacy send-keys, but
nothing replaces it for the bubbletea UI.

The symmetric gap on the consumer side: neither `cmd/rootloop.go`'s tmux
weave nor `cmd/enter.go`'s TUI-mode weave runs `internal/agentloop` — so the
queue drain (`cmd/agentloop.go:749-1019`, step 2a) that *would* re-inject a
queued message as a fresh turn is never invoked for the root. This is the
"weave root-loop doesn't use flushQueue" gap called out in the session
timeline, and it is real.

**Observable proof.** Launched `sprawl enter` against a fresh sandbox,
simulated a child with `SPRAWL_AGENT_IDENTITY=pretend-child sprawl messages
send weave "test" "hello"`. The message landed in the maildir
(`.sprawl/messages/weave/new/…`), the `weave.wake` file appeared — the TUI
pane was unchanged.

**Severity: S1.** This is the single biggest blocker. A coordinator UI that
doesn't tell you "your child is done" is not a usable coordinator UI.

**Fix sketch.** Options, in order of KISS:

1. A TUI-aware notifier. At TUI startup, *replace* the process-level default
   notifier with one that sends a new `InboxArrivalMsg` via the `tea.Program`
   sender already passed to `onStart` (`cmd/enter.go:458-493`). The palette
   and handoff channels already use this pattern. The notifier would fire
   for any `to == rootName` message and the UI would append a status-line
   banner + bump a new "unread" counter on the weave root tree node.
2. Add an inbox-poll tea.Cmd, similar to `tickAgentsCmd`, that calls
   `messages.List(sprawlRoot, "weave", "unread")` every ~2s and emits when
   the count rises. Cheapest to implement but laggier than option 1.
3. The full "root runs agentloop" route (genuine flushQueue for weave).
   Correct architecturally but much bigger: weave in TUI mode is driven by
   `host.Session` + stream-json, not by the agentloop. Out of scope for
   cutover.

Recommendation: do option 1 now, revisit 3 as part of the broader messaging
overhaul (QUM-292 family).

Files to touch: `cmd/enter.go` (replace/augment notifier before `runProgram`),
`internal/tui/messages.go` (new msg type), `internal/tui/app.go` (handler +
status banner + unread bump), `internal/tui/tree.go`
(`PrependWeaveRoot` needs an unread count parameter).

---

### S1-2. Weave root node shows no unread count

**Symptom.** `PrependWeaveRoot` in `internal/tui/tree.go:162` synthesises
the weave row with hardcoded `Unread: 0`; only children in `buildTreeNodes`
pull `unread[a.Name]`. So even once you install the inbox poller/notifier
from S1-1, the tree still doesn't mark weave as having unread mail.

**Severity: S1** (required for S1-1's UX to actually work).

**Fix.** Thread `rootUnread int` through `PrependWeaveRoot` and
`rebuildTree`. Re-poll via `tickAgentsCmd` or merge into the new notifier
path. Matches the open Linear issue QUM-205 in spirit (that issue's title
"show weave as root node" is already done, but its unread/turn-state
subpoint is not).

---

### S2-1. Identity & prompt drift in TUI mode (QUM-235)

Known issue. Weave introduces itself as "Claude", says it has no identity,
and its system prompt still leads with tmux-mode CLI guidance (`sprawl
spawn …`) rather than MCP tools (`sprawl_spawn`, …). Doesn't corrupt
function but degrades the agent's own behavior in TUI mode: it'll try CLI
escape hatches first. See QUM-235 for full remediation plan. Notable that
`buildSessionEnv` in `cmd/enter.go:216-221` *does* now set
`SPRAWL_AGENT_IDENTITY=weave`, so the env-var half is fixed; the prompt
rewrite in `internal/agent/prompt_mode.go` `applyRootTUIMode` still
appends rather than replaces the tmux KEY COMMANDS section.

Severity **S2**: visible but users can paper over it.

---

### S2-2. FinalizeHandoff freezes the UI (QUM-260)

Handoff path is synchronous in the bubbletea Update loop
(`cmd/enter.go:83-87` inside `makeRestartFunc`, called from
`internal/tui/app.go:390`). Consolidation + persistent-knowledge update
spawns a child `claude` and can take 30+ seconds, during which the UI is
frozen — no input, no spinner, no banner updates. Tmux mode has the same
wall-clock wait but the bash restart loop keeps echoing progress to the
pane so it *feels* less dead.

Severity **S2**: UX regression vs tmux, not a correctness bug.

---

### S3-1. Activity panel only visible on wide terminals, no protocol-stream

`ComputeLayout` only renders the activity panel when there's enough width
(>= a threshold — see `internal/tui/layout.go`). On 120-col terminals it's
hidden. Content is the supervisor's activity ring, not a live protocol
stream from the observed agent — fine for monitoring, but easy to miss
that it exists.

Severity **S3**: nice-to-have; track if users complain.

---

### S3-2. Subprocess stderr can still sneak onto the screen in rare paths

`cmd/enter.go:495-516` redirects stderr to a log file, which is the QUM-304
fix. There's still a Linear issue on file for "subprocess stderr bleeds
into status bar / corrupts layout" in certain sequences (disable via
`SPRAWL_TUI_NO_STDERR_REDIRECT`). Low occurrence now but worth keeping on
the radar.

Severity **S3**.

---

### S3-3. Clipboard yank copies raw markdown + requires tmux `set-clipboard on`

QUM-281 / QUM-302. `y` in visual-select mode copies the raw markdown
including glamour escape sequences stripped, which is arguably correct but
some users expect plain text. OSC 52 requires terminal buy-in.

Severity **S3**.

## (d) Recommended punchlist of Linear issues to file

DO NOT FILE — for weave to review.

1. **[P1] TUI: notify bubbletea UI when weave receives a message.** Replace
   the process-level notifier in `cmd/enter.go` with one that sends an
   `InboxArrivalMsg` via the `tea.Program` sender. Add a status-line banner
   ("inbox: new message from <child>") and wire into (2). Covers the
   "weave root-loop doesn't use flushQueue" note. Success criteria: run the
   smoke test from §c-S1-1 and see a visible banner + unread bump within
   1s of `sprawl report done`.

2. **[P1] TUI: show unread count on the weave root node in the agent tree.**
   Thread `rootUnread` through `PrependWeaveRoot`; populate via the
   `tickAgentsCmd` path already polling `messages.List`. Complements (1);
   attach to QUM-205 as a sub-task.

3. **[P2] TUI mode e2e test: child→weave notification.** Analogue of
   `scripts/test-notify-e2e.sh` but against `sprawl enter`. Use the TUI
   e2e harness (`scripts/test-tui-e2e.sh`) to assert the banner/unread
   signal appears after `sprawl report done` from a simulated child. Prevents
   regressions once (1) lands.

(Existing issues that also block cutover but are already filed: **QUM-235**
identity/prompt, **QUM-260** FinalizeHandoff off-main-goroutine. No new
issue needed; just surface them on the cutover checklist.)

Optional follow-ups not required for cutover: a root-level `agentloop`
flushQueue (true architectural fix; lives in the QUM-292 messaging overhaul
umbrella — QUM-303 tracks the adjacent interrupt-and-resume piece).

## (e) Non-obvious things worth knowing

- **`SPRAWL_MESSAGING=legacy` is the only thing keeping tmux mode's
  notification path alive.** Every sprawl CLI invocation installs the same
  notifier; it simply no-ops outside legacy mode (`cmd/messages_notify.go:33`).
  That's a neat design — but it also means TUI mode silently relies on a
  notifier that was never implemented. Easy to miss reading the code.

- **`cmd/init.go` propagates `SPRAWL_MESSAGING=legacy` into the tmux session
  env**, so children running in tmux-init mode also take the legacy path.
  Children spawned from TUI-mode weave inherit from the *weave* subprocess's
  env (`buildSessionEnv` in `cmd/enter.go:216-221`), which does not set it.
  This means child agents' notifiers *also* no-op in TUI mode — which is
  fine for *them* (children drain the queue via agentloop step 2a), but
  means cross-mode interop (tmux weave + TUI child, or vice versa) will not
  behave symmetrically.

- **Ctrl+C in TUI mode intentionally does NOT run FinalizeHandoff** and does
  NOT kill child agents (`cmd/enter.go:535-554`). This is different from
  tmux mode's bash restart loop, which on SIGHUP of the tmux window tears
  down the claude subprocess and leaves children orphaned. TUI's
  "detachable UI" story is actually cleaner. Worth documenting when we write
  the cutover migration note.

- **Resume path clears `--system-prompt-file` and `--session-id`** in
  `claude.LaunchOpts.BuildArgs` by design (`internal/claude/launch.go:65-75`)
  because the resumed transcript already owns them. This is why
  `buildEnterLaunchOpts` conditionally sets `SystemPromptFile` only on fresh
  launches. Symmetric with tmux.

- **The bubbletea `onStart` hook already has a `tea.Program` sender**
  (`cmd/enter.go:458`) that's used for handoff and resume-failure signaling.
  Reusing it for the inbox notifier is literally a few lines — this is why
  the fix is small.

- **Transcript replay on resume is capped at `tui.ReplayMaxMessages`** and
  loads from `~/.claude/projects/<hash>/<session>.jsonl`
  (`cmd/enter.go:444`). If a user resumes a huge session, expect truncation;
  the rest is still in the .jsonl but will not be rendered.

- **No TUI mode was exercised without an issue tracker cross-check.** The
  open Linear issues (QUM-195 parent, 205, 235, 260, 303) already cover
  several gaps we'd have filed; avoid duplicating.

## Manual test log

Sandbox: `/tmp/sprawl-audit-4aLOI5`, 160×45 tmux pane, `weave` as root.
Binary `v0.1.10-38-geb7e4c5`. Cleaned up after test.

1. **Launch.** `sprawl enter` booted, rendered tree+viewport+input+status,
   weave shown in tree as `● [W] weave (idle)`. `sess:4bfeb536`, agents: 1.
   ✅
2. **Inbound message.** From a simulated child
   (`SPRAWL_AGENT_IDENTITY=pretend-child sprawl messages send weave …`), the
   maildir received the message and `weave.wake` was written within the
   expected latency, but the TUI showed **no visible change**: no banner,
   no status line update, no tree-row highlight. Tree still said "agents:
   1", viewport still on welcome text. ✅ Confirms S1-1.

Other scenarios (spawn/retire, /handoff, resume across restart, scroll, yank,
palette) were not exercised in this session — relying on code audit + existing
Linear evidence.

## Reflections (for handback to weave)

**Surprising.** The notifier-as-`SetDefaultNotifier` pattern is so cleanly
factored that the TUI gap is a one-line guard away from working. I had
expected a deeper rewrite.

**Open questions.**

- Should weave's inbox banner include the message body preview (like tmux's
  `[inbox] New message from <child>`), or stay minimal? Probably minimal +
  bump unread, and let the user press `/switch` or cycle into the child to
  read. Sketch for the ticket.
- Is anyone actually depending on `SPRAWL_MESSAGING=legacy` in TUI mode? The
  gate could arguably default to on for everyone and let the legacy path
  coexist with a TUI sender — but that requires a `tea.Program` to exist,
  which it doesn't for CLI invocations. Keep the gate; just add a second
  notifier for TUI.
- What about `/handoff` while weave has unread mail? Current flow consumes
  the handoff signal on restart; we should make sure the inbox is carried
  across the restart transparently (it is — maildir is disk-backed and the
  new session picks up unread via the same polling).

**Would investigate with more time.**

- Run the full TUI e2e harness (`scripts/test-tui-e2e.sh`) and the full
  manual checklist from `/tui-testing`. Today I only exercised boot +
  inbound-message. Spawn/retire and handoff-with-children need live-exercise
  before cutover.
- Stress-test large-transcript replay (`ReplayMaxMessages` cap) to
  understand the UX when resuming week-old sessions.
- Confirm Ctrl+C behavior end-to-end with children mid-turn: theoretically
  children survive, but I didn't validate.
