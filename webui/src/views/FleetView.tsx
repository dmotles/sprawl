import { AsyncSection } from "../components/AsyncSection";
import { Notice, Page } from "../components/Page";
import { useApi } from "../api/ApiProvider";
import { useQuery } from "../api/useQuery";
import { UNKNOWN, formatCount, formatRelative, shortId } from "../format";

export function FleetView() {
  const api = useApi();
  const query = useQuery(() => api.listFleet(), [api]);

  return (
    <Page title="Fleet" subtitle="Agents that have finished a turn, most recently active first.">
      <Notice>
        Advisory only, and it reports activity rather than liveness. The log records turn
        boundaries, not whether an agent is running — a long quiet turn is indistinguishable from an
        agent that has stopped, and the host each agent runs on is not in the log at all.
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
                  <th scope="col">Session</th>
                  <th scope="col">Last turn</th>
                  <th scope="col">Outcome</th>
                  <th scope="col">Turns</th>
                </tr>
              </thead>
              {/* No Host or "Working on" column: the API reports both as null
                  for every row, and a column of em dashes is worse than the
                  honest absence of the column. */}
              <tbody>
                {agents.map((agent) => (
                  <tr key={agent.session_id}>
                    <td>{agent.agent_name}</td>
                    <td className="table__id">{shortId(agent.session_id)}</td>
                    <td>{formatRelative(agent.last_turn_at)}</td>
                    <td>{agent.last_turn_outcome === "" ? UNKNOWN : agent.last_turn_outcome}</td>
                    <td className="table__num">{formatCount(agent.turns)}</td>
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
