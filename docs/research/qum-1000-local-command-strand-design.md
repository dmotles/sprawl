# QUM-1000 — CLI-handled slash commands strand a pending-zone entry: design resolution

**Status:** design recommendation (read-only investigation; no production code touched)
**Author:** ghost (researcher), 2026-07-27
**Issue:** QUM-1000. Related: QUM-817 / QUM-814 (consumption-ack contract), QUM-833
(pending zone), QUM-832 (dim/bright), QUM-824 (recall), QUM-865 (`/compact`
passthrough), QUM-934 (`/perf`).

> **Headline: the issue's stated mechanism is wrong in two load-bearing ways.**
> The `system`/`local_command` frame **never reaches sprawl** — it exists only in
> the CLI's transcript JSONL, not on the stream-json wire. And a slash command the
> CLI *recognizes and executes* **does not strand** — it gets a normal `isReplay`
> echo and settles correctly today. Options 1 and 2 in the issue are therefore
> **unimplementable**, and the issue's "important case a verifier will forget to
> test" is the case that already works.

---

## 1. Evidence base

Two artifacts, both reproducible:

* **Probe 1** — `local_command` routing + `cancel_async_message` behaviour.
* **Probe 2** — which inputs receive an `isReplay` consumption ack (9 cases,
  prose positive controls at both ends).

Both drive a real `claude` in exactly sprawl's configuration
(`-p --input-format stream-json --output-format stream-json --verbose
--replay-user-messages`, uuid'd `priority:"next"` user messages — i.e. the
`UnifiedRuntime.writeMessage` wire shape), via `scripts/run-claude` so auth
survives the agent subshell (QUM-518).

Scripts and raw frames: `.sprawl/agents/ghost/findings/qum-1000/`
(`probe.py`, `probe2.py`, `frames.jsonl`, `frames2.jsonl`,
`probe2-results.json`). CLI version `2.1.219`, `entrypoint: sdk-cli`.

---

## 2. Q1 — Is the `system`/`local_command` frame routed anywhere today?

**No — and stronger than the issue supposes: it never arrives at all.**

### 2.1 It is not on the wire

Probe 1 submitted an unrecognized slash command and captured every stdout frame.
**`system`/`local_command` frames observed on the wire: 0 of 31.** The same
session's transcript JSONL (`~/.claude/projects/-tmp-ghost-qum1000-probe-wd/54de981e-….jsonl`)
contains **3** `subtype:"local_command"` records for the same commands.

So `local_command` is a **transcript-only artifact**. The wire-log evidence quoted
in the issue was read from a Claude Code *transcript*, not from sprawl's input
stream — a transcript that also contains no `type:"result"` and no
`subtype:"init"` anywhere in the project directory, which is the tell that it is a
different stream from the one `runReader` consumes.

What the CLI actually emits on the wire for a locally-handled command is:

```
system/session_state_changed {state: running}
command_lifecycle
command_lifecycle
system/init
assistant                       <- the command's output as ordinary assistant text
result/success
command_lifecycle
system/session_state_changed {state: idle}
```

The `<local-command-stdout>` body arrives as an **`assistant` text frame** (uuid
correlated: transcript record `f0af6ae3…` ≡ wire `assistant` frame `f0af6ae3…`).
That is why dmotles saw `Unknown command: /perf` rendered — it came through the
ordinary assistant path, not from any special frame.

### 2.2 Even if it did arrive, it would be dropped twice

Recorded for completeness, because it constrains any future design:

1. **`internal/backend/session.go:800`** — the route gate is
   `tf != nil || preInitTrigger || preInitCompactStatus || replayEcho || stateChange != ""`.
   A `system`/`local_command` matches none of the special arms (`preInitTrigger`
   requires `task_notification`; `preInitCompactStatus` requires
   `subtype:"status"`; `replayEcho` requires `type:"user"`; `stateChange`
   requires `session_state_changed`), and only `subtype:"init"` allocates a
   `turnFrame` (`session.go:743`). A slash command typed while idle has
   `tf == nil` ⇒ **observer-only, never routed to the runtime.**
2. **`internal/tui/protocol_mapping.go:91-164`** — `MapProtocolMessage`'s
   `case "system"` handles `compact_boundary`, `status`, `init`, and returns
   **`nil`** for every other subtype. So even if routed and published it would
   render nothing.

