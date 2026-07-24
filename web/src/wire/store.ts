// store.ts is a hand-rolled append-only store implementing the
// useSyncExternalStore contract (subscribe / getSnapshot). It wraps a
// TranscriptModel and exposes an immutable-enough snapshot wrapper whose
// reference changes only when visible state changes — so React re-renders on
// new frames but never tears or loops on an unchanged snapshot.

import { WireFrame } from "../../gen/hub/v1/hub_pb";
import { TranscriptModel, type Entry, type Status } from "./transcript";

export interface TailSnapshot {
  entries: Entry[];
  status: Status;
}

export interface TailStore {
  subscribe(cb: () => void): () => void;
  getSnapshot(): TailSnapshot;
  ingest(frame: WireFrame): void;
  reset(): void;
}

// DEFAULT_HIGH_WATER bounds the in-browser entry ring; oldest rows drop above
// it, mirroring the host-side buffer policy.
const DEFAULT_HIGH_WATER = 5000;

export function createTailStore(highWater = DEFAULT_HIGH_WATER): TailStore {
  const model = new TranscriptModel();
  const listeners = new Set<() => void>();
  let snapshot: TailSnapshot = { entries: model.entries, status: model.status };

  const rebuild = () => {
    snapshot = { entries: model.entries, status: model.status };
  };
  const notify = () => {
    for (const l of listeners) l();
  };

  return {
    subscribe(cb) {
      listeners.add(cb);
      return () => {
        listeners.delete(cb);
      };
    },
    getSnapshot: () => snapshot,
    ingest(frame) {
      // Only re-snapshot + notify when the frame actually changed visible
      // state; heartbeats and non-rendered frames are silent.
      if (!model.ingest(frame)) return;
      model.trim(highWater);
      rebuild();
      notify();
    },
    reset() {
      model.reset();
      rebuild();
      notify();
    },
  };
}
