// Typed client for the helixon read API (internal/helixon/dashboard/console.go).
// Every panel of the console renders one of these payloads; the client never
// fabricates data. Absence is a real state: an empty list, or an ApiError
// with the server's reason.

export type RunStatus = "running" | "completed" | "failed" | "needs_human";

export interface RunRecord {
  id: string;
  session_id: string;
  user_message: string;
  status: RunStatus;
  owner?: string;
  lease_until: string;
  attempts: number;
  iterations: number;
  tokens_in: number;
  tokens_out: number;
  final_content?: string;
  err?: string;
  meta?: Record<string, string>;
  created_at: string;
  updated_at: string;
}

export interface RunStep {
  run_id: string;
  seq: number;
  iteration: number;
  tool_call_id: string;
  tool: string;
  args: string;
  status: "pending" | "done" | "failed";
  result?: string;
  started_at: string;
  finished_at: string;
}

export interface Turn {
  id: string;
  session_id: string;
  role: string;
  content: string;
  tool_call_id?: string;
  tokens_in: number;
  tokens_out: number;
  created_at: string;
  seq: number;
}

export interface RunUsage {
  since: string;
  runs: number;
  running: number;
  completed: number;
  failed: number;
  needs_human: number;
  tokens_in: number;
  tokens_out: number;
}

export interface RunsResponse { runs: RunRecord[]; generated_at: string }
export interface RunDetail { run: RunRecord; steps: RunStep[]; turns: Turn[] }
export interface CostsResponse { last_24h: RunUsage; last_7d: RunUsage; all_time: RunUsage; generated_at: string }
export interface TextfileMetric { name: string; labels?: Record<string, string>; value: number; file: string }
export interface EvalsResponse {
  ledger: Record<string, unknown>[];
  ledger_path: string;
  ledger_error?: string;
  metrics: TextfileMetric[];
  textfile_dir: string;
  metrics_error?: string;
  generated_at: string;
}
export interface MemoryResponse { query: string; results: Record<string, unknown>[]; generated_at: string }
export interface DashboardResponse { agent_id: string; phase: string; heartbeat_every: string; channels: number; tools: number; generated_at: string }

export class ApiError extends Error {
  constructor(public readonly status: number, message: string, public readonly path: string) {
    super(message);
    this.name = "ApiError";
  }
}

// API calls are same-origin and outside the console's basePath, so they are
// written absolute. API_BASE lets `next dev` point at a running agent.
export const API_BASE = process.env.NEXT_PUBLIC_HLXN_API_BASE ?? "";

export async function fetchJSON<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(`${API_BASE}${path}`, { ...init, headers: { Accept: "application/json", ...(init?.headers ?? {}) } });
  const text = await res.text();
  let body: unknown = null;
  try { body = text ? JSON.parse(text) : null; } catch { body = null; }
  if (!res.ok) {
    const reason = (body as { error?: string } | null)?.error ?? `${res.status} ${res.statusText}`;
    throw new ApiError(res.status, reason, path);
  }
  return body as T;
}

export const api = {
  dashboard: () => fetchJSON<DashboardResponse>("/api/v1/dashboard"),
  runs: (status?: RunStatus | "", limit = 100) =>
    fetchJSON<RunsResponse>(`/api/v1/runs?${new URLSearchParams({ ...(status ? { status } : {}), limit: String(limit) })}`),
  run: (id: string) => fetchJSON<RunDetail>(`/api/v1/runs/${encodeURIComponent(id)}`),
  costs: () => fetchJSON<CostsResponse>("/api/v1/costs"),
  evals: (limit = 20) => fetchJSON<EvalsResponse>(`/api/v1/evals?limit=${limit}`),
  memory: (q: string, limit = 10) => fetchJSON<MemoryResponse>(`/api/v1/memory/search?${new URLSearchParams({ q, limit: String(limit) })}`),
};
