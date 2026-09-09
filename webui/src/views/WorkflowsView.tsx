import { AsyncSection } from "../components/AsyncSection";
import { Notice, Page } from "../components/Page";
import { useApi } from "../api/ApiProvider";
import { useQuery } from "../api/useQuery";
import { formatCount, formatTime, projectLabel, shortId } from "../format";

export function WorkflowsView() {
  const api = useApi();
  const query = useQuery(() => api.listWorkflows(), [api]);

  return (
    <Page
      title="Workflows"
      subtitle="Workflow instances seen in the event log, most recently active first."
    >
      <Notice>
        Derived from the event log, not from a workflow record: an instance counts as in flight
        while it has open contracts. The schema records no failure state, so a crashed instance and
        a running one look alike here.
      </Notice>
      <div className="card">
        <AsyncSection
          query={query}
          isEmpty={(workflows) => workflows.length === 0}
          empty={{
            title: "No workflow instances",
            body: "No event in the log carries a workflow instance yet.",
          }}
        >
          {(workflows) => (
            <table className="table">
              <thead>
                <tr>
                  <th scope="col">Workflow</th>
                  <th scope="col">Project</th>
                  <th scope="col">State</th>
                  <th scope="col">Events</th>
                  <th scope="col">Open contracts</th>
                  <th scope="col">Seq range</th>
                  <th scope="col">Last event</th>
                </tr>
              </thead>
              <tbody>
                {workflows.map((workflow) => (
                  <tr key={workflow.workflow_instance_id}>
                    <td className="table__id">{shortId(workflow.workflow_instance_id)}</td>
                    <td>{projectLabel(workflow.project_name)}</td>
                    <td>{workflow.state === "in_flight" ? "in flight" : "settled"}</td>
                    <td className="table__num">{formatCount(workflow.event_count)}</td>
                    <td className="table__num">{formatCount(workflow.open_contract_count)}</td>
                    <td className="table__num">
                      {workflow.first_seq}–{workflow.last_seq}
                    </td>
                    <td>{formatTime(workflow.last_at)}</td>
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
