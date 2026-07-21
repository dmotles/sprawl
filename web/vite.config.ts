import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// The SPA is served by hubd under /app/ (see internal/hub/server.go), so assets
// must resolve under that base. The built output goes into the go:embed target
// cmd/hubd/web/dist so `go build` / `make validate` need no node toolchain.
export default defineConfig({
  base: "/app/",
  plugins: [react()],
  build: {
    outDir: "../cmd/hubd/web/dist",
    emptyOutDir: true,
  },
});
