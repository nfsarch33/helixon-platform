import { describe, it, expect, vi, afterEach } from "vitest";
import { render, screen, waitFor, fireEvent } from "@testing-library/react";
import { SWRConfig } from "swr";
import BoardPage from "./page";

// The board page renders what the board proxy returns and nothing else, and
// exposes exactly the two operator verbs: requeue on terminal tickets,
// resolve on everything else. Claim never appears.

function mount() {
  return render(<SWRConfig value={{ provider: () => new Map(), dedupingInterval: 0 }}><BoardPage /></SWRConfig>);
}

afterEach(() => vi.unstubAllGlobals());

const sprintList = { count: 1, sprints: [{ id: "s-live", name: "v18850 run", status: "active", created_at: "t" }] };
const ticketList = {
  sprint_id: "s-live",
  tickets: [
    { id: "T1", title: "ship the guard", status: "done", priority: 3, claimed_by: "agent-a", updated_at: "2026-09-19T00:00:00Z" },
    { id: "T2", title: "write the audit", status: "in_progress", priority: 2, claimed_by: "agent-b", updated_at: "2026-09-19T00:00:00Z" },
    { id: "T3", title: "plan the next one", status: "ready", priority: 1, updated_at: "2026-09-19T00:00:00Z" },
  ],
};

describe("BoardPage", () => {
  it("renders the active sprint and its tickets", async () => {
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.endsWith("/api/v1/board/sprints")) return new Response(JSON.stringify(sprintList), { status: 200 });
      if (url.includes("/api/v1/board/sprints/s-live/tickets")) return new Response(JSON.stringify(ticketList), { status: 200 });
      return new Response("{}", { status: 404 });
    }));
    mount();
    await waitFor(() => expect(screen.getByText("ship the guard")).toBeInTheDocument());
    expect(screen.getByRole("button", { name: /v18850 run/ })).toBeInTheDocument();
    expect(screen.getByText(/3 tickets/)).toBeInTheDocument();
  });

  it("shows requeue only on terminal tickets and resolve only on open ones", async () => {
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.endsWith("/api/v1/board/sprints")) return new Response(JSON.stringify(sprintList), { status: 200 });
      if (url.includes("/api/v1/board/sprints/s-live/tickets")) return new Response(JSON.stringify(ticketList), { status: 200 });
      return new Response("{}", { status: 404 });
    }));
    mount();
    await waitFor(() => expect(screen.getByText("ship the guard")).toBeInTheDocument());
    expect(screen.getAllByRole("button", { name: "Requeue" })).toHaveLength(1); // T1 done
    expect(screen.getAllByRole("button", { name: "Resolve" })).toHaveLength(2); // T2, T3
  });

  it("requeues a terminal ticket through the proxy with a reason", async () => {
    const posts: string[] = [];
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/api/v1/board/sprints")) return new Response(JSON.stringify(sprintList), { status: 200 });
      if (url.includes("/api/v1/board/sprints/s-live/tickets")) return new Response(JSON.stringify(ticketList), { status: 200 });
      if (url.endsWith("/requeue")) {
        posts.push(`${init?.method} ${url} ${String(init?.body)}`);
        return new Response(JSON.stringify({ ...ticketList.tickets[0], status: "ready" }), { status: 200 });
      }
      return new Response("{}", { status: 404 });
    }));
    vi.spyOn(window, "prompt").mockReturnValue("wrong fix, run again");
    mount();
    await waitFor(() => expect(screen.getByText("ship the guard")).toBeInTheDocument());
    fireEvent.click(screen.getByRole("button", { name: "Requeue" }));
    await waitFor(() => expect(posts).toEqual([
      `POST /api/v1/board/tickets/T1/requeue {"actor":"operator","reason":"wrong fix, run again"}`,
    ]));
  });

  it("shows the empty state for a sprint with no tickets", async () => {
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.endsWith("/api/v1/board/sprints")) return new Response(JSON.stringify(sprintList), { status: 200 });
      if (url.includes("/tickets")) return new Response(JSON.stringify({ sprint_id: "s-live", tickets: [] }), { status: 200 });
      return new Response("{}", { status: 404 });
    }));
    mount();
    await waitFor(() => expect(screen.getByText("No tickets in this sprint")).toBeInTheDocument());
  });

  it("shows the server's reason when the board is unreachable", async () => {
    vi.stubGlobal("fetch", vi.fn(async () =>
      new Response(JSON.stringify({ error: "sprintboard unreachable: dial refused" }), { status: 502 })));
    mount();
    await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent("sprintboard unreachable"));
  });
});
