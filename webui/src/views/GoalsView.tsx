import { AsyncSection } from "../components/AsyncSection";
import { Notice, Page } from "../components/Page";
import { useApi } from "../api/ApiProvider";
import { useQuery } from "../api/useQuery";
import { UNKNOWN, formatTime, projectLabel, shortId } from "../format";

export function GoalsView() {
  const api = useApi();
  const query = useQuery(() => api.listGoals(), [api]);

  return (
    <Page title="Goals" subtitle="Goals that have been opened and not yet closed.">
      <Notice>
        Open goals only. A goal closes when a later event references it, and that reference is
        unindexed — so the API cannot report closed goals or their outcomes. A goal missing from
        this list closed; it was not lost.
      </Notice>
      <div className="card">
        <AsyncSection
          query={query}
          isEmpty={(goals) => goals.length === 0}
          empty={{
            title: "No open goals",
            body: "Nothing is currently being worked towards. A goal appears here as soon as a goal_opened event is appended.",
          }}
        >
          {(goals) => (
            <table className="table">
              <thead>
                <tr>
                  <th scope="col">Seq</th>
                  <th scope="col">Opened</th>
                  <th scope="col">Project</th>
                  <th scope="col">Type</th>
                  <th scope="col">Owner</th>
                  <th scope="col">Workflow</th>
                </tr>
              </thead>
              <tbody>
                {goals.map((goal) => (
                  <tr key={goal.id}>
                    <td className="table__num">{goal.seq}</td>
                    <td>{formatTime(goal.opened_at)}</td>
                    <td>{projectLabel(goal.project_name)}</td>
                    <td>
                      {goal.goal_type === "" ? (
                        <span className="table__unknown">{UNKNOWN}</span>
                      ) : (
                        goal.goal_type
                      )}
                      {/* Surfaced, not filtered: a legacy goal predates the
                          current goal record and carries less information, so
                          the reader needs to know which rows those are. */}
                      {goal.legacy ? <span className="badge">legacy</span> : null}
                    </td>
                    <td>{goal.owner === "" ? UNKNOWN : goal.owner}</td>
                    <td className="table__id">{shortId(goal.workflow_instance_id)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </AsyncSection>
      </div>
    </Page>
  );
}
