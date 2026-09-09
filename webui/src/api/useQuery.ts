import { useCallback, useEffect, useRef, useState } from "react";
import { ApiError } from "./client";

export type QueryState<T> =
  { status: "loading" } | { status: "error"; error: ApiError } | { status: "ready"; data: T };

/**
 * Runs an async read and exposes exactly one of loading / error / ready.
 *
 * Three states, never a `data` that is also `undefined` while loading: the
 * views must not be able to render an empty state for a request that failed or
 * has not answered yet, which would report "nothing here" for "we do not know".
 *
 * Refetching is keyed on `deps`, and `run` is held in a ref rather than being
 * an effect dependency. The obvious alternative — depending on `run` — makes an
 * inline lambda at the call site re-fire the effect on every render, and the
 * resulting loop exhausts the heap rather than failing visibly. Measured: it
 * OOM-killed the vitest worker.
 *
 * There is no AbortController: a superseded request runs to completion and its
 * answer is discarded by the generation guard. Consequences, both accepted for
 * reads this small — under StrictMode every view issues its mount request
 * twice in `npm run dev` (so a doubled dev API log is expected, not a broken
 * guard), and a slow superseded read still occupies a connection.
 */
export function useQuery<T>(
  run: () => Promise<T>,
  deps: unknown[],
): QueryState<T> & { reload: () => void } {
  const [state, setState] = useState<QueryState<T>>({ status: "loading" });
  const [attempt, setAttempt] = useState(0);
  const runRef = useRef(run);
  runRef.current = run;
  // Guards against a superseded request's late answer overwriting a newer one.
  const generation = useRef(0);

  useEffect(() => {
    const mine = ++generation.current;
    setState({ status: "loading" });
    runRef.current().then(
      (data) => {
        if (generation.current === mine) setState({ status: "ready", data });
      },
      (cause: unknown) => {
        if (generation.current !== mine) return;
        const error =
          cause instanceof ApiError
            ? cause
            : new ApiError(0, cause instanceof Error ? cause.message : String(cause));
        setState({ status: "error", error });
      },
    );
    return () => {
      // Bump so a late response from this request is ignored.
      generation.current++;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [...deps, attempt]);

  const reload = useCallback(() => setAttempt((n) => n + 1), []);
  return { ...state, reload };
}
