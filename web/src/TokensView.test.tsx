import { describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import {
  Code,
  ConnectError,
  createPromiseClient,
  createRouterTransport,
} from "@connectrpc/connect";
import { HubService } from "../gen/hub/v1/hub_connect";
import type { HostToken } from "../gen/hub/v1/hub_pb";
import { TokensView } from "./TokensView";

// buildClient wires a stateful fake HubService so create → list → revoke
// interact the way the real server does. revokeCalls records every revoke RPC
// so tests can prove confirm-gating (0 calls until confirm) and the cancel path
// (0 calls ever) — not just the rendered outcome.
function buildClient(seed: Partial<HostToken>[] = []) {
  let n = 0;
  const rows: Partial<HostToken>[] = seed.map((r) => ({ ...r }));
  const revokeCalls: string[] = [];
  const transport = createRouterTransport(({ service }) => {
    service(HubService, {
      createHostToken(req) {
        n += 1;
        const tokenId = `id-${n}`;
        rows.push({
          tokenId,
          label: req.label,
          createdAtUnixMs: 1_700_000_000_000n,
          revokedAtUnixMs: 0n,
        });
        return { token: `sprawl_hub_${tokenId}_secret${n}`, tokenId };
      },
      listHostTokens() {
        return { tokens: rows.map((r) => ({ ...r })) };
      },
      revokeHostToken(req) {
        revokeCalls.push(req.tokenId);
        const row = rows.find((r) => r.tokenId === req.tokenId);
        if (row) row.revokedAtUnixMs = 1_700_000_500_000n;
        return {};
      },
    });
  });
  return { client: createPromiseClient(HubService, transport), revokeCalls };
}

function errorClient(failRevoke = false) {
  const transport = createRouterTransport(({ service }) => {
    service(HubService, {
      createHostToken() {
        if (failRevoke) return { token: "sprawl_hub_id-1_secret1", tokenId: "id-1" };
        throw new ConnectError("boom", Code.Internal);
      },
      listHostTokens() {
        return {
          tokens: failRevoke
            ? [
                {
                  tokenId: "act",
                  label: "kill-me",
                  createdAtUnixMs: 1_700_000_000_000n,
                  revokedAtUnixMs: 0n,
                },
              ]
            : [],
        };
      },
      revokeHostToken() {
        throw new ConnectError("revoke-blew-up", Code.Internal);
      },
    });
  });
  return createPromiseClient(HubService, transport);
}

describe("TokensView create", () => {
  it("shows the plaintext token exactly once, copies it, and never persists it", async () => {
    const user = userEvent.setup();
    // Spy AFTER userEvent.setup() — it installs its own clipboard stub, so spy
    // on whatever clipboard is live at copy time.
    const writeText = vi.spyOn(navigator.clipboard, "writeText");
    const setItem = vi.spyOn(Storage.prototype, "setItem");
    render(<TokensView client={buildClient().client} />);

    await user.type(screen.getByLabelText(/label/i), "laptop");
    await user.click(screen.getByRole("button", { name: /create token/i }));

    const secret = await screen.findByText(/sprawl_hub_id-1_secret1/);
    expect(secret).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: /^copy$/i }));
    expect(writeText).toHaveBeenCalledWith("sprawl_hub_id-1_secret1");
    await screen.findByText(/copied/i);

    // Plaintext is state-only: never written to any web storage.
    expect(localStorage.length).toBe(0);
    expect(sessionStorage.length).toBe(0);
    expect(setItem).not.toHaveBeenCalled();

    // Dismiss removes the plaintext from the DOM permanently.
    await user.click(screen.getByRole("button", { name: /dismiss/i }));
    expect(screen.queryByText(/sprawl_hub_id-1_secret1/)).not.toBeInTheDocument();

    // The freshly-created token appears in the list by id/label, without the secret.
    const list = await screen.findByRole("list", { name: /tokens/i });
    expect(within(list).getByText("laptop")).toBeInTheDocument();
    expect(within(list).queryByText(/secret1/)).not.toBeInTheDocument();

    setItem.mockRestore();
  });

  it("reports a copy failure so the operator can copy manually", async () => {
    const user = userEvent.setup();
    vi.spyOn(navigator.clipboard, "writeText").mockRejectedValue(
      new Error("no clipboard"),
    );
    render(<TokensView client={buildClient().client} />);

    await user.type(screen.getByLabelText(/label/i), "laptop");
    await user.click(screen.getByRole("button", { name: /create token/i }));
    await screen.findByText(/sprawl_hub_id-1_secret1/);

    await user.click(screen.getByRole("button", { name: /^copy$/i }));

    expect(await screen.findByText(/copy failed/i)).toBeInTheDocument();
    // The plaintext is still on screen for manual selection.
    expect(screen.getByText(/sprawl_hub_id-1_secret1/)).toBeInTheDocument();
  });
});

