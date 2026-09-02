"use client";
import { useDashboard, useCosts, useRuns } from "../lib/hooks";
import { Panel, EmptyState, ErrorState, Loading } from "../components/States";
import { StatusBadge } from "../components/StatusBadge";
import { fmtInt, fmtTime, truncate } from "../lib/format";
import Link from "next/link";

export default function OverviewPage() {
  const dash = useDashboard();
  const costs = useCosts();
  const attention = useRuns("needs_human", 10);
  const recent = useRuns("", 8);
  return (
    <div className="grid gap-4 md:grid-cols-2">
      <Panel title="Agent">
        {dash.error ? <ErrorState error={dash.error} /> : !dash.data ? <Loading /> : (
          <dl className="grid grid-cols-2 gap-y-1 text-sm">
            <dt className="text-slate-500">Agent</dt><dd>{dash.data.agent_id || "—"}</dd>
            <dt className="text-slate-500">Phase</dt><dd>{dash.data.phase}</dd>
            <dt className="text-slate-500">Heartbeat</dt><dd>{dash.data.heartbeat_every}</dd>
            <dt className="text-slate-500">Channels / tools</dt><dd>{dash.data.channels} / {dash.data.tools}</dd>
          </dl>
        )}
      </Panel>
      <Panel title="Cost, last 24h">
        {costs.error ? <ErrorState error={costs.error} /> : !costs.data ? <Loading /> : (
          <dl className="grid grid-cols-2 gap-y-1 text-sm">
            <dt className="text-slate-500">Runs</dt><dd>{fmtInt(costs.data.last_24h.runs)}</dd>
            <dt className="text-slate-500">Tokens in / out</dt><dd>{fmtInt(costs.data.last_24h.tokens_in)} / {fmtInt(costs.data.last_24h.tokens_out)}</dd>
            <dt className="text-slate-500">Needs a human</dt><dd>{fmtInt(costs.data.last_24h.needs_human)}</dd>
          </dl>
        )}
      </Panel>
      <Panel title="Needs a human">
        {attention.error ? <ErrorState error={attention.error} /> : !attention.data ? <Loading /> : attention.data.runs.length === 0 ? (
          <EmptyState title="Nothing is waiting on you" />
        ) : (
          <ul className="divide-y divide-slate-200 dark:divide-slate-800">
            {attention.data.runs.map((r) => (
              <li key={r.id} className="py-2 text-sm">
                <Link href={`/runs/detail/?id=${encodeURIComponent(r.id)}`} className="font-mono underline">{r.id.slice(0, 8)}</Link>{" "}
                {truncate(r.user_message, 80)} <span className="text-slate-500">{fmtTime(r.updated_at)}</span>
              </li>
            ))}
          </ul>
        )}
      </Panel>
      <Panel title="Recent runs">
        {recent.error ? <ErrorState error={recent.error} /> : !recent.data ? <Loading /> : recent.data.runs.length === 0 ? (
          <EmptyState title="No runs yet" hint="Runs appear here as soon as the agent handles a message or claims a ticket." />
        ) : (
          <ul className="divide-y divide-slate-200 dark:divide-slate-800">
            {recent.data.runs.map((r) => (
              <li key={r.id} className="flex items-center gap-2 py-2 text-sm">
                <StatusBadge status={r.status} />
                <Link href={`/runs/detail/?id=${encodeURIComponent(r.id)}`} className="font-mono underline">{r.id.slice(0, 8)}</Link>
                <span className="truncate">{truncate(r.user_message, 60)}</span>
              </li>
            ))}
          </ul>
        )}
      </Panel>
    </div>
  );
}
