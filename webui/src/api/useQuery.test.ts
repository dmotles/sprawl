import { describe, expect, it } from "vitest";
import { act, renderHook, waitFor } from "@testing-library/react";
import { useQuery } from "./useQuery";
import { ApiError } from "./client";

/** A promise plus the handles to settle it later. */
function deferred<T>() {
  let resolve!: (v: T) => void;
  let reject!: (e: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

describe("useQuery", () => {
  it("does not refetch when the caller passes an inline lambda with stable deps", async () => {
    // `run` is deliberately NOT an effect dependency: an inline lambda would
    // otherwise re-fire the effect every render, and that loop OOMs the worker
    // rather than failing visibly.
    let calls = 0;
    const { rerender, result } = renderHook(() =>
      useQuery(() => {
        calls += 1;
        return Promise.resolve("x");
      }, []),
    );
    await waitFor(() => expect(result.current.status).toBe("ready"));
    rerender();
    rerender();
    expect(calls).toBe(1);
  });

  it("ignores a superseded request that answers late", async () => {
    // The race: the first read is still in flight when the query is replaced.
    // Without a generation guard its late answer lands last and wins, so the
    // view shows data for a request nobody is waiting on any more.
    const first = deferred<string>();
    const second = deferred<string>();

    const { result, rerender } = renderHook(({ run, key }) => useQuery(run, [key]), {
      initialProps: { run: () => first.promise, key: "a" },
    });
    expect(result.current.status).toBe("loading");

    rerender({ run: () => second.promise, key: "b" });
    second.resolve("second");
    await waitFor(() => expect(result.current.status).toBe("ready"));

    await act(async () => {
      first.resolve("first");
    });
    expect(result.current).toMatchObject({ status: "ready", data: "second" });
  });

  it("ignores a superseded request that fails late", async () => {
    const first = deferred<string>();
    const second = deferred<string>();
    const { result, rerender } = renderHook(({ run, key }) => useQuery(run, [key]), {
      initialProps: { run: () => first.promise, key: "a" },
    });

    rerender({ run: () => second.promise, key: "b" });
    second.resolve("second");
    await waitFor(() => expect(result.current.status).toBe("ready"));

    await act(async () => {
      first.reject(new ApiError(500, "too late"));
    });
    // An error branch that ignored generations would replace good data with a
    // stale failure, which is the more misleading direction of this bug.
    expect(result.current).toMatchObject({ status: "ready", data: "second" });
  });

  it("wraps a non-ApiError rejection so the view always has a message", async () => {
    const { result } = renderHook(() => useQuery(() => Promise.reject(new TypeError("boom")), []));
    await waitFor(() => expect(result.current.status).toBe("error"));
    expect(result.current).toMatchObject({ status: "error" });
    expect((result.current as { error: ApiError }).error.message).toBe("boom");
  });
});
