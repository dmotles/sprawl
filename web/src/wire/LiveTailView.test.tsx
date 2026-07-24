import { StrictMode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createPromiseClient, createRouterTransport } from "@connectrpc/connect";
import { HubService } from "../../gen/hub/v1/hub_connect";
import { WireFrame, WireFrameKind } from "../../gen/hub/v1/hub_pb";
import { LiveTailView } from "./LiveTailView";

// react-virtual measures 0-height rows in jsdom and would render nothing. Give
// the scroll container + rows a real size so head rows mount and are assertable.
beforeEach(() => {
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockReturnValue({
    height: 600,
    width: 800,
    top: 0,
    left: 0,
    right: 800,
    bottom: 600,
    x: 0,
    y: 0,
    toJSON: () => ({}),
  } as DOMRect);
});
afterEach(() => vi.restoreAllMocks());

function frame(seq: bigint, obj: unknown): { frame: WireFrame } {
  return { frame: new WireFrame({ kind: WireFrameKind.DATA, seq, raw: JSON.stringify(obj) }) };
}
function assistant(seq: bigint, id: string, block: unknown) {
  return frame(seq, { type: "assistant", message: { id, role: "assistant", content: [block] } });
}
function state(seq: bigint, s: string) {
  return frame(seq, { type: "system", subtype: "session_state_changed", state: s });
}

// makeClient serves one instance and a subscribe stream that cold-joins with a
// multi-frame assistant message + running, then (once `gate` resolves) flips to
// idle and holds open like a real live tail.
function makeClient(gate: Promise<void>) {
  const requests: Array<{ hostId: string; runId: string; sessionId: string }> = [];
  const transport = createRouterTransport(({ service }) => {
    service(HubService, {
      listInstances() {
        return { instances: [{ hostId: "host-1", repoLabel: "repo-a" }] };
      },
      async *subscribeWireLog(req) {
        requests.push({ hostId: req.hostId, runId: req.runId, sessionId: req.sessionId });
        yield assistant(1n, "m1", { type: "thinking", thinking: "…" });
        yield assistant(2n, "m1", { type: "text", text: "the answer" });
        yield assistant(3n, "m1", { type: "tool_use", id: "tu", name: "bash", input: {} });
        yield state(4n, "running");
        await gate;
        yield state(5n, "idle");
        await new Promise<void>(() => {}); // hold open until unmount aborts.
      },
    });
  });
  return { client: createPromiseClient(HubService, transport), requests };
}

async function startTail() {
  const user = userEvent.setup();
  await user.click(await screen.findByRole("button", { name: /host-1/ }));
  await user.type(screen.getByLabelText(/run id/i), "run-1");
  await user.type(screen.getByLabelText(/session id/i), "sess-1");
  await user.click(screen.getByRole("button", { name: /^start/i }));
  return user;
}

describe("LiveTailView", () => {
  it("gates Start until an instance is picked and run/session are entered", async () => {
    const gate = new Promise<void>(() => {});
    render(<LiveTailView client={makeClient(gate).client} />);
    // Before selecting, Start is disabled.
    await screen.findByRole("button", { name: /host-1/ });
    expect(screen.getByRole("button", { name: /^start/i })).toBeDisabled();

    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: /host-1/ }));
    await user.type(screen.getByLabelText(/run id/i), "run-1");
    // Still disabled without a session id.
    expect(screen.getByRole("button", { name: /^start/i })).toBeDisabled();
    await user.type(screen.getByLabelText(/session id/i), "sess-1");
    expect(screen.getByRole("button", { name: /^start/i })).toBeEnabled();
  });

  it("cold-joins, renders the accumulated multi-frame message, and flips the pill", async () => {
    let release!: () => void;
    const gate = new Promise<void>((res) => {
      release = res;
    });
    const { client, requests } = makeClient(gate);
    render(<LiveTailView client={client} />);
    await startTail();

    // Cold-join replay renders the accumulated text (not just the last block)…
    expect(await screen.findByText(/the answer/)).toBeInTheDocument();
    // …and the trailing tool_use block coexists (proves multi-block accumulation).
    const log = screen.getByLabelText(/wire log/i);
    expect(within(log).getByText(/bash/)).toBeInTheDocument();

    // The typed triple must actually flow to the SubscribeWireLog RPC.
    expect(requests[0]).toMatchObject({ hostId: "host-1", runId: "run-1", sessionId: "sess-1" });

    // Pill reflects running from the session_state_changed frame.
    const pill = await screen.findByRole("status", { name: /session status/i });
    await waitFor(() => expect(pill).toHaveTextContent(/running/i));

    // Live-tail flips it to idle.
    release();
    await waitFor(() =>
      expect(screen.getByRole("status", { name: /session status/i })).toHaveTextContent(/idle/i),
    );
  });

  it("does not duplicate replayed frames under StrictMode double-mount", async () => {
    const gate = new Promise<void>(() => {});
    render(
      <StrictMode>
        <LiveTailView client={makeClient(gate).client} />
      </StrictMode>,
    );
    await startTail();

    await screen.findByText(/the answer/);
    // StrictMode double-invokes the subscribe effect; the store must reset per
    // (re)subscribe so the fromSeq=0 replay is not appended twice.
    const log = screen.getByLabelText(/wire log/i);
    await waitFor(() => expect(within(log).getAllByText(/the answer/)).toHaveLength(1));
  });
});
