import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";

// Separate from vite.config.ts so the production build keeps its base:"/app/"
// config clean. Vitest is DELIBERATELY not part of `make validate` (which stays
// node-free) — run it with `cd web && npm test`.
export default defineConfig({
  plugins: [react()],
  test: {
    environment: "jsdom",
    globals: true,
    setupFiles: ["./test/setup.ts"],
  },
});
