import "@testing-library/jest-dom/vitest";
import { afterEach, beforeEach, vi } from "vitest";
import { cleanup } from "@testing-library/react";

// jsdom has no navigator.clipboard and no secure-context; stub writeText so the
// copy path is exercisable. Reinstalled per-test so spies reset cleanly.
beforeEach(() => {
  Object.defineProperty(navigator, "clipboard", {
    value: { writeText: vi.fn().mockResolvedValue(undefined) },
    configurable: true,
  });
  localStorage.clear();
});

afterEach(() => {
  cleanup();
});
