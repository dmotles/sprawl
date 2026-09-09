import { describe, expect, it } from "vitest";
import { screen } from "@testing-library/react";
import { UsageView } from "./UsageView";
import { ApiError } from "../api/client";
import { fakeClient, makeUsage, pending, renderWithApi } from "../../test/fakes";

describe("UsageView", () => {
  it("shows a loading state while the request is in flight", () => {
    renderWithApi(<UsageView />, fakeClient({ getUsage: () => pending() }));
    expect(screen.getByRole("status")).toHaveTextContent(/loading/i);
  });

  it("renders the totals it was given", async () => {
    renderWithApi(<UsageView />, fakeClient({ getUsage: async () => makeUsage() }));
    expect(await screen.findByText("$41.94")).toBeInTheDocument();
    expect(screen.getByText("18,442,901")).toBeInTheDocument();
    expect(screen.getByText("1,443")).toBeInTheDocument();
  });

  it("renders an unknown total as an em dash, never as a zero", async () => {
    // A $0.00 spend tile is a claim that nothing was spent. When SUM() over an
    // empty set returns null the truth is that we do not know.
    renderWithApi(
      <UsageView />,
      fakeClient({
        getUsage: async () =>
          makeUsage({
            totals: {
              cost_usd: null,
              input_tokens: null,
              output_tokens: null,
              turns: null,
            },
          }),
      }),
    );
    await screen.findAllByText("—");
    expect(screen.queryByText("$0.00")).not.toBeInTheDocument();
    expect(screen.queryByText("0")).not.toBeInTheDocument();
  });

  it("keeps the lower-bound caveat", async () => {
    renderWithApi(<UsageView />, fakeClient({ getUsage: async () => makeUsage() }));
    expect(await screen.findByText(/lower bound/i)).toBeInTheDocument();
  });

  it("plots one bar per bucket in the series", async () => {
    renderWithApi(<UsageView />, fakeClient({ getUsage: async () => makeUsage() }));
    const bars = await screen.findAllByRole("img");
    expect(bars).toHaveLength(2);
    expect(bars[1]).toHaveAccessibleName(/\$29\.53/);
  });

  it("shows an empty chart state when the series is empty but keeps the tiles", async () => {
    renderWithApi(<UsageView />, fakeClient({ getUsage: async () => makeUsage({ series: [] }) }));
    expect(await screen.findByText("No usage recorded")).toBeInTheDocument();
    expect(screen.getByText("$41.94")).toBeInTheDocument();
  });

  it("shows an error, and never zeroed tiles, when the request fails", async () => {
    renderWithApi(
      <UsageView />,
      fakeClient({
        getUsage: async () => {
          throw new ApiError(500, "summing usage failed");
        },
      }),
    );
    expect(await screen.findByRole("alert")).toHaveTextContent("summing usage failed");
    expect(screen.queryByText("$0.00")).not.toBeInTheDocument();
  });
});
