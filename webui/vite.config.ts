import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// The SPA is served at the origin root by its own nginx container (see
// deploy/webui/Dockerfile.web), which reverse-proxies /api to the read API. So
// base is "/" and the bundle contains NO absolute API endpoint — every request
// is same-origin and relative. `npm run dev` proxies /api to a locally running
// API so the dev server has the same origin shape as production. That target is
// dev-server config, never bundled — the endpoint scan below `npm run build`
// (grep dist for http://) is what holds that line.
export default defineConfig({
  base: "/",
  plugins: [react()],
  build: {
    outDir: "dist",
    emptyOutDir: true,
  },
  server: {
    proxy: {
      "/api": {
        target: "http://127.0.0.1:8080",
        changeOrigin: true,
      },
    },
  },
});
