"use client";
import { useEvals } from "../../lib/hooks";
import { Panel, EmptyState, ErrorState, Loading } from "../../components/States";
import { fmtTime } from "../../lib/format";

function str(v: unknown): string { return v === undefined || v === null ? "" : String(v); }

export default function EvalsPage() {
  const { data, error } = useEvals(20);
  if (error) return <ErrorState error={error} />;
  if (!data) return <Loading />;
  return (
    <div className="grid gap-4">
      <Panel title="Scoreboard (published metrics)">
        {data.metrics_error ? <ErrorState error={data.metrics_error} /> : data.metrics.length === 0 ? (
          <EmptyState title="No published metrics" hint={`Looked in ${data.textfile_dir}. The nightly and weekly eval gates publish hlxn_* samples here.`} />
        ) : (
          <table className="w-full text-left text-sm">
            <thead className="text-slate-500"><tr><th className="py-1 pr-3">Metric</th><th className="py-1 pr-3">Labels</th><th className="py-1 pr-3">Value</th><th className="py-1">File</th></tr></thead>
            <tbody className="divide-y divide-slate-200 dark:divide-slate-800">
              {data.metrics.map((m, i) => (
                <tr key={`${m.name}-${i}`}><td className="py-1 pr-3 font-mono">{m.name}</td><td className="py-1 pr-3 font-mono text-xs">{Object.entries(m.labels ?? {}).map(([k, v]) => `${k}=${v}`).join(" ")}</td><td className="py-1 pr-3">{m.value}</td><td className="py-1 text-slate-500">{m.file}</td></tr>
              ))}
            </tbody>
          </table>
        )}
      </Panel>
      <Panel title="Cycle ledger (newest first)">
        {data.ledger_error ? <ErrorState error={data.ledger_error} /> : data.ledger.length === 0 ? (
          <EmptyState title="No cycle recorded yet" hint={`Looked at ${data.ledger_path}. Every eval gate run and every teacher–student comparison appends one record.`} />
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-left text-sm">
              <thead className="text-slate-500"><tr><th className="py-1 pr-3">#</th><th className="py-1 pr-3">Status</th><th className="py-1 pr-3">Finished</th><th className="py-1 pr-3">Stick</th><th className="py-1">Verdict</th></tr></thead>
              <tbody className="divide-y divide-slate-200 dark:divide-slate-800">
                {data.ledger.map((r, i) => (
                  <tr key={str(r.cycle_id) || i}>
                    <td className="py-1 pr-3">{str(r.seq)}</td>
                    <td className="py-1 pr-3">{str(r.status)}</td>
                    <td className="py-1 pr-3 text-slate-500">{fmtTime(str(r.finished_at))}</td>
                    <td className="py-1 pr-3 font-mono text-xs">{str(r.rubric_version)} {str(r.corpus_version)}</td>
                    <td className="py-1 font-mono text-xs">{str(r.eval_ab) || str(r.proposal)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Panel>
    </div>
  );
}
