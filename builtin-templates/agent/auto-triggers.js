// auto-triggers.js — event triggers on the Automations page (D87): an
// automation that starts work when something happens — an event on a bus
// this agent may read, or a push from a tile bound to it (the webhooks tile).
// Its card and detail (what it takes, where each event goes, whether it is
// wired up — the grant or binding it still needs — and its recent events),
// the form, test fire, and pushes no trigger took yet ("create one").
import { html, nothing } from '/vendor/lit-all.min.js';
import { selfApi as api, jbody } from '/vendor/bx-kit.js';
import { registerKind, ago } from './automations.js';

const self = () => window.xbin?.self || 'apps/agent';
const st = { id: 0, events: [], unmatched: [], sessions: [], note: '' };
let form = null; // the trigger being made or edited

async function open(id) {
  if (st.id !== id) Object.assign(st, { id, events: [], note: '' });
  st.events = await api(`/triggers/${id}/events`).then((r) => r.events || []).catch(() => []);
}

// load: pushes nobody took (managers only — the route says no to others).
async function load(page) {
  st.unmatched = await api('/triggers/unmatched').then((r) => r.items || []).catch(() => []);
  if (form) await loadSessions(page);
}

// the channel sessions you own — where a trigger may announce its answers
async function loadSessions(page) {
  const mine = page.items.filter((i) => i.kind === 'channel' && i.access === 'owner');
  const lists = await Promise.all(mine.map((c) => api(`/channels/${c.id}/sessions`).then((r) => (r.sessions || [])
    .map((s) => ({ key: s.key, label: `${c.name} · ${s.key.split(':').slice(2).join(':')}` }))).catch(() => [])));
  st.sessions = lists.flat();
}

function startForm(page, it, preset = {}) {
  const c = (it && it.config) || {};
  form = {
    id: it ? it.id : 0, name: c.name || preset.name || '', source: c.source || preset.source || 'push',
    sourceRef: c.sourceRef || preset.sourceRef || '', match: c.match ?? preset.match ?? '', goal: c.goal || '',
    mode: c.mode || 'isolated', targetRun: c.targetRun || 0, toolset: c.toolset || 'private', dataClass: c.dataClass || 'private',
    deliver: c.deliver || '', maxPerHour: c.maxPerHour || 30, visibility: c.visibility || 'private', system: c.system || '',
  };
  page.custom = formTpl;
  page.err = '';
  page.changed();
  loadSessions(page).then(() => page.changed());
}

async function act(page, fn, note = '') {
  page.err = '';
  try {
    const said = await fn();
    st.note = typeof said === 'string' ? said : note;
    await page.load();
  } catch (e) { page.err = e.message; page.changed(); }
}

async function save(page) {
  const f = form;
  const body = { ...f, maxPerHour: +f.maxPerHour || 30, targetRun: +f.targetRun || 0 };
  delete body.id;
  page.err = '';
  try {
    const tr = f.id ? await api(`/triggers/${f.id}`, jbody(body, 'PUT')) : await api('/triggers', jbody(body, 'POST'));
    form = null;
    page.custom = null;
    await page.load();
    await page.show('trigger', tr.id);
  } catch (e) { page.err = e.message; page.changed(); }
}

const toggle = (it, page) => act(page, () => api(`/triggers/${it.id}`, jbody({ enabled: !it.enabled }, 'PUT')));
const test = (it, page) => act(page, async () => {
  const v = await api(`/triggers/${it.id}/test`, jbody({ text: 'a test event from the Automations page' }, 'POST'));
  return v.accepted ? 'Fired a test event — its run is below.' : `The test event was refused: ${v.reason}.`;
});
const reset = (it, page) => act(page, () => api(`/automations/trigger/${it.id}/reset`, { method: 'POST' }), 'Its next event starts a new thread.');
async function del(it, page) {
  if (!confirm(`Delete "${it.name}"? Its runs stay.`)) return;
  await act(page, () => api(`/triggers/${it.id}`, { method: 'DELETE' }));
  page.show(null);
}

// --- views ------------------------------------------------------------------------

const REASONS = {
  'data-class': 'private data, but this trigger takes public data only', halted: 'the agent was paused',
  rate: 'over its hourly cap', disabled: 'switched off', 'target-gone': 'its conversation is gone',
};
const MODES = { isolated: 'a new run for each event', persistent: 'one ongoing thread', conversation: 'into a conversation' };

function statusBadge(it) {
  const s = it.lastStatus || '';
  if (s.startsWith('needs-grant')) return html`<span class="badge error" title=${s}>needs a grant</span>`;
  if (s.startsWith('error')) return html`<span class="badge error" title=${s}>error</span>`;
  return nothing;
}

