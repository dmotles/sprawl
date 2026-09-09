import { AsyncSection } from "../components/AsyncSection";
import { BarSeries } from "../components/BarSeries";
import { ChartFrame } from "../components/ChartFrame";
import { EmptyState } from "../components/EmptyState";
import { Notice, Page } from "../components/Page";
import { StatTile } from "../components/StatTile";
import { useApi } from "../api/ApiProvider";
import { useQuery } from "../api/useQuery";
import { formatCount, formatUsd } from "../format";

const LOWER_BOUND_NOTE = "Lower bound";

export function UsageView() {
  const api = useApi();
  const query = useQuery(() => api.getUsage(), [api]);

  return (
    <Page
      title="Cost & usage"
      subtitle="Token and dollar spend, derived from turn_finished and run_finished events."
    >
      <Notice>
        Every number on this page is a <strong>lower bound</strong>. Cost lives only in event
        payloads, and those events are spillable — spend recorded while the database was unreachable
        never reaches the log.
      </Notice>
      {/* One query drives both the tiles and the chart, and it has no
          whole-page empty state: null totals are themselves the answer, and the
          chart owns its own empty series. On failure nothing renders, because a
          $0.00 tile beside an error would be a claim about spend. */}
      <AsyncSection query={query}>
        {(usage) => (
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
              title={`Spend over time (per ${usage.bucket})`}
              caveat="Single series, so no legend — the title names it."
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
        )}
      </AsyncSection>
    </Page>
  );
}
