import { AsyncSection } from "../components/AsyncSection";
import { Notice, Page } from "../components/Page";
import { useApi } from "../api/ApiProvider";
import { useQuery } from "../api/useQuery";
import { formatRelative, projectLabel } from "../format";

export function InboxView() {
  const api = useApi();
  const query = useQuery(() => api.listInbox(), [api]);

  return (
    <Page
      title="Inbox"
      subtitle="Questions agents have asked the user and are still waiting on, oldest first."
    >
      <Notice>
        Read-only. Answering from the browser is a write to the ledger and is deferred to the
        read-write follow-up.
      </Notice>
      <AsyncSection
        query={query}
        isEmpty={(questions) => questions.length === 0}
        empty={{
          title: "No open questions",
          body: "An open question is an unanswered user_question contract. Nothing ages these out, so anything listed here is genuinely still blocking an agent.",
        }}
      >
        {(questions) => (
          <ul className="questions">
            {questions.map((question) => (
              <li className="card question" key={question.event_id}>
                <p className="question__text">{question.question}</p>
                {/* Context is omitted when absent rather than dashed: the
                    question is the content, and a dash under every one is noise. */}
                {question.context === "" ? null : (
                  <p className="question__context">{question.context}</p>
                )}
                <p className="question__meta">
                  <span>{question.asker}</span>
                  <span aria-hidden="true">·</span>
                  <span>{projectLabel(question.project_name)}</span>
                  <span aria-hidden="true">·</span>
                  <span>asked {formatRelative(question.opened_at)}</span>
                </p>
              </li>
            ))}
          </ul>
        )}
      </AsyncSection>
    </Page>
  );
}
