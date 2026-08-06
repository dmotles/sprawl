# attachments-multimodal — Screenshot / Image Ingestion

*How a pasted/dropped image reaches the live claude session as an image content
block — and the verified feasibility of doing so.*

See also: [`00-overview.md`](00-overview.md) · [`01-architecture.md`](01-architecture.md) · [index](README.md)

---

## TL;DR

- **Feasibility: RESOLVED — YES.** The `claude` CLI in **stream-json input
  mode** (`--input-format stream-json`, exactly how sprawl launches it) accepts
  a user message whose `message.content` is an **array of content blocks**,
  including `image` blocks with a base64 source. Verified against Anthropic's
  Claude Agent SDK "Streaming Input" documentation (cited below).
- **One schema change gates everything:** `MessageParam.Content` goes from a
  plain `string` to *string-or-content-blocks* (mirroring the Anthropic message
  shape). Everything downstream (writer → stdin) already carries whatever we
  marshal.
- **v1 = browser-upload-only** (image → hub blob → downlink reference → host
  fetches → base64 stdin block) **+ local TUI paste**. No per-host push server.

---

## 1. MUST-RESOLVE: does claude stream-json INPUT accept image blocks?

**Verified answer: YES — via base64 (or URL / Files `file_id`) `image` content
blocks in a stream-json user message, and ONLY in streaming-input mode.**

Sprawl already launches claude with `--input-format stream-json
--output-format stream-json --verbose` (`internal/backend/claude/adapter.go`
`realStarter.Start`; flag assembly in `internal/claude/launch.go` `BuildArgs`).
That is precisely the **streaming input mode** of the Claude Agent SDK — the
mode that supports image attachments. The exact accepted wire shape:

```jsonc
{
  "type": "user",
  "message": {
    "role": "user",
    "content": [
      { "type": "text", "text": "Review this architecture diagram" },
      {
        "type": "image",
        "source": {
          "type": "base64",
          "media_type": "image/png",
          "data": "<BASE64_IMAGE_BYTES>"
        }
      }
    ]
  },
  "parent_tool_use_id": null
}
```

**Source (definitive):** Claude Agent SDK — *Streaming Input* /
*Streaming vs Single Mode*
(`https://code.claude.com/docs/en/agent-sdk/streaming-input`). It shows this
exact `message.content` array with a base64 `image` block in both the TypeScript
(`SDKUserMessage`) and Python examples, and explicitly states that **single
message input mode does *not* support image attachments** — only streaming
input mode does. The block shape (`image` → `source` → `{type, media_type,
data}`) matches the Messages API Vision contract
(`https://platform.claude.com/docs/en/build-with-claude/vision.md`).

**Why this is safe to build on:** the stream-json `message` object is an
Anthropic **`MessageParam`** — the same union the Messages API takes. `content`
is `string | ContentBlockParam[]`; `ContentBlockParam` includes text, image,
and document blocks. The CLI forwards it to the API unchanged.

### Verified constraints (base64 path, Claude API direct)

| Constraint | Value | Source |
|---|---|---|
| Source types | `base64`, `url`, `file` (`file_id`) | Vision docs |
| Formats | JPEG, PNG, GIF, WebP (`image/jpeg\|png\|gif\|webp`) | Vision docs |
| Max size / image | **10 MB** base64-encoded (Claude API direct) | Vision docs |
| Max dimensions | 8000×8000 px | Vision docs |
| Images / request | 100 (200k-ctx models) / 600 (others) | Vision docs |
| Token cost | high-res tier (Opus 4.8) ≤ 2576 px long edge, ≤ 4784 visual tokens | Vision docs |

### Fallback (not needed, documented for completeness)

If a future CLI ever rejected inline blocks, the fallbacks would be: (a) write
the image to a temp path and reference it in a text prompt (`Read this image:
/tmp/x.png`) so the model's own `Read` tool ingests it, or (b) upload to the
Anthropic Files API and send a `{"type":"image","source":{"type":"file",
"file_id":…}}` block (requires beta header `files-api-2025-04-14`). **Neither is
required** — inline base64 in stream-json is confirmed working.

---

## 2. Schema change — `MessageParam.Content` → content blocks

Today (`internal/protocol/types.go`, ~L187):

```go
type MessageParam struct {
    Role    string `json:"role"`
    Content string `json:"content"`   // plain string only
}
```

The Anthropic shape is a **union**: `content` is either a string *or* an array
of typed blocks. Go has no native unions, so model it as content that marshals
to whichever form is present.

