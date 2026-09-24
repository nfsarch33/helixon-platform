export function fmtTime(iso?: string): string {
  if (!iso) return "";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime()) || d.getFullYear() < 1971) return "";
  // The zone is part of the value: an operator in one zone reading a server in another could not tell which was meant.
  return d.toLocaleString(undefined, {
    year: "numeric", month: "short", day: "2-digit",
    hour: "2-digit", minute: "2-digit", second: "2-digit",
    hour12: false, timeZoneName: "short",
  });
}

export function fmtInt(n?: number): string {
  return (n ?? 0).toLocaleString();
}

export function truncate(s: string | undefined, n = 120): string {
  if (!s) return "";
  return s.length > n ? `${s.slice(0, n)}…` : s;
}

// prettyJSON indents tool arguments and JSON turns for reading. Anything that is not a JSON object or array comes
// back unchanged, so prose is never reformatted.
export function prettyJSON(s?: string): string {
  if (!s) return "";
  const t = s.trim();
  if (!(t.startsWith("{") || t.startsWith("["))) return s;
  try {
    return JSON.stringify(JSON.parse(t), null, 2);
  } catch {
    return s;
  }
}
