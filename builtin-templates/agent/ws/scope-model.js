// ws/scope-model.js — what a session is connected to, as the scope panel
// shows it (docs/workspace-agent.md §The agent's page; xbind's full view:
// GET /api/xbin/agents/connection?session=, /docs/protocol/agents.md §The
// connection view): orgs unfolding to tiles, each connected, hidden by the
// person, blocked by a rule (with the org's sentence) or out of reach; the
// person's ticks staged until Apply; what a scope of ticks is on the wire
// (PATCH /ws/sessions/{id} {scope}); a model change's what-if in words.
// Scope is focus, not permission: a blocked tile can't be ticked, and
// nothing here pretends otherwise. Pure: node-tested.

/** STATES: a tile's or an org's state as a glyph and a word. */
export const STATES = Object.freeze({
  connected: { icon: 'check', word: 'connected', tone: 'ok' },
  hidden: { icon: 'eye-slash', word: 'hidden by you', tone: '' },
  blocked: { icon: 'lock', word: 'blocked', tone: 'warn' },
  'no-access': { icon: '', word: 'no access', tone: '' },
  partial: { icon: '', word: '', tone: '' },
});

const NO_ORG = 'workspace';
/** tickable(t): the person may tick it — connected or hidden by them; a blocked tile stays blocked whatever is ticked. */
export const tickable = (t) => t.state === 'connected' || t.state === 'hidden';

/** initialTicks(view): the tiles connected now. */
export const initialTicks = (view) => new Set(((view && view.tiles) || []).filter((t) => t.state === 'connected').map((t) => t.tile));
/** allAllowed(view): every tile that may be ticked ("Select all allowed"). */
export const allAllowed = (view) => new Set(((view && view.tiles) || []).filter(tickable).map((t) => t.tile));

/**
 * tree(view, ticks, filter): the panel's rows — the orgs in xbind's order,
 * the tiles in no org last ("Workspace — tiles in no org"), each with its
 * tiles, how many are ticked of how many, its box (on | off | mixed |
 * disabled), its state word when they all share one, and its reason once.
 * filter: all | connected | not.
 */
export function tree(view, ticks, filter = 'all') {
  const tiles = (view && view.tiles) || [];
  const orgs = ((view && view.orgs) || []).slice();
  const byOrg = new Map();
  for (const t of tiles) {
    const o = t.org || NO_ORG;
    if (!byOrg.has(o)) byOrg.set(o, []);
    byOrg.get(o).push(t);
  }
  for (const id of byOrg.keys()) if (!orgs.some((o) => o.id === id)) orgs.push({ id, name: id === NO_ORG ? 'Workspace' : id, kind: id === NO_ORG ? 'workspace' : 'org' });
  orgs.sort((a, b) => (a.kind === 'workspace') - (b.kind === 'workspace'));
  const show = (t) => filter === 'all' || (filter === 'connected' ? ticks.has(t.tile) && tickable(t) : !(ticks.has(t.tile) && tickable(t)));
  const rows = [];
  for (const o of orgs) {
    const all = byOrg.get(o.id) || [];
    const list = all.filter(show);
    if (!list.length && filter !== 'all') continue;
    const can = all.filter(tickable);
    const on = can.filter((t) => ticks.has(t.tile)).length;
    const states = new Set(all.map((t) => t.state));
    const one = states.size === 1 ? [...states][0] : '';
    rows.push({
      id: o.id, name: o.name || o.id, kind: o.kind || 'org', reason: o.reason || (all.find((t) => t.state === 'blocked' && t.reason) || {}).reason || '',
      pinned: !!o.pinned, rule: o.rule || '', state: one, of: all.length, ticked: on,
      box: !can.length ? 'disabled' : on === 0 ? 'off' : on === can.length ? 'on' : 'mixed',
      tiles: list.map((t) => ({ tile: t.tile, name: t.name || t.tile, state: t.state, reason: t.reason || '', via: t.via || '', by: t.by || '',
        tickable: tickable(t), ticked: ticks.has(t.tile) && tickable(t) })),
    });
  }
  return rows;
}

/** toggleTile(ticks, view, path) / toggleOrg(ticks, view, org): the staged ticks after a click (a new Set). An org's box takes all its allowed tiles, or none. */
export function toggleTile(ticks, view, path) {
  const t = ((view && view.tiles) || []).find((x) => x.tile === path);
  const out = new Set(ticks);
  if (!t || !tickable(t)) return out;
  if (out.has(path)) out.delete(path); else out.add(path);
  return out;
}
export function toggleOrg(ticks, view, org) {
  const can = ((view && view.tiles) || []).filter((t) => (t.org || NO_ORG) === org && tickable(t));
  const out = new Set(ticks);
  const allOn = can.length && can.every((t) => out.has(t.tile));
  for (const t of can) { if (allOn) out.delete(t.tile); else out.add(t.tile); }
  return out;
}

