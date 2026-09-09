import { describe, expect, it, vi } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { LedgerView } from "./LedgerView";
import { ApiError } from "../api/client";
import { fakeClient, makeEvent, pending, renderWithApi } from "../../test/fakes";

describe("LedgerView", () => {
  it("shows a loading state while the request is in flight", () => {
    renderWithApi(<LedgerView />, fakeClient({ listEvents: () => pending() }));
    expect(screen.getByRole("status")).toHaveTextContent(/loading/i);
  });

  it("renders one row per event, newest first as the API returns them", async () => {
    const client = fakeClient({
      listEvents: async () => [
        makeEvent({ seq: 9, type: "run_finished" }),
        makeEvent({ seq: 8, type: "turn_finished" }),
      ],
    });
    renderWithApi(<LedgerView />, client);

    const rows = await screen.findAllByRole("row");
    // header + 2 events
    expect(rows).toHaveLength(3);
    expect(rows[1]).toHaveTextContent("9");
    expect(rows[1]).toHaveTextContent("run_finished");
    expect(rows[2]).toHaveTextContent("turn_finished");
  });

  it("renders an event whose type the API could not resolve, rather than dropping it", async () => {
    // The API LEFT JOINs the schema table so an orphaned event still appears
    // with an empty type. Hiding it here would undo that on the one view whose
    // job is to show everything.
    renderWithApi(
      <LedgerView />,
      fakeClient({ listEvents: async () => [makeEvent({ seq: 4, type: "" })] }),
    );
    const rows = await screen.findAllByRole("row");
    expect(rows).toHaveLength(2);
    expect(rows[1]).toHaveTextContent("unknown type");
  });

  it("renders an em dash when the API omitted the project label", async () => {
    // The parse boundary, not a hypothetical: an /api/events that predates the
    // project_name field would otherwise render a blank Project cell.
    const { project_name: _dropped, ...withoutProject } = makeEvent({ seq: 5 });
    renderWithApi(
      <LedgerView />,
      fakeClient({
        listEvents: async () => [withoutProject as ReturnType<typeof makeEvent>],
      }),
    );
    const rows = await screen.findAllByRole("row");
    expect(rows[1]).toHaveTextContent("—");
  });

  it("shows an empty state when the ledger is empty", async () => {
    renderWithApi(<LedgerView />, fakeClient({ listEvents: async () => [] }));
    expect(await screen.findByText("No events")).toBeInTheDocument();
    expect(screen.queryByRole("table")).not.toBeInTheDocument();
  });

  it("shows the API's message on failure and can retry", async () => {
    const user = userEvent.setup();
    const listEvents = vi
      .fn()
      .mockRejectedValueOnce(new ApiError(500, "reading the event log failed"))
      .mockResolvedValueOnce([makeEvent({ seq: 3 })]);
    renderWithApi(<LedgerView />, fakeClient({ listEvents }));

    expect(await screen.findByRole("alert")).toHaveTextContent("reading the event log failed");

    await user.click(screen.getByRole("button", { name: /retry/i }));

    await waitFor(() => expect(screen.getAllByRole("row")).toHaveLength(2));
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(listEvents).toHaveBeenCalledTimes(2);
  });

  it("never shows an empty state for a failed request", async () => {
    // The failure mode this guards: treating an error as "no data" tells the
    // operator the log is empty when it is actually unreadable.
    renderWithApi(
      <LedgerView />,
      fakeClient({
        listEvents: async () => {
          throw new ApiError(503, "database unreachable");
        },
      }),
    );
    await screen.findByRole("alert");
    expect(screen.queryByText("No events")).not.toBeInTheDocument();
  });
});
