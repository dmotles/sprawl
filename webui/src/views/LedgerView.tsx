import { AsyncSection } from "../components/AsyncSection";
import { Page } from "../components/Page";
import { useApi } from "../api/ApiProvider";
import { useQuery } from "../api/useQuery";
import { formatTime, projectLabel, shortId } from "../format";

export function LedgerView() {
  const api = useApi();
  const query = useQuery(() => api.listEvents(), [api]);

  return (
    <Page title="Event ledger" subtitle="The raw append-only event log, newest first.">
      <div className="card">
        <AsyncSection
          query={query}
          isEmpty={(events) => events.length === 0}
          empty={{
            title: "No events",
            body: "The event log is empty. Anything appended to the ledger shows up here.",
          }}
        >
          {(events) => (
            <table className="table">
              <thead>
                <tr>
                  <th scope="col">Seq</th>
                  <th scope="col">Time</th>
                  <th scope="col">Project</th>
                  <th scope="col">Type</th>
                  <th scope="col">Workflow</th>
                </tr>
              </thead>
              <tbody>
                {events.map((event) => (
                  <tr key={event.id}>
                    <td className="table__num">{event.seq}</td>
                    <td>{formatTime(event.at)}</td>
                    <td>{projectLabel(event.project_name)}</td>
                    <td>
                      {/* The API LEFT JOINs the schema table, so an event whose
                          schema row is missing arrives with an empty type. It is
                          shown, not dropped — it is the anomaly worth seeing. */}
                      {event.type === "" ? (
                        <span className="table__unknown">unknown type</span>
                      ) : (
                        event.type
                      )}
                    </td>
                    <td className="table__id">{shortId(event.workflow_instance_id)}</td>
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
