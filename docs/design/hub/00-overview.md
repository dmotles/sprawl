# 00 — Overview: The Sprawl Hub

*Why we're building an optional central hub, and what it is at a high level.*

See also: [`01-architecture.md`](01-architecture.md) · [index](README.md)

---

## Problem / Why

Sprawl today is **terminal-first and single-host**. That is great for a focused
session, but the maintainer's real workflow has outgrown it:

- **Window sprawl.** Several source repos, each with its own local sprawl
  instance in its own terminal window. Finding "the one that needs me right now"
  means hunting across windows. There is no bird's-eye view of activity across
  *all* instances.
- **No remote/mobile follow-up.** Work kicked off at a desk can't be checked on
  from a phone or a browser later — did it land? can I nudge it? — without
  returning to that exact terminal.
- **Memory is trapped.** weave's memory is local and untracked. It doesn't move
  between machines, so context is lost when the maintainer switches hosts.
- **No natural image ingestion.** Dropping a screenshot into a weave conversation
  (a design mock, an error screen) isn't a first-class path.

The unifying need is a **single pane of glass**: one place to observe and steer
*every* sprawl instance, reachable from anywhere, with memory that travels.

## Solution shape

An **optional central hub** — a **hub-and-spoke broker**, *not* an authoritative
state store.

```
   host A (repo 1)  ─┐
   host B (repo 2)  ─┼──dial out──▶  ┌──────────┐  ◀──browser (laptop)
   host C (repo 3)  ─┘               │   HUB    │  ◀──browser (phone)
                                     │ broker + │
   (each = a local                   │ store +  │
    `sprawl enter`                   │ auth     │
    session, still the               └──────────┘
    source of truth)
```

Key properties:

- **Broker, not brain.** The live claude session on each host remains the source
  of truth. The hub is a realtime **fan-out point**, a **durable store**, and an
  **auth boundary**. It never becomes the thing sprawl depends on to run.
- **Disconnected by default.** Sprawl works ~100% as today with **zero behavior
  change** when no hub is configured. There is **no default hub endpoint** baked
  into the code (public-repo hygiene). Connecting is opt-in via an explicit
  `--hub-url` / config / env value.
- **Hosts dial out.** Hosts sit behind NAT, so each host opens a persistent
  **bidirectional** connection *to* the hub: uplink (events, memory, logs) and
  downlink (commands, "the user typed X in the browser") over the same
  connection. No inbound ports on the host.
- **One event-log spine.** A single seq'd, resumable event log is the backbone
  (detailed in [`01`](01-architecture.md)). Every consumer — TUI, hub,
  browser — replays from its last seq, else loads a snapshot, then live-tails.
- **Read fan-out, not multiplayer.** Viewers all see the *same* claude output
  stream. Multiple people typing just enqueue into the one turn-queue. No
  presence/typing indicators, no hard driver-lock in v1 — only lightweight
  guards (e.g. "N clients connected").

### Simplest way vs. right way (at the product level)

- **Simplest way:** an SSH-tunneled `tmux`/web-terminal onto each host, or a
  read-only status scraper. Cost: no durable history, no memory portability, no
  clean auth boundary, no multi-host aggregation, and every new capability is a
  bespoke hack.
- **Right way:** the broker + event-log spine described here. Cost: a small
  always-on service to run and secure.
- **Recommendation:** build the broker, but keep it *optional and thin*. The
  spine is the one piece worth doing properly up front because every later
  feature (remote access, memory, attachments) rides on it; everything else
  stays minimal until proven necessary.

## Prior art — build vs. adopt

We web-searched "single pane of glass over many AI-agent sessions" (July 2026).
The space is crowded, but nothing fits sprawl's shape.

**Two clusters exist:**

1. **Enterprise agent/observability dashboards** — Grafana's AI-coding-agent
   dashboards, Datadog/Instana/New Relic "single pane of glass" monitoring,
   Microsoft Viva Agent Dashboard. These are *metrics/telemetry* panes (cost,
   tokens, latency, counts) for fleets of *deployed* agents. They observe; they
   don't let you *drive a live conversational session* or carry an agent's
   memory.
2. **Terminal-native agent multiplexers** — herdr, cmux, amux, Mato, Mission
   Control, Termdock. These run many local coding-agent panes with status
   awareness; some add a web/mobile view and SSH-remote modes. Closest in
   spirit, but each is a **local multiplexer** — the multiplexer *is* the
   runtime. None is an *optional, dial-out broker* that federates several
   *independent, already-running* orchestrators across NAT'd machines while
   leaving each one authoritative and fully functional offline.

**Why adopt doesn't fit:**

- Sprawl already has its own runtime, agent lifecycle, worktree/branch model,
  and MCP tool surface. Adopting a multiplexer would mean re-homing sprawl inside
  someone else's process model — the opposite of "broker, not brain."
- The event-log spine, fencing/lease write-authority, and per-(project,agent)
  portable memory are sprawl-specific and don't exist off the shelf.
- Public-repo + bring-your-own-IdP + multi-cloud-portable constraints rule out
  the BaaS-flavored options.

**What to borrow, not build:** infrastructure primitives, not the product.
Connect/protobuf for transport, managed Postgres, `gocloud.dev` for blob/secrets
portability, OIDC via `go-oidc`, Terraform for IaC. (Stack rationale lives in the
relevant leaf docs.)

**Recommendation: BUILD the hub, ADOPT the plumbing.** The differentiators
(optional broker, event-log spine, write-authority, portable memory) are exactly
the parts no tool provides; the parts tools *do* provide are commodity libraries
we should lean on rather than reinvent.

## North-star vision — NOT COMMITTED / future

> ⚠️ **This subsection is a directional sketch, not a commitment.** It exists so
> the foundation doesn't accidentally foreclose it. None of it is in scope for
> the hub MVP.

- **Persistent, declaratively-defined agents** that are reused (rarely retired),
  each with its own memory.
- **User-addressable sub-agents** — talk to any agent from the browser, not just
  weave.
- **A hierarchical org** — weave manages ~5–10 domain owners who hire their own
  children down to engineers.
- **Hierarchy-only delegation** with a **cross-tree work-request protocol** for
  work that spans branches of the org.
- **An org-chart watcher/summarizer** that keeps a live picture of who's doing
  what.
- **A memory/session console** for browsing and curating agent memory.

Recent lifecycle work already leans this way: `StatusComplete` is now a
*revivable resting state* (agents go dormant, not dead — see `CLAUDE.md`
lifecycle section), which is the substrate persistent reusable agents need.

## Open Questions

- What is the smallest useful MVP slice — read-only multi-host status pane
  first, or full drive-a-session round-trip? (Resolved in
  [`13-implementation-plan`](README.md).)
- Do we need per-host *push* of screenshots/attachments in v1, or is
  browser-side upload to the hub sufficient? (See attachments doc.)
- Is "N clients connected" enough of a guard for v1, or will accidental
  double-driving cause real confusion in practice?
- Should the hub ever run *co-located* with a host (single-machine convenience
  deploy), or is it always a separate service?
- How much of the north-star org model, if any, should the foundation schema
  anticipate now vs. migrate to later?
