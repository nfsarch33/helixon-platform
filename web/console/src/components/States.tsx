import type { ReactNode } from "react";
import { ApiError } from "../lib/api";

// Absence and failure are first-class states. A panel never shows sample
// data: it says what is missing and, where it can, what would fill it.
export function EmptyState({ title, hint }: { title: string; hint?: string }) {
  return (
    <div role="status" className="rounded border border-dashed border-slate-300 p-6 text-center text-slate-600 dark:border-slate-700 dark:text-slate-300">
      <p className="font-medium">{title}</p>
      {hint ? <p className="mt-1 text-sm">{hint}</p> : null}
    </div>
  );
}

export function ErrorState({ error }: { error: unknown }) {
  const msg = error instanceof ApiError ? `${error.status}: ${error.message}` : error instanceof Error ? error.message : String(error);
  return (
    <div role="alert" className="rounded border border-rose-300 bg-rose-50 p-4 text-rose-900 dark:border-rose-800 dark:bg-rose-950 dark:text-rose-100">
      <p className="font-medium">Could not load this panel</p>
      <p className="mt-1 font-mono text-sm">{msg}</p>
    </div>
  );
}

export function Loading({ label = "Loading" }: { label?: string }) {
  return <p role="status" aria-live="polite" className="text-sm text-slate-600 dark:text-slate-400">{label}…</p>;
}

// NotRecorded marks a value the API did not return, so an absent value never looks like a real "0" or a blank.
export function NotRecorded() {
  return <span aria-label="not recorded" className="text-slate-500 dark:text-slate-400">—</span>;
}

// A DOM id cannot contain whitespace, so a title of more than one word used
// to produce an aria-labelledby pointing at no element at all: the section
// was left with no accessible name, which is worse than having no landmark.
function panelId(title: string) {
  return `panel-${title.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, "")}`;
}

export function Panel({ title, children, actions }: { title: string; children: ReactNode; actions?: ReactNode }) {
  const id = panelId(title);
  return (
    <section className="min-w-0 rounded-lg border border-slate-200 bg-white p-4 shadow-sm dark:border-slate-800 dark:bg-slate-900" aria-labelledby={id}>
      <div className="mb-3 flex flex-wrap items-center justify-between gap-2">
        <h2 id={id} className="min-w-0 text-base font-semibold wrap-anywhere">{title}</h2>
        {actions}
      </div>
      {children}
    </section>
  );
}
