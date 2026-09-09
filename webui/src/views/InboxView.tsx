import { EmptyState } from "../components/EmptyState";
import { Notice, Page } from "../components/Page";

export function InboxView() {
  return (
    <Page
      title="Inbox"
      subtitle="Questions agents have asked the user and are still waiting on, oldest first."
    >
      <Notice>
        Read-only. Answering from the browser is a write to the ledger and is
        deferred to the read-write follow-up.
      </Notice>
      <div className="card">
        <EmptyState
          title="No open questions"
          body="An open question is an unanswered user_question contract. Nothing ages these out, so anything listed here is genuinely still blocking an agent."
        />
      </div>
    </Page>
  );
}
