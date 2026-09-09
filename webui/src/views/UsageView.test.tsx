import { describe, expect, it } from "vitest";
import { screen } from "@testing-library/react";
import { UsageView, summarizeUsage } from "./UsageView";
import { ApiError } from "../api/client";
import { fakeClient, makeUsageBucket, pending, renderWithApi } from "../../test/fakes";

describe("summarizeUsage", () => {
  it("sums a bucket's projects into one bar, so the chart is not one bar per project", () => {
    const usage = summarizeUsage([
      makeUsageBucket({ bucket_start: "2026-09-09T18:00:00Z", project_id: "p-1", cost_usd: 2 }),
      makeUsageBucket({ bucket_start: "2026-09-09T18:00:00Z", project_id: "p-2", cost_usd: 3 }),
    ]);
    expect(usage.series).toEqual([{ bucket: "2026-09-09T18:00:00Z", cost_usd: 5 }]);
  });

  it("orders the series oldest first, though the API answers newest first", () => {
    const usage = summarizeUsage([
      makeUsageBucket({ bucket_start: "2026-09-09T18:00:00Z", cost_usd: 3 }),
      makeUsageBucket({ bucket_start: "2026-09-09T17:00:00Z", cost_usd: 1 }),
    ]);
    expect(usage.series.map((p) => p.cost_usd)).toEqual([1, 3]);
  });

  it("totals every row, since the endpoint serves no totals object", () => {
    const usage = summarizeUsage([
      makeUsageBucket({ cost_usd: 1.5, turns: 10, input_tokens: 100, output_tokens: 20 }),
      makeUsageBucket({ cost_usd: 2.25, turns: 5, input_tokens: 50, output_tokens: 10 }),
    ]);
    expect(usage.totals).toEqual({
      cost_usd: 3.75,
      turns: 15,
      input_tokens: 150,
      output_tokens: 30,
    });
  });

  it("reports an empty response as unknown, not as zero spend", () => {
    // No rows is not a statement that nothing was spent, and the source events
    // are spillable — a $0.00 total would make a claim the data cannot support.
    expect(summarizeUsage([]).totals).toEqual({
      cost_usd: null,
      turns: null,
      input_tokens: null,
      output_tokens: null,
    });
  });
});

describe("UsageView", () => {
  it("shows a loading state while the request is in flight", () => {
    renderWithApi(<UsageView />, fakeClient({ listUsage: () => pending() }));
    expect(screen.getByRole("status")).toHaveTextContent(/loading/i);
  });

  it("renders totals summed from the buckets it was given", async () => {
    renderWithApi(
      <UsageView />,
      fakeClient({
        listUsage: async () => [
          makeUsageBucket({ cost_usd: 12.41, turns: 631, input_tokens: 100, output_tokens: 20 }),
          makeUsageBucket({
            bucket_start: "2026-09-09T19:00:00Z",
            cost_usd: 29.53,
            turns: 812,
            input_tokens: 50,
            output_tokens: 10,
          }),
        ],
      }),
    );
    expect(await screen.findByText("$41.94")).toBeInTheDocument();
    expect(screen.getByText("1,443")).toBeInTheDocument();
    expect(screen.getByText("150")).toBeInTheDocument();
  });

  it("renders an unknown total as an em dash, never as a zero", async () => {
    renderWithApi(<UsageView />, fakeClient({ listUsage: async () => [] }));
    await screen.findAllByText("—");
    expect(screen.queryByText("$0.00")).not.toBeInTheDocument();
    expect(screen.queryByText("0")).not.toBeInTheDocument();
  });

  it("keeps the lower-bound caveat", async () => {
    renderWithApi(<UsageView />, fakeClient({ listUsage: async () => [makeUsageBucket()] }));
    expect(await screen.findByText(/lower bound/i)).toBeInTheDocument();
  });

  it("says the totals cover only the buckets on the page", async () => {
    // ?limit= bounds rows, not buckets, so the oldest bucket of a full page can
    // be partial. Presenting the sum as fleet-wide spend would overstate what
    // the response supports.
    renderWithApi(<UsageView />, fakeClient({ listUsage: async () => [makeUsageBucket()] }));
    expect(await screen.findByText(/sum over the buckets on this page/i)).toBeInTheDocument();
  });

  it("plots one bar per bucket in the series", async () => {
    renderWithApi(
      <UsageView />,
      fakeClient({
        listUsage: async () => [
          makeUsageBucket({ bucket_start: "2026-09-09T17:00:00Z", cost_usd: 12.41 }),
          makeUsageBucket({ bucket_start: "2026-09-09T18:00:00Z", cost_usd: 29.53 }),
        ],
      }),
    );
    const bars = await screen.findAllByRole("img");
    expect(bars).toHaveLength(2);
    expect(bars[1]).toHaveAccessibleName(/\$29\.53/);
  });

  it("names the bucket unit the API reported, not an assumed one", async () => {
    renderWithApi(
      <UsageView />,
      fakeClient({ listUsage: async () => [makeUsageBucket({ bucket: "day" })] }),
    );
    expect(await screen.findByText(/per day/i)).toBeInTheDocument();
  });

  it("shows an empty chart state when nothing was returned", async () => {
    renderWithApi(<UsageView />, fakeClient({ listUsage: async () => [] }));
    expect(await screen.findByText("No usage recorded")).toBeInTheDocument();
  });

  it("shows an error, and never zeroed tiles, when the request fails", async () => {
    renderWithApi(
      <UsageView />,
      fakeClient({
        listUsage: async () => {
          throw new ApiError(500, "summing usage failed");
        },
      }),
    );
    expect(await screen.findByRole("alert")).toHaveTextContent("summing usage failed");
    expect(screen.queryByText("$0.00")).not.toBeInTheDocument();
  });
});
