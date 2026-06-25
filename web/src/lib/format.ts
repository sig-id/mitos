export function formatRelative(ts?: number | string): string {
  if (!ts) return "";
  const n = typeof ts === "number" ? ts : Date.parse(ts);
  if (!Number.isFinite(n)) return String(ts);
  const diff = Date.now() - n;
  const abs = Math.abs(diff);
  const past = diff >= 0;
  const units: Array<[string, number]> = [
    ["y", 31536000],
    ["d", 86400],
    ["h", 3600],
    ["m", 60],
    ["s", 1]
  ];
  for (const [label, sec] of units) {
    if (abs >= sec * 1000) {
      const v = Math.floor(abs / (sec * 1000));
      return past ? `${v}${label} ago` : `in ${v}${label}`;
    }
  }
  return "just now";
}

export function formatTime(ts?: number | string): string {
  if (!ts) return "";
  const d = new Date(typeof ts === "number" ? ts : Date.parse(ts));
  if (Number.isNaN(d.getTime())) return String(ts);
  return d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
}

export function compactId(value?: string): string {
  if (!value) return "n/a";
  return value.length > 22 ? `${value.slice(0, 10)}…${value.slice(-6)}` : value;
}

export function humanize(value?: string): string {
  return (value || "")
    .replace(/[-_]/g, " ")
    .replace(/\b\w/g, (c) => c.toUpperCase());
}
