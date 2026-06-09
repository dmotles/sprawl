# QUM-608 — Paste pipeline deep-dive: why bracketed paste fails in sprawl on tmux 3.2a (but works in claude code)

> **2026-06-09 note (QUM-699):** the `cmd/input_debug.go` diagnostic command
> referenced throughout this investigation was deleted after QUM-608 shipped.
> Line/file refs below are historical — recover from `git log` if needed.

**Status:** research-only. No code changes.
**Branch:** `dmotles/qum-608-paste-pipeline-research`.
**Investigated by:** ghost (research agent), 2026-05-20.
**Predecessors:** `paste-pipeline-architecture.md`, `paste-render-cadence.md`,
`qum-432-stripped-bracketed-paste-plan.md`, `paste-input-ux-synergy.md`.

---

## TL;DR

Three actions, in order. Be opinionated:

1. **Build a stdin coalescer (Path 2)** that wraps `os.Stdin` *before* Bubble Tea's
   ultraviolet reader. Buffer reads for ~5 ms after first byte, emit as one
   `Read()` return. This makes paste behave correctly **regardless of whether
   bracketed paste engaged**, fixes the typewriter UX on tmux 3.2a *and* every
   other environment where bracketed-paste enable is dropped, and is invisible
   when bracketed paste *does* work (the markers still parse). It is the only
   path that meets the brief's "works regardless of tmux version" bar.
2. **Add a startup probe (Path 3 lite)** — detect tmux server version via
   `tmux -V` (or by reading `$TMUX` socket and asking) and emit a one-line
   warning when `< 3.4`. Don't fail-fast; just inform.
3. **Do *not* rely on Path 1.** Bubble Tea v2 currently exposes no
   `ProgramOption` that makes bracketed-paste reliable on tmux 3.2x. The root
   cause is in the byte-level interaction between Bubble Tea's startup
   sequence and tmux 3.2a's input parser, and is not something sprawl can fix
   from above the library boundary without forking Bubble Tea.

Before implementing **anything**, run the **`script(1)` capture** in §7. That
single empirical step resolves the largest remaining unknown (is `?2004h`
actually leaving sprawl?) and lets us pick between Path 2 (definitely
necessary) and Path 1 (a long-shot but cheap if validated).

---

## 1. Why claude code (Ink) works on tmux 3.2a but sprawl (Bubble Tea v2) doesn't

The two TUI libraries emit a very different startup-init byte sequence to the
PTY. The byte-level difference is the entire story.

### 1.1 What Ink writes

Ink (`vadimdemedes/ink`, the framework claude code is built on) writes
**exactly one** mode-setting sequence to enable paste handling:

