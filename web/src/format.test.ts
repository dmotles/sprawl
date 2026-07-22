import { describe, expect, it } from "vitest";
import { formatUnixMs, isActive } from "./format";

describe("formatUnixMs", () => {
  it("renders an em dash for the zero sentinel", () => {
    expect(formatUnixMs(0n)).toBe("—");
  });

  it("renders a fixed epoch as a stable ISO string", () => {
    expect(formatUnixMs(1_700_000_000_000n)).toBe("2023-11-14T22:13:20.000Z");
  });
});

describe("isActive", () => {
  it("is active when revokedAtUnixMs is the zero sentinel", () => {
    expect(isActive({ revokedAtUnixMs: 0n })).toBe(true);
  });

  it("is inactive when revokedAtUnixMs is set", () => {
    expect(isActive({ revokedAtUnixMs: 5n })).toBe(false);
  });
});
