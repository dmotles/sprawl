import "@testing-library/jest-dom/vitest";
import { afterEach } from "vitest";
import { cleanup } from "@testing-library/react";

// jsdom has no matchMedia; the responsive shell queries it to decide whether the
// nav drawer starts collapsed. Default to the desktop answer (no match) so a
// test that does not care gets the wide layout.
// Defined unconditionally (and configurable) so a test can vi.spyOn it to take
// the phone branch; jsdom declares the property but leaves it undefined, so an
// `in` guard would skip the stub and leave the shell calling undefined.
Object.defineProperty(window, "matchMedia", {
  writable: true,
  configurable: true,
  value: (query: string) => ({
    matches: false,
    media: query,
    onchange: null,
    addEventListener: () => {},
    removeEventListener: () => {},
    dispatchEvent: () => false,
  }),
});

afterEach(() => {
  cleanup();
});