> ```js
> if (isEnabled) {
>   if (bracketedPasteModeEnabledCount.current === 0) {
>     stdout.write('[?2004h');
>   }
> ```
> — `ink/src/components/App.tsx` lines 606–617
> ([source](https://github.com/vadimdemedes/ink/blob/master/src/components/App.tsx))

Ink does **not** emit:

- alt-screen enter (`?1049h`),
- mouse-tracking modes,
- focus-event mode (`?1004h`),
- xterm modifyOtherKeys (`CSI > 4 ; 2 m`),
- Kitty keyboard protocol (`CSI > 1 u`),
- synchronized-output query (`CSI ? 2026 $ p`),
- unicode-core query (`CSI ? 2027 $ p`).

Source confirms this: an exhaustive search of `App.tsx` finds *no* other ANSI
mode-set sequences (see WebFetch summary in §7).

### 1.2 What Bubble Tea v2 writes

Bubble Tea v2 (`charm.land/bubbletea/v2 @ v2.0.3`, which sprawl uses; v2.0.6 is
identical in the relevant region) emits a **much longer** init script, in this
order, on the very first render:

1. `CSI ? 2026 $ p` + `CSI ? 2027 $ p` — synchronized-output / unicode-core
   *queries* (DECRQM), written from `tea.go:1113-1114` BEFORE the renderer
   starts:
   > ```go
   > if !p.disableRenderer && shouldQuerySynchronizedOutput(p.environ) {
   >     p.execute(ansi.RequestModeSynchronizedOutput +
   >         ansi.RequestModeUnicodeCore)
   > }
   > ```
   > — `bubbletea/v2@v2.0.3/tea.go:1109-1115`

2. The cursed renderer's first `flush()` then writes, in this order
   (`cursed_renderer.go:320-389`):

   1. `ESC[?1049h` — enter alt screen (sprawl sets `v.AltScreen = true` in
      `internal/tui/app.go:1797`),
   2. `ESC[?2004h` — bracketed paste,
   3. `ESC[?1004h` — focus events (only if `ReportFocus`; sprawl does **not**
      set this, so skipped),
   4. `ESC[?1000h ESC[?1006h` — mouse button + SGR (sprawl sets
      `MouseModeCellMotion` in `app.go:1798`),
   5. `ESC[>4;2m` — `ansi.SetModifyOtherKeys2`,
   6. `ESC[>...u` — `ansi.KittyKeyboard(flags,1)`.

   > ```go
   > if !s.lastView.DisableBracketedPasteMode {
   >     _, _ = s.scr.WriteString(ansi.SetModeBracketedPaste)
   > }
   > // …
   > _, _ = s.scr.WriteString(ansi.SetModifyOtherKeys2)
   > kittyFlags := keyboardEnhancementsFlags(s.lastView.KeyboardEnhancements)
   > _, _ = s.scr.WriteString(ansi.KittyKeyboard(kittyFlags, 1))
   > ```
   > — `cursed_renderer.go:115-139` (and the symmetric `flush()` path at
   > 332-389).

So the *delta* sprawl-sends versus what Ink sends, on tmux 3.2a, is:

- `CSI ? 2026 $ p` (DECRQM 2026)
- `CSI ? 2027 $ p` (DECRQM 2027)
- `ESC[?1049h` (alt-screen)
- `ESC[?1000h ESC[?1006h` (mouse)
- `ESC[>4;2m` (modifyOtherKeys2)
- `ESC[>1u` (Kitty keyboard)

Any one of these landing into tmux 3.2a's input parser in a way that disturbs
its tracking of `MODE_BRACKETPASTE` for the pane — *or* its propagation of
that mode to the outer terminal — is sufficient to break paste.

### 1.3 What tmux actually does with these bytes

The paste pipeline through tmux is:

```
sprawl ──?2004h──▶ tmux input.c (pane parser) ──MODE_BRACKETPASTE set on pane──▶
tmux tty.c tty_update_mode ──?2004h to outer tty via TTYC_ENBP─▶ coder term
coder term ──ESC[200~text ESC[201~── tmux tty-keys.c ─▶ input_key ─▶ pane PTY
```

Confirmed against tmux 3.2a source:

- The pane-side parser (`input.c`) recognises and stores `MODE_BRACKETPASTE`.
- `tty_update_mode` in tmux 3.2a `tty.c` *does* propagate it outward:
  > ```c
  > if (changed & MODE_BRACKETPASTE) {
  >     if (mode & MODE_BRACKETPASTE)
  >         tty_putcode(tty, TTYC_ENBP);
  >     else
  >         tty_putcode(tty, TTYC_DSBP);
  > }
  > ```
  > — `tmux/tty.c@3.2a` (via WebFetch).
- The `TTYC_ENBP`/`TTYC_DSBP` codes are defined via the "bpaste" feature in
  `tty-features.c`:
  > ```c
  > static const char *tty_feature_bpaste_capabilities[] = {
  >     "Enbp=\\E[?2004h", "Dsbp=\\E[?2004l", NULL };
  > ```
- The feature is auto-enabled for `mintty`, `tmux*`, `iTerm2.app`, and
  `XTerm` via `TTY_FEATURES_BASE_MODERN_XTERM`. Coder's web terminal almost
  certainly identifies via `xterm-256color`, which maps to the `XTerm` feature
  group (this is the reason claude code works at all on this stack).
- On the *return* path, tmux 3.2a forwards `KEYC_PASTE_START`/`KEYC_PASTE_END`
  via `input_key()` **unconditionally** — there is no mode gate (this gate was
  added in tmux 3.4: `if ((key == KEYC_PASTE_START || key == KEYC_PASTE_END) &&
  (~s->mode & MODE_BRACKETPASTE)) return (0);`).

So in tmux 3.2a, the paste sequence reaches the pane **iff**:

1. The outer terminal has `?2004h` enabled (i.e. the outer terminal is in
   bracketed-paste mode), and
2. tmux's `tty-keys.c` correctly parses `ESC[200~...ESC[201~` from the outer.

Condition (1) hinges on tmux having written `?2004h` outward, which requires
`MODE_BRACKETPASTE` to have been set on the focused pane via `tty_update_mode`.

### 1.4 The likely byte-level breakage

Because Ink writes only `?2004h` while Bubble Tea writes a salvo of mode
sequences around it, the most credible breakdowns are:

**Candidate A — `?1049h` (alt-screen) + tmux 3.2a's mode tracking.** Bubble
Tea writes `?1049h` *before* `?2004h`. On `?1049h` entry, tmux 3.2a swaps to
the pane's alt-screen object; mode flags are per-screen. `?2004h` then sets
`MODE_BRACKETPASTE` on the *alt screen*. `tty_update_mode` is only called from
specific call-sites (screen redraws, focus changes). If the order causes
tty_update_mode to see "no net change" between the two writes (because both
hit the same flush), the outer mode is never re-emitted. There is no evidence
in code that this *is* broken in 3.2a, but it's the most natural place to look.

**Candidate B — DECRQM 2026/2027 trips tmux 3.2a's input state.** Bubble Tea
sends `CSI ? 2026 $ p` and `CSI ? 2027 $ p` *before* anything else
(`tea.go:1113`). tmux 3.2a's `input.c` doesn't recognise these DECRQM
sequences. It *should* silently drop them, but a parser corner case
(intermediate `$` byte + private `?` prefix) could leave it in a state that
mis-handles the following `?2004h`. This is plausible but unverified.

**Candidate C — `CSI > 4 ; 2 m` or `CSI > 1 u` aborts pane mode propagation.**
tmux 3.2a's `input_csi_table` has no entry for the `>` private-CSI variants of
`m`/`u` (Stack Overflow / claude-code issue #29129 confirms this for `CSI > u`
specifically). These should be dropped as "unknown" but the `>` intermediate
byte can confuse older `input.c` state machines. Again — plausible, unverified.

**Whichever candidate is true, the practical implication is the same:** the
breakage is in the *interaction* of Bubble Tea's specific init salvo with tmux
3.2a's input parser. Distinguishing A/B/C requires `script(1)` capture
(§7) plus a stripped-down test program that emits only `?2004h` and observes
whether tmux 3.2a forwards paste markers in that case.

### 1.5 Why tmux 3.4 fixes it

Two big things changed:

- `CHANGES from 3.3a to 3.4`: *"Have tmux recognise pasted text wrapped in
  bracket paste sequences, rather than only forwarding them to the program
  inside."* ([source](https://raw.githubusercontent.com/tmux/tmux/3.4/CHANGES))
  This means tmux 3.4 has a more robust paste-marker recognition path.
- tmux 3.4 adds `if ((key == KEYC_PASTE_START || key == KEYC_PASTE_END) &&
  (~s->mode & MODE_BRACKETPASTE)) return (0);` in `input-keys.c` —
  i.e. tmux 3.4 *gates* the forward on the pane having BRACKETPASTE on. This
  isn't a fix for our symptom (it actually makes things stricter), but the
  parser-state cleanups around it likely repair whatever candidate A/B/C is
  the real cause.

Between 3.2a and 3.4 there are also significant rewrites of `tty.c` mode
propagation (most visible in the 3.3 changelog around DEC mode reporting:
`Add support for DECRQSS SP q, DECRQM ?12, DECRQM ?2004 ?1004 ?1006`). The 3.3
release notes are otherwise dominated by other features; nothing else jumps
out as paste-specific, but the parser rewrite is the load-bearing change.

---

## 2. Path 1 — fix bracketed paste via Bubble Tea (low confidence)

### 2.1 What's available via `ProgramOption`

Exhaustive read of `bubbletea/v2@v2.0.3/options.go`:

```
WithContext, WithOutput, WithInput, WithEnvironment,
WithoutSignalHandler, WithoutCatchPanics, WithoutSignals,
WithoutRenderer, WithFilter, WithFPS, WithColorProfile,
WithWindowSize
```

**There is no `WithBracketedPaste`, no `WithEscTimeout`, no
`WithoutKittyKeyboard`, no `WithoutModifyOtherKeys`, no
`WithoutSynchronizedOutputQuery`.** Bracketed paste enabling is governed by
the per-render `tea.View.DisableBracketedPasteMode` boolean
(`tea.go:172-173`), and the *other* problematic sequences (Kitty kbd,
modifyOtherKeys, sync-output query) are hard-coded.

The `KeyboardEnhancements` field on `tea.View` controls the *flags* passed to
`ansi.KittyKeyboard(flags, 1)` but does **not** suppress the enable write
itself. There is no clean knob.

### 2.2 Could we suppress the offending sequences via `WithInput`/`WithOutput`?

`WithOutput(io.Writer)` lets us wrap stdout. We could intercept and *strip*
the kitty / modifyOtherKeys / DECRQM bytes before they reach tmux. This is
the cleanest "fix tmux 3.2a from inside sprawl" option, *if* candidate B or C
is the true cause.

Cost: ~50 LOC writer wrapper that pattern-matches the offending byte
sequences. Risk: brittle (Bubble Tea v2.0.x → v2.0.y could add new init
sequences); doesn't help if candidate A is the cause; doesn't help in other
environments where paste markers are missing for unrelated reasons (e.g.
SSH-without-tmux on a non-bracket-paste-capable terminal).

### 2.3 Could we re-emit `?2004h` later, after the salvo settles?

We could `WithFilter` and on the first `WindowSizeMsg`, write
`ansi.SetModeBracketedPaste` directly to stdout. That gives tmux a second
chance to set the pane mode after the init salvo. This is a one-line
experiment — worth trying behind the `script(1)` capture in §7 to see if it
moves the needle.

### 2.4 Verdict

Path 1 is a *possible* mitigation if candidate B/C turns out to be the cause
and the byte-pattern is clean enough to strip. But it does **not** meet the
brief's "works regardless of tmux version" bar, because:

- It doesn't help on tmux 3.0/3.1 (older).
- It doesn't help in environments where paste markers are dropped for other
  reasons (mosh, certain ssh client configs, tmux nested in tmux).
- It still leaves a serial-Update bottleneck: even when paste markers arrive,
  *any* future regression in the marker pipeline reverts to typewriter UX.

If anything, treat Path 1 as a possible **filing-upstream candidate**: the
authoritative fix would be a Bubble Tea PR that introduces an option to
suppress the kitty/modifyOtherKeys/DECRQM emission, gated on environment
detection (e.g. `TMUX` set and `tmux -V` < 3.4).

---

## 3. Path 2 — custom input adapter (recommended)

### 3.1 Architecture

Insert a **byte-level coalescing reader** between `os.Stdin` and Bubble Tea.
Bubble Tea v2 supports this directly via `WithInput(io.Reader)`:

> ```go
> func WithInput(input io.Reader) ProgramOption {
>     return func(p *Program) {
>         p.input = input
>         p.disableInput = input == nil
>     }
> }
> ```
> — `bubbletea/v2@v2.0.3/options.go:40-45`

Bubble Tea's reader is `ultraviolet.TerminalReader`. Its `sendBytes` loop
(`ultraviolet/terminal_reader.go:121-135`) does one `Read(readBuf[:])` per
iteration into a 4096-byte buffer and forwards whatever it gets:

```go
const readBufSize = 4096
func (d *TerminalReader) sendBytes(ctx context.Context, readc chan []byte) error {
    for {
        var readBuf [readBufSize]byte
        n, err := d.r.Read(readBuf[:])
        // …
        case readc <- readBuf[:n]:
    }
}
```

The reader then runs `eventScanner.scanEvents()` over the accumulated buffer
with a `DefaultEscTimeout = 50 * time.Millisecond` window
(`terminal_reader.go:25-27`). **A single `Read` returning the whole paste
body would let the scanner identify the bracketed-paste markers (if present)
in one go, or — critically — see a large blob of UTF-8 text and emit it as a
single text event in the absence of markers.** Look at the existing decoder
path:

> ```go
> if d.paste != nil { // bracketed paste in progress
>     // accumulate every following event's bytes into d.paste
>     // until PasteEndEvent is seen.
> }
> ```
> — `ultraviolet/terminal_reader.go:289-365`

The wrapper sits at the **read syscall** boundary, so it works regardless of
whether bracketed paste enabled. Concretely:

```
os.Stdin ─▶ coalescingReader.Read(buf) ─▶ ultraviolet.TerminalReader ─▶
            └─ on first byte, peek/poll for ~5ms,
               return everything accumulated as a single slice
            ─▶ event scanner ─▶ Bubble Tea PasteMsg OR a synthetic
                                "BurstMsg" carrying the raw bytes
```

### 3.2 The reader contract

```go
// stdinCoalescer wraps an underlying *os.File (typically os.Stdin) and
// coalesces tight bursts of bytes into a single Read return.
//
//  - When the underlying fd has data, Read() returns whatever is immediately
//    available, up to len(p).
//  - If the *first* read returns >= burstThreshold bytes (e.g. 64), keep
//    reading non-blockingly for up to coalesceWindow (e.g. 5ms) and append
//    until the underlying fd would block.
//  - Never delay if the first read returns a small amount (1–63 bytes) — a
//    typed keystroke or small escape sequence should pass through immediately.
type stdinCoalescer struct { fd int; … }
func (c *stdinCoalescer) Read(p []byte) (int, error)
```

Implementation primitives:

- Use `golang.org/x/sys/unix.SetNonblock(fd, true)` once at construction.
- Use `unix.Poll([]unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}, …)` to
  drain without blocking.
- Make `burstThreshold` and `coalesceWindow` tunable; reasonable defaults are
  64 bytes / 5 ms based on typical paste burst characteristics (the
  `paste-render-cadence.md` doc gives a ~10ms median inter-byte interval; 5ms
  is below that gap so a normal keystroke won't be coalesced).

### 3.3 Plug-in point in sprawl

`cmd/enter.go:202` and `cmd/input_debug.go:297` both construct
`tea.NewProgram(model)` with no `WithInput`. Add:

```go
in, cleanup, err := newCoalescingStdin()
if err != nil { /* fall back to os.Stdin */ }
defer cleanup()
opts = append(opts, tea.WithInput(in))
```

`newCoalescingStdin()` lives in a new package `internal/inputio/` (no existing
package fits cleanly). It MUST handle:

- TTY detection (only coalesce if `term.IsTerminal(0)`; otherwise pass-through
  for test/CI).
- Cancellation: ultraviolet wraps in `muesli/cancelreader.NewReader`
  (`ultraviolet/terminal_reader.go:18`); the coalescer must satisfy the
  `io.Reader` contract such that `cancelreader` can still interrupt it. The
  cleanest implementation has the coalescer's `Read` be cancellable via a
  pipe/eventfd.

### 3.4 Invariants we must preserve

The wrapper coalesces *bytes*, not *events*. Bubble Tea / ultraviolet's
decoder still parses escape sequences and produces events. So we automatically
preserve:

- Key ordering (a coalesced 200-byte chunk parses to the same event stream as
  if it had arrived in 200 separate `Read`s — `eventScanner.scanEvents` is
  byte-position-deterministic).
- Bracketed paste markers (when present, the scanner emits one `PasteEvent`).
- Focus events, mouse events — these are short sequences usually emitted
  alone; they fall below the `burstThreshold` and pass through unchanged.
- Ctrl-C and other small interrupts — never coalesced because they don't meet
  the burst threshold.

The risky case is a paste that *also contains a Ctrl-C* mid-stream — the
coalescer would batch the Ctrl-C into the burst, delivered 5ms late. This is
fine UX (5ms is imperceptible) and is *better* than today's behaviour, which
would also deliver the Ctrl-C late (after 3 seconds of typewriter Updates).

### 3.5 Why this is the right answer

- Works regardless of tmux version (3.0, 3.2a, 3.4 — all collapse the burst to
  one Read return; if markers arrive, great; if not, the absence-of-markers
  case still gets one event).
- Works regardless of whether the user is in tmux at all (e.g. raw SSH, mosh,
  vscode integrated terminal).
- Works without modifying Bubble Tea or ultraviolet.
- Plays well with the **existing** `tea.PasteMsg` handler in
  `internal/tui/app.go:324-342` (per `paste-pipeline-architecture.md`).
- Is forward-compatible with any future Bubble Tea release that adds *more*
  init sequences.
- The remaining "stripped bracketed paste" case (where the burst arrives as
  one fat KeyPressMsg sequence) is the only remaining failure mode, and it
  can be detected at the event-stream layer (a single `Read` returning N
  bytes of UTF-8 → emit synthetic PasteMsg if no markers).

If we want to go one step further, we can emit our **own** synthetic
`PasteMsg` from the wrapper when a fat burst arrives without markers — that
makes the existing `PasteMsg` codepath in `app.go` handle 100% of pastes
uniformly. This is the "Option C+" referenced in `paste-pipeline-architecture.md`.

### 3.6 Estimated effort

- `internal/inputio/coalescer.go`: ~200 LOC + tests.
- Plug-in at `cmd/enter.go:202` + `cmd/input_debug.go:297`: ~10 LOC.
- Optional synthetic-`PasteMsg` filter via `tea.WithFilter`: ~30 LOC.
- E2E test under tmux 3.2a sandbox: extend `scripts/tui-testing` harness.

**Estimate: 1–2 days for an engineer; 0 risk of regressing tmux 3.4 behaviour.**

---

## 4. Path 3 — accept the limitation, require tmux >= 3.4

Detect `$TMUX` is set, run `tmux -V`, parse the version, fail-fast (or warn)
if `< 3.4`. Costs:

- We turn away users on stable Debian (bookworm ships tmux 3.3a as of writing,
  so this is *just* borderline), and any user on a Coder VM that ships 3.2a.
- We don't fix paste in non-tmux environments that strip markers (some SSH
  setups, mosh fallback, some web terminals).

Gains:

- ~50 LOC of detection + a clean error message.
- Zero ongoing maintenance.

This is the right answer **only** if Path 2 is somehow infeasible. It is
not — Bubble Tea's `WithInput` is a stable, documented API.

---

## 5. Comparison and recommendation

| Path | Time to ship | Risk | User impact | Coverage |
|---|---|---|---|---|
| 1 (Bubble Tea options + sequence-stripping output wrapper) | 0.5–1d | Medium (brittle to BT version bumps) | Helps tmux 3.2a only IF candidate B/C is the cause | tmux ≥ some version that supports `?2004h` at all |
| 1b (file upstream Bubble Tea PR) | weeks (review cycle) | Low | Helps eventually | Same as 1 |
| **2 (custom input coalescer)** | **1–2d** | **Low** | **Fixes paste UX universally** | **All tmux versions, raw SSH, mosh, vscode** |
| 3 (require tmux ≥ 3.4) | 0.5d | Low | Turns away tmux<3.4 users; no paste fix elsewhere | tmux ≥ 3.4 only |

**Recommendation: Path 2, with Path 3 as a co-shipped "informational warning"
when tmux < 3.4 is detected (because the user is on a brittle stack and we
should tell them, even if our coalescer compensates).**

Do **not** pursue Path 1 as the primary fix. It is a candidate for an upstream
Bubble Tea contribution if the Charm folks want to support tmux 3.2x natively.

---

## 6. Open questions (the empirical Phase 0)

Each can be answered in <30 minutes by a human with shell access to the
affected tmux 3.2a box.

### 6.1 Is sprawl actually emitting `?2004h`?

**Recipe:**

```sh
# In a tmux 3.2a pane:
script -q -c './sprawl input-debug' /tmp/sprawl-stdout.log
# Paste anything, then Ctrl+C to exit.
od -c /tmp/sprawl-stdout.log | grep -E '\?2004h|\?2004l|\?1049|\?1004|>4;2m|>1u' | head
# Or, more directly:
grep -aP '\x1b\[\?2004h' /tmp/sprawl-stdout.log && echo "Bubble Tea IS emitting ?2004h"
```

**Decision tree:**

- If `?2004h` is **absent** from sprawl's stdout → bug is in Bubble Tea's
  emit path or `cursed_renderer.flush` is short-circuiting. Investigate
  Bubble Tea source further; possibly file upstream.
- If `?2004h` is **present** → bytes leave sprawl correctly, tmux 3.2a is
  not propagating them. Move to §6.2.

### 6.2 Is the outer terminal getting `?2004h` from tmux 3.2a?

Hardest to capture (needs the *outer* tty output, which is not in script(1)'s
scope by default). Two options:

- Run sprawl outside tmux on the same outer terminal and confirm pastes
  work — this isolates whether the outer terminal supports bracketed paste
  at all.
- Run with `TERM=tmux-256color` (tmux's own terminfo, which always has
  `Enbp`) and observe whether behaviour changes. **Note:** changing TERM may
  change *other* things; this test is suggestive but not conclusive.

### 6.3 Does a minimal Bubble Tea program have the same problem?

Build the tiniest possible Bubble Tea v2 program: no alt-screen, no mouse, no
modifyOtherKeys (set `KeyboardEnhancements` to all-zero), and observe whether
paste markers arrive. If a no-alt-screen, no-mouse Bubble Tea reaches
PasteMsg correctly on tmux 3.2a, then alt-screen or mouse is the trigger
(candidate A). If even the bare Bubble Tea fails on tmux 3.2a but Ink works
in the same pane, then the trigger is one of `>4;2m`, `>1u`, or the DECRQM
queries (candidates B/C). This narrows Path 1 dramatically.

### 6.4 Does emitting `?2004h` ourselves *after* the init salvo recover paste?

Add to sprawl: after `WindowSizeMsg` first arrives, write
`\x1b[?2004h` directly to stdout, then test. If this single line fixes
paste on tmux 3.2a, the problem is *purely* an ordering/timing issue inside
tmux's mode tracking and Path 1c becomes very attractive (one-line fix).

### 6.5 GitHub issues to track

These are the most-relevant existing threads — file or comment on them rather
than opening duplicates:

- [bubbletea#1014](https://github.com/charmbracelet/bubbletea/issues/1014) —
  Proposal: use x/input to handle input events (umbrella for the v2 input
  rewrite that introduced PasteMsg).
- [bubbletea#1178](https://github.com/charmbracelet/bubbletea/issues/1178) —
  `tea.KeyboardEnhancementsMsg` is never received in `Update` in `tmux`.
  Closed; related class of problem.
- [tmux#280](https://github.com/tmux/tmux/issues/280) — Bracketed Paste mode
  should be set independently for each pane and window. Old, indicative of
  the per-pane mode-tracking history.
- [claude-code#29129](https://github.com/anthropics/claude-code/issues/29129)
  — Vi mode escape key has 50ms hardcoded delay in tmux — kitty keyboard
  protocol negotiation silently swallowed. Direct evidence that tmux's input
  parser silently drops `CSI > 1 u`.

---

## 7. Appendix — citations table

| Claim | Source | Lines |
|---|---|---|
| Bubble Tea v2 writes `?2004h` on first render | `charm.land/bubbletea/v2@v2.0.3/cursed_renderer.go` | 115–116, 332–338 |
| Bubble Tea v2 writes `>4;2m` (modifyOtherKeys2) | same | 134–136 |
| Bubble Tea v2 writes `>1u` (Kitty kbd) | same | 138–139 |
| Bubble Tea v2 queries sync-output / unicode-core | `charm.land/bubbletea/v2@v2.0.3/tea.go` | 1109–1115 |
| ultraviolet decodes `200~`/`201~` as paste markers | `ultraviolet@v0.0.0-20260416161146/decoder.go` | 558–563 |
| ultraviolet read loop is 4096 bytes, `DefaultEscTimeout=50ms` | `ultraviolet/terminal_reader.go` | 25–27, 117, 121–135 |
| `WithInput` accepts arbitrary `io.Reader` | `bubbletea/v2@v2.0.3/options.go` | 36–45 |
| Ink writes only `?2004h` for paste | `vadimdemedes/ink` `src/components/App.tsx` | 606–617 |
| tmux 3.2a `tty_update_mode` propagates `MODE_BRACKETPASTE` | `tmux/tty.c@3.2a` | (WebFetch) |
| tmux `bpaste` feature defs (Enbp/Dsbp) | `tmux/tty-features.c@master` | (WebFetch) |
| tmux 3.4 adds `MODE_BRACKETPASTE` gate on input-keys forward | `tmux/input-keys.c@3.4` | (WebFetch) |
| tmux 3.4 CHANGES: "recognise pasted text wrapped in bracket paste sequences" | `tmux/CHANGES@3.4` | (WebFetch) |
| Sprawl uses AltScreen + MouseModeCellMotion | `internal/tui/app.go` | 1797–1798, 1887–1893 |
| Sprawl plugs Bubble Tea at `cmd/enter.go:202` and `cmd/input_debug.go:297` | sprawl tree | — |

---

## 8. Reflections (per the agent contract)

**Surprising / unexpected:**

- The differentiator between Ink and Bubble Tea v2 turns out to be how
  *much* each library writes on startup, not *what* their paste path looks
  like. Both decode `ESC[200~` correctly; both want PasteMsg. Bubble Tea
  just emits a salvo of mode-setting bytes that includes sequences tmux 3.2a
  isn't built for. This shifts my prior — I came in expecting a Bubble Tea
  bug, and I'm leaving thinking it's an unfortunate tmux-3.2a + Bubble-Tea-v2
  *interaction* with no clean attribution.
- The 3.4 changelog line *"Have tmux recognise pasted text wrapped in bracket
  paste sequences, rather than only forwarding them to the program inside"*
  was initially confusing — it sounds like 3.4 added what 3.2a already did.
  Reading the input-keys.c diff clarified: pre-3.4 forwarded markers
  unconditionally; 3.4 added the BRACKETPASTE-mode gate and rebuilt the
  input parser around it. The "recognise" wording refers to tmux's own
  consumption of pastes (e.g. for `tmux paste-buffer`-like flows), not the
  outer-to-pane forwarding pipeline relevant to us.
- I was prepared to find a `WithBracketedPaste` option in Bubble Tea v2.
  There isn't one. The library treats paste as a "always on, configured per
  View" feature with no escape hatch for stripping the supporting sequences.

**Open questions worth resolving with more time:**

- Which of candidates A/B/C in §1.4 is the *actual* root cause? The Phase 0
  recipe in §6 answers this in <30 minutes of empirical work, but I didn't
  run it (no terminal access from this agent context).
- Is there a *single* Bubble Tea-side knob (e.g. emitting `?2004h` again
  after the first `WindowSizeMsg`) that recovers paste on tmux 3.2a? §6.4
  proposes the experiment.
- What does coder's web terminal set as TERM on the outer side? This affects
  whether tmux's bpaste feature auto-enables.

**What I'd investigate next:**

1. Run the §6.1 + §6.3 + §6.4 recipes back-to-back; ~1 hour of human time.
2. If §6.4 succeeds, prototype Path 1c (re-emit `?2004h` from sprawl) — that's
   a 30-minute change that could ship before the coalescer is built.
3. Build the §3 coalescer behind a feature flag and ship Path 2 + Path 3 lite
   together. Keep §6.4's mitigation in if it helps even slightly.
4. File a Bubble Tea upstream issue with the candidate-narrowed data,
   regardless of whether we ship Path 2 — this benefits every Bubble Tea v2
   user stuck on tmux < 3.4.

---

## 9. Sources

- [tmux CHANGES (3.4)](https://raw.githubusercontent.com/tmux/tmux/3.4/CHANGES)
- [tmux CHANGES (3.3)](https://raw.githubusercontent.com/tmux/tmux/3.3/CHANGES)
- [tmux tty-features.c (master)](https://github.com/tmux/tmux/blob/master/tty-features.c)
- [tmux 3.2a input-keys.c](https://raw.githubusercontent.com/tmux/tmux/3.2a/input-keys.c)
- [tmux 3.4 input-keys.c](https://raw.githubusercontent.com/tmux/tmux/3.4/input-keys.c)
- [tmux 3.2a tty.c](https://raw.githubusercontent.com/tmux/tmux/3.2a/tty.c)
- [ink App.tsx](https://github.com/vadimdemedes/ink/blob/master/src/components/App.tsx)
- [bubbletea v2 announcement discussion #1374](https://github.com/charmbracelet/bubbletea/discussions/1374)
- [bubbletea #1014 — x/input proposal](https://github.com/charmbracelet/bubbletea/issues/1014)
- [bubbletea #1178 — KeyboardEnhancementsMsg never received in tmux](https://github.com/charmbracelet/bubbletea/issues/1178)
- [tmux #280 — Bracketed Paste should be set per pane](https://github.com/tmux/tmux/issues/280)
- [claude-code #29129 — kitty keyboard CSI > u silently swallowed by tmux](https://github.com/anthropics/claude-code/issues/29129)
- [Bracketed-paste — Wikipedia](https://en.wikipedia.org/wiki/Bracketed-paste)
- [XTerm bracketed-paste reference](https://invisible-island.net/xterm/xterm-paste64.html)