/**
 * scopeOf(view, ticks): the scope the ticks are — null when every allowed
 * tile is ticked (everything the rules allow: tiles added later join
 * too), else {orgs, tiles}: an org whose allowed tiles are all ticked by
 * its id (later tiles of it join), the rest a tile at a time.
 */
export function scopeOf(view, ticks) {
  const tiles = ((view && view.tiles) || []).filter(tickable);
  if (tiles.every((t) => ticks.has(t.tile))) return null;
  const orgs = [], one = [];
  const groups = new Map();
  for (const t of tiles) { const o = t.org || NO_ORG; if (!groups.has(o)) groups.set(o, []); groups.get(o).push(t); }
  for (const [o, list] of groups) {
    if (list.every((t) => ticks.has(t.tile))) orgs.push(o);
    else for (const t of list) if (ticks.has(t.tile)) one.push(t.tile);
  }
  return { orgs, tiles: one };
}

/** staged(view, ticks): the ticks differ from what is connected now — what Apply would change, as {connect, disconnect} counts. */
export function staged(view, ticks) {
  const now = initialTicks(view);
  let connect = 0, disconnect = 0;
  for (const t of ((view && view.tiles) || []).filter(tickable)) {
    if (ticks.has(t.tile) && !now.has(t.tile)) connect++;
    if (!ticks.has(t.tile) && now.has(t.tile)) disconnect++;
  }
  return { connect, disconnect, any: connect + disconnect > 0 };
}

/**
 * whatIf(backend, full, current): a model change before anything changes —
 * the backend's answer (GET /ws/sessions/{id}/connection?whatIf=: counts,
 * allowed, taintedOrgs) and xbind's (the tiles by name: whatIf.connect,
 * whatIf.disconnect with their reasons) — as {model, allowed, connects:
 * [name], disconnects: [{name, reason}], summary, button, refused}.
 */
export function whatIf(backend, full, current = '') {
  const b = (backend && backend.whatIf) || {};
  const f = (full && full.whatIf) || {};
  const name = (t) => t.name || t.tile || String(t);
  const connects = (f.connect || []).map(name);
  const disconnects = (f.disconnect || []).map((t) => ({ name: name(t), reason: t.reason || '' }));
  const nc = connects.length || b.connects || 0, nd = disconnects.length || b.disconnects || 0;
  const model = b.model || (f.model && (f.model.upstream ? `${f.model.upstream}/${f.model.model}` : f.model.model)) || '';
  const allowed = backend ? b.allowed !== false : true;
  return {
    model, allowed, private: !!(f.model && f.model.private), connects, disconnects,
    connectsWords: nc ? `Connects ${nc} tile${nc === 1 ? '' : 's'}` : 'Connects none',
    disconnectsWords: nd ? `Disconnects ${nd}` : 'Disconnects none',
    summary: b.summary || (b.counts && b.counts.summary) || (f.counts && f.counts.summary) || '',
    button: nd ? `Switch and disconnect ${nd}` : 'Switch model',
    refused: allowed ? null : refusedWords({ model, orgs: b.taintedOrgs || [], current }),
  };
}

/**
 * heldWords(held): a session that can't go on on its model (GET
 * /ws/sessions → held: xbind won't let a new handle on it hold what the
 * conversation holds) and its ways forward: back to the model it was on,
 * where its model now names another, or a new session, which starts empty.
 */
export function heldWords(h) {
  const m = short(h.model);
  const who = (h.orgs || []).length ? h.orgs.join(', ') : 'an org';
  const back = h.offer && h.offer.model;
  return {
    bold: `This session can’t go on on ${m}.`,
    text: `It holds data from ${who}${h.reason ? `, and ${h.reason}` : ', whose rules no longer allow that model'}. Nothing is read, changed or sent to a model in it until it moves.`,
    back: back ? { label: `Move back to ${short(back)}`, model: back } : null,
    fresh: { label: `New session on ${m}`, model: (h.offer && h.offer.newSession && h.offer.newSession.model) || h.model },
  };
}
const short = (model) => String(model || '').replace(/^.*\//, '');

/**
 * refusedWords({model, orgs, reason, current}): a model change that is
 * refused — the conversation holds data from an org whose rules the new
 * model doesn't meet (409 model_taint, or a what-if that isn't allowed) —
 * and its way forward: a new session on that model, which starts empty.
 */
export function refusedWords({ model, orgs = [], reason = '', current = '' }) {
  const m = String(model || '').replace(/^.*\//, '');
  const who = orgs.length ? orgs.join(', ') : 'an org';
  return {
    title: `This session can’t move to ${m}`,
    body: `This conversation already holds data from ${who}${reason ? `, and ${reason}` : ', whose rules don’t allow that model'}. So the conversation can’t be sent to ${m}.`,
    after: `A new session on ${m} starts empty. It won’t carry anything from this one${orgs.length ? `, and ${who}’s tiles won’t be connected to it` : ''}.`,
    start: `Start a new session on ${m}`,
    stay: current ? `Stay on ${String(current).replace(/^.*\//, '')}` : 'Stay on this model',
    model,
  };
}
