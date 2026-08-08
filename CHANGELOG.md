# Changelog

All notable changes to Sprawl are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) loosely; releases are
not strictly semver while we are pre-1.0.

## [Unreleased]

## [v0.5.5] - 2026-08-08

### Changed

- **`sprawl merge` no longer squashes, and `-m` / `message:` is now refused** (QUM-1087, QUM-1088, QUM-1090, QUM-1100) — the old order was squash → rebase → `merge --ff-only` **into your branch** → validate in *your* worktree → on failure `git reset --hard HEAD~1` on your branch. Every confirmed loss lived in the window between "your branch was mutated" and "the tree was known good". The new order is rebase → prove fast-forwardable (before anything expensive) → validate **in the agent's worktree** → `--ff-only` → prove the tip moved to exactly the rebased SHA. Your branch is now mutated exactly once, forward-only, after the tree is already good, and `RealGitResetHard` and the `GitResetHard` seam are deleted outright rather than left uncalled, so no path can acquire a caller later. Two measured loss modes go with the restructure: a merge whose content was already upstream under a different SHA, where the rebase dropped it and `--ff-only` exited 0 *without moving the ref* while the rollback then rewound a **pre-existing** commit of yours (measured against the unmodified engine — `main` lost a commit it had before the merge was invoked); and a second merge landing during the first's validation, whose work the first's rollback removed. A parent branch that moves mid-validation is now a loud `--ff-only` refusal naming both SHAs.

  **Action required if you relied on the squash or passed a commit message.** The agent's own commits now land as-is, so squash on the agent's branch *before* declaring done. `-m` on the CLI hard-errors rather than being ignored — the flag stays registered precisely so it can refuse — and `message:` is dropped from the `merge` MCP tool's advertised schema while still being parsed and refused for any caller that knows the key.

  Recovery also stopped being manual. Before the first mutation, every non-noop non-dry-run merge writes `refs/sprawl/premerge/<agent>/<ts>/agent` and `.../parent`, recording both branch tips; unlike reflog entries these survive `git gc`, survive branch deletion at retire, are discoverable with `git for-each-ref`, are printed on success, and are named in every failure the engine diagnoses (rebase failure, validate failure, and both fast-forward refusals). A rebase failure now restores the agent's branch itself by compare-and-swap instead of printing a `git reset --hard <sha>` for a human to run — an audit of this repo's merge history found 11 rebase failures each left with the branch pointing at the engine's own squash. `sprawl gc` gained a third pass pruning premerge refs older than `--premerge-retention-days` (default 14; values below 1 are refused). Separately, `retire --merge` had been building its own merge config from the agent's *spawn-time* branch name while the engine resolved the worktree's real HEAD; once delegate reuse moved a worktree the two diverged, the merge **reported success**, your branch received only the stale branch's content, and retire then deleted the branch and worktree as "already merged" (QUM-1088). Both surfaces now resolve the same branch.

- **`status` reports the cost of an agent's current session, not its lifetime** (QUM-1093) — the per-agent cost figure summed every session that agent had ever had while being read as the cost of the one you are watching. Measured at the time on this repo's root agent, the lifetime column stood at roughly sixty times the current session's cost, across session files spanning about two months. The figure is a steering signal — is this agent running away, is this task worth what it costs — and a lifetime total answers neither: it only grows, so every long-lived agent eventually looks alarming regardless of behaviour, and a freshly spawned agent is incomparable with an old one by construction. No new recording was needed; usage was already written one file per session and was being flattened at read time, so `status` also stops scanning two months of history on every call. `sprawl usage` and the `/usage` modal deliberately still report **lifetime**, which is correct for them, and their shared aggregation path is pinned by test so a tidy-minded refactor cannot quietly change what they report.

  **If you parse the `status` MCP tool's output, the field is renamed `total_cost_usd` → `session_cost_usd`** — also across `observe.AgentInfo`, the TUI tree, and its cost tag. A stale name is how this class of defect survives review. **Known limitation:** usage records are not being written at all for a large fraction of live agents, so `status` will now honestly report `$0.00` where it previously reported a large wrong number. That is the intended trade — a visible absence of data beats a plausible-looking wrong answer that conceals it — but the underlying recording defect is real, predates this change, and is filed as QUM-1097.

- **Status pings and message notifications now reach the root session the instant they arrive** (QUM-925, QUM-1060, QUM-1064) — the root was fed solely by a 2-second TUI poll gated on turn state, and the gating was *lossy*, not merely late: the drain is destructive and the reducer dropped the frame if a turn had started in between, so a ping caught in that window was permanently gone. Delivery is now event-driven and ungated, with a periodic redrain as a backstop for entries no in-process producer pokes for. Along the way: notifications held in the pending zone render dim and brighten on settle, matching the treatment user bubbles already had, with a structural gutter marker as well as the dim styling because a faint-only delta silently no-ops on terminals that ignore SGR 2; a burst of status changes from one agent now collapses to a single last-wins line per agent in first-appearance order, since a status is a snapshot and an intermediate one is already stale by the time you read it (mail citations, which are genuinely distinct per message, are untouched); and three defects in the parent→child delivery path were fixed — an async notification re-injected at every turn boundary until its echo arrived (11 injections across 10 boundaries, exactly linear), a phantom consume published into the spinner reducer for every pre-restart message after a session restart, and unbounded stdin writes on the MCP handler goroutine where one wedged child could hang an unrelated agent's tool call indefinitely.

