import { describe, expect, it } from "vitest";
import { screen } from "@testing-library/react";
import { GoalsView } from "./GoalsView";
import { ApiError } from "../api/client";
import { fakeClient, makeGoal, pending, renderWithApi } from "../../test/fakes";

describe("GoalsView", () => {
  it("shows a loading state while the request is in flight", () => {
    renderWithApi(<GoalsView />, fakeClient({ listGoals: () => pending() }));
    expect(screen.getByRole("status")).toHaveTextContent(/loading/i);
  });

  it("renders a row per goal with its type, owner and project", async () => {
    renderWithApi(
      <GoalsView />,
      fakeClient({
        listGoals: async () => [
          makeGoal({
            seq: 41,
            goal_type: "research",
            owner: "tower",
            project_name: "sprawl",
          }),
        ],
      }),
    );
    const rows = await screen.findAllByRole("row");
    expect(rows).toHaveLength(2);
    expect(rows[1]).toHaveTextContent("research");
    expect(rows[1]).toHaveTextContent("tower");
    // All projects are listed together with no selector, so a row without its
    // project label is ambiguous about which repo it belongs to.
    expect(rows[1]).toHaveTextContent("sprawl");
  });

  it("marks a legacy goal rather than hiding it", async () => {
    renderWithApi(
      <GoalsView />,
      fakeClient({ listGoals: async () => [makeGoal({ legacy: true })] }),
    );
    expect(await screen.findByText(/legacy/i)).toBeInTheDocument();
  });

  it("renders an em dash for a goal with no recorded owner, never an empty cell", async () => {
    renderWithApi(
      <GoalsView />,
      fakeClient({
        listGoals: async () => [makeGoal({ owner: "", goal_type: "" })],
      }),
    );
    const rows = await screen.findAllByRole("row");
    expect(rows[1]).toHaveTextContent("—");
  });

  it("says the list is open goals only, because the API cannot report closed ones", async () => {
    // /api/goals has no state column: closes_event_id is unindexed, so the API
    // returns open goals only. A page titled just "Goals" would read as all of
    // them and make a closed goal look lost.
    renderWithApi(<GoalsView />, fakeClient({ listGoals: async () => [] }));
    expect(await screen.findByText(/open goals only/i)).toBeInTheDocument();
  });

  it("shows an empty state when there are no open goals", async () => {
    renderWithApi(<GoalsView />, fakeClient({ listGoals: async () => [] }));
    expect(await screen.findByText("No open goals")).toBeInTheDocument();
    expect(screen.queryByRole("table")).not.toBeInTheDocument();
  });

  it("shows an error, and never an empty state, when the request fails", async () => {
    renderWithApi(
      <GoalsView />,
      fakeClient({
        listGoals: async () => {
          throw new ApiError(503, "database unreachable");
        },
      }),
    );
    expect(await screen.findByRole("alert")).toHaveTextContent("database unreachable");
    expect(screen.queryByText("No open goals")).not.toBeInTheDocument();
  });
});
