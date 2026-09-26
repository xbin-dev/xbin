// auto-triggers.js — event triggers on the Automations page (D87), as the web
// draws them (the state: model/auto-triggers.js): an
// automation that starts work when something happens — an event on a bus
// this agent may read, or a push from a tile bound to it (the webhooks tile).
// Its card and detail (what it takes, where each event goes, whether it is
// wired up — the grant or binding it still needs — and its recent events),
// the form, test fire, and pushes no trigger took yet ("create one").
import { html, nothing } from '/vendor/lit-all.min.js';
import { extendKind, KINDS, ago } from './model/auto.js';
import { st, self, startForm as openForm, closeForm, firewall, save, toggle, test, reset, del as remove, triggerCan, status,
  wiring as needs, REASONS, MODES } from './model/auto-triggers.js';

// The trigger's state and actions are model/auto-triggers.js (shared with the
// native view); what is drawn here, and the "are you sure?" before a delete.
const startForm = (page, it, preset = {}) => openForm(page, it, preset, formTpl);
async function del(it, page) {
  if (!confirm(`Delete "${it.name}"? Its runs stay.`)) return;
  await remove(it, page);
}

// --- views ------------------------------------------------------------------------

function statusBadge(it) {
  const s = it.lastStatus || '';
  const k = status(it);
  if (k === 'grant') return html`<span class="badge error" title=${s}>needs a grant</span>`;
  if (k === 'error') return html`<span class="badge error" title=${s}>error</span>`;
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
  const can = triggerCan(it);
  return html`${can.test ? html`<button class="btn ghost btnsm" @click=${() => test(it, p)}>Test</button>` : nothing}
    ${can.toggle ? html`<label class="chk small"><input type="checkbox" .checked=${it.enabled} @change=${() => toggle(it, p)}> on</label>` : nothing}
    ${can.edit ? html`<button class="btn ghost btnsm" @click=${() => startForm(p, it)}>Edit</button>` : nothing}
    ${can.reset ? html`<button class="btn ghost btnsm" @click=${() => reset(it, p)}>Start afresh</button>` : nothing}
    ${can.del ? html`<button class="btn rm btnsm" @click=${() => del(it, p)}>Delete</button>` : nothing}`;
}

// wiring says what the trigger still needs from outside the agent.
function wiring(it) {
  const c = it.config || {};
  const w = needs(it);
  if (w === 'grant') {
    return html`<div class="note small">This agent may not read <code>${c.sourceRef}</code> yet. Add to its <code>xbin.json</code>
      <code>uses</code>: <code>{ "target": "${c.sourceRef}", "role": "reader" }</code>, and approve it (the grants panel, or
      <code>bx grant</code>). <span class="muted">${it.lastStatus}</span></div>`;
  }
  if (w === 'push') {
    return html`<div class="muted small">Pushes come from <code>${c.sourceRef}</code> once it is bound to this agent:
      <code>bx bind ${c.sourceRef} agents=${self()}</code>.</div>`;
  }
  return nothing;
}

function detail(it, p) {
  const c = it.config || {};
  if (triggerCan(it).oversee) return html`<div class="muted small">${it.summary}</div>`;
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
  const f = st.form;
  const set = (k) => (e) => { f[k] = e.target.type === 'checkbox' ? e.target.checked : e.target.value; p.changed(); };
  const opt = (k, v, label, dis = false) => html`<option value=${v} ?selected=${f[k] === v} ?disabled=${dis}>${label}</option>`;
  const { clash } = firewall(f);
  return html`<div class="autos-page">
    <div class="ahd"><a class="crumb" @click=${() => closeForm(p)}>Automations</a> ›
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

extendKind('trigger', { card, head, detail, listExtra, create: { ...KINDS.get('trigger').create, start: (p) => startForm(p, null) } });
