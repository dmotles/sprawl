import { AsyncSection } from "../components/AsyncSection";
import { Notice, Page } from "../components/Page";
import { useApi } from "../api/ApiProvider";
import { useQuery } from "../api/useQuery";
import { formatCount, formatRelative, projectLabel } from "../format";

export function FleetView() {
  const api = useApi();
  const query = useQuery(() => api.listFleet(), [api]);

  return (
    <Page title="Fleet" subtitle="Agents that have finished a turn, most recently active first.">
      <Notice>
        Advisory only, and it reports activity rather than liveness. The log records turn
        boundaries, not whether an agent is running — a long quiet turn is indistinguishable from an
        agent that has stopped, and the host each agent runs on is not in the log at all. Turn and
        token counts are lower bounds: a turn the log could not attribute to an agent is left out.
        An agent that has worked in two projects appears once per project.
      </Notice>
      <div className="card">
        <AsyncSection
          query={query}
          isEmpty={(agents) => agents.length === 0}
          empty={{
            title: "No agent activity",
            body: "No agent has finished a turn yet. This view is derived from turn_finished events, so an agent that has only just started does not appear.",
          }}
        >
          {(agents) => (
            <table className="table">
              <thead>
                <tr>
                  <th scope="col">Agent</th>
                  <th scope="col">Project</th>
                  <th scope="col">Last turn</th>
                  <th scope="col">Turns</th>
                  <th scope="col">Input</th>
                  <th scope="col">Output</th>
                </tr>
              </thead>
              {/* No Host, "Working on", Session or Outcome column: turn_finished
                  carries none of them, and a column of em dashes is worse than
                  the honest absence of the column. */}
              <tbody>
                {agents.map((agent) => (
                  <tr key={`${agent.agent_name} ${agent.project_id}`}>
                    <td>{agent.agent_name}</td>
                    <td>{projectLabel(agent.project_name)}</td>
                    <td>{formatRelative(agent.last_turn_at)}</td>
                    <td className="table__num">{formatCount(agent.turn_count)}</td>
                    <td className="table__num">{formatCount(agent.input_tokens)}</td>
                    <td className="table__num">{formatCount(agent.output_tokens)}</td>
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
