import { AsyncSection } from "../components/AsyncSection";
import { BarSeries } from "../components/BarSeries";
import { ChartFrame } from "../components/ChartFrame";
import { EmptyState } from "../components/EmptyState";
import { Notice, Page } from "../components/Page";
import { StatTile } from "../components/StatTile";
import { useApi } from "../api/ApiProvider";
import { useQuery } from "../api/useQuery";
import type { UsageBucket } from "../api/types";
import { formatCount, formatUsd } from "../format";

const LOWER_BOUND_NOTE = "Lower bound";

/**
 * Folds the returned rows into the four tiles and one series the page shows.
 *
 * The endpoint serves one row per (bucket, project) and NO totals object, so
 * every total here is a sum over the rows the page actually received — which is
 * why the tiles are labelled as such rather than as fleet-wide spend.
 *
 * An empty response yields `null`, not 0: "no rows" is not a statement that
 * nothing was spent, and a $0.00 tile would make exactly that claim.
 */
export function summarizeUsage(buckets: UsageBucket[]) {
  const byBucket = new Map<string, number>();
  for (const row of buckets) {
    byBucket.set(row.bucket_start, (byBucket.get(row.bucket_start) ?? 0) + row.cost_usd);
  }
  return {
    // The API orders newest first; a chart reads left to right in time.
    series: [...byBucket]
      .map(([bucket, cost_usd]) => ({ bucket, cost_usd }))
      .sort((a, b) => a.bucket.localeCompare(b.bucket)),
    unit: buckets[0]?.bucket ?? null,
    totals: {
      cost_usd: buckets.length === 0 ? null : sum(buckets, (b) => b.cost_usd),
      input_tokens: buckets.length === 0 ? null : sum(buckets, (b) => b.input_tokens),
      output_tokens: buckets.length === 0 ? null : sum(buckets, (b) => b.output_tokens),
      turns: buckets.length === 0 ? null : sum(buckets, (b) => b.turns),
    },
  };
}

function sum(buckets: UsageBucket[], of: (b: UsageBucket) => number): number {
  return buckets.reduce((total, bucket) => total + of(bucket), 0);
}

export function UsageView() {
  const api = useApi();
  const query = useQuery(() => api.listUsage(), [api]);

  return (
    <Page
      title="Cost & usage"
      subtitle="Token and dollar spend, derived from turn_finished and run_finished events."
    >
      <Notice>
        Every number on this page is a <strong>lower bound</strong>. Cost lives only in event
        payloads, and those events are spillable — spend recorded while the database was unreachable
        never reaches the log. The totals are a sum over the buckets on this page, and the oldest
        bucket of a full page can itself be partial — the row limit counts rows, not buckets.
      </Notice>
      {/* One query drives both the tiles and the chart, and it has no
          whole-page empty state: null totals are themselves the answer, and the
          chart owns its own empty series. On failure nothing renders, because a
          $0.00 tile beside an error would be a claim about spend. */}
      <AsyncSection query={query}>
        {(buckets) => {
          const usage = summarizeUsage(buckets);
          return (
            <>
              <div className="tiles">
                <StatTile
                  label="Spend"
                  value={formatUsd(usage.totals.cost_usd)}
                  note={LOWER_BOUND_NOTE}
                />
                <StatTile
                  label="Input tokens"
                  value={formatCount(usage.totals.input_tokens)}
                  note={LOWER_BOUND_NOTE}
                />
                <StatTile
                  label="Output tokens"
                  value={formatCount(usage.totals.output_tokens)}
                  note={LOWER_BOUND_NOTE}
                />
                <StatTile
                  label="Turns"
                  value={formatCount(usage.totals.turns)}
                  note={LOWER_BOUND_NOTE}
                />
              </div>
              <ChartFrame
                title={usage.unit ? `Spend over time (per ${usage.unit})` : "Spend over time"}
                caveat="Single series, so no legend — the title names it. Bars sum every project in the bucket."
              >
                {usage.series.length === 0 ? (
                  <EmptyState
                    title="No usage recorded"
                    body="No event in the window carries a cost, so there is no series to plot."
                  />
                ) : (
                  <BarSeries points={usage.series} />
                )}
              </ChartFrame>
            </>
          );
        }}
      </AsyncSection>
    </Page>
  );
}
