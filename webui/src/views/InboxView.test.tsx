import { describe, expect, it } from "vitest";
import { screen } from "@testing-library/react";
import { InboxView } from "./InboxView";
import { ApiError } from "../api/client";
import { fakeClient, makeQuestion, pending, renderWithApi } from "../../test/fakes";

describe("InboxView", () => {
  it("shows a loading state while the request is in flight", () => {
    renderWithApi(<InboxView />, fakeClient({ listInbox: () => pending() }));
    expect(screen.getByRole("status")).toHaveTextContent(/loading/i);
  });

  it("renders each question with its asker, project and age", async () => {
    renderWithApi(
      <InboxView />,
      fakeClient({
        listInbox: async () => [
          makeQuestion({
            asker: "zone",
            question: "Should the drawer trap focus?",
            project_name: "sprawl",
          }),
        ],
      }),
    );
    expect(await screen.findByText("Should the drawer trap focus?")).toBeInTheDocument();
    expect(screen.getByText(/zone/)).toBeInTheDocument();
    expect(screen.getByText(/sprawl/)).toBeInTheDocument();
  });

  it("renders the question's context when it has one and omits it when empty", async () => {
    const { unmount } = renderWithApi(
      <InboxView />,
      fakeClient({
        listInbox: async () => [makeQuestion({ context: "the phone drawer" })],
      }),
    );
    expect(await screen.findByText("the phone drawer")).toBeInTheDocument();
    unmount();

    renderWithApi(
      <InboxView />,
      fakeClient({ listInbox: async () => [makeQuestion({ context: "" })] }),
    );
    // An empty context is absent, not an em-dash placeholder: the question
    // itself is the content, and a dash under every question is noise.
    expect(await screen.findByText("Should the drawer trap focus?")).toBeInTheDocument();
    expect(screen.queryByText("—")).not.toBeInTheDocument();
  });

  it("keeps the read-only caveat", async () => {
    renderWithApi(<InboxView />, fakeClient({ listInbox: async () => [] }));
    expect(await screen.findByText(/read-only/i)).toBeInTheDocument();
  });

  it("shows an empty state when nothing is blocking", async () => {
    renderWithApi(<InboxView />, fakeClient({ listInbox: async () => [] }));
    expect(await screen.findByText("No open questions")).toBeInTheDocument();
  });

  it("shows an error, and never an empty state, when the request fails", async () => {
    // "No open questions" on a failed request tells the user nobody is blocked
    // while an agent sits waiting.
    renderWithApi(
      <InboxView />,
      fakeClient({
        listInbox: async () => {
          throw new ApiError(503, "database unreachable");
        },
      }),
    );
    expect(await screen.findByRole("alert")).toHaveTextContent("database unreachable");
    expect(screen.queryByText("No open questions")).not.toBeInTheDocument();
  });
});
