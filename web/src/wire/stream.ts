// stream.ts is the framework-agnostic wire-log transport: a reconnecting
// server-stream consumer that follows the "one rule" — track the last wire seq
// seen and, on any disconnect, resume from it so the hub replays with zero gaps
// and zero dupes. A fresh subscribe uses fromSeq=0 (full replay); reconnects
// use fromSeq=lastSeq (the hub resumes at lastSeq+1). Aborting the passed signal
// stops the loop.

import { WireFrame, WireFrameKind } from "../../gen/hub/v1/hub_pb";
import type { HubClient } from "../client";

export interface WireTarget {
  hostId: string;
  runId: string;
  sessionId: string;
}

export interface RunWireLogStreamOpts {
  client: HubClient;
  target: WireTarget;
  signal: AbortSignal;
  onFrame: (frame: WireFrame) => void;
  onError?: (err: unknown) => void;
  // backoffMs maps a consecutive-failure attempt count to a delay. Defaults to
  // capped exponential backoff; tests inject () => 0.
  backoffMs?: (attempt: number) => number;
}

function defaultBackoff(attempt: number): number {
  if (attempt <= 0) return 0;
  return Math.min(1000 * 2 ** (attempt - 1), 30_000);
}

// delay resolves after ms, or immediately when the signal aborts, so a pending
// backoff never outlives an abort.
function delay(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve) => {
    if (signal.aborted) {
      resolve();
      return;
    }
    const onAbort = () => {
      clearTimeout(timer);
      resolve();
    };
    const timer = setTimeout(() => {
      signal.removeEventListener("abort", onAbort);
      resolve();
    }, ms);
    signal.addEventListener("abort", onAbort, { once: true });
  });
}

// runWireLogStream runs until the signal is aborted. It reconnects after both
// clean stream ends (live-tail server rotated the connection) and errors,
// resuming from the last DATA seq observed.
export async function runWireLogStream(opts: RunWireLogStreamOpts): Promise<void> {
  const { client, target, signal, onFrame, onError } = opts;
  const backoff = opts.backoffMs ?? defaultBackoff;
  let lastSeq = 0n;
  let attempt = 0;

  while (!signal.aborted) {
    let gotData = false;
    try {
      const stream = client.subscribeWireLog(
        { hostId: target.hostId, runId: target.runId, sessionId: target.sessionId, fromSeq: lastSeq },
        { signal },
      );
      for await (const resp of stream) {
        const frame = resp.frame;
        if (!frame) continue;
        onFrame(frame);
        // Only DATA frames advance the resume cursor and reset backoff;
        // heartbeats carry a zero seq and must never lower/advance it — nor
        // let a heartbeat-then-close server busy-loop reconnects at zero delay.
        if (frame.kind === WireFrameKind.DATA) {
          gotData = true;
          if (frame.seq > lastSeq) lastSeq = frame.seq;
        }
      }
    } catch (err) {
      if (signal.aborted) return;
      onError?.(err);
      attempt += 1;
      await delay(backoff(attempt), signal);
      continue;
    }
    if (signal.aborted) return;
    // A connection that delivered real data resets backoff; a clean end with no
    // DATA (only heartbeats, or nothing) backs off so a server that keeps
    // closing can't be hot-looped.
    attempt = gotData ? 0 : attempt + 1;
    await delay(backoff(attempt), signal);
  }
}
