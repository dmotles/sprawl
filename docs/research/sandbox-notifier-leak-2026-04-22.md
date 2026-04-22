# Sandbox notifier leak — why `[inbox]` pokes escape into the outer weave pane

**Date:** 2026-04-22
**Author:** ghost (research agent, parent: tower)
**Status:** cosmetic — self-resolves when tmux-mode notifier is deleted in Phase 2.5. Tracked under QUM-195.
**Scope:** investigate-only. No production code changed on this branch.

## TL;DR

Weave's hypothesis — that the legacy notifier resolves its target pane from inherited `TMUX` / `TMUX_PANE` env vars — is **refuted**. The notifier never reads either variable. The leak comes from a different fallback chain: when neither `SPRAWL_NAMESPACE` nor `$SPRAWL_ROOT/.sprawl/namespace` resolves, `buildLegacyRootNotifier` quietly falls back to `tmux.DefaultNamespace` (`"⚡"`) and `tmux.DefaultRootName` (`"weave"`). The per-user tmux server is shared with the real outer weave session, so `⚡:weave` lands in dmotles' actual pane.

There are two real leak paths (see "Preconditions" below). Neither requires any tmux env inheritance; both are pure config-resolution misses.

## What the notifier actually does

`cmd/messages_notify.go:28-54` — `buildLegacyRootNotifier(getenv, tmuxRunner, sprawlRoot)` returns this closure:

```go
func(to, from, _ string, msgID string) {
    if getenv("SPRAWL_MESSAGING") != "legacy" { return }
    rootName := state.ReadRootName(sprawlRoot)
    if rootName == "" { rootName = tmux.DefaultRootName }          // "weave"
    if to != rootName { return }
    namespace := getenv("SPRAWL_NAMESPACE")
    if namespace == "" { namespace = state.ReadNamespace(sprawlRoot) }
    if namespace == "" { namespace = tmux.DefaultNamespace }        // "⚡"
    rootSession := tmux.RootSessionName(namespace)                  // == namespace
    _ = tmuxRunner.SendKeys(rootSession, tmux.RootWindowName, ...)  // window = "weave"
}
```

`SendKeys` (`internal/tmux/tmux.go:288-292`) shells out to:

```
tmux send-keys -t "=<session>:weave" "<payload>" Enter
```

No `-L`/`-S` socket override, no `TMUX`/`TMUX_PANE` lookups, no pane ID resolution. The target is purely `(session, window)` on the **default per-user tmux server**. That last bit is the load-bearing detail for the leak: every tmux session the same UID owns — sandbox or real — lives on the same server and is reachable by any process that can shell out to `tmux`.

## Refuting the TMUX/TMUX_PANE hypothesis

`grep -n TMUX` across the repo finds exactly one reader: `IsInsideTmux()` in `internal/tmux/tmux.go:327`, used only by `RealRunner.Attach` to pick `switch-client` vs `attach-session`. The notifier never calls `Attach`. `TMUX_PANE` is never read anywhere. So inherited pane env from the outer shell cannot influence where `send-keys` lands.

What *is* shared between sandbox and outer weave is the tmux server itself (default socket under `$TMUX_TMPDIR`/`/tmp`). That's sufficient to produce the leak without any env-var fallback — you just need the resolved `(session, window)` tuple to collide with the outer session.

## The real fallback chain

Both the session and the root-name resolution fail **open** (fall back to a hardcoded default) rather than **closed** (refuse to poke). The four layers, in resolution order:

| resolver | source | miss behavior |
| --- | --- | --- |
| `getenv("SPRAWL_NAMESPACE")` | process env | fall through |
| `state.ReadNamespace(sprawlRoot)` | `$SPRAWL_ROOT/.sprawl/namespace` | fall through |
| `tmux.DefaultNamespace` | hardcoded `"⚡"` | **used as final fallback** |
| `state.ReadRootName(sprawlRoot)` | `$SPRAWL_ROOT/.sprawl/root-name` | fall through |
| `tmux.DefaultRootName` | hardcoded `"weave"` | **used as final fallback** |

When both namespace resolvers miss and both root-name resolvers miss, the notifier targets `⚡:weave` — which for dmotles is the real interactive session.

## Preconditions for a leak

All of these must hold simultaneously:

1. `SPRAWL_MESSAGING=legacy` (rollback knob active — this is the only path that pokes tmux at all).
2. `SPRAWL_ROOT` is set and non-empty (`registerDefaultNotifier` in `cmd/root.go:27-37` bails if unset, so the notifier is never registered in that case).
3. The message recipient `to` equals the resolved `rootName` — typically `weave`.
4. Session resolution lands on the outer session's namespace. Two ways this happens:
   - **Case A — `SPRAWL_ROOT` points at the outer repo.** Happens when a harness spawns a "sandbox-child" process from inside the outer weave shell without overriding `SPRAWL_ROOT`. The child reads the outer `.sprawl/namespace` (`⚡`) and outer `.sprawl/root-name` (`weave`). Correctly-resolved — just pointing at the wrong sprawl.
   - **Case B — `SPRAWL_ROOT` points at the sandbox, but its namespace state is missing.** `SPRAWL_NAMESPACE` is unset in the child env, and `$SPRAWL_ROOT/.sprawl/namespace` doesn't exist (half-initialised sandbox, or SPRAWL_ROOT pointed at a directory that was never `sprawl init`'d). Falls through to the hardcoded `⚡` default, which by sheer coincidence matches the outer weave's default namespace.

