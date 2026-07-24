import "@testing-library/jest-dom/vitest";
import { afterEach, beforeEach, vi } from "vitest";
import { cleanup } from "@testing-library/react";

// jsdom has no ResizeObserver; @tanstack/react-virtual needs it to learn the
// scroll element's size. The stub reports the observed element's (test-mocked)
// getBoundingClientRect on observe so the virtualizer can compute a window;
// without a size report it renders zero rows.
if (!("ResizeObserver" in globalThis)) {
  globalThis.ResizeObserver = class {
    private cb: ResizeObserverCallback;
    constructor(cb: ResizeObserverCallback) {
      this.cb = cb;
    }
    observe(el: Element) {
      const rect = el.getBoundingClientRect();
      this.cb(
        [
          {
            target: el,
            contentRect: rect,
            borderBoxSize: [{ inlineSize: rect.width, blockSize: rect.height }],
          } as unknown as ResizeObserverEntry,
        ],
        this as unknown as ResizeObserver,
      );
    }
    unobserve() {}
    disconnect() {}
  } as unknown as typeof ResizeObserver;
}

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
