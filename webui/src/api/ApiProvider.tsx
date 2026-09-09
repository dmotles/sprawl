import { createContext, useContext, type ReactNode } from "react";
import type { ApiClient } from "./client";

const ApiContext = createContext<ApiClient | null>(null);

export function ApiProvider({ client, children }: { client: ApiClient; children: ReactNode }) {
  return <ApiContext.Provider value={client}>{children}</ApiContext.Provider>;
}

/**
 * The client for the current tree. Throws rather than falling back to a real
 * one: a missing provider in a test would otherwise reach the network and fail
 * in a way that looks like an API problem.
 */
export function useApi(): ApiClient {
  const client = useContext(ApiContext);
  if (!client) throw new Error("useApi: no <ApiProvider> above this component");
  return client;
}
