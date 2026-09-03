"use client";
import { useState } from "react";
import Link from "next/link";
import { useRuns } from "../../lib/hooks";
import type { RunStatus } from "../../lib/api";
import { Panel, EmptyState, ErrorState, Loading } from "../../components/States";
import { StatusBadge } from "../../components/StatusBadge";
import { fmtInt, fmtTime, truncate } from "../../lib/format";

const statuses: Array<RunStatus | ""> = ["", "running", "needs_human", "completed", "failed"];

export default function RunsPage() {
  const [status, setStatus] = useState<RunStatus | "">("");
  const { data, error } = useRuns(status, 100);
  return (
    <Panel
      title="Runs"
      actions={
        <label className="text-sm">
          <span className="mr-2 text-slate-500">Status</span>
          <select aria-label="Filter runs by status" value={status} onChange={(e) => setStatus(e.target.value as RunStatus | "")} className="rounded border border-slate-300 bg-white px-2 py-1 dark:border-slate-700 dark:bg-slate-900">
            {statuses.map((s) => <option key={s} value={s}>{s === "" ? "all" : s}</option>)}
          </select>
        </label>
      }
    >
      {error ? <ErrorState error={error} /> : !data ? <Loading /> : data.runs.length === 0 ? (
        <EmptyState title={status ? `No ${status} runs` : "No runs yet"} hint="This list is the durable run table; nothing here is sample data." />
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full text-left text-sm">
            <thead className="text-slate-500"><tr><th className="py-1 pr-3">Run</th><th className="py-1 pr-3">Status</th><th className="py-1 pr-3">Message</th><th className="py-1 pr-3">Iter.</th><th className="py-1 pr-3">Tokens</th><th className="py-1 pr-3">Attempts</th><th className="py-1">Updated</th></tr></thead>
            <tbody className="divide-y divide-slate-200 dark:divide-slate-800">
              {data.runs.map((r) => (
                <tr key={r.id}>
                  <td className="py-1 pr-3 font-mono"><Link href={`/runs/detail/?id=${encodeURIComponent(r.id)}`} className="underline">{r.id.slice(0, 8)}</Link></td>
                  <td className="py-1 pr-3"><StatusBadge status={r.status} /></td>
                  <td className="py-1 pr-3">{truncate(r.user_message, 70)}</td>
                  <td className="py-1 pr-3">{r.iterations}</td>
                  <td className="py-1 pr-3">{fmtInt(r.tokens_in)} / {fmtInt(r.tokens_out)}</td>
                  <td className="py-1 pr-3">{r.attempts}</td>
                  <td className="py-1 text-slate-500">{fmtTime(r.updated_at)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </Panel>
  );
}
