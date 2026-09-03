"use client";
import Link from "next/link";
import { usePathname } from "next/navigation";

const items = [
  { href: "/", label: "Overview" },
  { href: "/runs/", label: "Runs" },
  { href: "/evals/", label: "Evals" },
  { href: "/costs/", label: "Costs" },
  { href: "/memory/", label: "Memory" },
];

export function Nav() {
  const path = usePathname() ?? "/";
  return (
    <nav aria-label="Console sections" className="flex flex-wrap gap-1">
      {items.map((it) => {
        const active = it.href === "/" ? path === "/" : path.startsWith(it.href);
        return (
          <Link
            key={it.href}
            href={it.href}
            aria-current={active ? "page" : undefined}
            className={`rounded px-3 py-1.5 text-sm ${active ? "bg-slate-900 text-white dark:bg-white dark:text-slate-900" : "text-slate-700 hover:bg-slate-100 dark:text-slate-200 dark:hover:bg-slate-800"}`}
          >
            {it.label}
          </Link>
        );
      })}
    </nav>
  );
}
