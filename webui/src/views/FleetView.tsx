import { EmptyState } from "../components/EmptyState";
import { Notice, Page } from "../components/Page";

export function FleetView() {
  return (
    <Page
      title="Fleet"
      subtitle="Which agents are alive, on which host, and what they are working on."
    >
      <Notice>
        Advisory only. <code>agent_sessions</code> is a projection; each host's
        local agent state is the authority, so this view can lag or disagree.
      </Notice>
      <div className="card">
        <EmptyState
          title="No agent sessions"
          body="Sessions appear here once the read API is wired up."
        />
      </div>
    </Page>
  );
}
