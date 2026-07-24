import { describe, expect, it, vi } from "vitest";
import { createPromiseClient, createRouterTransport } from "@connectrpc/connect";
import { runWireLogStream, type WireTarget } from "./stream";
import { WireFrame, WireFrameKind, SubscribeWireLogResponse } from "../../gen/hub/v1/hub_pb";
import { HubService } from "../../gen/hub/v1/hub_connect";
import type { HubClient } from "../client";

const T: WireTarget = { hostId: "h", runId: "r", sessionId: "s" };

type StreamItem = { frame?: WireFrame };
type Script = () => AsyncGenerator<StreamItem>;
type RecordedRequest = { hostId: string; runId: string; sessionId: string; fromSeq: bigint };

// makeStub drives runWireLogStream with a scripted sequence of server-streams,
// one async generator per subscribe call, and records each request so tests can
// assert the resume `fromSeq` and the target passthrough. The last script
// repeats if more calls occur.
function makeStub(scripts: Script[]) {
  const requests: RecordedRequest[] = [];
  let call = 0;
  const client = {
    subscribeWireLog(req: RecordedRequest) {
      requests.push(req);
      const idx = Math.min(call, scripts.length - 1);
      call += 1;
      return scripts[idx]();
    },
  } as unknown as HubClient;
  return { client, requests, calls: () => call };
}

function dataFrame(seq: bigint): WireFrame {
  return new WireFrame({
    kind: WireFrameKind.DATA,
    seq,
    raw: JSON.stringify({ type: "system", subtype: "init" }),
  });
}

function heartbeat(): WireFrame {
  return new WireFrame({ kind: WireFrameKind.HEARTBEAT, seq: 0n, raw: "" });
}

describe("runWireLogStream", () => {
  it("issues a fresh subscribe with fromSeq 0", async () => {
    const ac = new AbortController();
    const stub = makeStub([
      async function* () {
        yield { frame: dataFrame(1n) };
        ac.abort();
      },
    ]);
    await runWireLogStream({
      client: stub.client,
      target: T,
      signal: ac.signal,
      onFrame: () => {},
      backoffMs: () => 0,
    });
    expect(stub.requests[0].fromSeq).toBe(0n);
    // Target must be carried through verbatim — a dropped/misrouted triple
    // would otherwise stay green.
    expect(stub.requests[0].hostId).toBe("h");
    expect(stub.requests[0].runId).toBe("r");
    expect(stub.requests[0].sessionId).toBe("s");
    expect(stub.calls()).toBe(1);
  });

  it("advances lastSeq from DATA frames as bigint and resumes there on reconnect", async () => {
    const ac = new AbortController();
    const big = 9007199254740993n; // > Number.MAX_SAFE_INTEGER — proves no Number() coercion.
    const stub = makeStub([
      async function* () {
        yield { frame: dataFrame(big) };
        // clean end → reconnect
      },
      async function* () {
        ac.abort();
      },
    ]);
    await runWireLogStream({
      client: stub.client,
      target: T,
      signal: ac.signal,
      onFrame: () => {},
      backoffMs: () => 0,
    });
    expect(stub.calls()).toBe(2);
    expect(stub.requests[1].fromSeq).toBe(big);
  });

  it("does not advance lastSeq for heartbeat frames", async () => {
    const ac = new AbortController();
    const stub = makeStub([
      async function* () {
        yield { frame: dataFrame(5n) };
        yield { frame: heartbeat() };
      },
      async function* () {
        ac.abort();
      },
    ]);
    await runWireLogStream({
      client: stub.client,
      target: T,
      signal: ac.signal,
      onFrame: () => {},
      backoffMs: () => 0,
    });
    expect(stub.requests[1].fromSeq).toBe(5n);
  });

  it("backs off (does not reset to attempt 0) after a heartbeat-only clean end", async () => {
    // Guards the hot-loop: a server that emits only a heartbeat then cleanly
    // closes must not busy-loop reconnects at zero backoff.
    const ac = new AbortController();
    const backoffMs = vi.fn(() => 0);
    const stub = makeStub([
      async function* () {
        yield { frame: heartbeat() };
        // clean end with no DATA
      },
      async function* () {
        ac.abort();
      },
    ]);
    await runWireLogStream({
      client: stub.client,
      target: T,
      signal: ac.signal,
      onFrame: () => {},
      backoffMs,
    });
    // A DATA-bearing connection would reset to attempt 0; heartbeat-only must
    // increment, so the backoff after the first connection is attempt 1.
    expect(backoffMs).toHaveBeenCalledWith(1);
  });

  it("resets backoff to attempt 0 after a connection that delivered DATA", async () => {
    const ac = new AbortController();
    const backoffMs = vi.fn(() => 0);
    const stub = makeStub([
      async function* () {
        yield { frame: dataFrame(1n) };
        // clean end after DATA
      },
      async function* () {
        ac.abort();
      },
    ]);
    await runWireLogStream({
      client: stub.client,
      target: T,
      signal: ac.signal,
      onFrame: () => {},
      backoffMs,
    });
    expect(backoffMs).toHaveBeenCalledWith(0);
  });

  it("reconnects after a stream error and reports it", async () => {
    const ac = new AbortController();
    const onError = vi.fn();
    const stub = makeStub([
      async function* () {
        throw new Error("boom");
      },
      async function* () {
        ac.abort();
      },
    ]);
    await runWireLogStream({
      client: stub.client,
      target: T,
      signal: ac.signal,
      onFrame: () => {},
      onError,
      backoffMs: () => 0,
    });
    expect(onError).toHaveBeenCalledTimes(1);
    expect(stub.calls()).toBe(2);
  });

  it("stops the loop and issues no further subscribes once aborted", async () => {
    const ac = new AbortController();
    const stub = makeStub([
      async function* () {
        yield { frame: dataFrame(1n) };
        ac.abort();
      },
    ]);
    await runWireLogStream({
      client: stub.client,
      target: T,
      signal: ac.signal,
      onFrame: () => {},
      backoffMs: () => 0,
    });
    expect(stub.calls()).toBe(1);
  });

  it("delivers every frame to onFrame in order", async () => {
    const ac = new AbortController();
    const seen: bigint[] = [];
    const stub = makeStub([
      async function* () {
        yield { frame: dataFrame(1n) };
        yield { frame: dataFrame(2n) };
        ac.abort();
      },
    ]);
    await runWireLogStream({
      client: stub.client,
      target: T,
      signal: ac.signal,
      onFrame: (f) => seen.push(f.seq),
      backoffMs: () => 0,
    });
    expect(seen).toEqual([1n, 2n]);
  });

  it("interoperates with the real generated client type via createRouterTransport", async () => {
    // Locks runWireLogStream against the actual connect-es client signature
    // (request, options) → AsyncIterable<SubscribeWireLogResponse{frame?}>.
    const ac = new AbortController();
    const transport = createRouterTransport(({ service }) => {
      service(HubService, {
        async *subscribeWireLog(req) {
          expect(req.hostId).toBe("h");
          expect(req.fromSeq).toBe(0n);
          yield new SubscribeWireLogResponse({ frame: dataFrame(1n) });
          yield new SubscribeWireLogResponse({ frame: dataFrame(2n) });
        },
      });
    });
    const client = createPromiseClient(HubService, transport);
    const seen: bigint[] = [];
    await runWireLogStream({
      client,
      target: T,
      signal: ac.signal,
      onFrame: (f) => {
        seen.push(f.seq);
        if (seen.length === 2) ac.abort();
      },
      backoffMs: () => 0,
    });
    expect(seen).toEqual([1n, 2n]);
  });
});
