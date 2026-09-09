import { Component, type ErrorInfo, type ReactNode } from "react";

/**
 * Keeps a throwing view from taking the application with it.
 *
 * Without one, React unmounts the WHOLE tree on an uncaught render error — nav
 * included — so the browser shows an empty dark page with no way to navigate
 * anywhere else. That is not hypothetical: QUM-1349 shipped a client reading the
 * wrong response envelope, and two views turned into a blank application rather
 * than two broken pages (QA finding F1).
 *
 * This is the last resort, not the error path. A failed request is handled by
 * `AsyncSection`; anything reaching here is a bug, so the message says so
 * instead of offering a retry that would re-run the same bug.
 *
 * A class component because that is the only way to catch a render error —
 * there is no hook equivalent.
 */
export class ErrorBoundary extends Component<{ children: ReactNode }, { error: Error | null }> {
  state: { error: Error | null } = { error: null };

  static getDerivedStateFromError(error: Error) {
    return { error };
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    // The console is the only sink this SPA has; there is no error-reporting
    // endpoint, and inventing one for a read-only internal dashboard is not
    // worth the network path.
    console.error("view crashed:", error, info.componentStack);
  }

  render() {
    if (!this.state.error) return this.props.children;
    return (
      <div className="card" role="alert">
        <p className="empty__title">This view failed to render</p>
        <p className="empty__body">
          A bug in the page, not a failure of the API. The rest of the application still works — use
          the navigation to move elsewhere.
        </p>
        <p className="empty__body">{this.state.error.message}</p>
      </div>
    );
  }
}
