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
  return <p role="status" aria-live="polite" className="text-sm text-slate-500">{label}…</p>;
}

export function Panel({ title, children, actions }: { title: string; children: ReactNode; actions?: ReactNode }) {
  return (
    <section className="rounded-lg border border-slate-200 bg-white p-4 shadow-sm dark:border-slate-800 dark:bg-slate-900" aria-labelledby={`panel-${title}`}>
      <div className="mb-3 flex items-center justify-between">
        <h2 id={`panel-${title}`} className="text-base font-semibold">{title}</h2>
        {actions}
      </div>
      {children}
    </section>
  );
}
