// native/auto-triggers.js — event triggers on the Automations screens (D87):
// an automation that starts work when something happens — an event on a bus
// this agent may read, or a push from a tile bound to it (the webhooks
// tile). Its row and detail (what it takes, where each event goes, what it
// still needs wired, its recent events), the form (the lane/data-class
// firewall refuses a clash), test fire, and pushes no trigger took yet. The
// state and the actions are model/auto-triggers.js — the web's
// auto-triggers.js draws the same.
import { html, repeat, nothing } from '/vendor/xb-native.js';
import { ago } from '../model/auto.js';
import { st, self, startForm, closeForm, firewall, save, toggle, test, reset, del, triggerCan, status, wiring, REASONS, MODES } from '../model/auto-triggers.js';

// The page's `custom` while the trigger form is open (the web puts its form
// template there; here it only says which screen to push).
export const TRIGGER_FORM = () => null;
export const startTrigger = (p, it, preset = {}) => startForm(p, it, preset, TRIGGER_FORM);

export function triggerRow(p, it) {
  const k = status(it);
  const badge = k === 'grant' ? ['needs a grant', 'danger'] : k === 'error' ? ['error', 'danger'] : it.unread ? [`${it.unread} new`, 'accent'] : !it.enabled ? ['off', 'muted'] : null;
  const whose = it.access === 'oversee' ? `${it.owner}'s · ` : it.access !== 'owner' && it.owner ? `by ${it.owner} · ` : '';
  return html`<row title=${it.name} subtitle=${`${whose}when ${it.summary}${it.lastRunAt ? ` · last ${ago(it.lastRunAt)}` : ''}${it.runs ? ` · ${it.runs} run${it.runs === 1 ? '' : 's'}` : ''}`}
    icon="bolt" badge=${badge ? badge[0] : nothing} tone=${badge ? badge[1] : nothing} nav @tap=${() => p.show('trigger', it.id)}/>`;
}

// unmatchedTpl: pushes nobody took — "create a trigger" from one.
export function unmatchedTpl(p) {
  if (!st.unmatched.length) return nothing;
  return html`<section title="Pushes nothing took">${repeat(st.unmatched, (u) => `${u.from}:${u.name}`, (u) => html`
    <row title=${`${u.from} sent ${u.name}${u.count > 1 ? ` ×${u.count}` : ''}`} subtitle="nothing took it">
      <actions><button icon="plus" @tap=${() => startTrigger(p, null, { name: u.name, source: 'push', sourceRef: u.from, match: u.name })}>Create a trigger</button></actions>
    </row>`)}</section>`;
}

export function triggerDetail(p, it) {
  const c = it.config || {};
  const can = triggerCan(it);
  const head = html`<section>
    ${can.test ? html`<button icon="bolt" @tap=${() => test(it, p)}>Fire a test event</button>` : nothing}
    ${can.toggle ? html`<toggle label="On" value=${!!it.enabled} @change=${() => toggle(it, p)}/>` : nothing}
    ${can.edit ? html`<button icon="pencil" @tap=${() => startTrigger(p, it)}>Edit</button>` : nothing}
    ${can.reset ? html`<button icon="refresh" confirm=${{ title: 'Start afresh?', message: 'Its next event starts a new thread.', label: 'Start afresh' }}
      @tap=${() => reset(it, p)}>Start afresh</button>` : nothing}
    ${can.del ? html`<button icon="trash" role="destructive" confirm=${{ title: `Delete "${it.name}"?`, message: 'Its runs stay.', label: 'Delete', destructive: true }}
      @tap=${() => del(it, p)}>Delete</button>` : nothing}
  </section>`;
  if (can.oversee) return html`<section><text tone="muted">${it.summary}</text></section>${head}`;
  const w = wiring(it);
  return html`<section>
      <row title=${`when ${it.summary}`} subtitle=${`${c.toolset === 'web' ? 'web lane' : 'internal lane'} · takes ${c.dataClass} data${c.deliver ? ` · announces to ${c.deliver}` : ''}`}/>
      ${c.mode === 'conversation' && c.targetRun ? html`<row title="Its conversation" icon="chat" nav @tap=${() => p.on.select(c.targetRun)}/>` : nothing}
      ${st.note ? html`<notice tone="ok" text=${st.note}/>` : nothing}
      ${w === 'grant' ? html`<notice tone="warn" title=${`This agent may not read ${c.sourceRef} yet`}
        text=${`Add to its xbin.json uses: { "target": "${c.sourceRef}", "role": "reader" }, and approve it (the grants panel, or bx grant). ${it.lastStatus || ''}`}/>` : nothing}
      ${w === 'push' ? html`<notice tone="info" text=${`Pushes come from ${c.sourceRef} once it is bound to this agent: bx bind ${c.sourceRef} agents=${self()}`}/>` : nothing}
      ${c.goal ? html`<text selectable>${c.goal}</text>` : nothing}
    </section>
    ${head}
    <section title="Recent events">${st.events.length ? repeat(st.events, (e, i) => e.eventId || i, (e) => html`
      <row title=${e.topic || e.eventId || 'event'} mono="title" subtitle=${ago(e.at)}
        detail=${e.accepted ? (e.runId ? `ran #${e.runId}` : 'ran') : REASONS[e.reason] || e.reason || 'seen'}
        tone=${e.accepted ? 'ok' : 'muted'} nav=${!!(e.accepted && e.runId)} @tap=${e.accepted && e.runId ? () => p.on.select(e.runId) : nothing}/>`)
      : html`<empty text="none yet"/>`}</section>`;
}

