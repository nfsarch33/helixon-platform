"use client";
import { useCosts } from "../../lib/hooks";
import type { RunUsage } from "../../lib/api";
import { Panel, ErrorState, Loading } from "../../components/States";
import { fmtInt } from "../../lib/format";

function Window({ title, u }: { title: string; u: RunUsage }) {
  return (
    <Panel title={title}>
      <dl className="grid grid-cols-2 gap-y-1 text-sm">
        <dt className="text-slate-500">Runs</dt><dd>{fmtInt(u.runs)}</dd>
        <dt className="text-slate-500">Completed / failed</dt><dd>{fmtInt(u.completed)} / {fmtInt(u.failed)}</dd>
        <dt className="text-slate-500">Running / needs a human</dt><dd>{fmtInt(u.running)} / {fmtInt(u.needs_human)}</dd>
        <dt className="text-slate-500">Tokens in</dt><dd>{fmtInt(u.tokens_in)}</dd>
        <dt className="text-slate-500">Tokens out</dt><dd>{fmtInt(u.tokens_out)}</dd>
      </dl>
    </Panel>
  );
}

export default function CostsPage() {
  const { data, error } = useCosts();
  if (error) return <ErrorState error={error} />;
  if (!data) return <Loading />;
  return (
    <div className="grid gap-4 md:grid-cols-3">
      <Window title="Last 24 hours" u={data.last_24h} />
      <Window title="Last 7 days" u={data.last_7d} />
      <Window title="All time" u={data.all_time} />
      <p className="text-xs text-slate-500 md:col-span-3">Tokens are the provider-reported counts recorded when each run ended; a run still in flight contributes nothing until it ends.</p>
    </div>
  );
}
