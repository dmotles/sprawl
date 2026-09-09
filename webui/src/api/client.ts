// The read API client (QUM-1349 slice C).
//
// Every path here is RELATIVE. The SPA is same-origin with its API by
// construction — nginx in front of the bundle owns the upstream — so an
// absolute URL in this file would bake a deployment into the image and would
// fail `npm run check:endpoints`.

import type { EventRow, FleetAgent, Goal, InboxQuestion, Usage, Workflow } from "./types";

export type * from "./types";

/**
 * A failed API call. `status` is the HTTP status, or 0 when the request never
 * got an answer at all (offline, DNS, proxy down) — a distinction the UI needs,
 * because only one of those is worth showing a server message for.
 */
export class ApiError extends Error {
  constructor(
    readonly status: number,
    message: string,
  ) {
    super(message);
    this.name = "ApiError";
  }
}

/** Options every list endpoint accepts. */
export interface ListOpts {
  limit?: number;
  /** Absent means all projects — there is no project selector in v1. */
  project_id?: string;
}

export interface ApiClient {
  listGoals(opts?: ListOpts): Promise<Goal[]>;
  listWorkflows(opts?: ListOpts & { state?: "in_flight" | "settled" | "all" }): Promise<Workflow[]>;
  listFleet(opts?: ListOpts): Promise<FleetAgent[]>;
  listEvents(
    opts?: ListOpts & { workflow_instance_id?: string; before_seq?: number },
  ): Promise<EventRow[]>;
  getUsage(opts?: { bucket?: "hour" | "day"; project_id?: string }): Promise<Usage>;
  listInbox(opts?: ListOpts): Promise<InboxQuestion[]>;
}

function buildPath(path: string, params: Record<string, string | number | undefined>) {
  const query = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (value !== undefined) query.set(key, String(value));
  }
  const qs = query.toString();
  return qs ? `${path}?${qs}` : path;
}

/**
 * Builds a client over an injected `fetch`. Injected rather than closed over so
 * tests drive the real client with a stub and no server.
 */
export function createApiClient(fetchImpl: typeof fetch = fetch): ApiClient {
  async function get<T>(path: string): Promise<T> {
    let response: Response;
    try {
      response = await fetchImpl(path, {
        headers: { Accept: "application/json" },
      });
    } catch (cause) {
      throw new ApiError(0, `could not reach the API: ${(cause as Error).message}`);
    }
    if (!response.ok) {
      // The API answers errors as {"error": "..."}; a proxy answering for it
      // (502/504) sends HTML, so the parse is allowed to fail.
      let detail = `request failed with status ${response.status}`;
      try {
        const body = (await response.json()) as { error?: string };
        if (body?.error) detail = body.error;
      } catch {
        /* not JSON — the status is the whole signal */
      }
      throw new ApiError(response.status, detail);
    }
    return (await response.json()) as T;
  }

  return {
    async listGoals(opts = {}) {
      return (await get<{ goals: Goal[] }>(buildPath("/api/goals", { ...opts }))).goals;
    },
    async listWorkflows(opts = {}) {
      return (await get<{ workflows: Workflow[] }>(buildPath("/api/workflows", { ...opts })))
        .workflows;
    },
    async listFleet(opts = {}) {
      return (await get<{ agents: FleetAgent[] }>(buildPath("/api/fleet", { ...opts }))).agents;
    },
    async listEvents(opts = {}) {
      return (await get<{ events: EventRow[] }>(buildPath("/api/events", { ...opts }))).events;
    },
    async getUsage(opts = {}) {
      return await get<Usage>(buildPath("/api/usage", { ...opts }));
    },
    async listInbox(opts = {}) {
      return (await get<{ questions: InboxQuestion[] }>(buildPath("/api/inbox", { ...opts })))
        .questions;
    },
  };
}
