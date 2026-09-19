"use client";
import { useMemo, useState } from "react";
import { useBoardSprints, useBoardTickets } from "../../lib/hooks";
import { api, ApiError, boardTerminal, type BoardTicket, type BoardTicketStatus } from "../../lib/api";
import { Panel, EmptyState, ErrorState, Loading } from "../../components/States";
import { StatusBadge } from "../../components/StatusBadge";
import { fmtTime } from "../../lib/format";

// The board page is the operator side of the ticket loop: see the sprints,
// see every ticket with its status, and act with the two human verbs --
// requeue closed work that should not be closed, resolve open work a person
// is taking over. Agents claim and complete; the console never does.

// The launch button: poke the agent's ticket poller out of its idle backoff
// so freshly created or requeued work is picked up in seconds. A 503 means
// ticket polling is off on this agent -- render that reason, not silence.
function PollNowButton() {
  const [state, setState] = useState<"idle" | "busy" | "done" | "off" | "err">("idle");
  const [msg, setMsg] = useState<string>("");
  async function pollNow() {
    setState("busy");
    setMsg("");
    try {
      await api.boardPollNow();
      setState("done");
    } catch (e) {
      if (e instanceof ApiError && e.status === 503) setState("off");
      else { setState("err"); setMsg(e instanceof Error ? e.message : String(e)); }
    }
  }
  return (
    <div className="flex items-center gap-2 text-sm">
      <button type="button" onClick={pollNow} disabled={state === "busy"}
        className="rounded bg-blue-100 px-3 py-1.5 font-medium text-blue-900 hover:bg-blue-200 disabled:opacity-50 dark:bg-blue-900 dark:text-blue-100 dark:hover:bg-blue-800">
        Poll now
      </button>
      {state === "done" ? <span className="text-emerald-700 dark:text-emerald-300" role="status">poll scheduled</span> : null}
      {state === "off" ? <span className="text-amber-700 dark:text-amber-300" role="status">ticket polling is off on this agent</span> : null}
      {state === "err" ? <span className="text-rose-700 dark:text-rose-300" role="alert">{msg}</span> : null}
    </div>
  );
}

const statusOrder: BoardTicketStatus[] = ["in_progress", "ready", "review", "ready_for_handoff", "blocked", "backlog", "done", "resolved_by_human"];

export default function BoardPage() {
  const sprints = useBoardSprints();
  const activeSprint = useMemo(() => {
    const list = sprints.data?.sprints ?? [];
    return list.find((s) => s.status === "active") ?? list[0] ?? null;
  }, [sprints.data]);
  const [selected, setSelected] = useState<string | null>(null);
  const sprintID = selected ?? activeSprint?.id ?? null;
  const tickets = useBoardTickets(sprintID);

  return (
    <main className="mx-auto max-w-6xl space-y-4 p-6">
      <header className="flex flex-wrap items-baseline justify-between gap-2">
        <h1 className="text-2xl font-semibold">Ticket board</h1>
        <PollNowButton />
      </header>

      <Panel title="Sprint">
        {sprints.error ? <ErrorState error={sprints.error} /> : sprints.isLoading ? <Loading /> : !sprints.data || sprints.data.sprints.length === 0 ? (
          <EmptyState title="No sprints on the board" hint="the board API returned no sprints" />
        ) : (
          <div className="flex flex-wrap gap-2">
            {sprints.data.sprints.map((s) => (
              <button
                key={s.id}
                type="button"
                onClick={() => setSelected(s.id)}
                aria-pressed={(sprintID ?? "") === s.id}
                className={`rounded px-3 py-1.5 text-sm ${sprintID === s.id ? "bg-slate-900 text-white dark:bg-white dark:text-slate-900" : "text-slate-700 hover:bg-slate-100 dark:text-slate-200 dark:hover:bg-slate-800"}`}
              >
                {s.name} <span className="opacity-60">({s.status})</span>
              </button>
            ))}
          </div>
        )}
      </Panel>

      <Panel title="Tickets">
        {tickets.error ? <ErrorState error={tickets.error} /> : tickets.isLoading ? <Loading /> : !tickets.data || tickets.data.tickets.length === 0 ? (
          <EmptyState title="No tickets in this sprint" hint="an empty sprint stays empty" />
        ) : (
          <TicketTable tickets={tickets.data.tickets} onChanged={tickets.mutate} />
        )}
      </Panel>
    </main>
  );
}

