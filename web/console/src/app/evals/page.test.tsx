import { describe, it, expect, vi, afterEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import { SWRConfig } from "swr";
import EvalsPage from "./page";

function mount() {
  return render(<SWRConfig value={{ provider: () => new Map(), dedupingInterval: 0 }}><EvalsPage /></SWRConfig>);
}
afterEach(() => vi.unstubAllGlobals());

const payload = {
  metrics: [{ name: "hlxn_student_score_ratio", labels: { tier: "minimax" }, value: 0.964, file: "hlxn_eval.prom" }],
  ledger: [{ seq: 3, cycle_id: "c3", status: "promoted", finished_at: "2026-09-03T02:31:00Z", rubric_version: "heuristic-v1", corpus_version: "v1", eval_ab: "26 -> 27" }],
  ledger_path: "/ledger.ndjson", textfile_dir: "/textfiles", generated_at: "t",
};

describe("EvalsPage", () => {
  it("renders published metrics and ledger rows", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify(payload), { status: 200 })));
    mount();
    await waitFor(() => expect(screen.getByText("hlxn_student_score_ratio")).toBeInTheDocument());
    expect(screen.getByText("tier=minimax")).toBeInTheDocument();
    expect(screen.getByText("promoted")).toBeInTheDocument();
  });

  it("names where it looked when there is nothing to show", async () => {
    // Absence has to say where it looked, or an operator cannot tell a quiet
    // gate from a mis-pointed path.
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ ...payload, metrics: [], ledger: [] }), { status: 200 })));
    mount();
    await waitFor(() => expect(screen.getByText("No published metrics")).toBeInTheDocument());
    expect(screen.getByText(/\/textfiles/)).toBeInTheDocument();
    expect(screen.getByText("No cycle recorded yet")).toBeInTheDocument();
    expect(screen.getByText(/\/ledger.ndjson/)).toBeInTheDocument();
  });

  it("surfaces a per-source error instead of an empty table", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ ...payload, metrics: [], metrics_error: "textfile dir unreadable" }), { status: 200 })));
    mount();
    await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent("textfile dir unreadable"));
  });
});
