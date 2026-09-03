"use client";
import { useState } from "react";
import { useMemorySearch } from "../../lib/hooks";
import { Panel, EmptyState, ErrorState, Loading } from "../../components/States";

export default function MemoryPage() {
  const [q, setQ] = useState("");
  const [submitted, setSubmitted] = useState("");
  const { data, error, isLoading } = useMemorySearch(submitted);
  return (
    <Panel title="Memory search">
      {/* Trim on submit: the API rejects a blank q with 400, and a query of
          spaces used to be "submitted" without ever producing a request, so
          the panel sat on <Loading/> for a search that was never made. */}
      <form className="mb-3 flex gap-2" onSubmit={(e) => { e.preventDefault(); setSubmitted(q.trim()); }}>
        <label className="sr-only" htmlFor="memory-q">Search memory</label>
        <input id="memory-q" value={q} onChange={(e) => setQ(e.target.value)} placeholder="What did the agent learn about…" className="flex-1 rounded border border-slate-300 bg-white px-3 py-1.5 text-sm dark:border-slate-700 dark:bg-slate-900" />
        <button type="submit" className="rounded bg-slate-900 px-3 py-1.5 text-sm text-white dark:bg-white dark:text-slate-900">Search</button>
      </form>
      {!submitted ? <EmptyState title="Type a query to search the agent's memory" /> : error ? <ErrorState error={error} /> : isLoading || !data ? <Loading /> : data.results.length === 0 ? (
        <EmptyState title={`Nothing matched “${data.query}”`} />
      ) : (
        <ol className="space-y-2 text-sm">
          {data.results.map((r, i) => (
            <li key={i} className="rounded border border-slate-200 p-2 dark:border-slate-800">
              <pre className="whitespace-pre-wrap break-words text-xs">{JSON.stringify(r, null, 1)}</pre>
            </li>
          ))}
        </ol>
      )}
    </Panel>
  );
}
