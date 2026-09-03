import type { RunStatus } from "../lib/api";

const styles: Record<string, string> = {
  running: "bg-blue-100 text-blue-900 dark:bg-blue-900 dark:text-blue-100",
  completed: "bg-emerald-100 text-emerald-900 dark:bg-emerald-900 dark:text-emerald-100",
  failed: "bg-rose-100 text-rose-900 dark:bg-rose-900 dark:text-rose-100",
  needs_human: "bg-amber-100 text-amber-900 dark:bg-amber-900 dark:text-amber-100",
  pending: "bg-slate-100 text-slate-900 dark:bg-slate-800 dark:text-slate-100",
  done: "bg-emerald-100 text-emerald-900 dark:bg-emerald-900 dark:text-emerald-100",
};

const labels: Record<string, string> = {
  needs_human: "needs a human",
};

export function StatusBadge({ status }: { status: RunStatus | RunStatus[number] | string }) {
  const cls = styles[status] ?? styles.pending;
  return (
    <span className={`inline-block rounded px-2 py-0.5 text-xs font-medium ${cls}`} aria-label={`status ${status}`}>
      {labels[status] ?? status}
    </span>
  );
}
