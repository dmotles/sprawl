import { EmptyState } from "../components/EmptyState";
import { Page } from "../components/Page";

export function WorkflowsView() {
  return (
    <Page
      title="Workflows"
      subtitle="Workflow instances in flight, and the event log for each one."
    >
      <div className="card">
        <EmptyState
          title="No workflow instances"
          body="An instance is in flight while it has no closed_at timestamp. The schema records no failure state, so a crashed instance and a running one look alike here."
        />
      </div>
    </Page>
  );
}
