// model/project-pins.js — the projects the conversation list shows at its
// top (D209), for both views, out of model/projects.js (its size budget):
// the list's order, the needs-you count, the pinned rows and their words,
// and the section's fold, kept per person. app.projects calls these with
// itself (pinned(), readFold(), setFolded()); model/projects.js re-exports
// pinWords and PINNED, where the views import them from.

export const byActivity = (a, b) => (b.updatedMs || 0) - (a.updatedMs || 0) || (b.id || 0) - (a.id || 0);
export const needOf = (p) => (p && p.state === 'active' && p.counts && p.counts['needs-you']) || 0;

/** PINNED: how many projects the conversation list shows at its top (D209). */
export const PINNED = 5;

const UNDER_WAY = [['working', 'working'], ['queued', 'queued'], ['pr', 'in PR']];
/** pinWords(view): a project as a row atop the conversations — {name, need (tasks that
 * need you), detail (what is under way: "2 working · 1 in PR"), repos, team (a team project)}. */
export function pinWords(pv) {
  const counts = (pv && pv.counts) || {};
  return {
    name: (pv && pv.name) || `#${pv && pv.id}`,
    need: needOf(pv),
    detail: UNDER_WAY.filter(([k]) => counts[k]).map(([k, w]) => `${counts[k]} ${w}`).join(' · '),
    repos: ((pv && pv.repos) || []).map((r) => r.repo).join(', '),
    team: !!pv && pv.kind !== 'personal' && !!pv.kind,
  };
}

// pinnedOf(projects, n): the projects the conversation list shows at its
// top, above its pinned conversations — your active ones, those with tasks
// that need you first, then by activity, at most n; `total` counts every
// project the page lists (All projects), `need` the tasks that need you;
// `none`: you have no project yet — the views draw one quiet row to the
// page (where New project is) instead of the section. A team project's
// definition is left out while your half of it is there: the half leads to it.
export function pinnedOf(pj, n = PINNED) {
  const live = pj.list.filter((p) => p.state === 'active');
  const halves = new Set(live.filter((p) => p.kind === 'membership' && p.teamRef).map((p) => +p.teamRef));
  const rows = live.filter((p) => !(p.kind === 'team' && halves.has(p.id)));
  const items = [...rows].sort((a, b) => (needOf(b) > 0) - (needOf(a) > 0) || byActivity(a, b)).slice(0, n);
  return { items, total: pj.list.length, need: pj.needsYou(), shown: pj.supported && pj.loaded, none: !pj.list.length };
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
