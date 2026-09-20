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

describe("BoardPage poll-now", () => {
  it("nudges the agent and confirms", async () => {
    const posts: string[] = [];
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/api/v1/board/sprints")) return new Response(JSON.stringify(sprintList), { status: 200 });
      if (url.includes("/tickets")) return new Response(JSON.stringify({ sprint_id: "s-live", tickets: [] }), { status: 200 });
      if (url.endsWith("/poll-now")) { posts.push(String(init?.method)); return new Response(JSON.stringify({ status: "scheduled" }), { status: 202 }); }
      return new Response("{}", { status: 404 });
    }));
    mount();
    fireEvent.click(await screen.findByRole("button", { name: "Poll now" }));
    await waitFor(() => expect(screen.getByText("poll scheduled")).toBeInTheDocument());
    expect(posts).toEqual(["POST"]);
  });

  it("says polling is off on a 503", async () => {
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.endsWith("/poll-now")) return new Response(JSON.stringify({ error: "ticket polling is not enabled on this agent" }), { status: 503 });
      if (url.endsWith("/api/v1/board/sprints")) return new Response(JSON.stringify(sprintList), { status: 200 });
      return new Response(JSON.stringify({ sprint_id: "s-live", tickets: [] }), { status: 200 });
    }));
    mount();
    fireEvent.click(await screen.findByRole("button", { name: "Poll now" }));
    await waitFor(() => expect(screen.getByText("ticket polling is off on this agent")).toBeInTheDocument());
  });
});

describe("BoardPage comments", () => {
  const escalatedList = {
    sprint_id: "s-live",
    tickets: [
      { id: "T-esc", title: "handed to human", status: "in_progress", priority: 1, claimed_by: "agent-a", updated_at: "2026-09-19T00:00:00Z" },
    ],
  };

  it("expands a ticket's escalation comments on demand", async () => {
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.endsWith("/api/v1/board/sprints")) return new Response(JSON.stringify(sprintList), { status: 200 });
      if (url.includes("/tickets/T-esc/comments")) {
        return new Response(JSON.stringify({ ticket_id: "T-esc", comments: [
          { id: 36, ticket_id: "T-esc", author: "helixon-fleet-wsl1", body: "Automated escalation.\n\nFailure: llm complete (iter 2)", created_at: "2026-09-19T00:00:00Z" },
        ] }), { status: 200 });
      }
      if (url.includes("/tickets")) return new Response(JSON.stringify(escalatedList), { status: 200 });
      return new Response("{}", { status: 404 });
    }));
    mount();
    fireEvent.click(await screen.findByRole("button", { name: "Comments" }));
    await waitFor(() => expect(screen.getByText(/Automated escalation/)).toBeInTheDocument());
    expect(screen.getByText(/Failure: llm complete/)).toBeInTheDocument();
    expect(screen.getByText("helixon-fleet-wsl1")).toBeInTheDocument();
  });

  it("shows the empty state for a ticket with no comments", async () => {
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.endsWith("/api/v1/board/sprints")) return new Response(JSON.stringify(sprintList), { status: 200 });
      if (url.includes("/comments")) return new Response(JSON.stringify({ ticket_id: "T-esc", comments: [] }), { status: 200 });
      if (url.includes("/tickets")) return new Response(JSON.stringify(escalatedList), { status: 200 });
      return new Response("{}", { status: 404 });
    }));
    mount();
    fireEvent.click(await screen.findByRole("button", { name: "Comments" }));
    await waitFor(() => expect(screen.getByText(/No comments/)).toBeInTheDocument());
  });

  it("does not fetch comments until expanded", async () => {
    const fetches: string[] = [];
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      fetches.push(url);
      if (url.endsWith("/api/v1/board/sprints")) return new Response(JSON.stringify(sprintList), { status: 200 });
      if (url.includes("/comments")) return new Response(JSON.stringify({ ticket_id: "T-esc", comments: [] }), { status: 200 });
      if (url.includes("/tickets")) return new Response(JSON.stringify(escalatedList), { status: 200 });
      return new Response("{}", { status: 404 });
    }));
    mount();
    await screen.findByText("handed to human");
    if (fetches.some((u) => u.includes("/comments"))) {
      throw new Error("comments were fetched before expansion");
    }
  });
});
