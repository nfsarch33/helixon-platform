"use client";
import { Suspense } from "react";
import { useSearchParams } from "next/navigation";
import { useRun } from "../../../lib/hooks";
import { Panel, EmptyState, ErrorState, Loading, NotRecorded } from "../../../components/States";
import { StatusBadge } from "../../../components/StatusBadge";
import { fmtInt, fmtTime, prettyJSON } from "../../../lib/format";

const muted = "text-slate-600 dark:text-slate-400";
// Long text is wrapped anywhere (tool output carries 50-character tokens), kept to a readable line length and
// scrolled inside its own box, so a 4 KB prompt or a 10 KB turn never widens the page.
const prose = "max-w-[80ch] whitespace-pre-wrap wrap-anywhere leading-relaxed";

// A static export cannot pre-render one page per run id, so the detail page
// reads ?id= on the client. The API it renders is /api/v1/runs/{id}.
function RunDetailInner() {
  const id = useSearchParams()?.get("id") ?? null;
  const { data, error } = useRun(id);
  if (!id) return <EmptyState title="No run selected" hint="Open a run from the Runs list." />;
  if (error) return <ErrorState error={error} />;
  if (!data) return <Loading label={`Loading run ${id.slice(0, 8)}`} />;
  const { run, steps, turns } = data;
  // The API returns the turns of the SESSION this run belongs to, not of the
  // run alone, and it says so in turns_scope. Labelling the panel
  // "Conversation" would have presented a session's whole history as this
  // run's, which is the kind of quiet over-claim an operator acts on.
  const sessionScoped = data.turns_scope === "session";
  return (
    // grid-cols-1 is minmax(0, 1fr): without it the implicit column grew to the widest
    // unbreakable line in any panel (23,254 px for one run) and pushed every value off-screen.
    <div className="grid grid-cols-1 items-start gap-4 2xl:grid-cols-[minmax(0,2fr)_minmax(0,3fr)]">
      <div className="grid min-w-0 grid-cols-1 gap-4">
        <Panel title={`Run ${run.id.slice(0, 8)}`}>
          <dl className="grid grid-cols-[max-content_minmax(0,1fr)] gap-x-4 gap-y-1.5 text-sm lg:grid-cols-[max-content_minmax(0,1fr)_max-content_minmax(0,1fr)]">
            <dt className={muted}>Status</dt><dd><StatusBadge status={run.status} /></dd>
            <dt className={muted}>Attempts / iterations</dt><dd className="tabular-nums">{run.attempts} / {run.iterations}</dd>
            <dt className={muted}>Tokens in / out</dt><dd className="tabular-nums">{fmtInt(run.tokens_in)} / {fmtInt(run.tokens_out)}</dd>
            <dt className={muted}>Updated</dt><dd>{fmtTime(run.updated_at) || <NotRecorded />}</dd>
            <dt className={muted}>Ticket</dt><dd className="font-mono wrap-anywhere">{run.meta?.ticket_id || <NotRecorded />}</dd>
            {run.err ? (<><dt className={muted}>Error</dt><dd className="font-mono wrap-anywhere text-rose-700 dark:text-rose-300">{run.err}</dd></>) : null}
          </dl>
          <h3 className={`mt-4 text-xs font-medium uppercase tracking-wide ${muted}`}>Message</h3>
          <div className="mt-1 max-h-[32rem] overflow-y-auto rounded border border-slate-200 p-3 text-sm dark:border-slate-800">
            <p className={prose}>{run.user_message || <NotRecorded />}</p>
          </div>
          {run.final_content ? (
            <>
              <h3 className={`mt-4 text-xs font-medium uppercase tracking-wide ${muted}`}>Final answer</h3>
              <div className="mt-1 max-h-96 overflow-y-auto rounded bg-slate-100 p-3 text-sm dark:bg-slate-800">
                <p className={prose}>{run.final_content}</p>
              </div>
            </>
          ) : null}
        </Panel>
        <Panel title={`Steps (${steps.length})`}>
          {steps.length === 0 ? <EmptyState title="No tool calls recorded" /> : (
            <ol className="divide-y divide-slate-200 text-sm dark:divide-slate-800">
              {steps.map((s) => (
                <li key={`${s.iteration}-${s.tool_call_id}`} className="py-2">
                  {/* One line per step; the full arguments open on demand instead of hiding behind an ellipsis. */}
                  <details>
                    <summary className="flex min-w-0 cursor-pointer items-center gap-2">
                      <span className={`w-10 shrink-0 tabular-nums ${muted}`}>#{s.seq}</span>
                      <StatusBadge status={s.status} />
                      <span className="shrink-0 font-mono">{s.tool}</span>
                      <span className={`min-w-0 flex-1 truncate font-mono text-xs ${muted}`}>{s.args}</span>
                      <span className={`hidden shrink-0 text-xs sm:inline ${muted}`}>{fmtTime(s.finished_at) || fmtTime(s.started_at)}</span>
                    </summary>
                    <pre className="mt-2 max-h-96 overflow-auto whitespace-pre-wrap wrap-anywhere rounded bg-slate-100 p-2 font-mono text-xs dark:bg-slate-800">{prettyJSON(s.args) || "(no arguments)"}</pre>
                  </details>
                </li>
              ))}
            </ol>
          )}
        </Panel>
      </div>
      <Panel title={`Session conversation (${turns.length} turns)`}>
        <p className={`mb-2 text-xs ${muted}`}>
          {sessionScoped ? `Every turn of session ${run.session_id.slice(0, 8)}, which may span more than this run.` : "Turns recorded for this run."}
          {data.turns_truncated ? ` Showing the first ${data.limit} — the session has more.` : ""}
        </p>
        {turns.length === 0 ? <EmptyState title="No turns yet" /> : (
          <ol className="space-y-2 text-sm">
            {turns.map((t) => (
              <li key={t.id} className="rounded border border-slate-200 p-2 dark:border-slate-800">
                <div className={`mb-1 flex flex-wrap gap-x-2 text-xs ${muted}`}>
                  <span className="font-medium uppercase">{t.role}</span><span className="tabular-nums">#{t.seq}</span>
                  {t.tool_call_id ? <span className="min-w-0 font-mono wrap-anywhere">{t.tool_call_id}</span> : null}
                  <span className="ml-auto">{fmtTime(t.created_at)}</span>
                </div>
                <div className="max-h-96 overflow-y-auto">
                  <p className={prose}>{prettyJSON(t.content)}</p>
                </div>
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
