import { describe, it, expect, vi, afterEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import { SWRConfig } from "swr";

const params = { get: (k: string) => (k === "id" ? currentId : null) };
let currentId: string | null = "run-1";
vi.mock("next/navigation", () => ({ useSearchParams: () => params }));

import RunDetailPage from "./page";

function mount() {
  return render(<SWRConfig value={{ provider: () => new Map(), dedupingInterval: 0 }}><RunDetailPage /></SWRConfig>);
}
afterEach(() => { vi.unstubAllGlobals(); currentId = "run-1"; });

const detail = {
  run: { id: "run-1abcdef", session_id: "sess-9876543", user_message: "summarise the incident", status: "completed", attempts: 1, iterations: 2, tokens_in: 880, tokens_out: 106, created_at: "2026-09-03T03:34:23Z", updated_at: "2026-09-03T03:34:25Z", lease_until: "", final_content: "a durable run survives a crash" },
  steps: [],
  turns: [
    { id: "t1", session_id: "sess-9876543", role: "user", content: "summarise the incident", seq: 1, created_at: "2026-09-03T03:34:23Z" },
    { id: "t2", session_id: "sess-9876543", role: "assistant", content: "a durable run survives a crash", seq: 2, created_at: "2026-09-03T03:34:25Z" },
  ],
  turns_scope: "session", limit: 200, steps_truncated: false, turns_truncated: false,
};

describe("RunDetailPage", () => {
  it("renders the run, its final content and its turns", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify(detail), { status: 200 })));
    mount();
    // The message shows twice by design: once as the run's prompt and once
    // as the first turn of the session.
    await waitFor(() => expect(screen.getAllByText(/summarise the incident/).length).toBeGreaterThan(1));
    expect(screen.getByLabelText("status completed")).toBeInTheDocument();
    expect(screen.getAllByText("a durable run survives a crash").length).toBeGreaterThan(0);
  });

  it("says the conversation is the session's, not the run's", async () => {
    // The API returns session-scoped turns. Presenting them as the run's own
    // conversation is a quiet over-claim, so the scope is on the page.
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify(detail), { status: 200 })));
    mount();
    await waitFor(() => expect(screen.getByText(/Session conversation \(2 turns\)/)).toBeInTheDocument());
    expect(screen.getByText(/may span more than this run/)).toBeInTheDocument();
  });

  it("says when the turn list was clipped rather than showing a short conversation", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ ...detail, turns_truncated: true, limit: 2 }), { status: 200 })));
    mount();
    await waitFor(() => expect(screen.getByText(/Showing the first 2/)).toBeInTheDocument());
  });

  it("shows the empty state when no run is selected, and never fetches", async () => {
    currentId = null;
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    mount();
    expect(screen.getByText("No run selected")).toBeInTheDocument();
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("shows the server's reason when the run is gone", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ error: "run not found" }), { status: 404 })));
    mount();
    await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent("run not found"));
  });
});
