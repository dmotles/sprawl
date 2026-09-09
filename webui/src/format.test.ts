import { describe, expect, it } from "vitest";
import {
  formatCount,
  formatRelative,
  formatTime,
  formatUsd,
  projectLabel,
  shortId,
} from "./format";

describe("formatTime", () => {
  it("renders UTC, not the browser's zone", () => {
    // A local-time rendering silently shifts every row against the server logs
    // an operator is comparing them to.
    expect(formatTime("2026-09-09T18:43:08.123456Z")).toBe("2026-09-09 18:43:08Z");
  });

  it("passes an unparseable timestamp through rather than showing Invalid Date", () => {
    expect(formatTime("not a time")).toBe("not a time");
  });
});

describe("formatRelative", () => {
  const now = new Date("2026-09-09T12:00:00Z");

  it.each([
    ["2026-09-09T11:59:50Z", "just now"],
    ["2026-09-09T11:57:00Z", "3m ago"],
    ["2026-09-09T09:00:00Z", "3h ago"],
    ["2026-09-06T12:00:00Z", "3d ago"],
  ])("renders %s as %s", (iso, want) => {
    expect(formatRelative(iso, now)).toBe(want);
  });

  it("does not claim a future timestamp is old", () => {
    // Two hosts' clocks disagree; a "-4m ago" would look like a bug in the log.
    expect(formatRelative("2026-09-09T12:04:00Z", now)).toBe("just now");
  });
});

describe("formatUsd / formatCount", () => {
  it("renders an em dash for null, never a zero", () => {
    // A 0 in a spend column is a claim that nothing was spent; the truth is
    // that we do not know.
    expect(formatUsd(null)).toBe("—");
    expect(formatCount(null)).toBe("—");
  });

  it("renders a real zero as zero", () => {
    expect(formatUsd(0)).toBe("$0.00");
    expect(formatCount(0)).toBe("0");
  });

  it("formats money to cents and counts with separators", () => {
    expect(formatUsd(41.9382)).toBe("$41.94");
    expect(formatCount(18442901)).toBe("18,442,901");
  });
});

describe("projectLabel", () => {
  it("renders an em dash for a remote with no repo path", () => {
    expect(projectLabel("")).toBe("—");
  });

  it("passes a derived label through unchanged", () => {
    expect(projectLabel("sprawl")).toBe("sprawl");
  });
});

describe("shortId", () => {
  it("keeps the first uuid segment", () => {
    expect(shortId("11111111-2222-4333-8444-555555555555")).toBe("11111111");
  });
});
