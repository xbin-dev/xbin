// model/project-pins.js — the projects the conversation list shows at its
// top (D209), for both views, out of model/projects.js (its size budget):
// the list's order, the needs-you count — split: what really waits on you
// (red) and the pull requests ready for review (#56) —, the pinned rows and their words,
// and the section's fold, kept per person. app.projects calls these with
// itself (pinned(), readFold(), setFolded()); model/projects.js re-exports
// pinWords and PINNED, where the views import them from.

export const byActivity = (a, b) => (b.updatedMs || 0) - (a.updatedMs || 0) || (b.id || 0) - (a.id || 0);

/** needSplit(pv): a project's count in the conversation list, split (#56) — asks (red: a question, an
 * approval or a sign-in holds a task up, a task failed, or your coordinator asks you something; never a
 * task whose turn just ended with its answer ready) and ready (its pull requests that passed CI). A
 * backend from before the split (no counts.asks) has its needs-you column count as asks. */
export function needSplit(pv) {
  const c = (pv && pv.state === 'active' && pv.counts) || {};
  const split = c.asks != null;
  const asks = split ? (c.asks || 0) + (c.failed || 0) + (c.coord || 0) : (c['needs-you'] || 0);
  const ready = c.ready || 0;
  const tip = !split ? 'tasks that need you'
    : [c.asks ? `${c.asks} waiting for your answer, approval or sign-in` : '', c.failed ? `${c.failed} failed` : '',
      c.coord ? 'your coordinator asks you something' : ''].filter(Boolean).join(' · ');
  return { asks, ready, total: asks + ready, split, tip };
}
// (a task whose PR passed CI — counts.ready, in the PR column — waits for you too)
export const needOf = (p) => needSplit(p).total;

/** PINNED: how many projects the conversation list shows at its top (D209). */
export const PINNED = 5;

const UNDER_WAY = [['working', 'working'], ['queued', 'queued'], ['pr', 'in PR']];
/** pinWords(view): a project as a row atop the conversations — {name, need (all that waits for you),
 * asks and ready (needSplit: the red count, the review one; split false: an older backend's needs-you
 * count, drawn as before), tip (the red one's words), detail (what is
 * under way: "2 working · 1 in PR"), repos, team (a team project)}. */
export function pinWords(pv) {
  const counts = (pv && pv.counts) || {};
  const s = needSplit(pv);
  return {
    name: (pv && pv.name) || `#${pv && pv.id}`,
    need: s.total, asks: s.asks, ready: s.ready, tip: s.tip, split: s.split,
    detail: UNDER_WAY.filter(([k]) => counts[k]).map(([k, w]) => `${counts[k]} ${w}`).join(' · '),
    repos: ((pv && pv.repos) || []).map((r) => r.repo).join(', '),
    team: !!pv && pv.kind !== 'personal' && !!pv.kind,
  };
}

// pinnedOf(projects, n): the projects the conversation list shows at its
// top, above its pinned conversations — your active ones, those with tasks
// that need you first, then by activity, at most n; `more` the active ones
// past n (All projects says so, D212); `total` counts every project the
// page lists (All projects), `need` the tasks that need you;
// `none`: you have no project yet — the views draw one quiet row to the
// page (where New project is) instead of the section. A team project's
// definition is left out while your half of it is there: the half leads to it.
export function pinnedOf(pj, n = PINNED) {
  const live = pj.list.filter((p) => p.state === 'active');
  const halves = new Set(live.filter((p) => p.kind === 'membership' && p.teamRef).map((p) => +p.teamRef));
  const rows = live.filter((p) => !(p.kind === 'team' && halves.has(p.id)));
  const items = [...rows].sort((a, b) => (needOf(b) > 0) - (needOf(a) > 0) || byActivity(a, b)).slice(0, n);
  const asks = live.reduce((n, p) => n + needSplit(p).asks, 0); // the folded heading's split (#56)
  return { items, more: rows.length - items.length, total: pj.list.length, need: pj.needsYou(), asks, ready: pj.needsYou() - asks,
    split: live.some((p) => needSplit(p).split), shown: pj.supported && pj.loaded, none: !pj.list.length };
}

// FOLDED: the person's prefs key (xbind's per-person store: a tile frame keeps
// no storage of its own) — the list's Projects section folded.
const FOLDED = '/api/xbin/prefs/projects-folded';

// readFold(projects): the section's fold, read once with the list; a
// refused read leaves it as the page has it, and a fold made meanwhile stands.
export async function readFold(pj) {
  try {
    const r = await globalThis.xbin.fetch(FOLDED);
    const v = r.ok ? await r.json() : null;
    if (r.ok && !pj.foldSet) { pj.folded = v === true; pj.changed(); }
  } catch { /* no prefs: unfolded */ }
}

// writeFold(projects, on): folds or unfolds the section, at once on the
// page, then in the person's prefs (a refused write keeps the page's).
export function writeFold(pj, on) {
  pj.folded = !!on;
  pj.foldSet = true;
  pj.changed();
  Promise.resolve().then(() => globalThis.xbin.fetch(FOLDED, { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(pj.folded) })).catch(() => {});
}
