import { describe, expect, it } from "vitest";
import { TranscriptModel, parseFrame } from "./transcript";
import { WireFrame, WireFrameKind } from "../../gen/hub/v1/hub_pb";

function dataFrame(seq: bigint, obj: unknown): WireFrame {
  return new WireFrame({ kind: WireFrameKind.DATA, seq, raw: JSON.stringify(obj) });
}

function assistantFrame(seq: bigint, id: string, block: unknown): WireFrame {
  return dataFrame(seq, {
    type: "assistant",
    message: { id, role: "assistant", model: "claude-x", content: [block] },
  });
}

describe("parseFrame", () => {
  it("classifies an assistant frame and extracts message fields", () => {
    const p = parseFrame(
      JSON.stringify({
        type: "assistant",
        message: {
          id: "m1",
          role: "assistant",
          model: "claude-x",
          content: [{ type: "text", text: "hi" }],
          stop_reason: "end_turn",
        },
      }),
    );
    expect(p.type).toBe("assistant");
    if (p.type === "assistant") {
      expect(p.id).toBe("m1");
      expect(p.role).toBe("assistant");
      expect(p.model).toBe("claude-x");
      expect(p.content).toHaveLength(1);
      expect(p.stopReason).toBe("end_turn");
    }
  });

  it("normalizes a user frame with string content to a text block", () => {
    const p = parseFrame(
      JSON.stringify({ type: "user", message: { role: "user", content: "hello" } }),
    );
    expect(p.type).toBe("user");
    if (p.type === "user") expect(p.content).toEqual([{ type: "text", text: "hello" }]);
  });

  it("passes through a user frame that already carries content blocks", () => {
    const blocks = [{ type: "tool_result", tool_use_id: "t1", content: "ok" }];
    const p = parseFrame(
      JSON.stringify({ type: "user", message: { role: "user", content: blocks } }),
    );
    expect(p.type).toBe("user");
    if (p.type === "user") expect(p.content).toEqual(blocks);
  });

  it("classifies a session_state_changed frame", () => {
    const p = parseFrame(
      JSON.stringify({ type: "system", subtype: "session_state_changed", state: "running" }),
    );
    expect(p.type).toBe("session_state");
    if (p.type === "session_state") expect(p.state).toBe("running");
  });

  it("classifies a result frame", () => {
    const p = parseFrame(
      JSON.stringify({ type: "result", subtype: "success", is_error: false, result: "done" }),
    );
    expect(p.type).toBe("result");
    if (p.type === "result") {
      expect(p.isError).toBe(false);
      expect(p.result).toBe("done");
    }
  });

  it("classifies a generic system frame carrying its subtype", () => {
    const p = parseFrame(JSON.stringify({ type: "system", subtype: "init", model: "m" }));
    expect(p.type).toBe("system");
    if (p.type === "system") expect(p.subtype).toBe("init");
  });

  it("returns parse_error on malformed JSON instead of throwing", () => {
    const p = parseFrame("{not json");
    expect(p.type).toBe("parse_error");
  });
});

