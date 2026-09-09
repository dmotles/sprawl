import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";

// Read the CSS as text rather than importing it: jsdom does not resolve custom
// properties from a stylesheet, and a `?raw` import comes back empty under the
// jsdom environment's CSS handling. Paths are relative to the vitest root
// (webui/), which is where `npm test` runs.
const read = (name: string) => readFileSync(resolve("src/styles", name), "utf8");
const tokens = read("tokens.css");
const app = read("app.css");

// jsdom does not compute custom properties from a stylesheet the way a browser
// does, so these assertions read the token source directly. That is enough for
// what they guard: the DARK-ONLY product decision (QUM-1349) and the "no raw
// colour outside tokens.css" rule, both of which are properties of the text.

describe("design tokens", () => {
  it("defines no light theme and no theme switch", () => {
    expect(tokens).not.toMatch(/prefers-color-scheme:\s*light/);
    expect(tokens).not.toMatch(/\[data-theme=/);
    expect(tokens).toMatch(/color-scheme:\s*dark/);
  });

  it("declares the full categorical series ramp in fixed slot order", () => {
    const slots = [...tokens.matchAll(/--series-(\d):\s*(#[0-9a-f]{6})/g)];
    expect(slots.map((m) => m[1])).toEqual(["1", "2", "3", "4", "5", "6", "7", "8"]);
    // The /dataviz dark column, unmodified — re-stepping by eye voids its
    // colour-blindness validation.
    expect(slots.map((m) => m[2])).toEqual([
      "#3987e5",
      "#d95926",
      "#199e70",
      "#c98500",
      "#d55181",
      "#008300",
      "#9085e9",
      "#e66767",
    ]);
  });

  it("keeps status colours out of the categorical slots", () => {
    const series = [...tokens.matchAll(/--series-\d:\s*(#[0-9a-f]{6})/g)].map((m) => m[1]);
    const status = [...tokens.matchAll(/--status-\w+:\s*(#[0-9a-f]{6})/g)].map((m) => m[1]);
    expect(status).toHaveLength(4);
    expect(series.filter((c) => status.includes(c))).toEqual([]);
  });

  // Hex is not the only way to write a colour: the first version of this
  // assertion greped for `#hex` alone and stayed green with a literal
  // `rgba(0, 0, 0, 0.6)` already in app.css.
  it("uses no raw colour outside the token file", () => {
    const stripped = app.replace(/\/\*[\s\S]*?\*\//g, "");
    expect(stripped).not.toMatch(/#[0-9a-fA-F]{3,8}\b|(?:rgba?|hsla?|color-mix)\(/);
  });
});
