// automations.js — the Automations page (D83): the agents that work without
// anyone typing — schedules and watchers here; chat channels (auto-channels.js)
// and triggers draw their own cards and details (extendKind). Each automation shows what it does, when,
// whose it is and how its last run went; open it for its runs (unread first
// to your eye), its settings, and — for a thread — "start afresh".
import { html, nothing, repeat } from '/vendor/lit-all.min.js';
import { AutoPage as Model, KINDS, kinds, CADENCES, MODES, ago, cadence, scheduleCan, summaryCount } from './model/auto.js';

// The page's state, kinds and schedule actions are model/auto.js (shared with
// the native view); what is drawn here, and the "are you sure?" before a delete.
export { registerKind, extendKind, ago } from './model/auto.js';

export class AutoPage extends Model {
  async del(it) {
    if (!confirm(`Delete "${it.name}"? Its runs stay.`)) return;
    return super.del(it);
  }
}

// autoPageTpl is the page (drawn into the timeline).
export function autoPageTpl(p) {
  if (p.custom) return p.custom(p);
  if (p.form) return formTpl(p);
  const it = p.item();
  if (p.open && it) return detailTpl(p, it);
  const groups = kinds().map(([kind, spec]) => ({ kind, spec, items: p.items.filter((i) => i.kind === kind) }));
  return html`<div class="autos-page">
    <div class="ahd"><h3>Automations</h3><span class="muted small">agents that work without anyone typing</span>
      <span style="flex:1"></span>
      <button class="btn btnsm" @click=${() => p.newSchedule(false)}>New schedule</button>
      <button class="btn ghost btnsm" @click=${() => p.newSchedule(true)}>New watcher</button>
      ${[...KINDS.values()].filter((s) => s.create).map((s) => html`<button class="btn ghost btnsm" @click=${() => s.create.start(p)}>${s.create.label}</button>`)}</div>
    ${p.err ? html`<div class="err">${p.err}</div>` : nothing}
    ${groups.map((g) => html`<h5>${g.spec.label}</h5>
      ${g.items.length ? repeat(g.items, (i) => i.kind + i.id, (i) => (g.spec.card || cardTpl)(p, i))
        : html`<div class="muted small empty-line">${g.spec.empty || 'none yet'}</div>`}
      ${g.spec.listExtra ? g.spec.listExtra(p) : nothing}`)}
  </div>`;
}

