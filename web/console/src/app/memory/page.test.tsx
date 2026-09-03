import { describe, it, expect, vi, afterEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { SWRConfig } from "swr";
import MemoryPage from "./page";

function mount() {
  return render(<SWRConfig value={{ provider: () => new Map(), dedupingInterval: 0 }}><MemoryPage /></SWRConfig>);
}
afterEach(() => vi.unstubAllGlobals());

describe("MemoryPage", () => {
  it("submitting whitespace does not leave the panel loading forever", async () => {
    // The hook skips a blank query, so nothing is ever fetched; before the
    // trim, `submitted` was truthy and the page sat on <Loading/> for a
    // request that was never made.
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    mount();
    await userEvent.type(screen.getByLabelText("Search memory"), "   ");
    await userEvent.click(screen.getByRole("button", { name: "Search" }));
    await waitFor(() => expect(screen.getByText("Type a query to search the agent's memory")).toBeInTheDocument());
    // The empty state carries role=status itself, so the thing that must be
    // absent is the LOADING indicator, not every status region.
    expect(screen.queryByText(/^Loading/)).toBeNull();
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("renders results for a real query", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ query: "lease", results: [{ id: "m1", text: "a lease is exclusive" }], generated_at: "t" }), { status: 200 })));
    mount();
    await userEvent.type(screen.getByLabelText("Search memory"), "lease");
    await userEvent.click(screen.getByRole("button", { name: "Search" }));
    await waitFor(() => expect(screen.getByText(/a lease is exclusive/)).toBeInTheDocument());
  });

  it("says so when nothing matched, rather than showing an empty box", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ query: "zzz", results: [], generated_at: "t" }), { status: 200 })));
    mount();
    await userEvent.type(screen.getByLabelText("Search memory"), "zzz");
    await userEvent.click(screen.getByRole("button", { name: "Search" }));
    await waitFor(() => expect(screen.getByText(/Nothing matched/)).toBeInTheDocument());
  });

  it("shows the server's reason when memory is not configured", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ error: "memory not configured on this runtime" }), { status: 503 })));
    mount();
    await userEvent.type(screen.getByLabelText("Search memory"), "anything");
    await userEvent.click(screen.getByRole("button", { name: "Search" }));
    await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent("memory not configured on this runtime"));
  });
});
