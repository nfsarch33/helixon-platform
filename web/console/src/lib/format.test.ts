import { describe, it, expect } from "vitest";
import { fmtTime, fmtInt, truncate } from "./format";

// These three run on every row of every table, so their edge cases are the
// ones an operator sees: a zero timestamp from a run that never finished, a
// missing token count, a message longer than the column.
describe("format", () => {
  it("renders an absent or zero time as nothing, not as 1970", () => {
    expect(fmtTime(undefined)).toBe("");
    expect(fmtTime("")).toBe("");
    expect(fmtTime("0001-01-01T00:00:00Z")).toBe("");
    expect(fmtTime("1970-01-01T00:00:00Z")).toBe("");
    expect(fmtTime("not a date")).toBe("");
  });
  it("renders a real time", () => {
    expect(fmtTime("2026-09-03T04:05:06Z")).not.toBe("");
  });
  it("renders a missing count as 0 rather than blank", () => {
    expect(fmtInt(undefined)).toBe("0");
    expect(fmtInt(0)).toBe("0");
    expect(fmtInt(1234)).toBe((1234).toLocaleString());
  });
  it("truncates only what is longer than the limit, and marks it", () => {
    expect(truncate(undefined)).toBe("");
    expect(truncate("short")).toBe("short");
    expect(truncate("abcdef", 3)).toBe("abc\u2026");
    expect(truncate("abc", 3)).toBe("abc");
  });
});
