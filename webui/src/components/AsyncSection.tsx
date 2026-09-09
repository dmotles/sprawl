import type { ReactNode } from "react";
import type { QueryState } from "../api/useQuery";
import { EmptyState } from "./EmptyState";

/**
 * Renders the one branch a query is actually in.
 *
 * The empty state is reachable ONLY from `ready`, which is the point: an error
 * rendered as "nothing here" tells an operator the log is empty when it is
 * unreadable.
 */
export function AsyncSection<T>({
  query,
  isEmpty,
  empty,
  children,
}: {
  query: QueryState<T> & { reload: () => void };
  /** Omitted when the view has no whole-page empty state of its own. */
  isEmpty?: (data: T) => boolean;
  empty?: { title: string; body: string };
  children: (data: T) => ReactNode;
}) {
  if (query.status === "loading") {
    return (
      <p className="async async--loading" role="status">
        Loading…
      </p>
    );
  }
  if (query.status === "error") {
    return (
      <div className="async async--error" role="alert">
        <p className="async__title">Could not load this view</p>
        <p className="async__detail">{query.error.message}</p>
        <button type="button" className="button" onClick={query.reload}>
          Retry
        </button>
      </div>
    );
  }
  if (empty && isEmpty?.(query.data)) {
    return <EmptyState title={empty.title} body={empty.body} />;
  }
  return <>{children(query.data)}</>;
}
