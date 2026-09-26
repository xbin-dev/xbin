// native/auto.js — the Automations page as pushed screens (D83): the list
// (a section per kind, a row per automation with its badges), one
// automation (what it does, its actions, its runs — opening them marks them
// read), and the schedule/watcher form. Channels and triggers draw their own
// detail (auto-channels.js, auto-triggers.js). The state and the actions are
// the model's AutoPage (model/auto.js) — the web's automations.js draws the
// same; destructive actions are confirmed natively (a button's confirm).
import { html, repeat, nothing } from '/vendor/xb-native.js';
import { ctx, when } from './ui.js';
import { KINDS, kinds, CADENCES, MODES, ago, cadence, scheduleCan } from '../model/auto.js';
import { channelDetail, channelRow } from './auto-channels.js';
import { triggerDetail, triggerRow, triggerForm, unmatchedTpl, TRIGGER_FORM, startTrigger, closeForm as closeTrigger } from './auto-triggers.js';

// autoScreens: the page, one automation, a form — as the model has them open.
export function autoScreens() {
  const p = ctx.app.autos;
  const out = [{ key: 'auto', tpl: () => listTpl(p), back: () => { if (p.open || p.form || p.custom) p.show(null); } }];
  const it = p.item();
  if (p.open && it) {
    out.push({ key: `auto:${it.kind}:${it.id}`, tpl: () => detailTpl(p, it), back: () => { if (p.form) p.closeForm(); if (p.custom) closeTrigger(p); } });
  }
  if (p.form) out.push({ key: 'auto-form', tpl: () => scheduleForm(p) });
  else if (p.custom === TRIGGER_FORM) out.push({ key: 'auto-trigger-form', tpl: () => triggerForm(p) });
  return out;
}

const errTpl = (p) => (p.err ? html`<section><notice tone="danger" text=${p.err}/></section>` : nothing);

function listTpl(p) {
  const groups = kinds().map(([kind, spec]) => ({ kind, spec, items: p.items.filter((i) => i.kind === kind) }));
  return html`<screen title="Automations" subtitle="agents that work without anyone typing" style="list" refreshable @refresh=${() => p.load()}>
    <toolbar><menu icon="plus" label="New">
      <button icon="clock" @tap=${() => p.newSchedule(false)}>New schedule</button>
      <button icon="eye" @tap=${() => p.newSchedule(true)}>New watcher</button>
      <button icon="bolt" @tap=${() => startTrigger(p, null)}>New trigger</button>
    </menu></toolbar>
    ${errTpl(p)}
    ${repeat(groups, (g) => g.kind, (g) => html`<section title=${g.spec.label}>
      ${g.items.length ? repeat(g.items, (i) => i.kind + ':' + i.id, (i) => rowTpl(p, i)) : html`<empty text=${g.spec.empty || 'none yet'}/>`}
    </section>${g.kind === 'trigger' ? unmatchedTpl(p) : nothing}`)}
  </screen>`;
}

function rowTpl(p, it) {
  if (it.kind === 'channel') return channelRow(p, it);
  if (it.kind === 'trigger') return triggerRow(p, it);
  const can = scheduleCan(it);
  const failed = (it.lastStatus || '').startsWith('error');
  const badge = failed ? ['failed', 'danger'] : it.unread ? [`${it.unread} new`, 'accent'] : !it.enabled ? ['off', 'muted'] : null;
  const whose = it.access === 'oversee' ? `${it.owner}'s · ` : !can.mine && it.owner ? `by ${it.owner} · ` : '';
  const what = it.config ? `${cadence(it.config.cron)} · ${(it.config.goal || '').slice(0, 140)}` : it.summary;
  const how = `${it.kind === 'schedule' ? MODES[it.mode] || '' : 'keeps only the rounds where something changed'}${it.lastRunAt ? ` · last ${ago(it.lastRunAt)}` : ''}${it.runs ? ` · ${it.runs} run${it.runs === 1 ? '' : 's'}` : ''}`;
  return html`<row title=${it.name} subtitle=${`${whose}${what} · ${how}`} icon=${it.kind === 'watcher' ? 'eye' : 'clock'}
      badge=${badge ? badge[0] : nothing} tone=${badge ? badge[1] : nothing} nav @tap=${() => p.show(it.kind, it.id)}>
    ${can.runNow ? html`<actions><button icon="play" @tap=${() => p.runNow(it)}>Run now</button></actions>` : nothing}
  </row>`;
}

