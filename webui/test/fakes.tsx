import type { ReactElement } from "react";
import { render } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import type {
  ApiClient,
  EventRow,
  FleetMember,
  Goal,
  InboxQuestion,
  UsageBucket,
  Workflow,
} from "../src/api/client";
import { ApiProvider } from "../src/api/ApiProvider";

/**
 * A client whose every method rejects until a test overrides it. Deliberately
 * not a client that returns empty arrays: a test that forgot to stub the call
 * it exercises should fail loudly rather than assert against a silent empty
 * state that looks like a legitimate result.
 */
export function fakeClient(overrides: Partial<ApiClient> = {}): ApiClient {
  const unstubbed = (name: string) => async () => {
    throw new Error(`fakeClient: ${name} was called but not stubbed`);
  };
  return {
    listGoals: unstubbed("listGoals"),
    listWorkflows: unstubbed("listWorkflows"),
    listFleet: unstubbed("listFleet"),
    listEvents: unstubbed("listEvents"),
    listUsage: unstubbed("listUsage"),
    listInbox: unstubbed("listInbox"),
    ...overrides,
  };
}

/** A client whose every endpoint answers "nothing yet". */
export function emptyClient(): ApiClient {
  return fakeClient({
    listGoals: async () => [],
    listWorkflows: async () => [],
    listFleet: async () => [],
    listEvents: async () => [],
    listInbox: async () => [],
    listUsage: async () => [],
  });
}

/** A never-settling promise, for asserting the loading state. */
export function pending<T>(): Promise<T> {
  return new Promise<T>(() => {});
}

export function renderWithApi(ui: ReactElement, client: ApiClient, path = "/") {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <ApiProvider client={client}>{ui}</ApiProvider>
    </MemoryRouter>,
  );
}

export function makeEvent(over: Partial<EventRow> = {}): EventRow {
  return {
    seq: 1,
    id: "11111111-2222-4333-8444-555555555555",
    type: "turn_finished",
    at: "2026-09-09T12:00:00Z",
    project_id: "aaaaaaaa-2222-4333-8444-555555555555",
    project_name: "sprawl",
    workflow_instance_id: "bbbbbbbb-2222-4333-8444-555555555555",
    payload: {},
    ...over,
  };
}

export function makeGoal(over: Partial<Goal> = {}): Goal {
  return {
    id: "0d3f0000-2222-4333-8444-555555555555",
    seq: 41,
    opened_at: "2026-09-09T17:02:11.401Z",
    project_id: "9b1c0000-2222-4333-8444-555555555555",
    project_name: "sprawl",
    workflow_instance_id: "77aa0000-2222-4333-8444-555555555555",
    goal_type: "research",
    owner: "tower",
    legacy: false,
    ...over,
  };
}

export function makeWorkflow(over: Partial<Workflow> = {}): Workflow {
  return {
    workflow_instance_id: "77aa0000-2222-4333-8444-555555555555",
    project_id: "9b1c0000-2222-4333-8444-555555555555",
    project_name: "sprawl",
    event_count: 214,
    open_contract_count: 3,
    first_seq: 12,
    last_seq: 802,
    first_at: "2026-09-09T16:55:00.000Z",
    last_at: "2026-09-09T19:31:44.120Z",
    state: "in_flight",
    ...over,
  };
}

export function makeAgent(over: Partial<FleetMember> = {}): FleetMember {
  return {
    agent_name: "ratz",
    project_id: "9b1c0000-2222-4333-8444-555555555555",
    project_name: "sprawl",
    turn_count: 143,
    input_tokens: 1844290,
    output_tokens: 120455,
    first_seq: 12,
    last_seq: 799,
    last_turn_at: "2026-09-09T19:28:02.004Z",
    ...over,
  };
}

export function makeQuestion(over: Partial<InboxQuestion> = {}): InboxQuestion {
  return {
    event_id: "9f210000-2222-4333-8444-555555555555",
    seq: 640,
    opened_at: "2026-09-09T18:12:00.220Z",
    project_id: "9b1c0000-2222-4333-8444-555555555555",
    project_name: "sprawl",
    workflow_instance_id: "77aa0000-2222-4333-8444-555555555555",
    asker: "zone",
    question: "Should the drawer trap focus?",
    context: "",
    goal_event_id: "0d3f0000-2222-4333-8444-555555555555",
    ...over,
  };
}

export function makeUsageBucket(over: Partial<UsageBucket> = {}): UsageBucket {
  return {
    bucket_start: "2026-09-09T18:00:00Z",
    bucket: "hour",
    project_id: "9b1c0000-2222-4333-8444-555555555555",
    project_name: "sprawl",
    cost_usd: 29.53,
    sessions: 4,
    turns: 812,
    input_tokens: 18442901,
    output_tokens: 1204553,
    ...over,
  };
}
