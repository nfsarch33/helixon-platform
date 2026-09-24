import { describe, it, expect } from "vitest";
import { fmtTime, fmtInt, truncate, prettyJSON } from "./format";

// These run on every row of every table, so their edge cases are the
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
  it("renders a real time with its zone, so the operator knows which clock it is", () => {
    const out = fmtTime("2026-09-03T04:05:06Z");
    const zone = new Intl.DateTimeFormat(undefined, { timeZoneName: "short" }).formatToParts(new Date("2026-09-03T04:05:06Z")).find((p) => p.type === "timeZoneName")?.value;
    expect(zone).toBeTruthy();
    expect(out).toContain(zone as string);
    expect(out).toContain("2026");
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
  it("indents JSON tool arguments and leaves everything else alone", () => {
    expect(prettyJSON('{"path":"a.go","n":1}')).toBe('{\n  "path": "a.go",\n  "n": 1\n}');
    expect(prettyJSON(' [1,2] ')).toBe("[\n  1,\n  2\n]");
    expect(prettyJSON("plain prose, not json")).toBe("plain prose, not json");
    expect(prettyJSON('{"broken": ')).toBe('{"broken": ');
    expect(prettyJSON(undefined)).toBe("");
  });
});
