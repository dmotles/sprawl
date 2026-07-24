// transcript.ts reconstructs renderable entries from stream-json protocol
// frames carried in WireFrame.raw. It mirrors the accumulation semantics of the
// Go internal/transcript parser: assistant frames sharing one message.id
// ACCUMULATE their content blocks in arrival order and are NEVER deduped by id
// (dedupe-by-id would keep only the final tool_use and silently drop the text).
//
// Unlike the Go parser it does NOT reassemble a per-direction byte stream: the
// hub streams the frame-oriented QUM-902 writer output, so WireFrame.raw already
// holds exactly one whole protocol frame. Heartbeat frames carry no payload and
// are ignored for rendering.

import { WireFrame, WireFrameKind } from "../../gen/hub/v1/hub_pb";

// JsonBlock is one Anthropic content block (thinking | text | tool_use |
// tool_result | image | …), kept as opaque JSON for the view layer to render.
export type JsonBlock = Record<string, unknown>;

export type Status = "running" | "idle" | "unknown";

// ParsedFrame is the classified shape of a single stream-json frame.
export type ParsedFrame =
  | {
      type: "assistant";
      id: string;
      role: string;
      model: string;
      content: JsonBlock[];
      stopReason: string;
    }
  | { type: "user"; content: JsonBlock[] }
  | { type: "session_state"; state: string }
  | { type: "result"; isError: boolean; result: string }
  | { type: "system"; subtype: string }
  | { type: "other" }
  | { type: "parse_error"; raw: string };

// Entry is one renderable row in the transcript. `key` is stable for React
// reconciliation: assistant rows key on message.id (so accumulating frames
// update in place), everything else keys on its wire seq.
export type Entry =
  | {
      kind: "assistant";
      key: string;
      id: string;
      role: string;
      model: string;
      blocks: JsonBlock[];
      stopReason: string;
    }
  | { kind: "user"; key: string; blocks: JsonBlock[] }
  | { kind: "result"; key: string; isError: boolean; text: string }
  | { kind: "parse_error"; key: string; raw: string };

function asRecord(v: unknown): Record<string, unknown> {
  return typeof v === "object" && v !== null ? (v as Record<string, unknown>) : {};
}

function str(v: unknown): string {
  return typeof v === "string" ? v : "";
}

// normalizeContent coerces the Anthropic message content union (bare string or
// array of blocks) into an array of blocks. A bare string becomes a single text
// block so the view has one shape to render.
function normalizeContent(content: unknown): JsonBlock[] {
  if (typeof content === "string") return [{ type: "text", text: content }];
  if (Array.isArray(content)) return content.map((b) => asRecord(b));
  return [];
}

// parseFrame classifies one raw stream-json frame. It never throws: malformed
// JSON yields a parse_error so a single corrupt frame can never abort the tail.
export function parseFrame(raw: string): ParsedFrame {
  let obj: unknown;
  try {
    obj = JSON.parse(raw);
  } catch {
    return { type: "parse_error", raw };
  }
  const o = asRecord(obj);
  switch (o.type) {
    case "assistant": {
      const msg = asRecord(o.message);
      return {
        type: "assistant",
        id: str(msg.id),
        role: str(msg.role),
        model: str(msg.model),
        content: normalizeContent(msg.content),
        stopReason: str(msg.stop_reason),
      };
    }
    case "user": {
      const msg = asRecord(o.message);
      return { type: "user", content: normalizeContent(msg.content) };
    }
    case "result":
      return { type: "result", isError: o.is_error === true, result: str(o.result) };
    case "system":
      if (o.subtype === "session_state_changed") {
        return { type: "session_state", state: str(o.state) };
      }
      return { type: "system", subtype: str(o.subtype) };
    default:
      return { type: "other" };
  }
}

// TranscriptModel is the append-only reducer over ingested frames. It owns the
// ordered entry list and the current running/idle status.
export class TranscriptModel {
  entries: Entry[] = [];
  status: Status = "unknown";
  private byId = new Map<string, Extract<Entry, { kind: "assistant" }>>();

  // ingest folds one frame into the model. Returns true when it changed
  // visible state (a new/extended entry or a status flip) so the store can
  // decide whether to notify — heartbeats and ignored frames return false.
  ingest(frame: WireFrame): boolean {
    if (frame.kind === WireFrameKind.HEARTBEAT) return false;
    const parsed = parseFrame(frame.raw);
    switch (parsed.type) {
      case "assistant": {
        let acc = this.byId.get(parsed.id);
        if (!acc) {
          acc = {
            kind: "assistant",
            key: `a:${parsed.id}`,
            id: parsed.id,
            role: parsed.role,
            model: parsed.model,
            blocks: [],
            stopReason: parsed.stopReason,
          };
          this.byId.set(parsed.id, acc);
          this.entries.push(acc);
        }
        acc.blocks.push(...parsed.content);
        // Last non-empty wins, mirroring the Go accumulator.
        if (parsed.model) acc.model = parsed.model;
        if (parsed.stopReason) acc.stopReason = parsed.stopReason;
        return true;
      }
      case "user":
        this.entries.push({ kind: "user", key: `u:${frame.seq}`, blocks: parsed.content });
        return true;
      case "result":
        this.entries.push({
          kind: "result",
          key: `r:${frame.seq}`,
          isError: parsed.isError,
          text: parsed.result,
        });
        return true;
      case "session_state": {
        const next: Status = parsed.state === "running" || parsed.state === "idle"
          ? parsed.state
          : this.status;
        if (next === this.status) return false;
        this.status = next;
        return true;
      }
      case "parse_error":
        this.entries.push({ kind: "parse_error", key: `e:${frame.seq}`, raw: parsed.raw });
        return true;
      case "system":
      case "other":
        // init / notification / stream_event etc. carry no renderable row here.
        return false;
    }
  }

  // trim drops oldest entries down to `max`, bounding in-memory growth. It also
  // forgets any dropped assistant ids so their accumulator cannot leak.
  trim(max: number): void {
    if (max <= 0 || this.entries.length <= max) return;
    const removed = this.entries.splice(0, this.entries.length - max);
    for (const e of removed) {
      if (e.kind === "assistant") this.byId.delete(e.id);
    }
  }

  reset(): void {
    this.entries = [];
    this.status = "unknown";
    this.byId.clear();
  }
}