function detailTpl(p, it) {
  const body = it.kind === 'channel' ? channelDetail(p, it) : it.kind === 'trigger' ? triggerDetail(p, it) : scheduleDetail(p, it);
  const spec = KINDS.get(it.kind) || {};
  return html`<screen title=${it.name} subtitle=${spec.label || nothing} style="list" refreshable @refresh=${() => p.load()}>
    ${errTpl(p)}
    ${body}
    <section title=${spec.runsLabel || 'Runs'}>
      ${p.runs.length ? repeat(p.runs, (r) => r.id, (r) => html`<row title=${r.title || 'run ' + r.id} subtitle=${when(r.activityMs)}
          badge=${r.status === 'error' ? 'failed' : r.status === 'running' ? 'running' : nothing}
          tone=${r.status === 'error' ? 'danger' : r.unread ? 'accent' : nothing} nav @tap=${() => ctx.app.select(r.id)}/>`)
        : html`<empty text="none yet"/>`}
      ${p.next ? html`<button @tap=${() => p.loadRuns(true)}>More</button>` : nothing}
    </section>
  </screen>`;
}

function scheduleDetail(p, it) {
  const can = scheduleCan(it);
  const c = it.config;
  return html`<section>
      <row title=${c ? cadence(c.cron) : it.summary} subtitle=${c ? (it.kind === 'schedule' ? MODES[it.mode] : 'a watcher') : nothing}
        detail=${it.lastStatus ? `last run: ${it.lastStatus}` : nothing}/>
      ${c && c.goal ? html`<text selectable>${c.goal}</text>` : nothing}
      ${it.mode === 'conversation' && it.targetRun ? html`<row title="Reports to its conversation" icon="chat" nav @tap=${() => ctx.app.select(it.targetRun)}/>` : nothing}
    </section>
    <section>
      ${can.runNow ? html`<button icon="play" @tap=${() => p.runNow(it)}>Run now</button>` : nothing}
      ${can.toggle ? html`<toggle label="On" value=${!!it.enabled} @change=${() => p.toggle(it)}/>` : nothing}
      ${can.edit ? html`<button icon="pencil" @tap=${() => p.editSchedule(it)}>Edit</button>` : nothing}
      ${can.reset ? html`<button icon="refresh" confirm=${{ title: 'Start afresh?', message: 'Its next run starts a new conversation; the old ones stay.', label: 'Start afresh' }}
        @tap=${() => p.reset(it)}>Start afresh</button>` : nothing}
      ${can.del ? html`<button icon="trash" role="destructive" confirm=${{ title: `Delete "${it.name}"?`, message: 'Its runs stay.', label: 'Delete', destructive: true }}
        @tap=${() => p.del(it)}>Delete</button>` : nothing}
    </section>`;
}

const CUSTOM = '';
function scheduleForm(p) {
  const f = p.form;
  const set = (k, paint = false) => (e) => { f[k] = e.value; if (paint) p.changed(); };
  const preset = CADENCES.some(([c]) => c === f.cron);
  const title = f.id ? 'Edit ' + (f.watcher ? 'watcher' : 'schedule') : f.watcher ? 'New watcher' : 'New schedule';
  const modes = [{ value: 'isolated', label: 'a new run each time (a report)' }, { value: 'persistent', label: 'one ongoing thread that builds on the last' },
    ...(f.mode === 'conversation' ? [{ value: 'conversation', label: 'into its conversation' }] : [])];
  return html`<screen title=${title} style="form">
    <toolbar><button role="primary" @tap=${() => p.save()}>${f.id ? 'Save' : 'Create'}</button></toolbar>
    ${errTpl(p)}
    <section>
      <field label="Name" placeholder="Morning digest" value=${f.name} @input=${set('name')}/>
      <picker label="When" style="menu" value=${preset ? f.cron : CUSTOM}
        options=${[...CADENCES.map(([c, l]) => ({ value: c, label: l })), { value: CUSTOM, label: 'custom…' }]}
        @change=${(e) => { f.cron = e.value || (preset ? '' : f.cron); p.changed(); }}/>
      ${preset ? nothing : html`<field label="Cron" placeholder="0 9 * * * or @every 30m" value=${f.cron} @input=${set('cron')}/>`}
      <field label=${f.watcher ? 'What to watch' : 'What to do'} kind="multiline" value=${f.goal} @input=${set('goal')}/>
    </section>
    <section>
      ${f.watcher ? nothing : html`<picker label="Where each run goes" style="menu" value=${f.mode} options=${modes} @change=${set('mode', true)}/>`}
      ${f.id ? html`<row title="Tool mode" detail=${f.toolset === 'web' ? 'web, no internal systems' : 'internal systems, no web'}/>`
        : html`<picker label="Tool mode" style="menu" value=${f.toolset}
          options=${[{ value: 'private', label: 'internal systems, no web' }, { value: 'web', label: 'web, no internal systems' }]} @change=${set('toolset', true)}/>`}
      <picker label="Who can see its runs" style="menu" value=${f.visibility === 'team' ? 'team' : 'private'}
        options=${[{ value: 'private', label: 'only you' }, { value: 'team', label: 'everyone who can open this agent' }]} @change=${set('visibility', true)}/>
    </section>
  </screen>`;
}
