# QUM-685: `RenderMessages_LongHistory_Cached` Bench Discrepancy — Resolved

**Status:** Investigation complete. No production code bug. No bench code bug
in the *current* tree (the original suspect bench was deleted by QUM-676 S6
before this investigation started). The 1.49 ms/op figure cited in finn's
QUM-671 S1 completion comment is **not reproducible from the bench-as-committed**
and should be retired as the anchor for downstream gates.

**Investigator:** trace (researcher agent), 2026-06-06.
**Branch:** `dmotles/qum-685-bench-investigation`.

---

## TL;DR

- The original bench
  `BenchmarkViewportModel_RenderMessages_LongHistory_Cached`
  (in the now-deleted `internal/tui/app_longhist_bench_test.go`) **cannot
  return ~1.49 ms/op** when run against the source committed at S1 (`8e28ddd`).
  Reason: `ViewportModel.SetMessages` defensively zeroes both the per-entry
  render cache AND the top-level fingerprint cache after the warm render
  it performs internally. The first `renderMessages()` call inside the bench
  loop is therefore always a cold render (~1050 ms), Go's framework adjusts
  `b.N=1`, and the reported `ns/op` is ~1050 ms.
- The "1.49 ms" number was likely captured against a different source state
  than the one committed (uncommitted experiment, different bench entry, or
  a transcription mistake). We have no way to recover which.
- The post-S6 replacement bench
  `BenchmarkChatList_Render_LongHistory_Cached` (in
  `internal/tui/chatlist_longhist_bench_test.go`) is **correctly measuring
  per-item-cache-hit performance**. Current numbers on the arm64-Linux
  worktree host:
  | Bench | ns/op | B/op | allocs/op |
  | -- | --: | --: | --: |
  | `ChatList_Render_LongHistory_SteadyState` | ~10.6 ms | 69.6 MB | 25 |
  | `ChatList_Render_LongHistory_Cold`        | ~1053 ms | 263 MB  | 5.16 M |
  | `ChatList_Render_LongHistory_Cached`      | ~10–16 ms | 69.6 MB | 25 |
  These reflect a real cache (~100× cold→cached). The remaining ~10 ms is
  the cost of walking ~5 000 envelopes and concatenating their cached bytes
  into a single ~70 MB string — there is no top-level assembled-string cache
  in `ChatList` (intentional; see §"Why ~10 ms, not sub-ms" below).
- **Use ~10 ms/op as the credible `Cached` baseline for downstream
  slices, not 1.49 ms.** QUM-671's S1 completion comment and any plan-doc
  reference to "≤1.49 ms" / "700× speedup" should be updated.

## Reproduction

On `dmotles/qum-685-bench-investigation` (parented off current main, head
`714c52b`):

```
$ go test -bench=ChatList_Render_LongHistory -benchtime=1s -count=3 \
    -benchmem ./internal/tui/...
goos: linux
goarch: arm64
pkg: github.com/dmotles/sprawl/internal/tui
BenchmarkChatList_Render_LongHistory_SteadyState-4    100   10015727 ns/op   69640410 B/op   25 allocs/op
BenchmarkChatList_Render_LongHistory_SteadyState-4    100   10852081 ns/op   69640357 B/op   25 allocs/op
BenchmarkChatList_Render_LongHistory_SteadyState-4     97   10629042 ns/op   69640360 B/op   25 allocs/op
BenchmarkChatList_Render_LongHistory_Cold-4              1 1053653317 ns/op  262555000 B/op  5157035 allocs/op
BenchmarkChatList_Render_LongHistory_Cold-4              1 1049263665 ns/op  262518712 B/op  5157023 allocs/op
BenchmarkChatList_Render_LongHistory_Cold-4              1 1055993087 ns/op  262517176 B/op  5157019 allocs/op
BenchmarkChatList_Render_LongHistory_Cached-4          100   16442933 ns/op   69640378 B/op   25 allocs/op
BenchmarkChatList_Render_LongHistory_Cached-4           97   10643993 ns/op   69640360 B/op   25 allocs/op
BenchmarkChatList_Render_LongHistory_Cached-4           96   10531121 ns/op   69640359 B/op   25 allocs/op
```

