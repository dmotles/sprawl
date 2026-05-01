# QUM-432 — Multi-line paste with stripped bracketed-paste markers

Research-only. The plan below is what we should hand to an engineer.

## TL;DR

- Bracketed paste **is already enabled** in our Bubble Tea v2 program (it's the default — there is no `WithBracketedPaste()` option in v2, and we don't set `View.DisableBracketedPasteMode`). QUM-430's `case tea.PasteMsg` is correct and stays.
- dmotles still hits the degraded path because his outer environment (tmux config / SSH / Terminal.app combo) **strips the `ESC[200~` / `ESC[201~` markers** before they reach our process. We can't fix that from inside our process.
- When markers are stripped the paste arrives as a stream of `tea.KeyPressMsg` events. Embedded line terminators land as `KeyPressMsg{Code: tea.KeyEnter}` (because terminals deliver pasted `\r` in raw mode and the Ultraviolet decoder maps `\r` → `KeyEnter`; `\n` maps to `Ctrl+J`, which we never see in practice). Our `InputModel.Update` Enter-as-submit check (`internal/tui/input.go:57`) fires on the first embedded `\r` and submits the first line.
- Bubble Tea's reader pushes events from a single `read(2)` chunk onto its message channel one-by-one; chunk boundaries are lost by the time we see a `KeyPressMsg`. That kills any direct port of Claude Code's "trailing lone `\r` vs embedded `\r`" chunk-position heuristic — but the events from a single chunk arrive within microseconds of each other, so a **time-based** classifier works just as well in practice.
- Recommended fix is a small **time-based paste-in-progress** layer in `InputModel`: an Enter that arrives within ~10 ms of the previous keypress is reclassified as an embedded newline and inserted into the textarea instead of submitting. Plain Enter outside that window submits as today. KISS.

## Q1 — Are we in bracketed paste mode?

**Yes, by default.** Evidence:

- `cmd/enter.go:158` constructs the program as `tea.NewProgram(model)` — no options.
- In Bubble Tea v2 there is no `WithBracketedPaste()` program option. Bracketed paste is controlled by the `View.DisableBracketedPasteMode` field (`charm.land/bubbletea/v2@v2.0.3/tea.go:172`). Default is `false` — meaning bracketed paste is **on**. The renderer writes `ansi.SetModeBracketedPaste` (`cursed_renderer.go:115`) on every frame where the view doesn't disable it.
- Ultraviolet's reader buffers everything between `ESC[200~` and `ESC[201~` and emits a single `PasteEvent` (`ultraviolet/terminal_reader.go:289–333`, `decoder.go:559–563`). Bubble Tea translates that to `tea.PasteMsg` (`bubbletea/v2/input.go:36–41`).

QUM-430 already added `case tea.PasteMsg` at `internal/tui/app.go:277` and forwards to `InputModel`, which forwards to `bubbles/textarea` v2.1.0 — and `textarea` natively handles `tea.PasteMsg` via `insertRunesFromUserInput` (`bubbles/v2@v2.1.0/textarea/textarea.go:1223–1224`). That path is fine.

**So why does dmotles see the degraded path?** Because the *outer* environment is eating the markers before they get to our terminal. Likely culprits, in order of probability for a Mac SSH-into-tmux setup:

1. `tmux` not configured with bracketed-paste passthrough (`set -g set-clipboard external` and/or `set -as terminal-features ',*:bpaste'`/`extkeys` on modern tmux). Older tmux versions (< 3.2) strip bracketed paste on the outer side regardless.
2. macOS Terminal.app — does not advertise bracketed-paste capability the same way iTerm/Alacritty/WezTerm do.
3. `screen` between us and the terminal (always strips).
4. The user's outer terminal having bracketed paste off but the inner (our app) having it on — markers are written by our renderer but the outer layer never wraps the paste content with them on the way *in*.

In all cases the problem is upstream of our process. We cannot turn it on from inside the TUI; we can only build a fallback for the degraded byte stream.

## Q2 — What does the degraded stream actually look like?

Reasoning from Ultraviolet's decoder:

