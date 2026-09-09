import { EmptyState } from "../components/EmptyState";
import { Page } from "../components/Page";

export function LedgerView() {
  return (
    <Page
      title="Event ledger"
      subtitle="The raw append-only event log, in sequence order."
    >
      <div className="card">
        <EmptyState
          title="No events"
          body="Events appear here once the read API is wired up. Filters will be limited to the indexed columns — project, workflow instance, and sequence range."
        />
      </div>
    </Page>
  );
}
