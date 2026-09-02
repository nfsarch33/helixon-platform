"use client";
import { Suspense } from "react";
import { useSearchParams } from "next/navigation";
import { useRun } from "../../../lib/hooks";
import { Panel, EmptyState, ErrorState, Loading } from "../../../components/States";
import { StatusBadge } from "../../../components/StatusBadge";
import { fmtInt, fmtTime } from "../../../lib/format";

// A static export cannot pre-render one page per run id, so the detail page
// reads ?id= on the client. The API it renders is /api/v1/runs/{id}.
function RunDetailInner() {
  const id = useSearchParams()?.get("id") ?? null;
  const { data, error } = useRun(id);
  if (!id) return <EmptyState title="No run selected" hint="Open a run from the Runs list." />;
  if (error) return <ErrorState error={error} />;
  if (!data) return <Loading label={`Loading run ${id.slice(0, 8)}`} />;
  const { run, steps, turns } = data;
  return (
    <div className="grid gap-4">
      <Panel title={`Run ${run.id.slice(0, 8)}`}>
        <dl className="grid grid-cols-2 gap-y-1 text-sm md:grid-cols-4">
          <dt className="text-slate-500">Status</dt><dd><StatusBadge status={run.status} /></dd>
          <dt className="text-slate-500">Attempts / iterations</dt><dd>{run.attempts} / {run.iterations}</dd>
          <dt className="text-slate-500">Tokens in / out</dt><dd>{fmtInt(run.tokens_in)} / {fmtInt(run.tokens_out)}</dd>
          <dt className="text-slate-500">Updated</dt><dd>{fmtTime(run.updated_at)}</dd>
          {run.meta?.ticket_id ? (<><dt className="text-slate-500">Ticket</dt><dd className="font-mono">{run.meta.ticket_id}</dd></>) : null}
          {run.err ? (<><dt className="text-slate-500">Error</dt><dd className="font-mono text-rose-700 dark:text-rose-300">{run.err}</dd></>) : null}
        </dl>
        <p className="mt-3 whitespace-pre-wrap text-sm"><span className="text-slate-500">Message: </span>{run.user_message}</p>
        {run.final_content ? <p className="mt-2 whitespace-pre-wrap rounded bg-slate-100 p-2 text-sm dark:bg-slate-800">{run.final_content}</p> : null}
      </Panel>
      <Panel title={`Steps (${steps.length})`}>
        {steps.length === 0 ? <EmptyState title="No tool calls recorded" /> : (
          <ol className="divide-y divide-slate-200 text-sm dark:divide-slate-800">
            {steps.map((s) => (
              <li key={`${s.iteration}-${s.tool_call_id}`} className="flex flex-wrap items-center gap-2 py-2">
                <span className="w-10 text-slate-500">#{s.seq}</span>
                <StatusBadge status={s.status} />
                <span className="font-mono">{s.tool}</span>
                <span className="truncate text-slate-500">{s.args}</span>
                <span className="ml-auto text-slate-500">{fmtTime(s.finished_at) || fmtTime(s.started_at)}</span>
              </li>
            ))}
          </ol>
        )}
      </Panel>
      <Panel title={`Conversation (${turns.length} turns)`}>
        {turns.length === 0 ? <EmptyState title="No turns yet" /> : (
          <ol className="space-y-2 text-sm">
            {turns.map((t) => (
              <li key={t.id} className="rounded border border-slate-200 p-2 dark:border-slate-800">
                <div className="mb-1 flex gap-2 text-xs text-slate-500"><span className="font-medium uppercase">{t.role}</span><span>#{t.seq}</span>{t.tool_call_id ? <span className="font-mono">{t.tool_call_id}</span> : null}<span className="ml-auto">{fmtTime(t.created_at)}</span></div>
                <p className="whitespace-pre-wrap break-words">{t.content}</p>
              </li>
            ))}
          </ol>
        )}
      </Panel>
    </div>
  );
}

export default function RunDetailPage() {
  return <Suspense fallback={<Loading />}><RunDetailInner /></Suspense>;
}