function card(p, it) {
  return html`<div class="acard2 ${it.enabled ? '' : 'off'}" data-auto=${'trigger:' + it.id} @click=${() => p.show('trigger', it.id)}>
    <div class="ah"><span class="nm">${it.name}</span>${statusBadge(it)}
      ${it.unread ? html`<span class="badge unread">${it.unread} new</span>` : nothing}
      ${it.enabled ? nothing : html`<span class="badge">off</span>`}
      <span style="flex:1"></span>
      ${it.access === 'oversee' ? html`<span class="muted small">${it.owner}'s</span>` : it.access !== 'owner' && it.owner ? html`<span class="muted small">by ${it.owner}</span>` : nothing}
    </div>
    <div class="as muted small">when ${it.summary}${it.lastRunAt ? ` · last ${ago(it.lastRunAt)}` : ''}${it.runs ? ` · ${it.runs} run${it.runs === 1 ? '' : 's'}` : ''}</div>
  </div>`;
}

function head(it, p) {
  const mine = it.access === 'owner';
  return html`${mine ? html`<button class="btn ghost btnsm" @click=${() => test(it, p)}>Test</button>` : nothing}
    ${mine || it.access === 'oversee' ? html`<label class="chk small"><input type="checkbox" .checked=${it.enabled} @change=${() => toggle(it, p)}> on</label>` : nothing}
    ${mine ? html`<button class="btn ghost btnsm" @click=${() => startForm(p, it)}>Edit</button>` : nothing}
    ${mine && it.mode === 'persistent' ? html`<button class="btn ghost btnsm" @click=${() => reset(it, p)}>Start afresh</button>` : nothing}
    ${mine || it.access === 'oversee' ? html`<button class="btn rm btnsm" @click=${() => del(it, p)}>Delete</button>` : nothing}`;
}

// wiring says what the trigger still needs from outside the agent.
function wiring(it) {
  const c = it.config || {};
  if (c.source === 'bus' && (it.lastStatus || '').startsWith('needs-grant')) {
    return html`<div class="note small">This agent may not read <code>${c.sourceRef}</code> yet. Add to its <code>xbin.json</code>
      <code>uses</code>: <code>{ "target": "${c.sourceRef}", "role": "reader" }</code>, and approve it (the grants panel, or
      <code>bx grant</code>). <span class="muted">${it.lastStatus}</span></div>`;
  }
  if (c.source === 'push') {
    return html`<div class="muted small">Pushes come from <code>${c.sourceRef}</code> once it is bound to this agent:
      <code>bx bind ${c.sourceRef} agents=${self()}</code>.</div>`;
  }
  return nothing;
}

function detail(it, p) {
  const c = it.config || {};
  if (it.access === 'oversee') return html`<div class="muted small">${it.summary}</div>`;
  return html`<div class="muted small">when ${it.summary} · ${c.toolset === 'web' ? 'web lane' : 'internal lane'} · takes ${c.dataClass} data
      ${c.deliver ? html` · announces to <code>${c.deliver}</code>` : nothing}
      ${c.mode === 'conversation' && c.targetRun ? html` · <a @click=${() => p.on.select(c.targetRun)}>its conversation</a>` : nothing}</div>
    ${st.note ? html`<div class="note small said">${st.note}</div>` : nothing}
    ${wiring(it)}
    <div class="agoal">${c.goal}</div>
    <h5>Recent events</h5>
    ${st.events.length ? st.events.map((e) => html`<div class="chrow">
        <span class="muted small">${ago(e.at)}</span><span class="small mono">${e.topic || e.eventId}</span><span style="flex:1"></span>
        ${e.accepted ? (e.runId ? html`<a class="small" @click=${() => p.on.select(e.runId)}>ran #${e.runId}</a>` : html`<span class="small">ran</span>`)
          : html`<span class="small muted">${REASONS[e.reason] || e.reason || 'seen'}</span>`}</div>`)
      : html`<div class="muted small empty-line">none yet</div>`}`;
}

// pushes nobody took: make a trigger from one
function listExtra(p) {
  if (!st.unmatched.length) return nothing;
  return html`${st.unmatched.map((u) => html`<div class="chrow small">
    <span><code>${u.from}</code> sent <b>${u.name}</b>${u.count > 1 ? ` ×${u.count}` : ''} — nothing took it</span><span style="flex:1"></span>
    <button class="btn ghost btnsm" @click=${() => startForm(p, null, { name: u.name, source: 'push', sourceRef: u.from, match: u.name })}>Create a trigger</button>
  </div>`)}`;
}

function formTpl(p) {
  const f = form;
  const set = (k) => (e) => { f[k] = e.target.type === 'checkbox' ? e.target.checked : e.target.value; p.changed(); };
  const opt = (k, v, label, dis = false) => html`<option value=${v} ?selected=${f[k] === v} ?disabled=${dis}>${label}</option>`;
  const outward = f.toolset === 'web' || !!f.deliver;
  if (f.source === 'bus') f.dataClass = 'private';
  const clash = outward && f.dataClass === 'private';
  return html`<div class="autos-page">
    <div class="ahd"><a class="crumb" @click=${() => { form = null; p.custom = null; p.changed(); }}>Automations</a> ›
      <b>${f.id ? 'Edit trigger' : 'New trigger'}</b></div>
    ${p.err ? html`<div class="err">${p.err}</div>` : nothing}
    <div class="row2">
      <div class="field"><label>Name</label><input .value=${f.name} @input=${set('name')} placeholder="deploys"></div>
      <div class="field"><label>When</label><select @change=${set('source')}>
        ${opt('source', 'push', 'a tile bound to this agent pushes (e.g. webhooks)')}${opt('source', 'bus', 'an event appears on a bus')}</select></div>
    </div>
    <div class="row2">
      <div class="field"><label>${f.source === 'bus' ? 'The bus' : 'The tile'}</label>
        <input class="mono" .value=${f.sourceRef} @input=${set('sourceRef')} placeholder=${f.source === 'bus' ? 'res:apps/calendar/bus' : 'apps/webhooks'}></div>
      <div class="field"><label>Topics starting with (empty: all)</label><input class="mono" .value=${f.match} @input=${set('match')} placeholder="deploy"></div>
    </div>
    <div class="field"><label>What to do with each event</label>
      <textarea rows="3" .value=${f.goal} @input=${set('goal')} placeholder="Check the {{topic}} deploy and summarise what changed"></textarea>
      <div class="muted small">{{topic}} and {{text}} are the event's; its data comes after, marked as data.</div></div>
    <div class="row2">
      <div class="field"><label>Where each event goes</label><select @change=${set('mode')}>
        ${opt('mode', 'isolated', MODES.isolated)}${opt('mode', 'persistent', MODES.persistent)}
        ${f.mode === 'conversation' ? opt('mode', 'conversation', MODES.conversation) : nothing}</select></div>
      <div class="field"><label>At most, per hour</label><input type="number" min="1" .value=${String(f.maxPerHour)} @input=${set('maxPerHour')}></div>
    </div>
    <div class="row2">
      <div class="field"><label>Tool mode</label><select @change=${set('toolset')}>
        ${opt('toolset', 'private', 'internal systems, no web')}${opt('toolset', 'web', 'web, no internal systems')}</select></div>
      <div class="field"><label>The data it takes</label><select @change=${set('dataClass')} ?disabled=${f.source === 'bus'}>
        ${opt('dataClass', 'private', 'private (from inside the workspace)')}${opt('dataClass', 'public', 'public (e.g. webhooks from outside)', f.source === 'bus')}</select></div>
    </div>
    <div class="row2">
      <div class="field"><label>Announce its answers to</label><select @change=${set('deliver')}>
        <option value="" ?selected=${!f.deliver}>nobody — read them here</option>
        ${st.sessions.map((s) => html`<option value=${s.key} ?selected=${f.deliver === s.key}>${s.label}</option>`)}
        ${f.deliver && !st.sessions.some((s) => s.key === f.deliver) ? html`<option value=${f.deliver} selected>${f.deliver}</option>` : nothing}</select></div>
      <div class="field"><label>Who can see its runs</label><select @change=${set('visibility')}>
        ${opt('visibility', 'private', 'only you')}${opt('visibility', 'team', 'everyone who can open this agent')}</select></div>
    </div>
    ${clash ? html`<div class="err small">The web lane and announcing to a chat both reach outside the workspace, so this trigger
      must take public data only — or use internal systems and read its answers here.</div>` : nothing}
    <div><button class="btn" ?disabled=${clash} @click=${() => save(p)}>${f.id ? 'Save' : 'Create'}</button></div>
  </div>`;
}

registerKind('trigger', {
  label: 'Triggers', order: 3, card, head, detail, open, load, listExtra,
  create: { label: 'New trigger', start: (p) => startForm(p, null) },
  empty: 'none — a trigger starts work when a bus event or a push from a bound tile arrives',
});
