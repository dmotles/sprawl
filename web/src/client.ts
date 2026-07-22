import { createPromiseClient, type PromiseClient } from "@connectrpc/connect";
import { HubService } from "../gen/hub/v1/hub_connect";
import { transport } from "./transport";

// HubClient is the typed connect-es client surface. It is injected into
// components as a prop (default: defaultClient) so tests can substitute a
// createRouterTransport-backed fake without touching global fetch.
export type HubClient = PromiseClient<typeof HubService>;

export const defaultClient: HubClient = createPromiseClient(HubService, transport);