- Terminals in raw mode (which Bubble Tea/uv enable) deliver Enter as `\r` (CR, 0x0D), not `\n`. The kernel's `ICRNL` translation that turns `\r` into `\n` is disabled in raw mode.
- Pasted multi-line content, when the bracketed-paste wrapper is stripped, shows up as the raw byte stream the terminal would have sent for typing those characters. In practice that's printable bytes interleaved with `\r` between lines.
- `ultraviolet/key_table.go:72` maps `string(byte(ansi.CR))` → `enter := Key{Code: KeyEnter}`. `KeyEnter = rune(ansi.CR)` (`ultraviolet/key.go:221`). So every embedded `\r` decodes to `tea.KeyPressMsg{Code: tea.KeyEnter, Mod: 0}` — exactly indistinguishable from a real Enter press at the message level.
- A `\n` (LF) would decode to `Ctrl+J` (`key_table.go:69`). Real terminals don't normally emit raw `\n` in raw mode for newlines, so this won't be the dominant case — but a fix that targets *only* `\r`-as-embedded-newline is sufficient.

So the failure stream in dmotles's env looks like:

```
KeyPressMsg{Code:'l',Text:"l"} KeyPressMsg{Code:'i',Text:"i"} ...
KeyPressMsg{Code:'1',Text:"1"} KeyPressMsg{Code:KeyEnter}
KeyPressMsg{Code:'l',Text:"l"} ... KeyPressMsg{Code:'2',Text:"2"} KeyPressMsg{Code:KeyEnter}
KeyPressMsg{Code:'l',Text:"l"} ... KeyPressMsg{Code:'3',Text:"3"} KeyPressMsg{Code:KeyEnter}
```

The first KeyEnter trips `input.go:57`; the rest types into the freshly-cleared textarea.

**Chunk-position info is gone.** `ultraviolet/terminal_reader.go` reads a chunk via the cancel-reader, calls `eventScanner.scanEvents(buf)` which returns `[]Event`, and the reader pushes each event onto Bubble Tea's `p.msgs` channel one-at-a-time (`bubbletea/v2@v2.0.3/tea.go:752–753`). By the time a `KeyPressMsg` lands in `Update` we have no boundary marker. **But** events from a single chunk arrive within microseconds of each other; human typing's minimum inter-key gap is ~30 ms (200 wpm sustained is ~50 ms/key, peak bursts maybe 20 ms). A 10–20 ms classifier window cleanly separates them.

## Q3 — Does textarea already have a paste-aware path?

`bubbles/v2@v2.1.0/textarea`:

- `case tea.PasteMsg:` at line 1223 — yes, native handling.
- `Model.InsertString(s)` at line 488 — exposed; we can call it directly to insert a literal `\n` when we reclassify an Enter.
- `KeyMap.InsertNewline` defaults to bindings that include `shift+enter` (we already further restrict to *only* `shift+enter` in `input.go:37`).

For a degraded paste, textarea will not *itself* differentiate Enter-the-key from Enter-the-embedded-newline — the `KeyPressMsg` looks identical. So the classification has to live in `InputModel` upstream of the textarea.

## Q4 — Layered defenses feasibility

Mapping each option in the issue's open questions to our framework:

| Layer | Available? | Cost | Verdict |
|---|---|---|---|
| (a) Enable bracketed paste at program | Already on by default in Bubble Tea v2 | 0 LOC | Already done. Doesn't help dmotles because his env strips markers upstream. |
| (b) Chunk-position `\r` heuristic (Claude Code #7) | Not directly — chunks aren't preserved through `p.msgs`. **Time-based equivalent** is reliable and trivial. | ~30–50 LOC in `input.go` + tests | **Recommended primary fix.** |
| (c) "Paste in progress, ignore Enter" (Claude Code #4) | Easy as a small state machine layered on (b) — once an embedded `\r` is reclassified, suppress Enter-submit for a 50–100 ms quiet window. Defense in depth. | ~10 extra LOC | Recommended add-on. |
| (d) Real `PasteStartMsg`/`PasteEndMsg` to drive (c) | Available (`tea.PasteStartMsg` / `tea.PasteEndMsg` exist), but only fire when markers reach us — i.e. **not in the degraded path we're fixing**. Wire them anyway as belt-and-suspenders for cases where uv detects markers but emits PasteStartMsg without coalescing (rare). | ~10 LOC in `app.go` | Cheap, do it. |
| (e) Shift+Enter chord as fallback | Already wired (`input.go:37`). **But** unreliable — most terminals send `\r` for both `Enter` and `Shift+Enter` unless kitty keyboard protocol or modifyOtherKeys is negotiated. Fine for power users on iTerm/Alacritty/Ghostty/WezTerm with the right settings; **cannot be the primary defense.** | 0 LOC | Keep, document, don't rely on it. |

`tea.WithFilter` (intercept all messages before Update) was considered as a way to do chunk-aware coalescing. It still delivers messages one-at-a-time, so it offers no real advantage over doing the same logic inside `InputModel.Update`. Skip it.

## Q5 — Recommended minimal plan

**Keep plain Enter as submit.** Add a small time-based "paste in progress" guard inside `InputModel`. Implementation sketch (target: `internal/tui/input.go`):

```go
type InputModel struct {
    // ... existing fields ...

    // Paste-detection state for the degraded-bracketed-paste path (QUM-432).
    // When KeyPressMsg events arrive faster than a human can type, we classify
    // an embedded KeyEnter as a literal newline rather than a submit.
    lastKeyAt    time.Time   // last printable KeyPressMsg
    pasteUntil   time.Time   // suppress Enter-submit until this time
}

const (
    pasteBurstWindow  = 10 * time.Millisecond  // Enter within this of last key → embedded
    pasteQuietWindow  = 50 * time.Millisecond  // stay in paste-mode for this long
)

// in Update, KeyPressMsg branch, before existing Enter-submit check:
now := time.Now()

// Enter classification
if keyMsg.Code == tea.KeyEnter && keyMsg.Mod&tea.ModShift == 0 {
    // Embedded if either:
    //   (1) we've recently classified a paste in progress, OR
    //   (2) this Enter is < pasteBurstWindow after a printable key.
    embedded := now.Before(m.pasteUntil) ||
        (!m.lastKeyAt.IsZero() && now.Sub(m.lastKeyAt) < pasteBurstWindow)

    if embedded {
        m.ta.InsertString("\n")
        m.pasteUntil = now.Add(pasteQuietWindow)
        m.lastKeyAt = now
        return m, nil
    }
    // existing submit path...
}

// printable / other keys: update timing
if keyMsg.Text != "" || isPrintableCode(keyMsg.Code) {
    m.lastKeyAt = now
    if now.Before(m.pasteUntil) {
        // extend quiet window while bursts continue
        m.pasteUntil = now.Add(pasteQuietWindow)
    }
}
```

Optional belt-and-suspenders in `app.go`:

```go
case tea.PasteStartMsg:
    m.input.beginPaste()  // sets pasteUntil = far-future sentinel
    return m, nil
case tea.PasteEndMsg:
    m.input.endPaste()    // clears pasteUntil
    return m, nil
```

Why this works:

- A real paste's per-character events arrive sub-millisecond apart. Every Enter inside the paste hits an embedded classification because the previous char was just received.
- The trailing Enter that comes *with* the paste content (e.g. `"line1\rline2\rline3\r"`) is also < 10 ms after `'3'` → classified embedded → newline inserted. Result: paste lands as a single multi-line input. User then hits a real Enter (with no recent printable activity → > 10 ms gap) to submit. ✅ matches AC.
- Real Enter submits unchanged in normal typing because the user pauses ≥ 10 ms after typing a character before pressing Enter (and they have to physically move their finger). The pasteQuietWindow extension only triggers once we've already seen a paste burst.
- Single-line paste with no embedded `\r` is uneffected — there's nothing to misclassify.

Trade-offs and known limitations to call out in the PR:

- A user typing absurdly fast (>200 wpm sustained) and chasing a character with Enter inside 10 ms could see a misclassification (newline instead of submit). They'd see the trailing newline and press Enter again. Acceptable.
- Slow SSH that pauses mid-paste by > 10 ms could fragment a paste into two bursts. The pasteQuietWindow (50 ms) covers most realistic cases; tunable.
- `\n` (LF, 0x0a) is not the path we observe — it'd decode to Ctrl+J, which textarea ignores anyway. No special handling needed.

**Files to change:**

- `internal/tui/input.go` — add timing state + classification (~40 LOC).
- `internal/tui/input_test.go` — new tests (TDD-first):
  - rapid-fire `KeyPressMsg` printable + `KeyEnter` interleaved at 0 ms intervals → no `SubmitMsg`, textarea contents include all lines joined by `\n`.
  - lone `KeyEnter` after a 50 ms pause → emits `SubmitMsg`.
  - shift+Enter (legacy newline path) still inserts newline.
  - paste of `"abc\r"` followed by quiet period — the trailing `\r` is treated as embedded; user must press Enter again to submit. (Document this is the intended behavior.)
- `internal/tui/app.go` — optional `tea.PasteStartMsg` / `tea.PasteEndMsg` wiring to set/clear paste-in-progress state (defense in depth; no harm if it never fires).
- `internal/tui/app_paste_test.go` — extend with PasteStart/End tests if the optional layer is added.

Make the constants (`pasteBurstWindow`, `pasteQuietWindow`) package-level `var`s if we want to override them in tests via a fake clock; otherwise tests can use a `time.Now` interface (the current package doesn't yet inject a clock — small new dependency).

## Q6 — Validation plan

The `tmux paste-buffer -p` path is already known to work post-QUM-430 because that flag explicitly wraps with bracketed-paste markers. The test we need is the **un-flagged** path, which exercises stripped delivery:

```sh
# Reproducer for the degraded path (no -p flag).
tmux set-buffer 'paragraph one
paragraph two
paragraph three'
tmux paste-buffer            # NOTE: no -p — sends raw, no bracketed-paste wrap
```

`paste-buffer -p` adds the wrapper iff the target pane's outer terminal supports it; without `-p` the bytes go in raw, exactly as a Mac/SSH/old-tmux paste would arrive. This is the e2e reproduction recipe to put in the AC.

Additional reproducers to keep in the engineer's pocket:

1. **Outer tmux disables bracketed paste explicitly:** in the outer tmux, `set -g window-active-style ''` is irrelevant; what we want is to run sprawl inside an *inner* tmux nested under an *outer* shell where bracketed paste isn't honored. Easiest reliable way: launch sprawl directly under `screen` (no tmux), which historically strips bracketed paste, and paste in the macOS Terminal.app paste menu.
2. **Synthetic unit-level repro:** drive `AppModel.Update` directly with a sequence of `tea.KeyPressMsg` values constructed in the test — that's what `internal/tui/input_test.go` should do for the TDD case. No real terminal needed.
3. **dmotles's actual environment:** required for AC sign-off — paste a multi-paragraph message into the running TUI, confirm it lands as one input. The `tmux paste-buffer` (no `-p`) inside dmotles's tmux is a very close proxy if direct paste from the host clipboard doesn't reproduce.

Order for the engineer: write the unit tests first (TDD), implement, run `make validate`, then the two e2e tests already required (`make test-notify-tui-e2e`, `make test-handoff-e2e`), then the manual repro in dmotles's env using `tmux paste-buffer` (no `-p`) and an actual host paste.

## Reflections

- **Surprise:** Bubble Tea v2 doesn't have a `WithBracketedPaste()` option at all. Bracketed paste defaulted-on is a v2-era choice; I had been expecting an explicit toggle based on the issue's Q1 wording.
- **Surprise:** Ultraviolet maps `\r` to `KeyEnter` and `\n` to `Ctrl+J`. The asymmetry matters: a fix that searches for "embedded `\n`" would miss the actual failure mode entirely, because raw-mode terminals send `\r` for newlines.
- **Open question I'd chase next:** does Bubble Tea's reader ever batch events under load (e.g. via `runtime.Gosched`) such that the inter-event gap exceeds 10 ms even for one chunk? Worth instrumenting in dmotles's env once the fix is in. Mitigation if so: bump `pasteBurstWindow` to 25–30 ms; we still beat human typing.
- **Open question:** would a kitty-keyboard-protocol opt-in (`View.KeyboardEnhancements`) help dmotles? It would make Shift+Enter reliably distinguishable, which is orthogonal to this fix but a UX nicety. Out of scope here, possibly a follow-up issue.
- **What I'd investigate with more time:** test on an actual Mac+Terminal.app+SSH+tmux stack to confirm the marker-stripping hypothesis is right. The plan stands either way — the fix is for the symptom, not the upstream cause — but it would give us better confidence in the threshold tuning.