const O = (pairs) => pairs.map(([value, label]) => ({ value, label }));

export function triggerForm(p) {
  const f = st.form;
  if (!f) return html`<screen title="Trigger"/>`;
  const set = (k, paint = false) => (e) => { f[k] = e.value; if (paint) p.changed(); };
  const { clash } = firewall(f);
  const sessions = [{ value: '', label: 'nobody — read them here' }, ...st.sessions.map((s) => ({ value: s.key, label: s.label })),
    ...(f.deliver && !st.sessions.some((s) => s.key === f.deliver) ? [{ value: f.deliver, label: f.deliver }] : [])];
  const modes = O([['isolated', MODES.isolated], ['persistent', MODES.persistent], ...(f.mode === 'conversation' ? [['conversation', MODES.conversation]] : [])]);
  return html`<screen title=${f.id ? 'Edit trigger' : 'New trigger'} style="form">
    <toolbar><button role="primary" ?disabled=${clash} @tap=${() => save(p)}>${f.id ? 'Save' : 'Create'}</button></toolbar>
    ${p.err ? html`<section><notice tone="danger" text=${p.err}/></section>` : nothing}
    <section>
      <field label="Name" placeholder="deploys" value=${f.name} @input=${set('name')}/>
      <picker label="When" style="menu" value=${f.source} @change=${set('source', true)}
        options=${O([['push', 'a tile bound to this agent pushes (e.g. webhooks)'], ['bus', 'an event appears on a bus']])}/>
      <field label=${f.source === 'bus' ? 'The bus' : 'The tile'} placeholder=${f.source === 'bus' ? 'res:apps/calendar/bus' : 'apps/webhooks'}
        value=${f.sourceRef} @input=${set('sourceRef')}/>
      <field label="Topics starting with (empty: all)" placeholder="deploy" value=${f.match} @input=${set('match')}/>
    </section>
    <section title="What to do with each event" footer="{{topic}} and {{text}} are the event's; its data comes after, marked as data.">
      <field kind="multiline" placeholder="Check the {{topic}} deploy and summarise what changed" value=${f.goal} @input=${set('goal')}/>
    </section>
    <section>
      <picker label="Where each event goes" style="menu" value=${f.mode} options=${modes} @change=${set('mode', true)}/>
      <field label="At most, per hour" kind="number" value=${String(f.maxPerHour)} @input=${set('maxPerHour')}/>
      <picker label="Tool mode" style="menu" value=${f.toolset} @change=${set('toolset', true)}
        options=${O([['private', 'internal systems, no web'], ['web', 'web, no internal systems']])}/>
      ${f.source === 'bus' ? html`<row title="The data it takes" detail="private (from inside the workspace)"/>`
        : html`<picker label="The data it takes" style="menu" value=${f.dataClass} @change=${set('dataClass', true)}
          options=${O([['private', 'private (from inside the workspace)'], ['public', 'public (e.g. webhooks from outside)']])}/>`}
      <picker label="Announce its answers to" style="menu" value=${f.deliver} options=${sessions} @change=${set('deliver', true)}/>
      <picker label="Who can see its runs" style="menu" value=${f.visibility} @change=${set('visibility', true)}
        options=${O([['private', 'only you'], ['team', 'everyone who can open this agent']])}/>
    </section>
    ${clash ? html`<section><notice tone="danger" text="The web lane and announcing to a chat both reach outside the workspace, so this trigger must take public data only — or use internal systems and read its answers here."/></section>` : nothing}
  </screen>`;
}

export { closeForm };
