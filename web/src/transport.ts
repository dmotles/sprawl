import { createConnectTransport } from "@connectrpc/connect-web";

// Same-origin transport. baseUrl "/" means RPC requests go to
// /hub.v1.HubService/<Method> on whatever origin served the SPA — NO hardcoded
// hub endpoint (public-repo hygiene, docs 01 §3 / 04). The browser's HttpOnly
// session cookie rides these fetches automatically (fetch defaults to
// same-origin credentials); the SPA never holds or sends the bearer/login
// token after login.
export const transport = createConnectTransport({
  baseUrl: "/",
});
