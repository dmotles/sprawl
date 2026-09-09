import { describe, expect, it } from "vitest";
import { screen } from "@testing-library/react";
import { FleetView } from "./FleetView";
import { ApiError } from "../api/client";
import { fakeClient, makeAgent, pending, renderWithApi } from "../../test/fakes";

describe("FleetView", () => {
  it("shows a loading state while the request is in flight", () => {
    renderWithApi(<FleetView />, fakeClient({ listFleet: () => pending() }));
    expect(screen.getByRole("status")).toHaveTextContent(/loading/i);
  });

  it("renders a row per agent with its project, turn count and token lower bounds", async () => {
    renderWithApi(
      <FleetView />,
      fakeClient({
        listFleet: async () => [
          makeAgent({
            agent_name: "ratz",
            project_name: "sprawl",
            turn_count: 143,
            input_tokens: 1844290,
            output_tokens: 120455,
          }),
        ],
      }),
    );
    const rows = await screen.findAllByRole("row");
    expect(rows).toHaveLength(2);
    expect(rows[1]).toHaveTextContent("ratz");
    expect(rows[1]).toHaveTextContent("sprawl");
    expect(rows[1]).toHaveTextContent("143");
    expect(rows[1]).toHaveTextContent("1,844,290");
    expect(rows[1]).toHaveTextContent("120,455");
  });

  it("renders one row per project for an agent that worked in two", async () => {
    // The API groups by (agent_name, project_id), so two rows share a name.
    // Without the project column they read as an accidental duplicate. (The row
    // key is the pair for the same reason, but that is not what this asserts —
    // a duplicate key only warns, it does not drop the row.)
    renderWithApi(
      <FleetView />,
      fakeClient({
        listFleet: async () => [
          makeAgent({ agent_name: "ratz", project_id: "p-1", project_name: "sprawl" }),
          makeAgent({ agent_name: "ratz", project_id: "p-2", project_name: "atlas" }),
        ],
      }),
    );
    const rows = await screen.findAllByRole("row");
    expect(rows).toHaveLength(3);
    expect(rows[1]).toHaveTextContent("sprawl");
    expect(rows[2]).toHaveTextContent("atlas");
  });

  it("does not claim an agent is alive or offline", async () => {
    // Fleet is derived from turn_finished events. There is no liveness signal
    // in the log, and a long quiet turn is legitimate — so an age is honest and
    // a status badge would be invented.
    renderWithApi(<FleetView />, fakeClient({ listFleet: async () => [makeAgent()] }));
    await screen.findAllByRole("row");
    expect(screen.queryByText(/\b(alive|online|offline|dead)\b/i)).not.toBeInTheDocument();
  });

  it("does not render a host or working-on column, which the API always reports as null", async () => {
    renderWithApi(<FleetView />, fakeClient({ listFleet: async () => [makeAgent()] }));
    await screen.findAllByRole("row");
    // Session and Outcome went the same way as Host in the rework: the UI
    // declared both, turn_finished carries neither, and a column the server
    // cannot fill is a column of em dashes pretending to be data.
    for (const header of ["Host", "Working on", "Session", "Outcome"]) {
      expect(screen.queryByRole("columnheader", { name: header })).not.toBeInTheDocument();
    }
  });

  it("keeps the advisory-only caveat", async () => {
    renderWithApi(<FleetView />, fakeClient({ listFleet: async () => [] }));
    expect(await screen.findByText(/advisory only/i)).toBeInTheDocument();
  });

  it("shows an empty state when no agent has finished a turn", async () => {
    renderWithApi(<FleetView />, fakeClient({ listFleet: async () => [] }));
    expect(await screen.findByText("No agent activity")).toBeInTheDocument();
    expect(screen.queryByRole("table")).not.toBeInTheDocument();
  });

  it("shows an error, and never an empty state, when the request fails", async () => {
    renderWithApi(
      <FleetView />,
      fakeClient({
        listFleet: async () => {
          throw new ApiError(0, "could not reach the API: Failed to fetch");
        },
      }),
    );
    expect(await screen.findByRole("alert")).toHaveTextContent("could not reach the API");
    expect(screen.queryByText("No agent activity")).not.toBeInTheDocument();
  });
});