This matches the second-finn S3-prep numbers verbatim for `Cold` and is
~100× faster than `Cold` for `Cached` — confirming the cache is load-bearing
even at the per-item-only level. It is also consistent with tower's S7
completion line "Bench under all S1 floors (... Cached 9.97 ms/op)".

## Root Cause of the 1.49 ms Anomaly

### 1. The S1 bench was structurally incapable of returning 1.49 ms

The deleted `BenchmarkViewportModel_RenderMessages_LongHistory_Cached` body
(from `8e28ddd:internal/tui/app_longhist_bench_test.go`):

```go
func BenchmarkViewportModel_RenderMessages_LongHistory_Cached(b *testing.B) {
    entries := loadLongHistoryFixture(b)
    theme := NewTheme("")
    vp := NewViewportModel(&theme)
    vp.SetSize(200, 60)
    vp.SetMessages(entries) // "warms both per-entry and top-level cache"
    b.ResetTimer()
    for i := 0; i < b.N; i++ {
        _ = vp.renderMessages()
    }
}
```

The author's comment ("warms both per-entry and top-level cache") is wrong
about the *visible* effect of `SetMessages`. Look at the S1 viewport source
(`8e28ddd:internal/tui/viewport.go` lines 610–639):

```go
func (m *ViewportModel) SetMessages(msgs []MessageEntry) {
    m.messages = make([]MessageEntry, len(msgs))
    copy(m.messages, msgs)
    // ... bookkeeping ...
    m.renderAndUpdate()  // internally warms per-entry + top-level caches
    // QUM-667: defensively zero cache fields so a GetMessages → SetMessages
    // round-trip doesn't carry stale rendered output back in on the next
    // render. Done after renderAndUpdate so the inner viewport content
    // reflects the new messages but the per-entry cache is fresh.
    for i := range m.messages {
        m.messages[i].renderedCache = ""
        m.messages[i].renderedCacheKey = ""
    }
    m.lastRenderedContent = ""
    m.lastRenderedFingerprint = ""
    m.appliedContent = ""
}
```

So by the time `b.ResetTimer()` fires, **all** cache state is empty.
The first iteration of the loop must perform a full cold render
(~1050 ms). Once an iteration takes that long, Go's benchmark framework
keeps `b.N=1`, and reports ~1050 ms/op as the result. The "many cheap
cache-hit iterations averaging to 1.49 ms" outcome the author imagined
cannot occur given this `SetMessages` contract.

The defensive zero-out was introduced in QUM-667 (commit `98b4c7a`), which
also added the cache. The S1 bench (`8e28ddd`, written later) did not
account for the zero-out. So the bench has been incorrect — not in *what*
it measures (a single cold render *is* a coherent measurement) but in
its *name* and *claimed* measurement target ("cache-hit performance").

### 2. So where did 1.49 ms come from?

We cannot recover this with certainty. Plausible explanations:
- finn was running a build whose `SetMessages` did not include the
  defensive zero-out (e.g., a local edit, or an in-flight refactor branch).
  At that point a single 1050 ms cold render in the first iteration is
  followed by 99+ top-level-fingerprint-cache hits (~µs each) and the
  ns/op rolls down toward sub-ms.
- finn ran a different bench (perhaps a hand-written one-off in a scratch
  file) and labelled it `RenderMessages_LongHistory_Cached` in the comment.
- The number was transcribed from a different measurement entirely
  (e.g., the per-entry microbench in `app_view_cache_test.go`-adjacent
  files, which exercises the top-level cache hit directly).

In all three cases, the 1.49 ms figure does not reflect anything any
agent can reproduce today against the committed source. **Retire it.**

## Why the Current `ChatList_Render_..._Cached` is ~10 ms, Not Sub-ms

The legacy `ViewportModel.renderMessages` had **two** caches:
1. **Per-entry** render cache (the QUM-667 contribution): each
   `MessageEntry` carries a `renderedCache` string keyed by the entry's
   intrinsic state. Hit → skip glamour / lipgloss work for that entry.
2. **Top-level assembled-string** cache: a fingerprint over all entries'
   cache keys. Hit → return the previously assembled string verbatim
   without even walking entries.

