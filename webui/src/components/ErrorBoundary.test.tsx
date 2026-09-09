import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { ErrorBoundary } from "./ErrorBoundary";

function Boom(): never {
  throw new Error("agents.length is not a function");
}

describe("ErrorBoundary", () => {
  beforeEach(() => {
    // React logs the caught error itself, and componentDidCatch logs it again.
    // Silenced so an EXPECTED throw does not look like a failing run.
    vi.spyOn(console, "error").mockImplementation(() => {});
  });
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("renders its children when nothing throws", () => {
    render(
      <ErrorBoundary>
        <p>the fleet</p>
      </ErrorBoundary>,
    );
    expect(screen.getByText("the fleet")).toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("catches a render error and shows the message instead of unmounting", () => {
    // Without a boundary React unmounts the WHOLE tree, which is how QUM-1349's
    // envelope-key bug turned two broken views into a blank application.
    render(
      <ErrorBoundary>
        <Boom />
      </ErrorBoundary>,
    );
    expect(screen.getByRole("alert")).toHaveTextContent(/failed to render/i);
    expect(screen.getByRole("alert")).toHaveTextContent("agents.length is not a function");
  });

  it("leaves everything outside the boundary mounted", () => {
    render(
      <div>
        <nav aria-label="Views">nav</nav>
        <ErrorBoundary>
          <Boom />
        </ErrorBoundary>
      </div>,
    );
    expect(screen.getByRole("navigation", { name: "Views" })).toBeInTheDocument();
  });

  it("does not offer a retry, because the failure is a bug and not a request", () => {
    render(
      <ErrorBoundary>
        <Boom />
      </ErrorBoundary>,
    );
    expect(screen.queryByRole("button", { name: /retry/i })).not.toBeInTheDocument();
  });
});