**Recommended (KISS):** keep a string fast-path, add an optional blocks slice,
and give the type a custom `MarshalJSON` that emits a bare string when only
`Content` is set and an array when blocks are present. This is 100%
backward-compatible on the wire — every existing text turn still serializes to
`"content":"…"`, so no reader/replay/`--replay-user-messages` behavior changes.

```go
type MessageParam struct {
    Role    string         `json:"role"`
    Content string         `json:"-"` // text fast-path (existing callers)
    Blocks  []ContentBlock `json:"-"` // set when multimodal
}

// ContentBlock mirrors the Anthropic block union (text | image | …).
type ContentBlock struct {
    Type   string       `json:"type"`             // "text" | "image"
    Text   string       `json:"text,omitempty"`   // type=="text"
    Source *ImageSource `json:"source,omitempty"` // type=="image"
}

type ImageSource struct {
    Type      string `json:"type"`                 // "base64" | "url" | "file"
    MediaType string `json:"media_type,omitempty"` // base64 only
    Data      string `json:"data,omitempty"`       // base64 only
    URL       string `json:"url,omitempty"`        // url only
    FileID    string `json:"file_id,omitempty"`    // file only
}

func (m MessageParam) MarshalJSON() ([]byte, error) {
    type alias struct {
        Role    string `json:"role"`
        Content any    `json:"content"`
    }
    a := alias{Role: m.Role}
    if len(m.Blocks) > 0 {
        a.Content = m.Blocks   // → array form
    } else {
        a.Content = m.Content  // → bare string form
    }
    return json.Marshal(a)
}
```

**Where it serializes (unchanged):** `MessageParam` is embedded in
`protocol.UserMessage`, marshaled by `protocol.Writer.WriteJSON`
(`internal/protocol/writer.go`) and written to the subprocess stdin pipe via the
`transport.Send` path (`internal/backend/claude/adapter.go`). The custom
`MarshalJSON` slots in transparently — no changes to the writer or the pipe.

> **Simplest vs. right.** *Simplest:* `Content any` (accept a string or
> `[]map[string]any`) — least code, zero type safety, easy to malform a block.
> *Right:* typed `ContentBlock`/`ImageSource` + `MarshalJSON` above — a few
> dozen lines, but the block shape is validated at compile time and reused by
> both the browser and local-paste paths. **Recommendation: the typed union.**
> It's cheap and it's the one thing that's ugly to retrofit once callers depend
> on `any`.

Construction sites that build a text `MessageParam` (e.g.
`internal/runtime/unified.go` `WriteUserPrompt`) are untouched — they keep
setting `Content`. Only the new attachment path sets `Blocks`.

---

## 3. Pipeline — browser upload (v1 primary)

Attachments ride the **existing hub topology** (persistent dial-out conn +
blob store) from [`01-architecture.md`](01-architecture.md); nothing new at the
transport layer. The image bytes take the blob store, not the event log.

```
 browser (paste/drop image)
   │  1. HTTPS multipart upload  →  HUB
   │                                 ├─ store bytes in blob store (gocloud.dev/blob)
   │                                 └─ mint {attachment_id, media_type, size, sha256}
   │  2. browser sends a normal turn-input downlink command, carrying
   │     attachment refs instead of bytes:
   │        { text: "what's wrong here?", attachments: ["att_abc"] }
   ▼
  HUB ── downlink on the host's persistent conn ──▶ HOST
                                                      │ 3. host fetches bytes for att_abc
                                                      │    from hub blob store (RPC/HTTPS)
                                                      │ 4. base64-encode + sniff media_type
                                                      │ 5. build MessageParam.Blocks:
                                                      │      [ {image, base64 …}, {text …} ]
                                                      │ 6. enqueue into the ONE turn-queue
                                                      ▼
                                              claude stdin (stream-json)
                                                      │  (image-then-text ordering)
                                                      ▼
                                              result re-enters the uplink
```

Key properties:

- **Bytes travel via the blob store, references via the turn-queue.** The
  downlink command stays tiny; the event log never carries multi-MB base64. This
  is the "broker, not brain" principle applied to attachments — the hub stores
  and transports, the host assembles the claude message.
- **Host pulls, hub doesn't push bytes down the event stream.** Reuses the same
  blob-fetch seam the hub already needs for snapshots. A hub outage between
  upload and fetch just delays the turn (host retries), never corrupts it.
- **Image-then-text ordering.** Vision docs: Claude works best with the image
  block *before* the text. Assemble `Blocks` as `[image…, text]`.