The post-S6 `ChatList.Render(width)` (in `internal/tui/chatlist.go`) keeps
only (1) — there is no top-level assembled-string cache. The render path is:

```go
func (c *ChatList) Render(width int) string {
    if width <= 0 || c.width <= 0 {
        return ""
    }
    var sb strings.Builder
    var prevType string
    for idx, env := range c.items {
        curType := itemTypeKey(env.item)
        if idx > 0 && curType != prevType {
            sb.WriteString("\n")
        }
        sb.WriteString(c.renderEnvelope(env, width))  // per-item cache hit if Finished
        sb.WriteString("\n")
        prevType = curType
    }
    return sb.String()
}
```

So every `Render` walks all ~5 000 envelopes (`-25 allocs/op` is just
strings.Builder backing-array growth amortized across iterations) and
produces a ~70 MB string. That walk is what takes ~10 ms. This is real
work, not measurement noise.

Whether to add a top-level assembled-string cache to `ChatList` is a
separate design question. The legacy top-level cache was load-bearing
because the legacy hot path called `renderMessages` on every keystroke /
spinner tick via `View()`; the post-S6 hot path is different (ChatList's
view contract is the caller's responsibility) and the benefit may be
smaller. That is **out of scope** for QUM-685.

## Recommended Anchor for Downstream Slices

Use these numbers as the floor going forward (arm64-Linux, sprawl coder
worktree host, `-benchtime=1s`, ChatList items at 200-col width over the
1500-frame fixture):

| Bench                                          | Anchor   | +30% gate |
| --                                             | --:      | --:       |
| `BenchmarkChatList_Render_LongHistory_SteadyState` | ~10.6 ms | ≤13.8 ms |
| `BenchmarkChatList_Render_LongHistory_Cold`        | ~1053 ms | ≤1369 ms |
| `BenchmarkChatList_Render_LongHistory_Cached`      | ~12 ms\* | ≤16 ms   |

\*Use the mean of three `Cached` runs; the first run is consistently the
slowest (~16 ms) due to warmup effects from `b.StopTimer()`/`StartTimer()`
in the surrounding `Cold` bench. Steady-state is ~10.6 ms.

These are also already aligned with tower's S7-completion line: "Bench
under all S1 floors (View 12.20, Cold 1056.61, Cached 9.97 ms/op)".

## Action Items (Outside This Investigation)

- [ ] (Bookkeeping) Add a correction comment to QUM-671's S1 completion
      thread noting the 1.49 ms figure is irreproducible and the credible
      `Cached` anchor is ~10 ms/op for the `ChatList_Render_*_Cached` bench.
- [ ] (Optional) Decide whether ChatList needs a top-level assembled-
      string cache. Likely **no** — the post-S6 view contract is
      different — but worth a quick justification doc if any S-slice
      starts hitting render-cost limits on a per-Render basis.

## Reflection

- **Surprising:** how fragile a benchmark name + author comment is as a
  source of truth. Both the author of the S1 bench and the author of
  `SetMessages` (the QUM-667 defensive zero-out) were correct in their
  individual contexts, but the *intersection* of those two contracts
  silently turned a "cache-hit" bench into a "cold-render" bench. There
  was no test of the bench itself.
- **Open question I'd chase with more time:** is the QUM-667 defensive
  zero-out in `SetMessages` actually needed in production? Its commit
  message says "GetMessages → SetMessages round-trip" — i.e. the worry is
  a caller mutating the slice and re-installing it. If `GetMessages` is
  no longer called anywhere post-S6, the zero-out is dead code and could
  be removed (which would also retroactively make the now-deleted S1 bench
  honest). Not blocking — `ViewportModel` itself is on its way out per
  S6's deletion arc.
- **Next investigation if I had more time:** profile the ~10 ms
  `ChatList_Render_*_Cached` walk to confirm where it spends time. If
  `strings.Builder` growth dominates, an `sb.Grow(estimatedSize)` hint
  would cheaply trim it; if it's all glamour/lipgloss inside cached
  renders, that suggests the per-item cache isn't actually hitting and
  there's a key-stability bug worth chasing.
