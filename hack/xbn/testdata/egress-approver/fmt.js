// Stand-in for builtin-tiles/egress-approver/fmt.js (not extracted yet): the
// page's fmtBytes/ago, verbatim, so the native.js example of the design
// renders what the page shows.
export const fmtBytes = (n) => {
  if (!n) return '0';
  const u = ['B', 'K', 'M', 'G', 'T']; let i = 0;
  while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
  return (i ? n.toFixed(1) : n) + u[i];
};
export const ago = (ms) => {
  if (!ms) return '—';
  const s = Math.max(0, (Date.now() - ms) / 1000);
  if (s < 60) return `${s | 0}s ago`;
  if (s < 3600) return `${s / 60 | 0}m ago`;
  if (s < 86400) return `${s / 3600 | 0}h ago`;
  return `${s / 86400 | 0}d ago`;
};
