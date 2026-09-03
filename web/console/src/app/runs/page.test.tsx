import { describe, it, expect, vi, afterEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import { SWRConfig } from "swr";
import RunsPage from "./page";

// The page renders whatever the API returns and nothing else: a run list
// from a stubbed /api/v1/runs shows those runs; an empty list shows the
// empty state; a 503 shows the server's reason.
function mount() {
  return render(<SWRConfig value={{ provider: () => new Map(), dedupingInterval: 0 }}><RunsPage /></SWRConfig>);
}

afterEach(() => vi.unstubAllGlobals());

describe("RunsPage", () => {
  it("renders live runs from the API", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ runs: [
      { id: "abcdef12-0000", session_id: "s", user_message: "refactor the parser", status: "needs_human", attempts: 2, iterations: 3, tokens_in: 120, tokens_out: 40, created_at: "2026-09-03T00:00:00Z", updated_at: "2026-09-03T00:01:00Z", lease_until: "" },
    ], generated_at: "t" }), { status: 200 })));
    mount();
    await waitFor(() => expect(screen.getByText("refactor the parser")).toBeInTheDocument());
    expect(screen.getByText("abcdef12")).toBeInTheDocument();
    expect(screen.getByLabelText("status needs_human")).toHaveTextContent("needs a human");
  });
  it("shows the empty state when the store is empty", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ runs: [], generated_at: "t" }), { status: 200 })));
    mount();
    await waitFor(() => expect(screen.getByText("No runs yet")).toBeInTheDocument());
  });
  it("shows the server's reason on a 503", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ error: "run store not initialized" }), { status: 503 })));
    mount();
    await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent("run store not initialized"));
  });
});