`scripts/sprawl-test-env.sh` and `scripts/test-notify-e2e.sh` sidestep both cases: they `sprawl init --detached --namespace "test-<hex>"` (which writes `.sprawl/namespace`) and export `SPRAWL_NAMESPACE` before spawning children. So the existing e2e test can't reproduce the M13 leak — the harness is stricter than the M13 sandbox-child invocation path.

## Where does the fix belong?

Both sides have a legitimate claim. I recommend doing both, but only the notifier-side fix is worth shipping now.

### Harness side (Case A mitigation)

Whatever M13 code path spawns sandbox-child processes needs to ensure `SPRAWL_ROOT` is overridden to the sandbox before exec. This is not a one-liner I can ship without seeing the M13 harness — it lives outside this repo / in work that hasn't landed yet. Leaving to the M13 author.

Isolation via `tmux -L <socket>` would also work (forces a separate per-user tmux server) but it's a bigger move — every tmux call in `internal/tmux/tmux.go` would need to plumb the socket flag through, and sandboxes would stop being observable via the user's normal `tmux ls`. Not worth it for a cosmetic issue that disappears in Phase 2.5.

### Notifier side (Case B mitigation — cheap, defensive)

Replace the two silent fallbacks with fail-closed behavior:

```go
// Skip notification if we can't prove the target session is sandbox-scoped.
namespace := getenv("SPRAWL_NAMESPACE")
if namespace == "" { namespace = state.ReadNamespace(sprawlRoot) }
if namespace == "" { return } // was: namespace = tmux.DefaultNamespace

rootName := state.ReadRootName(sprawlRoot)
if rootName == "" { return }  // was: rootName = tmux.DefaultRootName
if to != rootName { return }
```

Two objections worth naming:

- **This could regress fresh installs where `.sprawl/namespace` hasn't been written yet.** Check: `cmd/init.go:171` writes the namespace unconditionally during `sprawl init`, and `registerDefaultNotifier` only registers when `SPRAWL_ROOT` is set — which normally means init has run. The tests in `cmd/messages_test.go:1208+` cover the fallback-to-default path; they'd need updating to assert the new no-op behavior. Rough blast radius: ~5-6 tests.
- **It doesn't help Case A** — if `SPRAWL_ROOT` is wrong, every resolver returns outer-repo state and the notifier fires normally. Correct: Case A genuinely needs a harness fix. Case B is the one the notifier can prevent unilaterally.

Since the whole tmux notifier is scheduled for deletion in Phase 2.5 (punchlist item #1 — "do not use tmux send-keys"), even the cheap notifier-side fix may not be worth the churn. Recommendation below reflects that.

## Recommendation

**Do not ship a fix on this branch.** File this as QUM subtask of QUM-195 and let M13 / Phase 2.5 authors decide:

- If M13 lands before Phase 2.5 rips out the tmux notifier, M13 author fixes its harness (Case A) and optionally takes the 6-line notifier patch (Case B).
- If Phase 2.5 ships first, the whole file is deleted and the bug evaporates.

I'm filing a Linear issue (parent QUM-195) documenting both cases with pointers into this doc.

## Reflection

**Surprising:** the notifier is entirely `-L`-free. I expected to find at least an optional socket-override plumbed through for test isolation — there isn't one. Sprawl's tmux-mode sandboxing is implicitly relying on unique session names being sufficient separation, which is true *if* the namespace resolver never falls back to the default.

**Still open:**
- I didn't trace the M13 sandbox-child spawn path itself (task said "not landed / forthcoming"). Case A vs Case B attribution is a guess from the symptom; the M13 author can confirm which one they're hitting by checking whether the child's `SPRAWL_ROOT` points at the sandbox or the outer repo.
- Didn't audit other `SendKeys` callsites (`internal/observe`, `cmd/spawn_subagent`, `cmd/agentops/spawn`) for the same default-fallback pattern. Quick look says they operate on explicitly-constructed session names from agent state, not default fallbacks, but worth a pass if someone wants to eliminate the class of bug.

**If I had more time:** I'd grep every call to `tmux.DefaultNamespace` / `tmux.DefaultRootName` and decide whether each is a legit default or a silent-leak risk. The notifier is the one that matters for QUM-195, but the same class of "fall through to a hardcoded value that happens to match the operator's real session" bug is probably lurking elsewhere.