describe("TranscriptModel", () => {
  it("reports changed=true for a rendered DATA frame", () => {
    const m = new TranscriptModel();
    expect(m.ingest(assistantFrame(1n, "m1", { type: "text", text: "hi" }))).toBe(true);
  });

  it("accumulates content blocks across frames sharing message.id (never dedupe-by-id)", () => {
    const m = new TranscriptModel();
    m.ingest(assistantFrame(1n, "m1", { type: "thinking", thinking: "hmm" }));
    m.ingest(assistantFrame(2n, "m1", { type: "text", text: "the answer" }));
    m.ingest(assistantFrame(3n, "m1", { type: "tool_use", id: "tu1", name: "bash", input: {} }));

    expect(m.entries).toHaveLength(1);
    const e = m.entries[0];
    expect(e.kind).toBe("assistant");
    if (e.kind === "assistant") {
      expect(e.id).toBe("m1");
      expect(e.blocks).toHaveLength(3);
      // Regression guard: dedupe-by-id would keep only the final tool_use and
      // silently drop the text block.
      expect(
        e.blocks.some((b) => (b as { type?: string; text?: string }).text === "the answer"),
      ).toBe(true);
      expect((e.blocks[0] as { type: string }).type).toBe("thinking");
      expect((e.blocks[2] as { type: string }).type).toBe("tool_use");
    }
  });

  it("accumulates model and stop_reason across frames (last non-empty wins)", () => {
    const m = new TranscriptModel();
    // First frame carries model but no stop_reason; last frame carries both.
    m.ingest(
      dataFrame(1n, {
        type: "assistant",
        message: { id: "m1", role: "assistant", model: "claude-x", content: [{ type: "text", text: "a" }] },
      }),
    );
    m.ingest(
      dataFrame(2n, {
        type: "assistant",
        message: {
          id: "m1",
          role: "assistant",
          model: "",
          content: [{ type: "text", text: "b" }],
          stop_reason: "end_turn",
        },
      }),
    );
    expect(m.entries).toHaveLength(1);
    const e = m.entries[0];
    expect(e.kind).toBe("assistant");
    if (e.kind === "assistant") {
      // Empty model on the later frame must NOT clobber the earlier value.
      expect(e.model).toBe("claude-x");
      expect(e.stopReason).toBe("end_turn");
      expect(e.blocks).toHaveLength(2);
    }
  });

  it("keeps distinct message.ids as separate entries in first-seen order", () => {
    const m = new TranscriptModel();
    m.ingest(assistantFrame(1n, "m1", { type: "text", text: "first" }));
    m.ingest(assistantFrame(2n, "m2", { type: "text", text: "second" }));

    expect(m.entries).toHaveLength(2);
    const [e0, e1] = m.entries;
    expect(e0.kind).toBe("assistant");
    if (e0.kind === "assistant") expect(e0.id).toBe("m1");
    expect(e1.kind).toBe("assistant");
    if (e1.kind === "assistant") expect(e1.id).toBe("m2");
  });

  it("ignores heartbeat frames (no entry, no status change)", () => {
    const m = new TranscriptModel();
    const changed = m.ingest(new WireFrame({ kind: WireFrameKind.HEARTBEAT, seq: 0n, raw: "" }));
    expect(changed).toBe(false);
    expect(m.entries).toHaveLength(0);
    expect(m.status).toBe("unknown");
  });

  it("tracks running/idle from session_state_changed with no needs-input state", () => {
    const m = new TranscriptModel();
    expect(m.status).toBe("unknown");
    m.ingest(dataFrame(1n, { type: "system", subtype: "session_state_changed", state: "running" }));
    expect(m.status).toBe("running");
    m.ingest(dataFrame(2n, { type: "system", subtype: "session_state_changed", state: "idle" }));
    expect(m.status).toBe("idle");
    // requires_action is tolerated on the wire but must never become a status.
    m.ingest(
      dataFrame(3n, { type: "system", subtype: "session_state_changed", state: "requires_action" }),
    );
    expect(m.status).toBe("idle");
  });

  it("appends a user entry with normalized blocks", () => {
    const m = new TranscriptModel();
    m.ingest(dataFrame(1n, { type: "user", message: { role: "user", content: "hi there" } }));
    expect(m.entries).toHaveLength(1);
    const e = m.entries[0];
    expect(e.kind).toBe("user");
    if (e.kind === "user") expect(e.blocks).toEqual([{ type: "text", text: "hi there" }]);
  });

  it("skips malformed frames but keeps parsing later valid frames", () => {
    const m = new TranscriptModel();
    m.ingest(new WireFrame({ kind: WireFrameKind.DATA, seq: 1n, raw: "{bad" }));
    m.ingest(assistantFrame(2n, "m1", { type: "text", text: "ok" }));
    expect(m.entries.find((e) => e.kind === "assistant")).toBeDefined();
  });
});
