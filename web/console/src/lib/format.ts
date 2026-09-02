export function fmtTime(iso?: string): string {
  if (!iso) return "";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime()) || d.getFullYear() < 1971) return "";
  return d.toLocaleString(undefined, { hour12: false });
}

export function fmtInt(n?: number): string {
  return (n ?? 0).toLocaleString();
}

export function truncate(s: string | undefined, n = 120): string {
  if (!s) return "";
  return s.length > n ? `${s.slice(0, n)}…` : s;
}
