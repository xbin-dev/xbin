// fmt.js — the egress approver's formatters, shared by its web page
// (index.html) and its native view (native.js) so both show the same text.

// fmtBytes(1536) → "1.5K": binary units, one decimal past bytes, "0" for none.
export const fmtBytes = (n) => {
  if (!n) return '0';
  const u = ['B', 'K', 'M', 'G', 'T']; let i = 0;
  while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
  return (i ? n.toFixed(1) : n) + u[i];
};

// ago(ms) → "38s ago" / "2m ago" / "3h ago" / "1d ago" (floored); "—" for never.
export const ago = (ms) => {
  if (!ms) return '—';
  const s = Math.max(0, (Date.now() - ms) / 1000);
  if (s < 60) return `${s | 0}s ago`;
  if (s < 3600) return `${s / 60 | 0}m ago`;
  if (s < 86400) return `${s / 3600 | 0}h ago`;
  return `${s / 86400 | 0}d ago`;
};

// norm(state) fills in the lists a fresh backend may omit.
export const norm = (d) => { d.pending ??= []; d.approved ??= []; d.denied ??= []; return d; };