describe("TokensView list", () => {
  it("renders active and revoked rows distinctly", async () => {
    // Status-neutral labels so the status badge text is the only /active|revoked/ match.
    render(
      <TokensView
        client={
          buildClient([
            {
              tokenId: "a",
              label: "alpha",
              createdAtUnixMs: 1_700_000_000_000n,
              revokedAtUnixMs: 0n,
            },
            {
              tokenId: "b",
              label: "beta",
              createdAtUnixMs: 1_700_000_000_000n,
              revokedAtUnixMs: 1_700_000_500_000n,
            },
          ]).client
        }
      />,
    );

    const activeRow = (await screen.findByText("alpha")).closest("li")!;
    const revokedRow = screen.getByText("beta").closest("li")!;
    expect(within(activeRow).getByText(/active/i)).toBeInTheDocument();
    expect(within(revokedRow).getByText(/revoked/i)).toBeInTheDocument();
  });
});

describe("TokensView revoke", () => {
  it("requires confirmation, then revokes and refreshes", async () => {
    const user = userEvent.setup();
    const { client, revokeCalls } = buildClient([
      {
        tokenId: "act",
        label: "kill-me",
        createdAtUnixMs: 1_700_000_000_000n,
        revokedAtUnixMs: 0n,
      },
    ]);
    render(<TokensView client={client} />);

    const row = (await screen.findByText("kill-me")).closest("li")!;
    await user.click(within(row).getByRole("button", { name: /^revoke$/i }));

    // Confirm prompt appears; RPC must NOT have fired yet.
    const confirmRow = (await screen.findByText("kill-me")).closest("li")!;
    expect(
      within(confirmRow).getByRole("button", { name: /confirm/i }),
    ).toBeInTheDocument();
    expect(revokeCalls).toHaveLength(0);

    await user.click(within(confirmRow).getByRole("button", { name: /confirm/i }));

    await waitFor(() => {
      const after = screen.getByText("kill-me").closest("li")!;
      expect(within(after).getByText(/revoked/i)).toBeInTheDocument();
    });
    expect(revokeCalls).toEqual(["act"]);
  });

  it("cancels without calling the RPC", async () => {
    const user = userEvent.setup();
    const { client, revokeCalls } = buildClient([
      {
        tokenId: "act",
        label: "keep-me",
        createdAtUnixMs: 1_700_000_000_000n,
        revokedAtUnixMs: 0n,
      },
    ]);
    render(<TokensView client={client} />);

    const row = (await screen.findByText("keep-me")).closest("li")!;
    await user.click(within(row).getByRole("button", { name: /^revoke$/i }));
    await user.click(within(row).getByRole("button", { name: /cancel/i }));

    const after = screen.getByText("keep-me").closest("li")!;
    expect(within(after).getByText(/active/i)).toBeInTheDocument();
    expect(revokeCalls).toHaveLength(0);
  });
});

describe("TokensView errors", () => {
  it("renders a create ConnectError without unmounting", async () => {
    const user = userEvent.setup();
    render(<TokensView client={errorClient()} />);

    await user.type(await screen.findByLabelText(/label/i), "x");
    await user.click(screen.getByRole("button", { name: /create token/i }));

    expect(await screen.findByText(/boom/i)).toBeInTheDocument();
    // Still interactive — the form survives the error.
    expect(screen.getByLabelText(/label/i)).toBeInTheDocument();
  });

  it("renders a revoke ConnectError without unmounting", async () => {
    const user = userEvent.setup();
    render(<TokensView client={errorClient(true)} />);

    const row = (await screen.findByText("kill-me")).closest("li")!;
    await user.click(within(row).getByRole("button", { name: /^revoke$/i }));
    await user.click(within(row).getByRole("button", { name: /confirm/i }));

    expect(await screen.findByText(/revoke-blew-up/i)).toBeInTheDocument();
    // Row still present and interactive.
    expect(screen.getByText("kill-me")).toBeInTheDocument();
  });
});
