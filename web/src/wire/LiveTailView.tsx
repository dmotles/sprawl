import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { useVirtualizer } from "@tanstack/react-virtual";
import type { Instance } from "../../gen/hub/v1/hub_pb";
import type { HubClient } from "../client";
import { useLiveTail } from "./useLiveTail";
import type { WireTarget } from "./stream";
import type { Entry, JsonBlock, Status } from "./transcript";

// LiveTailView is the read-only live-tail screen: pick an instance, supply the
// (runId, sessionId) the SubscribeWireLog contract requires, then cold-join +
// live-tail the wire log. No input box — this pane never writes to the session.
export function LiveTailView({ client }: { client: HubClient }) {
  const [instances, setInstances] = useState<Instance[]>([]);
  const [hostId, setHostId] = useState("");
  const [runId, setRunId] = useState("");
  const [sessionId, setSessionId] = useState("");
  const [target, setTarget] = useState<WireTarget | null>(null);

  useEffect(() => {
    let cancelled = false;
    client
      .listInstances({})
      .then((resp) => {
        if (!cancelled) setInstances(resp.instances);
      })
      .catch(() => {
        // The parent App gates auth; a transient list failure just leaves the
        // picker empty rather than tearing down the whole shell.
      });
    return () => {
      cancelled = true;
    };
  }, [client]);

  const canStart = hostId !== "" && runId.trim() !== "" && sessionId.trim() !== "";

  const start = () => {
    if (!canStart) return;
    setTarget({ hostId, runId: runId.trim(), sessionId: sessionId.trim() });
  };

  return (
    <section>
      <h2>Live tail</h2>
      <div>
        <h3>Instance</h3>
        {instances.length === 0 ? (
          <p>No instances registered.</p>
        ) : (
          <ul aria-label="instances">
            {instances.map((i) => (
              <li key={i.hostId}>
                <button
                  type="button"
                  aria-pressed={hostId === i.hostId}
                  onClick={() => setHostId(i.hostId)}
                >
                  {i.hostId} — {i.repoLabel || "(no repo)"}
                </button>
              </li>
            ))}
          </ul>
        )}
      </div>
      <div>
        <label>
          Run ID <input value={runId} onChange={(e) => setRunId(e.target.value)} />
        </label>{" "}
        <label>
          Session ID <input value={sessionId} onChange={(e) => setSessionId(e.target.value)} />
        </label>{" "}
        <button type="button" onClick={start} disabled={!canStart}>
          Start tail
        </button>
      </div>
      {target && <TailPane client={client} target={target} />}
    </section>
  );
}

// TailPane owns the active subscription. useLiveTail rebuilds its store + stream
// whenever the target identity changes, so a new session starts clean.
function TailPane({ client, target }: { client: HubClient; target: WireTarget }) {
  const { entries, status } = useLiveTail(client, target);
  return (
    <div>
      <StatusPill status={status} />
      <LogView entries={entries} />
    </div>
  );
}

function StatusPill({ status }: { status: Status }) {
  const label = status === "unknown" ? "connecting…" : status;
  return (
    <span role="status" aria-label="session status" data-state={status}>
      {label}
    </span>
  );
}

function LogView({ entries }: { entries: Entry[] }) {
  const parentRef = useRef<HTMLDivElement>(null);
  // Stick to the bottom (live-follow) unless the operator has scrolled up to
  // read history; scrolling back near the bottom re-arms follow.
  const followRef = useRef(true);
  const virtualizer = useVirtualizer({
    count: entries.length,
    getScrollElement: () => parentRef.current,
    estimateSize: () => 96,
    overscan: 12,
  });

  const onScroll = () => {
    const el = parentRef.current;
    if (!el) return;
    followRef.current = el.scrollHeight - el.scrollTop - el.clientHeight < 48;
  };

  useLayoutEffect(() => {
    if (followRef.current && entries.length > 0) {
      virtualizer.scrollToIndex(entries.length - 1, { align: "end" });
    }
  }, [entries.length, virtualizer]);

  const items = virtualizer.getVirtualItems();
  return (
    <div
      ref={parentRef}
      onScroll={onScroll}
      aria-label="wire log"
      style={{ height: "60vh", overflow: "auto", border: "1px solid #ccc" }}
    >
      <div style={{ height: virtualizer.getTotalSize(), position: "relative", width: "100%" }}>
        {items.map((vi) => {
          const entry = entries[vi.index];
          return (
            <div
              key={entry.key}
              data-index={vi.index}
              ref={virtualizer.measureElement}
              style={{ position: "absolute", top: 0, left: 0, width: "100%", transform: `translateY(${vi.start}px)` }}
            >
              <EntryView entry={entry} />
            </div>
          );
        })}
      </div>
    </div>
  );
}

function EntryView({ entry }: { entry: Entry }) {
  switch (entry.kind) {
    case "assistant":
      return (
        <article data-role="assistant">
          {entry.blocks.map((b, i) => (
            <BlockView key={i} block={b} />
          ))}
        </article>
      );
    case "user":
      return (
        <article data-role="user">
          {entry.blocks.map((b, i) => (
            <BlockView key={i} block={b} />
          ))}
        </article>
      );
    case "result":
      return (
        <article data-role="result" data-error={entry.isError || undefined}>
          {entry.text}
        </article>
      );
    case "parse_error":
      return (
        <article data-role="parse-error">
          <code>{entry.raw}</code>
        </article>
      );
  }
}

// BlockView renders one Anthropic content block. Unknown block types fall back
// to their raw JSON so nothing is silently dropped.
function BlockView({ block }: { block: JsonBlock }) {
  const type = typeof block.type === "string" ? block.type : "";
  switch (type) {
    case "text":
      return <p>{String(block.text ?? "")}</p>;
    case "thinking":
      return <p data-block="thinking">{String(block.thinking ?? "")}</p>;
    case "tool_use":
      return (
        <p data-block="tool_use">
          🔧 {String(block.name ?? "tool")}
        </p>
      );
    case "tool_result":
      return <pre data-block="tool_result">{stringifyContent(block.content)}</pre>;
    default:
      return <pre data-block="other">{JSON.stringify(block)}</pre>;
  }
}

function stringifyContent(content: unknown): string {
  if (typeof content === "string") return content;
  return JSON.stringify(content);
}
