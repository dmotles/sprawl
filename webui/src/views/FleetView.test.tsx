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

  it("renders a row per agent with its last turn age, outcome and turn count", async () => {
    renderWithApi(
      <FleetView />,
      fakeClient({
        listFleet: async () => [
          makeAgent({
            agent_name: "ratz",
            last_turn_outcome: "success",
            turns: 143,
          }),
        ],
      }),
    );
    const rows = await screen.findAllByRole("row");
    expect(rows).toHaveLength(2);
    expect(rows[1]).toHaveTextContent("ratz");
    expect(rows[1]).toHaveTextContent("success");
    expect(rows[1]).toHaveTextContent("143");
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
    for (const header of ["Host", "Working on"]) {
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