function TicketTable({ tickets, onChanged }: { tickets: BoardTicket[]; onChanged: () => void }) {
  const sorted = useMemo(
    () =>
      [...tickets].sort((a, b) => {
        const d = statusOrder.indexOf(a.status) - statusOrder.indexOf(b.status);
        return d !== 0 ? d : b.priority - a.priority || a.id.localeCompare(b.id);
      }),
    [tickets],
  );
  const tally = useMemo(() => {
    const t = {} as Record<string, number>;
    for (const tk of tickets) t[tk.status] = (t[tk.status] ?? 0) + 1;
    return t;
  }, [tickets]);

  return (
    <div className="space-y-3">
      <p className="text-sm text-slate-500">
        {tickets.length} tickets · {Object.entries(tally).map(([k, v]) => `${v} ${k}`).join(" · ")}
      </p>
      <ul className="divide-y divide-slate-200 dark:divide-slate-800">
        {sorted.map((tk) => (
          <TicketRow key={tk.id} ticket={tk} onChanged={onChanged} />
        ))}
      </ul>
    </div>
  );
}

function TicketRow({ ticket, onChanged }: { ticket: BoardTicket; onChanged: () => void }) {
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const terminal = boardTerminal(ticket.status);

  async function act(kind: "requeue" | "resolve") {
    const reason = window.prompt(`Reason for ${kind} of ${ticket.id}?`);
    if (!reason) return;
    setBusy(true);
    setErr(null);
    try {
      if (kind === "requeue") await api.boardRequeue(ticket.id, "operator", reason);
      else await api.boardResolve(ticket.id, "operator", reason);
      onChanged();
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <li className="flex flex-wrap items-start justify-between gap-2 py-2">
      <div className="min-w-0">
        <p className="truncate font-medium">
          <span className="font-mono text-sm text-slate-500">{ticket.id}</span> {ticket.title}
        </p>
        <p className="mt-0.5 flex flex-wrap items-center gap-2 text-xs text-slate-500">
          <StatusBadge status={ticket.status} />
          {ticket.claimed_by ? <span>claimed by {ticket.claimed_by}</span> : null}
          {ticket.acceptance_criteria ? <span className="truncate" title={ticket.acceptance_criteria}>AC: {ticket.acceptance_criteria}</span> : null}
          {ticket.updated_at ? <span>{fmtTime(ticket.updated_at)}</span> : null}
        </p>
        {err ? <p role="alert" className="mt-1 text-xs text-rose-700 dark:text-rose-300">{err}</p> : null}
      </div>
      <div className="flex shrink-0 gap-2">
        {terminal ? (
          <button type="button" disabled={busy} onClick={() => act("requeue")}
            className="rounded bg-amber-100 px-2 py-1 text-xs font-medium text-amber-900 hover:bg-amber-200 disabled:opacity-50 dark:bg-amber-900 dark:text-amber-100 dark:hover:bg-amber-800">
            Requeue
          </button>
        ) : (
          <button type="button" disabled={busy} onClick={() => act("resolve")}
            className="rounded bg-slate-100 px-2 py-1 text-xs font-medium text-slate-900 hover:bg-slate-200 disabled:opacity-50 dark:bg-slate-800 dark:text-slate-100 dark:hover:bg-slate-700">
            Resolve
          </button>
        )}
      </div>
    </li>
  );
}
