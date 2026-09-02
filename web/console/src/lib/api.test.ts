import { describe, it, expect, vi, afterEach } from "vitest";
import { api, ApiError, fetchJSON } from "./api";

function mockFetch(status: number, body: unknown) {
  const text = typeof body === "string" ? body : JSON.stringify(body);
  vi.stubGlobal("fetch", vi.fn(async () => new Response(text, { status, headers: { "Content-Type": "application/json" } })));
}

afterEach(() => vi.unstubAllGlobals());

describe("fetchJSON", () => {
  it("returns the parsed body on 200", async () => {
    mockFetch(200, { runs: [], generated_at: "t" });
    await expect(fetchJSON("/api/v1/runs")).resolves.toEqual({ runs: [], generated_at: "t" });
  });
  it("surfaces the server's reason on an error status", async () => {
    mockFetch(503, { error: "run store not initialized" });
    await expect(fetchJSON("/api/v1/runs")).rejects.toMatchObject({ name: "ApiError", status: 503, message: "run store not initialized" });
  });
  it("never invents a body: a 404 without JSON is still an ApiError", async () => {
    mockFetch(404, "not found");
    const err = await fetchJSON("/api/v1/runs/x").catch((e) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect((err as ApiError).status).toBe(404);
  });
});

describe("api paths", () => {
  it("encodes the run id and the status filter", async () => {
    const f = vi.fn(async () => new Response("{}", { status: 200 }));
    vi.stubGlobal("fetch", f);
    await api.run("a/b");
    await api.runs("needs_human", 5);
    await api.memory("lease renewal", 3);
    const calls = (f.mock.calls as unknown as unknown[][]).map((c) => String(c[0]));
    expect(calls[0]).toBe("/api/v1/runs/a%2Fb");
    expect(calls[1]).toBe("/api/v1/runs?status=needs_human&limit=5");
    expect(calls[2]).toBe("/api/v1/memory/search?q=lease+renewal&limit=3");
  });
});