**Search validation (per the evidence standard).** `grep -rn 'local_command\|local-command' internal/ cmd/ web/src`
→ **0 hits**. Probe validation of that same grep shape against a known-present
subtype: `grep -rn 'compact_boundary\|"system"' internal/` → 30+ hits across the
same tree. The probe can find subtype strings where they exist; it finds none for
`local_command`. Independently corroborated by reading the routing code above, so
this is not an argument from grep silence alone.

**Consequence for the issue's options:** correlation strategies **1 (content
match)** and **2 (oldest-outstanding)** both key on observing a `local_command`
frame. That frame does not exist on sprawl's input stream. They are not
"fragile" — they are **unimplementable as specified**.

---

## 3. Q4 — Which inputs actually strand? (empirical)

Probe 2, 9 cases, prose positive controls first and last so a systemic
echo failure cannot masquerade as a per-command result. "ACK" = an `isReplay`
`user` frame carrying the submitted uuid arrived ⇒ `markConsumed` fires ⇒
`ZoneSettle` ⇒ no ghost.

| input | CLI response | `isReplay` ack | outcome |
|---|---|---|---|
| `Reply with exactly: ack1` *(control)* | normal turn | **YES** | settles |
| `/perf` | `Unknown command: /perf` | **NO** | **GHOST** |
| `/qum1000-nope arg1 arg2` | `Unknown command: /qum1000-nope` | **NO** | **GHOST** |
| `/status` | `/status isn't available in this environment.` | **NO** | **GHOST** |
| `/help` | `/help isn't available in this environment.` | **NO** | **GHOST** |
| `/model` | prints current model + usage | **YES** | settles |
| `/context` | prints context table | **YES** | settles |
| `/etc/hosts is broken, …` *(prose, leading slash)* | normal turn | **YES** | settles |
| `Reply with exactly: ack2` *(control)* | normal turn | **YES** | settles |

### 3.1 The real discriminator

Not "recognized vs unrecognized". It is:

> **The CLI emits an `isReplay` echo iff it *accepted and executed* the input as a
> conversation turn. Inputs it REFUSES produce an `assistant` text explaining the
> refusal and no echo at all.**

Two disjoint refusal classes, both stranding:

* **unknown command** — `/perf`, `/qum1000-nope`
* **known builtin unavailable in this environment** — `/status`, `/help`
  (these are real CLI commands; the SDK/headless entrypoint declines them)

And two accepting classes, both fine today: executed builtins (`/model`,
`/context`) and ordinary prose (including prose that merely starts with `/`).

### 3.2 This inverts the issue's priority claim

The issue asserts that `/compact`, `/model`, `/cost` "take the identical path …
would work correctly *and* strand an identical permanent ghost entry", and AC #3
makes that "the important case". **Measured: false.** `/model` and `/context`
echo and settle. The uuid is preserved across the echo, so `markConsumed` keys on
it correctly; only the echo's *content* is rewritten (see §3.3).

**This is a false-green trap in the ACs as written.** A verifier who tests
"a command the CLI recognizes and successfully executes" will pick `/model`, see
no ghost, and tick the box — having exercised nothing. AC #3's second case must
be re-specified as **a refused builtin (`/status`)**, not an executed one.

### 3.3 Echo content is rewritten (kills content-matching for good)

For an accepted builtin the echo does not carry what sprawl sent:

```
sent:   "/model"
echoed: "<command-name>/model</command-name>\n  <command-message>model</command-message>\n  <command-args></command-args>"

sent:   "/cost"
echoed: "<command-name>/usage</command-name>…"     # note: aliased to a DIFFERENT name
```

