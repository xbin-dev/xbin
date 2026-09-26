// model/auto-triggers.js — event triggers on the Automations page (D87), the
// state and the actions: an automation that starts work when something
// happens — an event on a bus this agent may read, or a push from a tile
// bound to it (the webhooks tile). Its detail (recent events), the form, test
// fire, and pushes no trigger took yet. auto-triggers.js draws it on the web.
import { selfApi as api, jbody } from '/vendor/bx-kit.js';
import { registerKind } from './auto.js';

// st: the open trigger's recent events, the pushes nobody took, the channel
// sessions a trigger may announce to, what the last action said, and the
// trigger being made or edited (form; null when none).
export const st = { id: 0, events: [], unmatched: [], sessions: [], note: '', form: null };

export const self = () => globalThis.xbin?.self || 'apps/agent';

export async function open(id) {
  if (st.id !== id) Object.assign(st, { id, events: [], note: '' });
  st.events = await api(`/triggers/${id}/events`).then((r) => r.events || []).catch(() => []);
}

// load: pushes nobody took (managers only — the route says no to others).
export async function load(page) {
  st.unmatched = await api('/triggers/unmatched').then((r) => r.items || []).catch(() => []);
  if (st.form) await loadSessions(page);
}

// the channel sessions you own — where a trigger may announce its answers
export async function loadSessions(page) {
  const mine = page.items.filter((i) => i.kind === 'channel' && i.access === 'owner');
  const lists = await Promise.all(mine.map((c) => api(`/channels/${c.id}/sessions`).then((r) => (r.sessions || [])
    .map((s) => ({ key: s.key, label: `${c.name} · ${s.key.split(':').slice(2).join(':')}` }))).catch(() => [])));
  st.sessions = lists.flat();
}

// startForm opens the form for a trigger (it; null: a new one, from preset).
// custom is what the page shows instead of its list (page.custom — the web
// passes its form template).
export function startForm(page, it, preset = {}, custom = null) {
  const c = (it && it.config) || {};
  st.form = {
    id: it ? it.id : 0, name: c.name || preset.name || '', source: c.source || preset.source || 'push',
    sourceRef: c.sourceRef || preset.sourceRef || '', match: c.match ?? preset.match ?? '', goal: c.goal || '',
    mode: c.mode || 'isolated', targetRun: c.targetRun || 0, toolset: c.toolset || 'private', dataClass: c.dataClass || 'private',
    deliver: c.deliver || '', maxPerHour: c.maxPerHour || 30, visibility: c.visibility || 'private', system: c.system || '',
  };
  page.custom = custom;
  page.err = '';
  page.changed();
  loadSessions(page).then(() => page.changed());
}

export function closeForm(page) {
  st.form = null;
  page.custom = null;
  page.changed();
}

// firewall: the web lane and announcing to a chat both reach outside the
// workspace, so such a trigger must take public data only; a bus event is
// always private. clash: the form cannot be saved as it is.
export function firewall(f) {
  const outward = f.toolset === 'web' || !!f.deliver;
  if (f.source === 'bus') f.dataClass = 'private';
  return { clash: outward && f.dataClass === 'private' };
}

async function act(page, fn, note = '') {
  page.err = '';
  try {
    const said = await fn();
    st.note = typeof said === 'string' ? said : note;
    await page.load();
  } catch (e) { page.err = e.message; page.changed(); }
}

export async function save(page) {
  const f = st.form;
  const body = { ...f, maxPerHour: +f.maxPerHour || 30, targetRun: +f.targetRun || 0 };
  delete body.id;
  page.err = '';
  try {
    const tr = f.id ? await api(`/triggers/${f.id}`, jbody(body, 'PUT')) : await api('/triggers', jbody(body, 'POST'));
    st.form = null;
    page.custom = null;
    await page.load();
    await page.show('trigger', tr.id);
  } catch (e) { page.err = e.message; page.changed(); }
}

export const toggle = (it, page) => act(page, () => api(`/triggers/${it.id}`, jbody({ enabled: !it.enabled }, 'PUT')));
export const test = (it, page) => act(page, async () => {
  const v = await api(`/triggers/${it.id}/test`, jbody({ text: 'a test event from the Automations page' }, 'POST'));
  return v.accepted ? 'Fired a test event — its run is below.' : `The test event was refused: ${v.reason}.`;
});
export const reset = (it, page) => act(page, () => api(`/automations/trigger/${it.id}/reset`, { method: 'POST' }), 'Its next event starts a new thread.');
// del removes a trigger (its runs stay). The view confirms first.
export async function del(it, page) {
  await act(page, () => api(`/triggers/${it.id}`, { method: 'DELETE' }));
  page.show(null);
}

// triggerCan: its owner tests, edits and restarts it; a manager overseeing it
// may switch it off or delete it (and sees only its summary).
export function triggerCan(it) {
  const mine = it.access === 'owner';
  const oversee = it.access === 'oversee';
  return { mine, oversee, test: mine, edit: mine, toggle: mine || oversee, reset: mine && it.mode === 'persistent', del: mine || oversee };
}

// status: what its last run says it needs — 'grant' (a bus it may not read
// yet), 'error', or ''.
export function status(it) {
  const s = it.lastStatus || '';
  return s.startsWith('needs-grant') ? 'grant' : s.startsWith('error') ? 'error' : '';
}

// wiring: what the trigger still needs from outside the agent — 'grant' (add
// a uses entry and approve it), 'push' (bind the pushing tile to this agent), ''.
export function wiring(it) {
  const c = it.config || {};
  if (c.source === 'bus' && (it.lastStatus || '').startsWith('needs-grant')) return 'grant';
  if (c.source === 'push') return 'push';
  return '';
}

// Why an event did not run (GET /triggers/{id}/events items[].reason).
export const REASONS = {
  'data-class': 'private data, but this trigger takes public data only', halted: 'the agent was paused',
  rate: 'over its hourly cap', disabled: 'switched off', 'target-gone': 'its conversation is gone',
};
export const MODES = { isolated: 'a new run for each event', persistent: 'one ongoing thread', conversation: 'into a conversation' };

registerKind('trigger', {
  label: 'Triggers', order: 3, open, load, create: { label: 'New trigger' },
  empty: 'none — a trigger starts work when a bus event or a push from a bound tile arrives',
});