- **`.sprawl/config.yaml` now has one representation and rejects unknown keys** (QUM-1086, fixes QUM-1078) — the config carried both a typed struct *and* a parallel `map[string]string`, hand-synced between them. Any nested block made the map unmarshal fail, and the error path threw away a correctly-populated map, so `worktree.setup` silently stopped resolving and new agent worktrees came up with **no** QUM-808 pre-commit guard and **no** QUM-837 reference-transaction guard — with nothing said anywhere. A subsequent `sprawl config set` then rewrote the file from the truncated map, deleting the key from disk. The map is gone; the typed struct is the single representation, `worktree.setup` / `worktree.teardown` / `memory_model` are now real fields (same flat dotted YAML keys — **no file-format migration**), `Load` parses once and `Save` marshals the struct, so no field can be dropped by construction. An unrecognized key is now a **hard error** that reports every offending key with its line number, a did-you-mean, the explicit remedy for a retired key (`liveness` → "removed in QUM-1071 — delete this block"), and the full recognized-key reference — generated from the struct, so it cannot drift, and now also printed by `sprawl config --help`. Seven `config.Load` call sites were swallowing that error, including two in the TUI launch path where a broken config costs you exactly those guards; `sprawl enter` now fails before bubbletea takes the screen, spawning a worktree aborts rather than proceeding without the guards, and `sprawl config set`/`get` on an unknown key errors instead of silently persisting or printing nothing. **`hubd` refuses to start on a malformed project config too** — a separate deployable, so a hub operator seeing a new startup refusal is seeing this change, not a regression. **The scope is the project config only:** the user-level config still ignores unrecognized keys, deliberately and by its own documentation. Side effects worth knowing: `Config.Set` returns an error, `Load` no longer prefills defaults into fields (they live in the accessors, so `Save` cannot freeze today's default into your file), and `Save` writes only keys that are actually set. The error prints the reference table *first* and the offending keys *last*, nearest the prompt: the table wraps to 26 rows at 80 columns, so with the keys on top the actionable half scrolled off an 80x24 terminal and left you holding the list of valid keys with no indication which of yours was wrong. `sprawl config get` on an unknown key now renders through the same error, so it gains the did-you-mean it previously lacked.

- **Command errors now print once, after the usage block, instead of twice** (QUM-1086) — cobra was printing the error itself and then again from the top-level handler. Worth knowing if you scrape sprawl's stderr, which agents do.

- **`scratch/` is now gitignored at any depth** — unanchored, so a `scratch/` directory anywhere in the tree is ignored rather than only one at the root. Nothing tracked matches today. **If you have a legitimately tracked `scratch/` directory, staging inside it now needs `git add -f`.**

- **The repo's own agent instructions and docs were restructured** (QUM-1155) — `CLAUDE.md`, which every agent loads on every turn, was cut from 938 lines to 74, against a 250-line ceiling (it stood at 561 at v0.5.4 and grew through this cycle before the cut) — and that ceiling is now resolved and enforced by `make validate` over the tracked tree, rather than asserted in prose. The removed content was relocated, not deleted, into `.claude/skills/` — git and merge recovery, the mandatory e2e matrix table, repo internals and Go conventions, testing practices, sandbox and `tmux` hygiene, TUI testing — each with a frontmatter trigger describing *when* to read it, since for several skills that line is the only trigger surface an agent gets. What stayed in the always-loaded file is what an agent can get wrong before it would think to look anything up: the terminology, the hard prohibitions, the validate gate, the e2e union rule, the assertion rules, the public-vs-private hygiene rule, the mandate that TUI changes be validated in a TUI, the Linear lifecycle, and a skills index whose entries are written as conditions rather than titles. The three automated gates that read `CLAUDE.md` were repointed at their new homes rather than dropped. Alongside it, `docs/` was consolidated: `docs/design/` merged into `docs/designs/`, 52 superseded documents quarantined to `docs/archive/` and 64 more deleted, and a new `docs/README.md` indexes what remains. **If you hold bookmarks or links into this repo's `docs/` tree, re-check them** — and note that relative links *inside* the archived documents were not rewritten when they moved, so a link from an archived doc to a still-live one dangles.

### Fixed

- **A prompt flushed mid-turn with `Ctrl+G` could hang in the pending zone forever** (QUM-1111) — the reducer for "user message sent" re-armed the TUI's one-shot event pump unconditionally, but that message has mixed provenance: three code paths return it directly without consuming a pump event. Each typed prompt therefore *added* a reader without retiring one, measured climbing 1 → 2 → 3 → 4. With several readers on one channel the event bus's ordering stops surviving to the reducer, two adjacent events arrive inverted, and the settle lands on a zone entry that does not exist yet — after which the entry that *is* created can never settle, because the delivery ack is idempotent and will not fire twice. The re-arm is now conditional on the message actually having come from the pump.

- **`Ctrl+G` could destroy prompt text it had already cancelled** (QUM-1112) — flush-now is cancel-and-replace: it flips each pending prompt to cancelled and then rewrites the collected text as one now-priority message. If the cancel partially failed, it returned early and discarded that text outright. By then the successfully-cancelled entries were gone from `Ctrl+U` recall and their bubbles already dropped from the transcript, so a typed prompt was destroyed leaving only a generic error toast. No path may now leave text that was flipped out of pending without either rewriting it or handing it back: the text comes back to the caller, the TUI restores it to the input (prepending, so anything you typed during the flush survives), and the toast states the recovery clause *before* the unbounded error text, because toasts render on one line and truncate to pane width — which had been silently eating the entire actionable half of the message on an 80-column pane. Prompts whose cancel actually failed are untouched, stay pending, and remain recallable.

- **A slash command the CLI refuses left a permanent ghost row** (QUM-1000) — an unknown command, or a builtin the CLI declines, produces no echo, so the pending-zone entry never settled and sat dimmed in the transcript indefinitely. The oldest still-pending prompt is now settled at a clean, non-interrupted turn terminal, and only when that turn acknowledged nothing — all three conditions load-bearing, because settling early is not cosmetic: it would silently remove the prompt from `Ctrl+U` recall.

- **Restarting the root session could inject one 38,673-byte notification frame and destroy its context** (QUM-730) — liveness checks were content-free edge signals ("are you alive *now*", with an empty body) that were nonetheless stored as durable, replayable mail. A durable queue promises to deliver everything ever written, and the envelopes were invisible to `messages` listing, so roughly two months of them accumulated unnoticed and the first restart after a drain was wired delivered all 123 at once. Liveness checks stopped being written as mail, and the feature that produced them was then removed outright (see Removed) — the message type and its display filters are retained only so any legacy envelope still on your disk stays hidden. The durable fix, though, is the cap: every system frame now passes through one choke point that dedups losslessly and then truncates at 8 KiB with an explicit marker, so no future channel can flood the session by forgetting to bound itself. The cap is never applied to text you typed — a human sending the same prompt twice means it twice.

### Removed

- **Supervisor heartbeat and liveness checks** (QUM-1071) — the QUM-730 background liveness scan, its nudge plumbing, and the `liveness:` block in `.sprawl/config.yaml` (`enabled`, `heartbeat_interval`, `idle_threshold`, `tier2_consecutive_ticks`, `escalation_threshold`) are gone. Measured across 196 structured stderr logs spanning ~two months, the feature had emitted 32 "appears stuck" warnings — 31 of them the same overnight-idle false positive, already gated off — and had escalated to a parent **zero** times, which analysis showed was structural rather than luck: any agent that answers a nudge resets the escalation counter, and any agent that does not is already excluded by an earlier gate. Its one genuinely useful output, a root-wedged toast, was never wired to a user-visible surface. **Migration: DELETE the `liveness:` block from `.sprawl/config.yaml`.** Not optional, and not merely tidiness — as of QUM-1086 an unrecognized key is a startup error, so a leftover block now fails `sprawl enter` with a message naming `liveness` and stating this remedy. That is the intended direction: the earlier claim in this entry, that "the typed config ignores unknown keys, so the block causes no parse error", was true of the typed struct and **false of the file as a whole**. The block is a *nested* block, which broke `config.Load`'s second unmarshal into its flat dotted-key map, and that error path discarded the whole map — silently, and not confined to `liveness:`. `worktree.setup` stopped resolving, so the QUM-808 pre-commit and QUM-837 reference-transaction commit guards were no longer installed on new agent worktrees, with no error or warning anywhere. Worse, `sprawl config set` would then rewrite `config.yaml` from the truncated map, deleting `worktree.setup` from disk. That parser defect was pre-existing rather than introduced here — any nested block triggered it — and this repo's own config uses only flat keys, so it was never exposed; but this removal is what left such a block behind. Filed as QUM-1078 and **fixed by QUM-1086** (see Changed, above). The capability-blurb refresher that rode along on the heartbeat now has its own 30-minute ticker with unchanged behaviour.

## [v0.5.4] - 2026-07-30

### Added

- **Hub MVP** (QUM-870 … QUM-911) — a new remote hub for observing sprawl sessions from a browser. Lands as one arc: a Connect/protobuf wire + `hubd` process spine, a storage seam (Postgres + object store, goose migrations), host registration and browser session auth, a read-only SPA with a wire-log live-tail and running/idle status pill, and Terraform IaC to deploy it on Azure (private database, remote state backend). Read-only in this release — no control surface from the browser.
- **Durable wire log** (QUM-902, QUM-903, QUM-904) — every session's frames are now written to a seq'd, frame-oriented wire log, which becomes the authoritative transcript source. The TUI rehydrates resume/replay from it instead of the Claude JSONL, so content a live render drop missed reappears on reload. In-turn state is now driven by the CLI's own `session_state_changed` signal, fixing spurious "thinking" indicators.
- **`toast` MCP tool** (QUM-898) — agents can raise a TUI toast directly. `Ctrl+T` now opens the agent tree and `Esc` clears toasts (QUM-895).
- **Agent blurbs in `status` / `peek`** (QUM-899) — each agent carries a short self-describing blurb, surfaced in the reshaped `status` and `peek` output so a parent can tell at a glance what a child is doing.
- **User-level config file** (QUM-886) — `sprawl` reads a `0600` user config for hub URL and token, with documented precedence over environment and project config.
- **Race detector enforced in `make validate`** (QUM-972) — `validate` now runs the whole unit suite under `-race` instead of an uninstrumented `go test`, plus a gate test that fails if the flag is ever dropped or rendered inert. Nine live data races (one of them a production defect) had been sitting behind a green `validate`; new ones are caught automatically going forward.
- **Richer incident bundles** (QUM-934) — `Ctrl+\` now captures CPU and heap pprof profiles and the binary identity alongside the existing dumps, and the pprof listener can be toggled on a running session with `SIGUSR2` (an unconfigured default relocates off a busy port and advertises its bound address).
- **Employer-name leak guard** (QUM-872, QUM-873) — a pre-commit guard plus a whole-tree scan to keep private context out of this public repo.

### Changed

- **Sidechain internals no longer clutter the chat stream** (QUM-928) — frames from in-process `Agent`-tool spawns (Explore, Plan, oracle, …) are suppressed live and on reload; an `Agent()` call now renders as a plain tool row that closes on its launch ack. Measured against 660 wire logs, this removed ~20.5k interleaved frames from the transcript view.
- **Interrupt handling is turn-scoped** (QUM-927, QUM-929, QUM-931) — the interrupt arm now tracks *which* turn it belongs to instead of a single boolean, collapsing a family of Esc-at-a-turn-boundary bugs into one fix. A genuine transport failure or subprocess death at a turn boundary now surfaces as a Session Error rather than being misreported as "Interrupted". The redundant `[auto-continue]` stdin injection was deleted — the CLI self-resumes on background-task completion on its own; old sessions still rehydrate the ↻ marker.
- **`git add -A` is banned repo-wide** (QUM-989) — agent worktrees sit on a shared filesystem, so staging must name explicit paths. Backed by additional gitignore rules for infrastructure artifact classes.

### Fixed

- **Slash-command popover clipped and overran on wide terminals** (QUM-930) — the popover had no width call at all, so it rendered as a fixed 52-column stub whose already-bordered output was then guillotined, eating the closing border glyphs and cutting descriptions mid-word. It is now width-adaptive (clamped to a readable 20–100 columns), elides with an explicit `…`, and measures display width correctly for wide runes.
- **TUI event pump could freeze permanently after a gap-detect** (QUM-978) — `EventDropDetectedMsg` consumed the one-shot armed command without re-arming on any exit path, so a detected event gap parked the pump and froze live render until something unrelated happened to re-arm it. It now re-arms on every leg.
- **Unbounded render-cost growth in long sessions** (QUM-933) — intermediate assistant text blocks were never finalized, so any text block followed by a tool call stayed permanently uncacheable and re-ran the full markdown pipeline every frame. Measured ~88× improvement (51 ms → 0.6 ms per frame) and stray streaming cursors dropped to zero.
- **Assistant output stranded on session restart** (QUM-986) — an in-flight assistant item is now finalized at the restart chokepoint.
- **Child processes could outlive their parent** (QUM-896) — `Pdeathsig`/`Setpgid` and process-group kill were restored in the Claude launcher after a regression.
- **`[auto-continue]` markers on reload** (QUM-924) — replayed transcripts render them as the ↻ marker again via a shared prefix constant.

## [v0.5.3] - 2026-07-10

### Added

- **`/attach` for local images** (QUM-860) — attach local image files to a turn with `/attach <path...> "optional prompt"`. Media type is content-sniffed against a jpeg/png/gif/webp allowlist, non-regular files are rejected, and limits are 10 MiB per file on disk plus a 90 MiB ceiling on the combined base64-encoded payload; each file shows a `📎 name · type · size` chip in the transcript. Local base64 files only — URLs, browser upload, and inline image rendering are out of scope, and API-side rejections (e.g. oversized dimensions) surface as an error.
- **Inline `/`-command popover** (QUM-864) — typing `/` opens a compact suggestion list just above the input instead of taking over the screen. It filters as you type, ↑/↓ move the highlight with wrap, Enter selects, Esc dismisses, and it auto-hides on the first space. Composited onto the frame rather than modal, so it never blocks scrolling, the mouse, or typing, and Esc dismisses the popover instead of interrupting your turn.
- **First-party `/compact`** (QUM-865) — `/compact` is now a recognized command whose full line is forwarded verbatim to the backend, capability-gated so it only appears and only routes when the active backend advertises the feature. When compaction lands, sprawl renders its own one-line banner (e.g. `🗜 context compacted · 236k→9k tok · manual`) and suppresses the enormous built-in compaction-summary bubble in both live and replay views, for manual and automatic triggers alike.
- **Compaction progress and failure feedback** (QUM-867) — a `compacting…` status-bar label covers the in-progress window, which can run minutes on a large context, and a failed `/compact` raises an error toast carrying the backend's reason instead of appearing to do nothing. Success stays quiet because the banner already reports it.

### Changed

- **Slash commands route at submit time** (QUM-863) — typed and pasted commands dispatch through one exact-token matcher in the submit handler, so a pasted `/usage` behaves identically to a typed one. Matching is exact on the leading token rather than prefix or fuzzy, so an ordinary prompt that merely starts with a slash (`/etc/hosts is broken`) still passes through, and a default-consume guard keeps an unwired command from leaking to the backend.
- **`report_status(complete|failure)` teardown deferred to turn-end** (QUM-866) — an agent reporting complete is no longer torn down synchronously mid-turn, so a follow-on `send_message` or trailing text in the same turn is no longer silently cut off. The new reusable `StopAfterTurn` primitive subscribes to the runtime event bus before checking in-turn state, waits for a genuine turn-end event, and has a runaway timeout; an agent already idle at report time still tears down immediately, so nothing is retained longer than before.
- **Observed-agent footer keys on the agent's own turn state** (QUM-861) — the footer previously read a subtree-rolled-up in-turn flag, so an idle manager displayed "working" purely because a descendant was mid-turn. Tree rows keep rolled-up activity indication and blink.

### Removed

- **Full-screen command palette** (QUM-864) — superseded by the inline popover. Visible consequence: `/` on an empty input no longer intercepts the keystroke to open a modal; the slash is inserted literally and the popover appears purely as a function of the input text.

## [v0.5.2] - 2026-07-07

### Fixed

- **Queued prompt invisible on a fresh session** (QUM-854) — the empty-session placeholder was gated on committed-item count alone, so the very first prompt typed in a brand-new session stayed hidden until the backend echo arrived. It now also accounts for pending entries, so a first prompt — and a first-frame system notification — renders immediately.
- **Raw markdown dump in the auto-continue marker** (QUM-855) — when a background sidechain completed, the `↻ auto-continued` marker rendered the sidechain's entire result body verbatim through the unstyled system-text path, painting walls of raw markdown. Only the styled one-line marker is shown now (the result still reaches the model by its own path), and yank output matches.
- **Scroll indicator missed pending content** (QUM-856) — the "new content below" cue counted committed chat items only, so a prompt held pre-echo or a notification drained on the first frame did not cue it while scrolled up. Units are now consistent, so an entry settling from pending into committed cannot cause a spurious flip either.
- **Missing thinking indicator after Ctrl+G send-all-now** (QUM-858) — flushing a queue with Ctrl+G started the turn but left the footer idle until the backend echo returned, a multi-hundred-millisecond window in which the TUI looked hung. The handler now flips Idle→Thinking optimistically (guarded on a non-empty queue, reset on the failure leg), and the durable half routes turn-started events through a reducer that flips Idle→Thinking without stomping Streaming — so autonomous and continuation turns get the indicator too.

### Removed

- **Dead auto-continue summary state** (QUM-857) — internal cleanup following QUM-855; the trigger gate is preserved and the body is simply no longer propagated. No behavior change.

## [v0.5.1] - 2026-07-06

### Added

- **Per-launch model override for the root agent** (QUM-849, QUM-850) — `sprawl enter --model <string>` overrides the root agent's model for that launch only, passed verbatim to the backend. Deliberately free-form rather than an enum, and deliberately not persisted: a plain `sprawl enter` reverts to the default.
- **Per-agent model and operator instructions on spawn** (QUM-849, QUM-851) — `spawn` takes an optional `model` (enum: haiku, sonnet, opus, fable, opus[1m], sonnet[1m]) and an optional `system_prompt` carrying custom identity, personality, or operating instructions. The prompt is appended under a delimited `## Operator Instructions` header and never replaces the built-in role prompt. Both persist on agent state and re-apply on wake; existing agent state migrates on load, and per-type defaults are unchanged.

### Changed

- **Contributor-facing:** the Sprawl Hub design-doc set landed under `docs/design/hub/` and was then re-scoped in a second pass to a single-user MVP. Design only — no hub code ships in this release; the implementation landed in v0.5.4.

### Fixed

- **`retire` left zombie descendants behind** (QUM-852) — retiring a parent now cascades into descendants that are already resolved (complete/killed/faulted/died) instead of stranding them, and the abandon flag propagates into the child recursion rather than being dropped. `retire` also returns the set it actually retired, so the MCP tool no longer claims "and descendants" when it retired none. Blocking behavior for live children is unchanged.

## [v0.5.0] - 2026-06-29

### Added

- **Recall and flush-now for queued prompts** (QUM-824, QUM-838, QUM-845) — in the root session, Ctrl+U cancels every pending prompt and rehydrates the text back into the input box (newline-joined, in submit order), while Ctrl+G cancels them and sends them as one urgent write that supersedes whatever the model was doing. Both are root-only and both are documented in the F1 help overlay alongside "type + Enter while busy queues the message".
- **Type-while-busy queueing** (QUM-828, QUM-831, QUM-832, QUM-833) — keep typing and pressing Enter during a turn: each prompt is written through immediately and rendered as a dimmed bubble that brightens the moment the model consumes it, so more than one prompt can legitimately be in flight. The working spinner is derived from outstanding work rather than turn state alone, so it stays lit while a queue drains and clears only at true idle.
- **`sprawl hooks install` / `sprawl hooks uninstall`** (QUM-842) — adopt the branch-protection guards on any repo. Canonical guard scripts are embedded in the binary; install chains a marker-delimited managed block onto pre-existing hooks rather than clobbering them and detects the default branch (overridable with `--branch`); uninstall removes exactly what was added. Both are idempotent.
- **Bypass-proof protection for the shared main branch** (QUM-808, QUM-837) — a pre-commit guard refuses commits on `main` from non-root agents, and a reference-transaction hook rejects any update to `refs/heads/main` from a non-root agent even under `git commit --no-verify`. The root agent and humans running git directly are unaffected. Backed by hook-independent defenses: an agent whose worktree HEAD sits on `main` is refused at resume/wake, and a stale advertised branch self-heals from the real HEAD with a warning. Auto-installed on every worktree creation.

### Changed

- **Single-stream control path to the backend CLI** (QUM-813, QUM-814, QUM-815, QUM-817, QUM-821, QUM-829) — sprawl's own Go-side message queue and turn loop are gone. User messages, delegated tasks, and system notifications are written straight to the CLI's stdin and tracked by a small outstanding map driven by the CLI's own replay echo. Delivery has one code path instead of several forks, plus an explicit urgency tier (`now` = cancel-and-replace, preempting a running turn; `next` = normal; `later` = delegated tasks that must not jump the line), and turn lifecycle is uniform whether a turn was started by the user or autonomously. Net ~3,300 lines removed across the arc (~9,100 deleted, ~5,800 added), nearly all of it the deleted queue and turn-loop files and the test suites they carried.
- **Idle background-task continuation fires on its own** (QUM-812, closed by QUM-815) — when a background task finishes while the session is idle, the session resumes without a human typing a nudge.
- **Honest, uniform delivery semantics for `interrupt=true`** (QUM-821) — an urgent send routes through the `now` tier and wakes or preempts the recipient, including mid-turn; a bare interrupt is a pure halt and never carries content.
- **Contributor-facing:** hermetic test environments and a pre-commit fix so agent commit pipelines are no longer broken by inherited repo-scoping `GIT_*` vars (QUM-836), plus new live e2e gates and repairs to probes that keyed on the retired `⏳` indicator (QUM-839).

### Fixed

- **Live TUI render was dead** (QUM-826) — streamed assistant output did not appear while it was being produced; the transcript only materialized on resume or replay. Three reducers fed by the event pump failed to re-arm it, parking the pump on the first consumption of every typed turn. This was the headline symptom of the release.
- **Esc mid-turn no longer kills the session** (QUM-827) — it previously surfaced a spurious "Session Error". It now aborts only the model's turn: the session survives, in-flight tool handlers unwind at teardown instead of being cancelled underneath the CLI, and queued prompts stay queued and are still consumed afterwards.
- **Ctrl+G no longer crashes the root session, and its message no longer vanishes** (QUM-830, QUM-838) — the preempted turn is classified as an interrupt rather than an error, a debounce latch makes a double-tap safe, and the flushed message settles into the committed transcript exactly once in consume order.
- **System notifications rendered twice** (QUM-833) — live rendering and resume-rehydration now share one classifier and a uuid-keyed pending zone, so a notification appears once and system-styled, including across a session restart.
- **Parked "complete" agents were unrevivable after a root restart** (QUM-818) — `wake` (and therefore `delegate` and `send_message`) falls back to loading agent state from disk when the in-memory registry has been emptied by a restart. Only retired/retiring still refuse.

### Removed

- **TUI liveness watchdog and its drop seam** (QUM-829) — made redundant by the undroppable-terminal-event guarantee plus gap-detect viewport resync; two mechanisms were racing to recover the same wedge.
- **Single-slot pending-submit UX** (QUM-828) — the old "busy → hold one prompt in a preview slot", the Esc-preempt-and-send path, and the Ctrl+C-recall-into-slot rung are gone, replaced by the always-write queue. The `⏳ N queued` input indicator was retired in QUM-833 in favor of the in-transcript pending zone.
- **Idle-interrupt content-injection machinery** (QUM-821) — superseded by the `now` priority tier.

## [v0.4.0] - 2026-06-11

### Added

- **Activity sparkle indicator** (QUM-796) — an animated accent glyph plus a short status word whenever an agent is non-idle: above the prompt input for the root agent, and as a footer for an observed child pane. It ticks on its own ~250ms clock so it does not invalidate the chat render cache.
- **Transient agent-tree HUD** (QUM-805) — a corner-anchored read-only tree flashes briefly on agent navigation (Ctrl+N/P) and on spawn/retire, highlighting the changed node (including a ghost row for a just-retired agent). Non-modal and self-expiring; `/tree` remains the full browsable view.
- **`/usage` time-range filtering** (QUM-798) — keys 1-5 scope the totals to a time window (all-time by default, recomputed on each open). The CLI gained a matching `--since` on `sprawl usage summary` and `export`, accepting any Go duration plus an `Nd` day suffix (e.g. `24h`, `7d`, `365d`), an absolute RFC3339 timestamp, or `all`.

### Changed

- **Quieter tool-call rows** (QUM-796) — top-level tool calls render as a single inline header `<glyph> ToolName(command preview)` instead of box chrome, with output indented two spaces beneath and a `⎿` trailer on collapsed results. The preview is no longer quoted, and control characters and raw escapes are neutralized so a stray ESC cannot bleed styling past the truncation point.
- **Toasts moved to the lower left** (QUM-804) — anchored just above the input box rather than centered under the header.
- **Breathing pulse on working agents** (QUM-806) — orbital header pills for actively working agents gently pulse.
- **Contributor-facing:** contributor docs now spell out a public/private repo hygiene policy — what must never be committed to a public repo, and where such material belongs instead.

### Fixed

- **Long user messages were not word-wrapped** (QUM-797) — user blocks now wrap per source line with a two-space hang indent aligned under the `›` chevron, preserving explicit newlines. Very narrow terminals fall back to unwrapped output rather than degenerate wrapping.
- **Auto-continue double-fired on one background task** (QUM-807) — a re-observed task notification could inject a second continuation and produce a duplicate turn. Continuations are now idempotent per task id, with unparseable ids falling back to the previous always-continue behavior.

## [v0.3.2] - 2026-06-11

### Added

- **Prompt-history recall on Up/Down** (QUM-774) — with an empty input, Up and Down walk previously submitted prompts, persisted across restarts; while you are typing they are a no-op instead of hijacking the cursor. PgUp/PgDn, Home/End, and the mouse wheel remain the chat-scroll path.
- **Dormant pill for completed agents** (QUM-788) — a `◌` glyph in faint blue, visually distinct from both idle and failed.

### Changed

- **Completed agents are revivable, not terminal** (QUM-786, QUM-787, QUM-789, QUM-790) — `state:complete` now lands an agent in a `complete` status that keeps its session id, worktree, and branch, and `delegate` / `send_message` auto-wake such an agent with no extra flag, so finishing a task no longer means losing the agent. Only a deliberate retire or kill is terminal; `wake` accepts everything else. Existing v0.3.1 state files migrate on first load (to `complete` if the last report was complete, else `faulted`) and are rewritten to disk.

### Fixed

- **Streaming could wedge the TUI mid-turn** (QUM-775) — terminal turn events were droppable, so a lost one left the UI stuck in streaming forever. Terminal events now use a bounded blocking send, a watchdog force-finalizes after 30s of silence, and Esc while idle emits a synthetic interrupt so the UI leaves the streaming state.
- **Validate-failure popup was inescapable** (QUM-609) — a failed post-merge validate pinned a modal that no key would dismiss, forcing Ctrl+C and a full `sprawl enter` restart. Esc and Ctrl+V now dismiss it, and the footer says so.
- **Stale fault toast after retire** (QUM-776) — retiring a faulted agent left its backend-fault toast on screen; the fault is now cleared without falsely firing the recovery banner.
- **Palette commands could act on stale modal state** (QUM-793) — the palette closed asynchronously, so a dispatched command's modal gate sometimes still saw it open and the action was swallowed. Closing is now synchronous within the same update.
- **Four wasted rows and columns around the chat area** (QUM-779) — the viewport was sized four cells smaller than its own layout allocation, leaving a visible dead gap.

## [v0.3.1] - 2026-06-10

### Added

- **`pause` and `wake` MCP tools** (QUM-722, QUM-724) — `pause` stops an agent at its next turn boundary, preserving transcript, worktree, and branch, escalating to a kill after `timeout_seconds` (default 30) and cascading to descendants by default. `wake` replaces `recover` and brings back any offline agent (paused/killed/died/faulted/resume_failed), resuming the prior session by id with a fallback to a fresh session if the cookie is rejected.
- **Wake-on-traffic** (QUM-726) — a `wake_if_offline` flag on `delegate` and `send_message` hands work to an offline child in one call, and the message or task becomes its first inbox item. Without the flag, delivery to an offline agent still errors.
- **Sub-agents that share the parent worktree** (QUM-709, QUM-756) — `spawn` accepts `subagent: true`, and the child reuses the caller's worktree and branch (so `branch` must be omitted), subject to a depth limit of 3 and a caller-type capability gate. `branch` is no longer a required spawn field, and `qa` joined the accepted agent types.
- **Death observability** (QUM-725) — messages to a dead agent route up to its parent instead of vanishing, and the TUI raises a death toast.
- **Token and cost usage tracking** (QUM-368, QUM-721) — per-turn usage is recorded to `.sprawl/logs/usage/<agent>/<session>.ndjson`, with a new `sprawl usage tail|summary|export` (`--agent`, `--follow`, `--last`, `--quiet`; `--by tokens|cost|all`, `--group agent|model|session|day`, `--since`/`--until`; `--format csv|json`) and a `/usage` modal in the TUI. Cost is API-reported and a stderr footer says so, since it does not reflect subscription-plan credits. Per-agent cost fields were dropped from agent state in favor of the log.
- **Supervisor heartbeat and liveness checks** (QUM-730) — a background scan nudges stuck agents with an ephemeral system notification and escalates to the parent after a threshold. Configured by a new `liveness:` block in `.sprawl/config.yaml` (`enabled`, `heartbeat_interval` default 30m, `idle_threshold` default 15m, `tier2_consecutive_ticks`, `escalation_threshold`); enabled by default, and a partial block cannot silently disable it.
- **Incident snapshot hotkey** (QUM-728) — Ctrl+\ writes a forensic bundle to `.sprawl/incidents/<ISO8601>-tui-snapshot/` (goroutine dump, fd list, status, process tree, root-agent process state, recent MCP calls, per-agent activity rates, memory and load). Non-blocking and best-effort per artifact, with a status-bar confirmation or an error toast.
- **`/tree` modal** (QUM-733) — the full agent tree as a centered overlay with Up/Down navigation and Enter to switch the observed agent; each row shows a status dot, type chip, cost tag, and last report.
- **Mouse-wheel chat scrolling** (QUM-731) — mouse capture is on so the wheel scrolls the chat (suppressed while a modal is open). Text selection moves to Shift+drag, tmux copy-mode, or right-click Copy, with no modal toggle.
- **Seeded, persisted accent color** (QUM-704) — first run picks and persists a random accent so separate installs look distinguishable. A persist failure warns but never blocks startup or falls back mid-session.

### Changed

- **Esc-preempt and Ctrl+C-recall for a queued prompt** (QUM-630) — the single-slot queue for typing during a turn already existed (QUM-340); what is new is what you can do with the held prompt. Esc now preempts the in-flight turn and sends it — enqueued before the preempt, so it survives a failed interrupt — and Ctrl+C recalls it into the input, taking over the QUM-576 "reload draft on empty input" affordance that previously sat on Esc.
- **Per-item tool-call spinners** (QUM-732) — replace the retired global ticker with per-item animation scoped to the observed pane, so animation resumes correctly after switching agents away and back.
- **`status` and `peek` documented as explicitly non-waking** (QUM-724) — inspecting an offline or idle agent is a pure read with no lifecycle side effect, so monitoring can never itself revive a subtree.
- **Typing latency in long sessions** (QUM-769) — chat render and viewport output are cached against a revision counter, removing the per-keystroke full re-render. On a 1500-envelope session, chat render went from 10.7ms to 3.7ns and viewport view from 230ms to 61ns, with allocations to zero.
- **Contributor-facing:** new e2e matrix rows and harness work (pause-lifecycle, paused-persistence, wake-live renamed from recover-live, death-observability, wake-on-traffic, subagent-model, sidechain-discovery-smoke, usage), plus a contributor-docs terminology section fixing the agent / sub-agent / sidechain distinction.

### Fixed

- **Stopped agents leaked their backend subprocess and event-bus subscribers** (QUM-727) — a terminal status report flipped state without tearing the runtime down, leaving a live subprocess and four subscribers behind per dead agent. Teardown is now wired into the terminal-report path, and `status` exposes `subprocess_alive`, `eventbus_subscribed`, and `eventbus_sub_count`.
- **Terminal children blocked `retire` and `merge`** (QUM-739) — already-dead children counted as active in the pre-checks, so a parent with a zombie child could be neither retired nor merged. They are now filtered at all three check sites.
- **Turns could hang forever with no terminal event** (QUM-647) — if the event stream closed without a result frame (seen after an interrupted local Bash call) nothing finalized the turn and the TUI stuck. A safety net now publishes a terminal event on channel close.
- **MCP call log lost in-flight entries** (QUM-729) — calls still open at shutdown, and error or panic exits, now flush through a single exit path.

### Removed

- **`recover` MCP tool** (QUM-724) — superseded by `wake`.

## [v0.3.0] - 2026-06-09

### Added

- **Toast notification subsystem** (QUM-649, QUM-651, QUM-701) — bordered floating overlays for transient TUI events. Three wired consumers: recovery (post-resume "recovered N agents"), interrupt (Esc during streaming), and terminal-fault (agent-side error). Ctrl+T dismisses all toasts.
- **`sprawl debug colors`** (QUM-698) — palette × visual-treatment grid viewer. First child under a new `sprawl debug` parent command group reserved for future diagnostics.
- **Live pprof endpoint** (QUM-678) — `--pprof` flag and `SPRAWL_PPROF_ADDR` env on `sprawl enter` expose net/http/pprof at /debug/pprof for live perf inspection.
- **Keyboard scroll in chat** (QUM-653) — PgUp / PgDn / Home / End and Up / Down (when input is empty) scroll the chat region. Mouse capture removed so terminal-native text selection (Cmd+C, tmux copy-mode, Shift+drag) now works without a modal toggle.

### Changed

- **Toast positioning and styling** (QUM-701) — toasts now render as rounded-border boxes, horizontally centered below the SPRAWL header (previously top-right text strips). Stack vertically; remaining toasts shift up when one dismisses. Info toasts track the configured accent (`Palette.Primary`); warning/error toasts keep their respective palette colors.
- **Palette swap** (QUM-700) — `Palette.Accent` moved from ANSI 39 → 51 (cyan); `Palette.Info` moved 51 → 39 (cyan-blue). Keeps `Accent` visually distinct from a user-customized `Primary`.
- **Header strip** (QUM-656, QUM-657, QUM-689, QUM-694, QUM-695) — SPRAWL wordmark + orbital agent tree port. Activity pane removed; tree column gone from main row; `?`-as-help dropped (F1 canonical). Anchor `──●` hidden when the root has no children.
- **ChatList sole render path** (QUM-673, QUM-676, QUM-677, QUM-693) — `internal/tui/viewport.go` (3453 LOC) and the `ViewportModel` facade (340 LOC) deleted. Yank-mode, `activePanel`, and Tab cycling all removed. Single-responsibility chat rendering.
- **Error surfaces** (QUM-680) — `agentops.TerminalAgentError` produces clearer Peek / SendMessage / Retire error messages.
- **`Real.Status` disk fallback** (QUM-682) — uses streaming `ReadActivityTail` instead of slurping the whole activity log.

### Fixed

- **Interrupt toast race-with-self** (QUM-697) — `ConditionDismiss` cleared the interrupt toast in the same event-loop pass it was spawned, so it never rendered. Switched to `TimerDismiss(2s)`.
- **Tree pivot for empty roots** (QUM-686) — root with no children no longer renders a dangling anchor.
- **Transient-label clear rules documented** (QUM-690) — long-standing comment debt resolved.

### Removed

- **`sprawl input-debug`** (QUM-699) — QUM-608-era hidden paste-coalesce diagnostic deleted. `Coalescer.Done()` removed (no other consumers). Helper `isStdinTTY` inlined into `cmd/enter.go`.

### Deprecated

- **Legacy CLI commands now emit deprecation warnings** (QUM-337). Phase 2.1
  of the M13 TUI cutover begins steering humans and agents toward the
  `sprawl_*` MCP tool surface that has matured to cover the full
  agent-callable workflow. Every invocation of the following CLI forms now
  prints a one-line stderr warning naming the MCP replacement; the CLI
  continues to work otherwise (exit code, stdout, and behavior are
  unchanged):

  | CLI form | Replacement |
  | --- | --- |
  | `sprawl spawn` / `sprawl spawn agent` | `spawn` |
  | `sprawl retire` | `retire` |
  | `sprawl kill` | `kill` |
  | `sprawl delegate` | `delegate` |
  | `sprawl messages send` | `send_async` (or `send_interrupt` for the rare urgent case) |
  | `sprawl messages list` | `messages_list` |
  | `sprawl messages read` | `messages_read` |
  | `sprawl messages archive` | `messages_archive` |
  | `sprawl report …` | `report_status` |
  | `sprawl status` | `status` |
  | `sprawl tree` | `status` (or `peek` for one agent) |
  | `sprawl handoff` | `handoff` |
  | `sprawl init` | (no MCP equivalent; `sprawl enter` replaces tmux mode) |
  | `sprawl color` | (no MCP equivalent; slated for deletion) |

  Set `SPRAWL_QUIET_DEPRECATIONS=1` in any environment that intentionally
  exercises the legacy path (CI scripts, tests, etc.) to suppress the
  warning. The three remaining tmux-path e2e scripts
  (`scripts/test-init-e2e.sh`, `scripts/test-notify-e2e.sh`,
  `scripts/test-notify-tui-e2e.sh`) already set this and will be removed in
  Phase 2.5 alongside the tmux machinery.

  **Soak agreement (per QUM-314):** when zero agent runs hit a deprecation
  warning over a 7-day window in production use, the next phase (2.2) may
  begin removing the tmux-only machinery, followed by deletion of the CLI
  forms in 2.3.

  The `/handoff` skill has been migrated from the `sprawl handoff` CLI to
  the `handoff` MCP tool to stop self-inflicted warning noise from
  agent-prompted CLI calls.

