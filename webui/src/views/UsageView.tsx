import { ChartFrame } from "../components/ChartFrame";
import { EmptyState } from "../components/EmptyState";
import { Notice, Page } from "../components/Page";
import { StatTile } from "../components/StatTile";

const LOWER_BOUND_NOTE = "Lower bound";

export function UsageView() {
  return (
    <Page
      title="Cost & usage"
      subtitle="Token and dollar spend, derived from turn_finished and run_finished events."
    >
      <Notice>
        Every number on this page is a <strong>lower bound</strong>. Cost lives
        only in event payloads, and those events are spillable — spend recorded
        while the database was unreachable never reaches the log.
      </Notice>
      <div className="tiles">
        <StatTile label="Spend" note={LOWER_BOUND_NOTE} />
        <StatTile label="Input tokens" note={LOWER_BOUND_NOTE} />
        <StatTile label="Output tokens" note={LOWER_BOUND_NOTE} />
        <StatTile label="Turns" note={LOWER_BOUND_NOTE} />
      </div>
      <ChartFrame
        title="Spend over time"
        caveat="Single series, so no legend — the title names it."
      >
        <EmptyState
          title="No usage data"
          body="The series renders here once the read API is wired up."
        />
      </ChartFrame>
    </Page>
  );
}
