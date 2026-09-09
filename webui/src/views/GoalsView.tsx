import { EmptyState } from "../components/EmptyState";
import { Page } from "../components/Page";

export function GoalsView() {
  return (
    <Page
      title="Goals"
      subtitle="Open and closed goals, with their outcomes and closing summaries."
    >
      <div className="card">
        <EmptyState
          title="No goals yet"
          body="Goals appear here once the read API is wired up. A goal is a goal_opened event; it closes when a goal_closed event references it."
        />
      </div>
    </Page>
  );
}
