// web/code-shared.js — what the code panels share (D202): which events mean a
// tile's work tree changed, a unified diff split per file, commit times, and
// the commit-activity summary. <bx-code> and the tile window's panels
// (code-tree.js, code-view.js) read the same rules from here. Pure — no
// imports — so hack/code-panels.test.mjs runs it under node.

// savedEvent(e, src): whether a /ws/events event means src's files changed on
// disk. A save reloads the tile (reload, build-ok) — or, while its live reload
// is paused or attached to another tile deployment, only the tile's
// `deployments` event says so: op work-tree (the changed count moved) or that
// deployment's op reload and op build ok. The work tree changed all the same.
export function savedEvent(e, src) {
  if (!e || !src || !e.component) return false;
  const d = e.type === 'deployments' ? e.data || {} : null;
  const saved = e.type === 'reload' || e.type === 'build-ok' || d?.op === 'work-tree' || d?.op === 'reload' || (d?.op === 'build' && d.phase === 'ok');
  return saved && (e.component === src || e.component.startsWith(src + '/'));
}

// fileDiffs(diff) → [{path, from, status, add, del, text}]: a unified diff
// (git's: `diff --git a/x b/y` per file) cut into one section per file, in
// order. status: 'A' a new file, 'D' a deleted one, 'R' a rename, 'M' else;
// path is the file's name after the change (before it, for a deletion);
// add/del count its +/- content lines (not the ---/+++ headers).
export function fileDiffs(diff) {
  const out = [];
  let cur = null;
  for (const line of (diff || '').split('\n')) {
    if (line.startsWith('diff --git ')) {
      const m = line.match(/^diff --git a\/(.+?) b\/(.+)$/);
      cur = { path: m ? m[2] : line.slice(11), from: m ? m[1] : '', status: 'M', add: 0, del: 0, lines: [line] };
      out.push(cur);
      continue;
    }
    if (!cur) continue;
    cur.lines.push(line);
    if (line.startsWith('new file mode')) cur.status = 'A';
    else if (line.startsWith('deleted file mode')) { cur.status = 'D'; cur.path = cur.from || cur.path; }
    else if (line.startsWith('rename from ')) cur.status = 'R';
    else if (line.startsWith('+++') || line.startsWith('---')) continue;
    else if (line[0] === '+') cur.add++;
    else if (line[0] === '-') cur.del++;
  }
  return out.map(({ lines, ...f }) => {
    while (lines.length > 1 && lines[lines.length - 1] === '') lines.pop();
    return { ...f, text: lines.join('\n') + '\n' };
  });
}

// statusMap(diff) → {path: 'M'|'A'|'D'|'R'}: the working tree's changed files.
export const statusMap = (diff) => Object.fromEntries(fileDiffs(diff).map((f) => [f.path, f.status]));

// relTime(seconds): "5m ago" … "2y ago"; '—' without a time.
export function relTime(ts, now = Date.now() / 1000) {
  if (!ts) return '—';
  const s = Math.floor(now) - ts;
  if (s < 3600) return `${Math.max(1, Math.floor(s / 60))}m ago`;
  if (s < 86400) return `${Math.floor(s / 3600)}h ago`;
  if (s < 2592000) return `${Math.floor(s / 86400)}d ago`;
  if (s < 31536000) return `${Math.floor(s / 2592000)}mo ago`;
  return `${Math.floor(s / 31536000)}y ago`;
}
export const fmtDate = (ts) => (ts ? new Date(ts * 1000).toISOString().slice(0, 10) : '—');

// activitySummary(activity) → null (none, or not a repo) or the numbers the
// Analysis view draws: totals, the top authors, and 52 weekly buckets
// (bucket 0 the oldest week, 51 this one) of the local commits and, when
// the repo tracks one, the upstream's.
export function activitySummary(a, now = Math.floor(Date.now() / 1000)) {
  if (!a || a.repo === false) return null;
  const local = a.local || [];
  const up = a.upstream || null;
  const WEEK = 7 * 86400, WEEKS = 52;
  const bucket = (arr) => {
    const b = new Array(WEEKS).fill(0);
    for (const c of arr) { const wi = Math.floor((now - c.t) / WEEK); if (wi >= 0 && wi < WEEKS) b[WEEKS - 1 - wi]++; }
    return b;
  };
  const authors = {};
  for (const c of local) authors[c.a || '—'] = (authors[c.a || '—'] || 0) + 1;
  const topAuthors = Object.entries(authors).sort((x, y) => y[1] - x[1]).slice(0, 6);
  const first = local.reduce((m, c) => (c.t < m ? c.t : m), Infinity);
  const last = local.reduce((m, c) => (c.t > m ? c.t : m), 0);
  return {
    total: local.length, last30: local.filter((c) => c.t >= now - 30 * 86400).length,
    first: first === Infinity ? 0 : first, last, topAuthors,
    weeksLocal: bucket(local), weeksUp: up ? bucket(up) : null, WEEK, now,
    remote: a.remote || '', upstreamRef: a.upstreamRef || '',
    upstreamTotal: up ? up.length : 0,
    upstreamLast: up ? up.reduce((m, c) => (c.t > m ? c.t : m), 0) : 0,
  };
}

// commitOf(log, rev): a commit of git/log's answer by its hash or a prefix.
export function commitOf(log, rev) {
  if (!rev) return null;
  return (log?.commits || []).find((c) => (c.hash || c.rev || '').startsWith(rev) || (rev.length >= 7 && rev.startsWith(c.short || '\0'))) || null;
}
