import { describe, expect, it } from "vitest";
import { screen } from "@testing-library/react";
import { WorkflowsView } from "./WorkflowsView";
import { ApiError } from "../api/client";
import { fakeClient, makeWorkflow, pending, renderWithApi } from "../../test/fakes";

describe("WorkflowsView", () => {
  it("shows a loading state while the request is in flight", () => {
    renderWithApi(<WorkflowsView />, fakeClient({ listWorkflows: () => pending() }));
    expect(screen.getByRole("status")).toHaveTextContent(/loading/i);
  });

  it("renders a row per instance with its project, counts and seq range", async () => {
    renderWithApi(
      <WorkflowsView />,
      fakeClient({
        listWorkflows: async () => [
          makeWorkflow({
            project_name: "sprawl",
            event_count: 214,
            first_seq: 12,
            last_seq: 802,
          }),
        ],
      }),
    );
    const rows = await screen.findAllByRole("row");
    expect(rows).toHaveLength(2);
    expect(rows[1]).toHaveTextContent("sprawl");
    expect(rows[1]).toHaveTextContent("214");
    expect(rows[1]).toHaveTextContent("12");
    expect(rows[1]).toHaveTextContent("802");
  });

  it("labels an instance with open contracts in flight and one without settled", async () => {
    // `state` is derived from open_contract_count, not stored. It is the only
    // liveness signal available, and it is not one.
    renderWithApi(
      <WorkflowsView />,
      fakeClient({
        listWorkflows: async () => [
          makeWorkflow({
            workflow_instance_id: "aaaa1111-0000-4000-8000-000000000001",
            open_contract_count: 3,
            state: "in_flight",
          }),
          makeWorkflow({
            workflow_instance_id: "bbbb2222-0000-4000-8000-000000000002",
            open_contract_count: 0,
            state: "settled",
          }),
        ],
      }),
    );
    const rows = await screen.findAllByRole("row");
    expect(rows[1]).toHaveTextContent("in flight");
    expect(rows[2]).toHaveTextContent("settled");
  });

  it("keeps the caveat that a crashed instance is indistinguishable from a live one", async () => {
    renderWithApi(<WorkflowsView />, fakeClient({ listWorkflows: async () => [] }));
    expect(await screen.findByText(/records no failure state/i)).toBeInTheDocument();
  });

  it("shows an empty state when there are no instances", async () => {
    renderWithApi(<WorkflowsView />, fakeClient({ listWorkflows: async () => [] }));
    expect(await screen.findByText("No workflow instances")).toBeInTheDocument();
    expect(screen.queryByRole("table")).not.toBeInTheDocument();
  });

  it("shows an error, and never an empty state, when the request fails", async () => {
    renderWithApi(
      <WorkflowsView />,
      fakeClient({
        listWorkflows: async () => {
          throw new ApiError(500, "aggregating the event log failed");
        },
      }),
    );
    expect(await screen.findByRole("alert")).toHaveTextContent("aggregating the event log failed");
    expect(screen.queryByText("No workflow instances")).not.toBeInTheDocument();
  });
});
