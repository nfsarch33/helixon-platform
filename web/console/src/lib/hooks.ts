"use client";
import useSWR from "swr";
import { api, type RunStatus } from "./api";

// One hook per panel. Polling intervals are short because the operator
// watches a live agent; SWR dedupes and pauses in background tabs.
export function useDashboard() { return useSWR("dashboard", api.dashboard, { refreshInterval: 5000 }); }
export function useRuns(status: RunStatus | "" = "", limit = 100) {
  return useSWR(["runs", status, limit], () => api.runs(status, limit), { refreshInterval: 3000 });
}
export function useRun(id: string | null) {
  return useSWR(id ? ["run", id] : null, () => api.run(id as string), { refreshInterval: 2000 });
}
export function useCosts() { return useSWR("costs", api.costs, { refreshInterval: 10000 }); }
export function useEvals(limit = 20) { return useSWR(["evals", limit], () => api.evals(limit), { refreshInterval: 30000 }); }
export function useMemorySearch(q: string) {
  return useSWR(q.trim() ? ["memory", q] : null, () => api.memory(q), { revalidateOnFocus: false });
}