- **Sniff, don't trust.** Host validates `media_type` ∈ {jpeg,png,gif,webp} and
  size ≤ 10 MB before encoding; reject early with a clear downlink error.

### Simplest vs. right (transport of the bytes)

- **Simplest:** browser base64-encodes and inlines the image directly into the
  downlink command; host passes it straight through. *Cost:* multi-MB payloads
  on the turn-queue/event log, blows the (existing) 10 MB stdin cap and bloats
  every replay/snapshot; no dedupe.
- **Right:** upload to hub blob store, downlink carries a small reference, host
  fetches. *Cost:* one extra fetch round-trip + a blob-GC policy (already owed by
  snapshots).
- **Recommendation: blob store + reference.** The blob store already exists in
  the stack for snapshots/attachments; inlining bytes into the log is the thing
  the whole event-log spine design is trying to avoid.

---

## 4. Local TUI paste (secondary)

When the user pastes/drops an image into the local `sprawl enter` TUI, the bytes
are already on the host — **skip the hub entirely.**

```
TUI paste (image bytes on host)
   → validate media_type + size
   → base64-encode
   → MessageParam{ Blocks: [ {image,base64…}, {text…} ] }
   → same WriteUserMessage → stdin path as any typed turn
```

This path needs **only** the §2 schema change — no hub, no blob store, no
downlink. It is therefore the cheapest way to ship *and prove* the image-input
plumbing end-to-end before the hub exists, and it works in disconnected mode.

- **Terminal image capture** is the fiddly part: bracketed-paste of raw image
  bytes is terminal-dependent. Simplest reliable v1 = a `/attach <path>` command
  or drag-drop that yields a filesystem path the TUI reads; treat true
  clipboard-image paste as a follow-up.

> **Simplest vs. right (local capture).** *Simplest:* `/attach <path>` — read a
> file the user points at. *Right:* intercept clipboard/drag image bytes across
> terminals. **Recommendation: ship `/attach` first**; it exercises the exact
> same `Blocks` assembly and is terminal-agnostic.

---

## 5. Simplest way vs. right way (overall scope)

| | Simplest (v1) | Right (later) | Cost of "right" |
|---|---|---|---|
| Ingest | Browser upload → hub blob + local `/attach` | + true TUI clipboard paste; per-host push | Terminal-specific capture code |
| Bytes to model | Inline **base64** block | + Files API `file_id` for reuse across turns | Anthropic Files upload + beta header + GC |
| Storage | Hub blob store, ref in downlink | + dedupe by sha256, thumbnails, retention tiers | GC/retention policy work |

**Recommendation:** ship **browser-upload + local `/attach`, base64 blocks,
blob-store-by-reference**. It resolves the feasibility risk, reuses existing
infra (blob store, turn-queue, stream-json), and needs exactly one schema change.
Defer Files-API `file_id`, clipboard capture, and per-host push until a real cost
(repeated large images across many turns) justifies them.

---

## Open Questions

- **Files API vs. inline base64 for multi-turn:** base64 re-sends full bytes on
  every turn (history replay). If screenshot-heavy sessions get expensive, switch
  to a `file_id` block — but that means the *host* uploads to the Anthropic Files
  API (beta `files-api-2025-04-14`) and tracks file ids. Worth it, or premature?
- **Blob fetch channel:** does the host pull attachment bytes over the existing
  persistent bidi conn, or via a separate authenticated HTTPS GET to the hub? The
  latter is simpler but adds a second auth surface.
- **Where to enforce size/format:** browser (fail fast, but spoofable), hub
  (single choke point), or host (authoritative, but wasted upload on reject)? Lean
  host-authoritative + browser-advisory.
- **`--replay-user-messages` echo of image turns:** confirm the CLI echoes an
  image-bearing user message intact (uuid preserved) so the Slice-2 consumption-
  ack contract still holds for multimodal turns. (Text turns verified; image
  turns to be smoke-tested.)
- **Turn-queue ordering with attachments:** if a text turn and an image turn are
  enqueued near-simultaneously from different sources, is strict arrival order
  still the right rule, or should an attachment "stick" to its accompanying text?
- **Redaction / retention:** screenshots may contain secrets. Does the hub blob
  store need a shorter retention / on-demand purge for attachments than for the
  event log? (Ties into security-privacy doc.)
- **Max practical inline size on stdin:** existing piped-stdin cap is 10 MB and
  the API base64 cap is 10 MB/image — confirm the combined stream-json line
  (base64 + JSON overhead ≈ 1.37×) stays under the host's stdin write limits.
