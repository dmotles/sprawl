import { describe, expect, it } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { App } from "./App";
import { VIEWS } from "./views";

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <App />
    </MemoryRouter>,
  );
}

describe("App shell", () => {
  it("links to all six views from the nav", () => {
    renderAt("/goals");
    const nav = screen.getByRole("navigation", { name: "Views" });
    const labels = within(nav)
      .getAllByRole("link")
      .map((a) => a.textContent);
    expect(labels).toEqual([
      "◎Goals",
      "⇄Workflows",
      "◇Fleet",
      "≡Event ledger",
      "▤Cost & usage",
      "✉Inbox",
    ]);
  });

  it.each(VIEWS.map((v) => [v.path, v.label] as const))(
    "renders %s with a heading and an empty state",
    (path, label) => {
      renderAt(path);
      expect(screen.getByRole("heading", { level: 1 })).toBeInTheDocument();
      // The topbar title tracks the route, so the shell and the router agree.
      expect(screen.getByText(label, { selector: ".topbar__title" })).toBeInTheDocument();
      expect(screen.getAllByText(/^No /).length).toBeGreaterThan(0);
    },
  );

  it("navigates between views and marks the active link", async () => {
    const user = userEvent.setup();
    renderAt("/goals");
    expect(screen.getByRole("link", { name: /Goals/ })).toHaveAttribute(
      "aria-current",
      "page",
    );

    await user.click(screen.getByRole("link", { name: /Inbox/ }));

    expect(screen.getByRole("heading", { level: 1, name: "Inbox" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /Inbox/ })).toHaveAttribute(
      "aria-current",
      "page",
    );
    expect(screen.getByRole("link", { name: /Goals/ })).not.toHaveAttribute("aria-current");
  });

  it.each(["/", "/no-such-view"])("redirects %s to the first view", (path) => {
    renderAt(path);
    expect(screen.getByRole("heading", { level: 1, name: "Goals" })).toBeInTheDocument();
  });

  it("opens the phone nav drawer and closes it on navigation", async () => {
    const user = userEvent.setup();
    const { container } = renderAt("/goals");
    const shell = container.querySelector(".shell")!;
    expect(shell).toHaveAttribute("data-nav-open", "false");

    await user.click(screen.getByRole("button", { name: "Open navigation" }));
    expect(shell).toHaveAttribute("data-nav-open", "true");

    await user.click(screen.getByRole("link", { name: /Fleet/ }));
    expect(shell).toHaveAttribute("data-nav-open", "false");
  });

  it("closes the drawer from the scrim without navigating", async () => {
    const user = userEvent.setup();
    const { container } = renderAt("/goals");
    const shell = container.querySelector(".shell")!;

    await user.click(screen.getByRole("button", { name: "Open navigation" }));
    await user.click(screen.getByRole("button", { name: "Close navigation" }));

    expect(shell).toHaveAttribute("data-nav-open", "false");
    expect(screen.getByRole("heading", { level: 1, name: "Goals" })).toBeInTheDocument();
  });
});
