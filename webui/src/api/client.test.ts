import { describe, expect, it } from "vitest";
import { ApiError, createApiClient } from "./client";

/** A fetch stub that records the URL it was called with. */
function stubFetch(response: Partial<Response> & { jsonBody?: unknown }) {
  const calls: string[] = [];
  const fetchImpl = async (input: RequestInfo | URL) => {
    calls.push(String(input));
    return {
      ok: response.ok ?? true,
      status: response.status ?? 200,
      json: async () => response.jsonBody,
      ...response,
    } as Response;
  };
  return { calls, fetchImpl: fetchImpl as typeof fetch };
}

describe("createApiClient", () => {
  it("requests a same-origin relative path, never an absolute URL", async () => {
    const { calls, fetchImpl } = stubFetch({ jsonBody: { events: [] } });
    await createApiClient(fetchImpl).listEvents();
    expect(calls).toHaveLength(1);
    // The bundle carries no host: the nginx in front of the SPA owns the
    // upstream. An absolute URL here would be a deployment baked into the app.
    expect(calls[0]).toMatch(/^\/api\//);
    expect(calls[0]).not.toMatch(/^(https?:)?\/\//);
  });

  it("passes limit through as a query parameter", async () => {
    const { calls, fetchImpl } = stubFetch({ jsonBody: { events: [] } });
    await createApiClient(fetchImpl).listEvents({ limit: 25 });
    expect(calls[0]).toBe("/api/events?limit=25");
  });

  it("omits the parameter when no limit is given, so the API's default applies", async () => {
    const { calls, fetchImpl } = stubFetch({ jsonBody: { events: [] } });
    await createApiClient(fetchImpl).listEvents();
    expect(calls[0]).toBe("/api/events");
  });

  it.each([
    ["listGoals", "/api/goals", { goals: [] }],
    ["listWorkflows", "/api/workflows", { workflows: [] }],
    ["listFleet", "/api/fleet", { agents: [] }],
    ["listEvents", "/api/events", { events: [] }],
    ["listInbox", "/api/inbox", { questions: [] }],
    ["getUsage", "/api/usage", { totals: {}, series: [], bucket: "hour" }],
  ] as const)("%s reads %s and unwraps its named key", async (method, path, body) => {
    const { calls, fetchImpl } = stubFetch({ jsonBody: body });
    const client = createApiClient(fetchImpl);
    const got = await client[method]();
    expect(calls[0]).toBe(path);
    // The unwrap is the other half of the claim: a client that returned the
    // whole envelope would still pass a URL-only assertion.
    expect(got).toEqual(method === "getUsage" ? body : []);
  });

  it("passes project_id through on a list endpoint", async () => {
    const { calls, fetchImpl } = stubFetch({ jsonBody: { goals: [] } });
    await createApiClient(fetchImpl).listGoals({
      project_id: "9b1c0000-0000-4000-8000-000000000001",
    });
    expect(calls[0]).toBe("/api/goals?project_id=9b1c0000-0000-4000-8000-000000000001");
  });

  it("returns the events array", async () => {
    const event = {
      seq: 7,
      id: "5f4d1c1e-0000-4000-8000-000000000001",
      type: "turn_finished",
      at: "2026-09-09T12:00:00Z",
      project_id: "5f4d1c1e-0000-4000-8000-000000000002",
      project_name: "sprawl",
      workflow_instance_id: "5f4d1c1e-0000-4000-8000-000000000003",
      payload: { cost_usd: 0.01 },
    };
    const { fetchImpl } = stubFetch({ jsonBody: { events: [event] } });
    await expect(createApiClient(fetchImpl).listEvents()).resolves.toEqual([event]);
  });

  it("raises ApiError on a non-2xx response, carrying the API's error message", async () => {
    const { fetchImpl } = stubFetch({
      ok: false,
      status: 400,
      jsonBody: { error: "limit must be at least 1, got 0" },
    });
    const err = await createApiClient(fetchImpl)
      .listEvents({ limit: 0 })
      .catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect((err as ApiError).status).toBe(400);
    expect((err as ApiError).message).toContain("limit must be at least 1");
  });

  it("raises ApiError when the error body is not JSON, rather than a parse crash", async () => {
    const fetchImpl = (async () => ({
      ok: false,
      status: 502,
      json: async () => {
        throw new SyntaxError("Unexpected token < in JSON");
      },
    })) as unknown as typeof fetch;
    const err = await createApiClient(fetchImpl)
      .listEvents()
      .catch((e: unknown) => e);
    // A 502 from the proxy is an HTML page; the status is the only signal.
    expect(err).toBeInstanceOf(ApiError);
    expect((err as ApiError).status).toBe(502);
  });

  it("raises ApiError when the transport itself fails", async () => {
    const fetchImpl = (async () => {
      throw new TypeError("Failed to fetch");
    }) as unknown as typeof fetch;
    const err = await createApiClient(fetchImpl)
      .listEvents()
      .catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect((err as ApiError).status).toBe(0);
  });
});