function cardTpl(p, it) {
  const mine = scheduleCan(it).mine;
  const failed = (it.lastStatus || '').startsWith('error');
  return html`<div class="acard2 ${it.enabled ? '' : 'off'}" data-auto=${it.kind + ':' + it.id} @click=${() => p.show(it.kind, it.id)}>
    <div class="ah"><span class="nm">${it.name}</span>
      ${it.unread ? html`<span class="badge unread">${it.unread} new</span>` : nothing}
      ${failed ? html`<span class="badge error" title=${it.lastStatus}>failed</span>` : nothing}
      ${it.enabled ? nothing : html`<span class="badge">off</span>`}
      <span style="flex:1"></span>
      ${it.access === 'oversee' ? html`<span class="muted small">${it.owner}'s</span>` : !mine && it.owner ? html`<span class="muted small">by ${it.owner}</span>` : nothing}
      ${mine ? html`<button class="btn ghost btnsm" @click=${(e) => { e.stopPropagation(); p.runNow(it); }}>Run now</button>` : nothing}
    </div>
    <div class="as muted small">${it.config ? cadence(it.config.cron) : it.summary}${it.config ? ' · ' + (it.config.goal || '').slice(0, 140) : ''}</div>
    <div class="as muted small">${it.kind === 'schedule' ? MODES[it.mode] || '' : 'keeps only the rounds where something changed'}
      ${it.lastRunAt ? ` · last ${ago(it.lastRunAt)}` : ''}${it.runs ? ` · ${it.runs} run${it.runs === 1 ? '' : 's'}` : ''}</div>
  </div>`;
}

function detailTpl(p, it) {
  const spec = KINDS.get(it.kind) || {};
  return html`<div class="autos-page">
    <div class="ahd"><a class="crumb" @click=${() => p.show(null)}>Automations</a> › <b>${it.name}</b>
      <span style="flex:1"></span>${(spec.head || scheduleHead)(it, p)}</div>
    ${p.err ? html`<div class="err">${p.err}</div>` : nothing}
    ${(spec.detail || scheduleDetail)(it, p)}
    <h5>${spec.runsLabel || 'Runs'}</h5>
    ${p.runs.length ? repeat(p.runs, (r) => r.id, (r) => html`<div class="run ${r.unread ? 'unread' : ''}" data-id=${r.id} @click=${() => p.on.select(r.id)}>
        <div class="t">${r.title || 'run ' + r.id}</div>
        <span class="gl">${new Date(r.activityMs).toLocaleString()}</span>
        ${r.status === 'error' ? html`<span class="gl err">!</span>` : r.status === 'running' ? html`<span class="spin"></span>` : nothing}
      </div>`) : html`<div class="muted small empty-line">none yet</div>`}
    ${p.next ? html`<button class="btn ghost btnsm" @click=${() => p.loadRuns(true)}>more</button>` : nothing}
  </div>`;
}

function scheduleHead(it, p) {
  const can = scheduleCan(it);
  return html`${can.runNow ? html`<button class="btn ghost btnsm" @click=${() => p.runNow(it)}>Run now</button>` : nothing}
    ${can.toggle ? html`<label class="chk small"><input type="checkbox" .checked=${it.enabled} @change=${() => p.toggle(it)}> on</label>` : nothing}
    ${can.edit ? html`<button class="btn ghost btnsm" @click=${() => p.editSchedule(it)}>Edit</button>` : nothing}
    ${can.reset ? html`<button class="btn ghost btnsm" title="its next run starts a new conversation; the old ones stay" @click=${() => p.reset(it)}>Start afresh</button>` : nothing}
    ${can.del ? html`<button class="btn rm btnsm" @click=${() => p.del(it)}>Delete</button>` : nothing}`;
}

function scheduleDetail(it, p) {
  return html`<div class="muted small">${it.config ? html`${cadence(it.config.cron)} · ${it.kind === 'schedule' ? MODES[it.mode] : 'a watcher'}
      ${it.mode === 'conversation' && it.targetRun ? html` · <a @click=${() => p.on.select(it.targetRun)}>reports to its conversation</a>` : nothing}`
      : it.summary}${it.lastStatus ? ` · last run: ${it.lastStatus}` : ''}</div>
    ${it.config && it.config.goal ? html`<div class="agoal">${it.config.goal}</div>` : nothing}`;
}

function formTpl(p) {
  const f = p.form;
  const set = (k) => (e) => { f[k] = e.target.type === 'checkbox' ? e.target.checked : e.target.value; p.changed(); };
  const preset = CADENCES.some(([c]) => c === f.cron);
  return html`<div class="autos-page">
    <div class="ahd"><a class="crumb" @click=${() => p.closeForm()}>Automations</a> ›
      <b>${f.id ? 'Edit ' + (f.watcher ? 'watcher' : 'schedule') : f.watcher ? 'New watcher' : 'New schedule'}</b></div>
    ${p.err ? html`<div class="err">${p.err}</div>` : nothing}
    <div class="field"><label>Name</label><input .value=${f.name} @input=${set('name')} placeholder="Morning digest"></div>
    <div class="field"><label>When</label>
      <select @change=${(e) => { if (e.target.value) { f.cron = e.target.value; p.changed(); } }}>
        ${CADENCES.map(([c, l]) => html`<option value=${c} ?selected=${f.cron === c}>${l}</option>`)}
        <option value="" ?selected=${!preset}>custom…</option></select>
      ${preset ? nothing : html`<input class="mono" .value=${f.cron} @input=${set('cron')} placeholder="0 9 * * * or @every 30m">`}</div>
    <div class="field"><label>${f.watcher ? 'What to watch' : 'What to do'}</label>
      <textarea rows="3" .value=${f.goal} @input=${set('goal')}></textarea></div>
    ${f.watcher ? nothing : html`<div class="field"><label>Where each run goes</label>
      <select @change=${set('mode')}>
        <option value="isolated" ?selected=${f.mode === 'isolated'}>a new run each time (a report)</option>
        <option value="persistent" ?selected=${f.mode === 'persistent'}>one ongoing thread that builds on the last</option>
        ${f.mode === 'conversation' ? html`<option value="conversation" selected>into its conversation</option>` : nothing}
      </select></div>`}
    <div class="row2">
      <div class="field"><label>Tool mode</label><select @change=${set('toolset')} ?disabled=${!!f.id}>
        <option value="private" ?selected=${f.toolset !== 'web'}>internal systems, no web</option>
        <option value="web" ?selected=${f.toolset === 'web'}>web, no internal systems</option></select></div>
      <div class="field"><label>Who can see its runs</label><select @change=${set('visibility')}>
        <option value="private" ?selected=${f.visibility !== 'team'}>only you</option>
        <option value="team" ?selected=${f.visibility === 'team'}>everyone who can open this agent</option></select></div>
    </div>
    <div><button class="btn" @click=${() => p.save()}>${f.id ? 'Save' : 'Create'}</button></div>
  </div>`;
}

// sideEntryTpl is the sidebar's entry: the page, with what is new there.
export function sideEntryTpl(p, on, open) {
  const n = summaryCount(p.summary);
  return html`<div class="autos-entry ${on ? 'on' : ''}" @click=${open}>
    <span>Automations</span>
    ${n ? html`<span class="badge unread">${n}</span>` : nothing}
    ${p.summary.failing ? html`<span class="badge error" title="an automation's last run failed">!</span>` : nothing}
  </div>`;
}