And the form is **entrypoint-dependent** — under `sdk-cli` the transcript echo of
`/perf` is the bare string `"/perf"`, while an interactive-entrypoint session
records `<command-name>/model</command-name>` XML. Any parser keying on either
shape misses the other. (Harmless today: `ZoneSettle` renders the text stored at
`ZoneAddUser`, i.e. sprawl's original line, not the echo body.)

---

## 4. Q2 — Does `Ctrl+U` clear a stranded entry?

**Yes. Proven, not inferred.** The bug is annoying-but-recoverable, not a
permanently unremovable UI element.

The chain, with the one unknown resolved empirically:

1. `app.go:801-804` — Ctrl+U (root/weave only, `observedAgent == rootAgent`) → `bridge.Recall()`.
2. `tuiadapter.go:337-348` → `rt.Recall(context.Background())`.
3. `unified.go:924` → `cancelPendingUser` → `Session.CancelAsyncMessage(ctx, uuid)` per pending entry.
4. **Probe 1, phase 4/5 — the CLI always answers, and always with `cancelled:false`:**

   ```json
   {"type":"control_response","response":{"subtype":"success","request_id":"probe-cancel-0","response":{"cancelled":false}}}
   ```

   Tested for the unknown-command uuid, the `/cost` uuid, an already-consumed
   prose uuid, **and a uuid the CLI never saw** (negative control) — all four
   returned `cancelled:false`. No hangs, no missing responses.
5. `unified.go:891-894` — `cancelled == false` ⇒ `markConsumed(uuid)` ⇒
   `EventUserMessageConsumed`.
6. `app.go:1301` — `ZoneSettle(uuid)`: the entry leaves the pending zone,
   un-dims, and relocates into the committed transcript.

So Ctrl+U clears the ghost by **settling** it (a bright `/status` bubble in the
transcript), not by dropping it. That is the honest end state — the user did type
it — and it is exactly the end state §5 recommends reaching automatically.

### 4.1 Two secondary observations (do not block the fix)

* **`CONFIRMED` — unbounded ack wait.** `CancelAsyncMessage`'s *send* is bounded
  (`interruptSendTimeout = 2s`, `session.go:36`) but the *ack wait* is not:
  `session.go:1292-1307` selects only on `replyCh`, `ctx.Done()`, and
  `s.readerDone`, and `ctx` is `context.Background()` all the way from
  `tuiadapter.go:345`. If the CLI ever failed to answer, Ctrl+U would block
  forever with **no user-visible signal at all** (no toast, no
  `PromptsRecalledMsg`), and `cancelPendingUser` is serial so one unanswered uuid
  stalls the rest. Probe 1 shows the CLI does answer in every case tested, so this
  is **latent, not live** — worth a `context.WithTimeout` as hardening, not a
  prerequisite. Ctrl+U also has no in-flight latch (unlike Ctrl+G's
  `sendAllNowInFlight`, `app.go:805-823`), so each press spawns another blocked
  goroutine in that scenario.
* **`UNVERIFIED — implementer should check.`** `UserMessageConsumedMsg` flips
  `TurnIdle → TurnThinking` (`app.go:1314-1316`, QUM-831, on the premise that a
  consume means a turn is starting). On the Ctrl+U `cancelled:false` path — and on
  any never-acked sweep (§5) — **no turn is starting**, so the spinner may light
  spuriously until the next real turn. I did not trace whether anything else
  clears `turnState` from idle, so this is flagged, not asserted.

---

## 5. Q3 — Recommended strategy

### 5.1 Verdict on the three options

| option | verdict |
|---|---|
| **1. content match on `local_command`** | **Unimplementable.** Frame never reaches sprawl (§2.1). Content also rewritten/entrypoint-dependent (§3.3). |
| **2. oldest-outstanding on `local_command`** | **Unimplementable**, same missing frame. Would additionally have been *dangerous*: the transcript shows **2** `local_command` records per command, so a per-frame "settle the oldest" fires twice and would settle an innocent legitimately-queued prompt. |
| **3. detect `/`-prefix at submission** | Implementable — it is the only one that survives §2.1 — **but no prefix heuristic can be made correct.** See §5.2. Recommended only as the *fast path*, never as the whole fix. |

### 5.2 Resolving the asymmetry the issue flagged

The issue frames the tension as: option 3 is the only frame-independent option,
but it "hard-codes a `/`-prefix guess about what the CLI intercepts, which sprawl
cannot actually know." **Measurement resolves this against option 3 as a
standalone fix — the guess is provably wrong in both directions in the real
command set:**

* `/model`, `/context` are `/`-prefixed and **do** echo. Suppressing their zone
  entry (the shipped `/compact` behaviour) silently swallows a line the user
  typed — it never appears in the transcript at all.
* `/etc/hosts is broken, …` is `/`-prefixed prose that **does** echo. A bare
  `HasPrefix("/")` rule swallows a real message. The single-token refinement at
  `app.go:3238` (`HasPrefix("/") && !ContainsAny(" \t")`) rescues this case but
  then **misses `/qum1000-nope arg1 arg2`**, which has args and strands.

There is no predicate over the input text that separates these, because the
distinction lives in the CLI's command table and its per-entrypoint availability
rules — neither of which sprawl can see. **So do not try to predict.** Detect
after the fact, from a signal that always arrives.

### 5.3 Recommendation: settle on turn-terminal, guarded by submit order

Every case in §3 — echo or no echo — ends with the **identical** envelope:
`session_state_changed{running}` … `result/success` … `session_state_changed{idle}`.
Both terminals are already routed and already authoritative in sprawl
(`unified.go:356-364` phase machine, QUM-903; `unified.go:441-459` `EndOfTurn`).
That is the signal to use.

> **Rule.** On the transition to `phaseIdle`, settle every `kind:user` outstanding
> entry that is still `statePending` **and was written before the most recent
> `running` transition**. Rationale: if the entry existed before the turn began
> running, and the CLI has now gone idle, the CLI processed that turn without
> acking it — it will never ack it.

Implementation shape (`internal/runtime/unified.go` only; **no `internal/tui/app.go`
change required**, which matters while citadel owns that file):

* Snapshot `outSeq` into a new `lastRunningMark` on the `phaseRunning` transition.
  `OutstandingEntry.seq` already exists (`unified.go:159-162`, `714`), so no new
  timestamps are needed.
* On entry to `phaseIdle`, sweep `seq <= lastRunningMark && state == statePending && kind == kindUser`.

**Why this is not the QUM-927/QUM-935 mistake.** `retireUnclaimedNextArmLocked`
carries an explicit scar (`unified.go:510-518`) warning that `p == phaseIdle` is
"a phase signal, not an identity signal", because *a trailing
`session_state_changed:idle` from the previous turn routinely lands after a new
prompt is submitted*. That objection applies to a **bare** idle trigger and the
`seq <= lastRunningMark` guard is precisely the missing identity signal:

* Prompt B submitted from idle, then T1's trailing `idle` lands. T1's `running`
  preceded B's write ⇒ `lastRunningMark < B.seq` ⇒ **B is not swept.** ✅
* B queued mid-turn T1 at `priority:"next"`. `B.seq > mark(T1)` ⇒ survives T1's
  idle; then T2 opens, its echo for B settles it normally. ✅
* `/status` submitted from idle ⇒ `running` (mark ≥ its seq) ⇒ refusal ⇒ `idle`
  ⇒ **swept**, within the same ~1s turn (invisible to the user). ✅
* `/model` ⇒ echo settles it first; the sweep is a no-op because `markConsumed`
  and `flipPending` both guard on `statePending` (`unified.go:814`, `903-912`). ✅

**Accepted residual, stated rather than hidden:** an autonomous turn's `running`
could advance the mark past a still-queued entry, and that turn's `idle` would
then settle it early. Blast radius is cosmetic only — the bubble brightens early;
it cannot render twice (`ZoneSettle` no-ops on an untracked uuid) and cannot lose
a message.

**Two hard constraints for the implementer:**

1. **Restrict the sweep to `kind:user`, and do NOT fire `OnDelivered`.**
   `markConsumed` invokes `rt.cfg.OnDelivered(entryIDs)` (`unified.go:820-822`),
   which marks maildir entries delivered. `kind:system` entries (inbox drains)
   carry `entryIDs`; sweeping one would durably mark an inbox message delivered
   that was never consumed. Add a distinct `settleNeverAcked` rather than reusing
   `markConsumed` wholesale. (System writes are never slash commands — they are
   `<system-notification>`-prefixed — so excluding them costs nothing.)
2. Check the spurious-spinner risk in §4.1, second bullet.

### 5.4 Keep the shipped option-3 fast path where sprawl genuinely owns the call

QUM-865 already implemented option 3 for exactly one command, and it is the right
precedent to *narrow*, not extend: `SendPassthrough` writes `/compact` to stdin
verbatim and returns `UserMessageSentMsg{Passthrough: true}`, whose reducer
creates no zone entry (`tuiadapter.go:250-268`, suppression `app.go:1227-1237`).
That is sound because it is registry-driven — sprawl *knows* it routed
`/compact` — not a prefix guess. Leave it. Note two edges:

* If the backend does not advertise `SupportsCompactCommand`, `/compact` falls
  through to the plain `SendMessage` path (`app.go:3287-3293` → `1223`) and gets a
  zone entry again. The §5.3 sweep covers that regression for free.
* Precedent caution: `Passthrough` suppresses the bubble entirely, so the typed
  line never enters the transcript. Prefer §5.3's *settle* semantics for the
  general case — a refused command should leave a visible record, since the CLI's
  refusal text renders as assistant output right next to it.

### 5.5 Optional, separable enhancement (explicitly NOT load-bearing)

`local_command` is transcript-only, so there is nothing extra to render — the
refusal text already arrives as an `assistant` frame and renders today. **No
routing change to `internal/backend/session.go` and no new `internal/protocol`
type are needed.** Both are listed in the issue's file list; both should be
struck.

---

## 6. Durable reproduction (survives `/perf` being wired)

Wiring `/perf` (QUM-934 — currently unregistered; `perfmodal.go` exists but
`ShowPerfMsg` has no producer or reducer) makes sprawl own `/perf` and the
observed repro vanishes. **That is the repro disappearing, not the bug.** Three
durable substitutes, in order of preference:

1. **`/status`** — *the recommended primary.* A real CLI builtin that the CLI
   **refuses in this environment** (`"/status isn't available in this
   environment."`), not registered in sprawl's 8-command registry, and no
   plausible reason sprawl would ever own it. Exercises the refused-builtin
   class, which is the class AC #3 currently mis-specifies.
2. **`/qum1000-nope`** — a nonsense command. Unownable by construction, forever;
   guaranteed `Unknown command`. Best choice for a unit/e2e fixture where
   stability matters more than realism.
3. **`/compact` with `SupportsCompactCommand` forced false** — exercises the
   §5.4 capability-gate edge.

**Negative controls the test must also assert** (these are what make the
assertions falsifiable rather than trivially green, per QUM-953 / AC #6):

* `/model` — must settle exactly once **and settle today, before the fix**. This
  is the guard against a fix that suppresses too broadly.
* `/etc/hosts is broken, what does that mean?` — leading-slash prose; must render
  and settle normally. This is the guard against a `HasPrefix("/")` regression.
* An ordinary prose prompt — dim→bright, settles exactly once (AC #5).

**Assertion-rigor note.** AC #6 already flags that "the zone is empty" passes
trivially if nothing was ever added. The sharper trap here: **the pre-fix state
for `/model` and `/context` is already green**, so any assertion written only
against those commands can never have failed. Every stranding assertion must be
red-first against `/status` or `/qum1000-nope` specifically, and the transcript
must record that red run.

---

## 7. Suggested issue edits

* **Mechanism section** — replace "the `local_command` frame … is the only signal
  that the input was consumed" with §2.1: the frame is transcript-only; the
  wire signal is the *absence* of an `isReplay` echo, bracketed by a normal
  `running`/`result`/`idle` envelope.
* **Strike** `internal/backend/session.go` and `internal/protocol/types.go` from
  the file list (§5.5). Likely also `internal/tui/app.go` and
  `internal/tui/pendingzone.go` / `chatlist.go` if §5.3 is adopted — the fix is
  runtime-local, which conveniently avoids citadel's in-flight `app.go` work.
* **"Why this is worse than the observed instance suggests"** — correct the
  `/compact` / `/model` / `/cost` claim (§3.2). The silent-strand class is real
  but its members are *refused* commands, not *executed* ones.
* **AC #3** — re-specify the second case as a refused builtin (`/status`), and
  add `/model` as a must-still-work negative control.
* **AC #4** — resolved: Ctrl+U **does** clear it (§4). Downgrade from "possible
  second defect" to "confirmed working; §4.1 hardening optional."
* **Priority** — the "permanently unremovable UI element with no user recourse"
  worst case does **not** obtain. Recall works and session restart works. High
  still defensible on silent-strand grounds; Medium is arguable.
* **Matrix rows** — if §5.3 lands runtime-only, `notif-stacked-restart` /
  `pending-dim-bright` / `tui-live-render` are still worth running (the settle
  path's *trigger* changes even though the reducers do not), but the touched-file
  table's `internal/runtime/unified.go` rows (`idle-interrupt-inject`,
  `sendnow-tui`, `recall-sendnow`, `esc-interrupt-survives`) become the
  mandatory set.
