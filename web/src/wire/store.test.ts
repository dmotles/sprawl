import { describe, expect, it, vi } from "vitest";
import { createTailStore } from "./store";
import { WireFrame, WireFrameKind } from "../../gen/hub/v1/hub_pb";

function assistantFrame(seq: bigint, id: string, text: string): WireFrame {
  return new WireFrame({
    kind: WireFrameKind.DATA,
    seq,
    raw: JSON.stringify({
      type: "assistant",
      message: { id, role: "assistant", content: [{ type: "text", text }] },
    }),
  });
}

describe("createTailStore", () => {
  it("reflects ingested frames in the snapshot", () => {
    const s = createTailStore();
    expect(s.getSnapshot().entries).toHaveLength(0);
    s.ingest(assistantFrame(1n, "m1", "hi"));
    expect(s.getSnapshot().entries).toHaveLength(1);
  });

  it("returns a stable snapshot reference when nothing changes", () => {
    const s = createTailStore();
    s.ingest(assistantFrame(1n, "m1", "hi"));
    expect(s.getSnapshot()).toBe(s.getSnapshot());
  });

  it("returns a new snapshot reference after an ingest that changes data", () => {
    const s = createTailStore();
    const before = s.getSnapshot();
    s.ingest(assistantFrame(1n, "m1", "hi"));
    expect(s.getSnapshot()).not.toBe(before);
  });

  it("notifies subscribers on ingest and stops after unsubscribe", () => {
    const s = createTailStore();
    const cb = vi.fn();
    const unsub = s.subscribe(cb);
    s.ingest(assistantFrame(1n, "m1", "a"));
    expect(cb).toHaveBeenCalledTimes(1);
    unsub();
    s.ingest(assistantFrame(2n, "m2", "b"));
    expect(cb).toHaveBeenCalledTimes(1);
  });

  it("does not notify or change the snapshot for heartbeat frames", () => {
    const s = createTailStore();
    const cb = vi.fn();
    s.subscribe(cb);
    const before = s.getSnapshot();
    s.ingest(new WireFrame({ kind: WireFrameKind.HEARTBEAT, seq: 0n, raw: "" }));
    expect(s.getSnapshot()).toBe(before);
    expect(cb).not.toHaveBeenCalled();
  });

  it("bounds memory with a drop-oldest ring at the high-water mark", () => {
    const s = createTailStore(3);
    for (let i = 1; i <= 5; i += 1) s.ingest(assistantFrame(BigInt(i), `m${i}`, `t${i}`));
    const entries = s.getSnapshot().entries;
    expect(entries).toHaveLength(3);
    // Oldest dropped → exactly m3, m4, m5 remain, in order.
    const ids = entries.map((e) => (e.kind === "assistant" ? e.id : e.kind));
    expect(ids).toEqual(["m3", "m4", "m5"]);
  });

  it("reset clears entries and status", () => {
    const s = createTailStore();
    s.ingest(assistantFrame(1n, "m1", "hi"));
    s.reset();
    expect(s.getSnapshot().entries).toHaveLength(0);
    expect(s.getSnapshot().status).toBe("unknown");
  });
});
