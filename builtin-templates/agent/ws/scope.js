// ws/scope.js — the scope chip and its panel on the workspace agent's page
// (docs/workspace-agent.md §The agent's page): the session's connection
// line always in view, and one click away what each org and tile is and
// why; the person's ticks staged and applied with one button; the model
// with its what-if before anything changes, and a refused change that
// offers a new session. The words of the chip and the reasons are xbind's
// (the backend's counts, xbind's full view for the names); the model is in
// ws/scope-model.js.
import { html, nothing } from '/vendor/lit-all.min.js';
import { chipWords, short, W } from './words.js';
import { STATES, tree, staged } from './scope-model.js';

const icon = (name) => (name ? html`<bx-icon name=${name} aria-hidden="true"></bx-icon>` : nothing);

/**
 * chipView(c, act): the chip — {counts, model, private, narrow, open} —
 * one button: "Scope: 14 tiles in 3 orgs connected, 6 not connected.
 * Model big."
 */
export function chipView(c, act) {
  const w = chipWords(c.counts, c.model, c.narrow, c.private, c.reach);
  return html`<button class="wsp-chip ${c.open ? 'on' : ''}" aria-label=${w.label} aria-haspopup="dialog" aria-expanded=${c.open ? 'true' : 'false'}
      title=${[w.summary || w.label, w.netTitle].filter(Boolean).join(' ')} @click=${() => act.toggle()}>
    <span class="seg">${icon('plug')}${w.conn}</span>${w.not ? html`<span class="seg">${icon('lock')}${w.not}</span>` : nothing}
    ${w.net ? html`<span class="seg net" data-net>${icon('globe')}${w.net}</span>` : nothing}
    <span class="seg model">${w.model || 'no model'}${icon('chevron-down')}</span></button>`;
}

/**
 * panelView(s, act): the panel — s: {view: 'tree' | 'models' | 'whatif' |
 * 'refused', full, err, counts, model, private, filter, ticks, expanded,
 * models, pick, what, refused, busy}.
 */
export function panelView(s, act) {
  if (s.view === 'refused' && s.refused) return refusedView(s.refused, act);
  if (s.view === 'models' || s.view === 'whatif') return modelsView(s, act);
  const c = s.counts || {};
  const rows = s.full ? tree(s.full, s.ticks, s.filter) : [];
  const st = s.full ? staged(s.full, s.ticks) : { any: false };
  const filters = [['all', `All ${(c.tiles || 0) + (c.notConnected || 0)}`], ['connected', 'Connected'], ['not', `Not connected ${c.notConnected || 0}`]];
  return html`<div class="wsp-pop wsp-scope" role="dialog" aria-label="What this session is connected to" @keydown=${(e) => act.key(e)}>
    <div class="wsp-pop-h"><b>What this session is connected to</b><button class="quiet" aria-label="close" @click=${() => act.close()}>${icon('xmark')}</button></div>
    <div class="wsp-totals">${[['connected', c.tiles, 'check'], ['hidden by you', c.hidden, 'eye-slash'], ['blocked', c.blocked, 'lock'], ['no access', c.noAccess, '']].map(([w, n, g]) =>
      html`<div class="wsp-total"><b>${n || 0}</b><span>${icon(g)}${w}</span></div>`)}</div>
    <div class="wsp-modelrow"><span class="wsc-muted">Model</span> <b>${short(s.model) || 'none'}</b> <span class="wsc-muted">${s.private ? 'private' : s.full && s.full.model ? 'not private' : ''}</span>
      <span class="grow"></span><button @click=${() => act.models()}>Change…</button></div>
    ${s.err ? html`<div class="wsc-err" role="alert">${s.err}</div>` : nothing}
    ${s.full ? html`
      <div class="wsp-filter" role="group" aria-label="show">
        ${filters.map(([k, w]) => html`<button class=${s.filter === k ? 'on' : ''} aria-pressed=${s.filter === k ? 'true' : 'false'} @click=${() => act.filter(k)}>${w}</button>`)}
        <span class="grow"></span><button class="quiet" @click=${() => act.selectAll()}>Select all allowed</button>
      </div>
      <div class="wsp-tree" role="tree" aria-label="orgs and tiles" aria-multiselectable="true" @keydown=${(e) => act.treeKey(e)}>
        ${rows.map((o, i) => orgRow(o, i, s, act))}
      </div>` : s.err ? nothing : html`<div class="wsc-muted wsp-pad">Reading what this session is connected to…</div>`}
    <div class="wsp-pop-f"><p>${W.scopeFoot}</p>
      <button @click=${() => act.close()}>Cancel</button>
      <button class="primary" ?disabled=${!st.any || s.busy} @click=${() => act.apply()}>Apply</button></div>
  </div>`;
}

const boxOf = (state) => (state === 'mixed' ? 'mixed' : state === 'on' ? 'true' : 'false');
function box(state) {
  return html`<span class="wsp-box ${state}" aria-hidden="true">${state === 'on' ? icon('check') : state === 'mixed' ? icon('minus') : nothing}</span>`;
}

function orgRow(o, i, s, act) {
  const open = s.expanded.has(o.id);
  const word = o.state && STATES[o.state] ? STATES[o.state] : null;
  const label = `${o.name}, ${o.ticked} of ${o.of} connected${o.box === 'mixed' ? ', partly selected' : ''}${o.state === 'blocked' ? `, blocked: ${o.reason}` : ''}, ${open ? 'expanded' : 'collapsed'}`;
  return html`<div class="wsp-org" data-org=${o.id}>
    <div class="wsp-ti org" role="treeitem" aria-level="1" aria-expanded=${open ? 'true' : 'false'} aria-checked=${boxOf(o.box)} aria-disabled=${o.box === 'disabled' ? 'true' : 'false'}
        aria-label=${label} tabindex=${i === 0 ? '0' : '-1'} data-key=${'o:' + o.id}
        @click=${(e) => (e.target.closest('.wsp-box,.wsp-tick') ? act.tickOrg(o.id) : act.expand(o.id))}>
      <span class="wsp-tick" @click=${(e) => { e.stopPropagation(); if (o.box !== 'disabled') act.tickOrg(o.id); }}>${box(o.box)}</span>
      ${icon(open ? 'caret-down' : 'caret-right')}${icon(o.kind === 'workspace' ? 'grid' : 'org')}
      <b>${o.name}</b>${o.kind === 'workspace' ? html`<span class="wsc-muted"> tiles in no org</span>` : nothing}
      <span class="grow"></span><span class="wsc-muted">${o.ticked} of ${o.of}</span>
      ${word && word.word ? html`<span class="wsp-state ${word.tone}">${icon(word.icon)}${word.word}</span>` : nothing}
    </div>
    ${open ? html`<div role="group">
      ${o.reason && o.tiles.some((t) => t.state === 'blocked') ? html`<div class="wsp-reason">${icon('lock')}<div><span>${o.reason}${o.pinned && !/pinned/.test(o.reason) ? ' (pinned by a workspace admin)' : ''}.</span>
        ${o.rule === 'models' ? html` <button @click=${() => act.models({ private: true })}>See what a private model would connect…</button>` : nothing}</div></div>` : nothing}
      ${o.tiles.map((t) => tileRow(t, act))}
    </div>` : nothing}
  </div>`;
}

function tileRow(t, act) {
  const w = STATES[t.state] || { icon: '', word: t.state, tone: '' };
  const label = `${t.name}, ${w.word}${t.state === 'blocked' && t.reason ? `: ${t.reason}` : ''}${t.tickable ? '' : ', dimmed'}`;
  return html`<div class="wsp-ti tile ${t.tickable ? '' : 'dim'}" role="treeitem" aria-level="2" aria-checked=${t.ticked ? 'true' : 'false'} aria-disabled=${t.tickable ? 'false' : 'true'}
      aria-label=${label} tabindex="-1" data-key=${'t:' + t.tile} data-tile=${t.tile} @click=${() => act.tickTile(t.tile)}>
    ${box(t.tickable ? (t.ticked ? 'on' : 'off') : 'disabled')}<span class="wsp-tname">${t.name}</span>
    <span class="grow"></span><span class="wsp-state ${w.tone}">${icon(w.icon)}${w.word}</span>
    ${t.via ? html`<div class="wsp-via wsc-muted">This tile is under ${t.by || 'this org'} by its share <span class="wsc-mono">${t.via}</span>.</div>` : nothing}
  </div>`;
}

// ---- the model -----------------------------------------------------------------------------

function modelsView(s, act) {
  const list = s.models || [];
  const w = s.what;
  return html`<div class="wsp-pop wsp-models" role="dialog" aria-label="Change this session’s model" @keydown=${(e) => act.key(e)}>
    <div class="wsp-pop-h"><button class="quiet" aria-label="back" @click=${() => act.back()}>${icon('chevron-left')}</button><b>Change this session’s model</b></div>
    <div class="wsp-mlist" role="radiogroup" aria-label="models">${list.map((m) => html`<label class="wsp-model ${s.pick === m.id ? 'on' : ''}" data-model=${m.id}>
      <input type="radio" name="wsp-model" .checked=${s.pick === m.id} @change=${() => act.pick(m.id)}>
      <span><b>${m.name || short(m.id)}</b>${m.id === s.model ? html` <span class="wsc-muted">current</span>` : nothing}
        ${m.private ? html` <span class="wsp-tag ok">${icon('lock')}private</span>` : nothing}
        ${m.where || m.upstream ? html`<span class="wsc-muted wsp-where">${m.where || m.upstream}</span>` : nothing}</span></label>`)}
      ${list.length ? nothing : html`<div class="wsc-muted wsp-pad">No other model is open to you.</div>`}</div>
    ${s.pick && s.pick !== s.model ? (w ? html`<div class="wsp-whatif" role="status">
        <div class="wsp-wh">${icon('plug')}If you switch to ${short(w.model || s.pick)}${w.private ? html` <span class="wsp-tag ok">${icon('lock')}private</span>` : nothing}</div>
        <div class="wsp-conn">${icon('plus')}${w.connectsWords}</div>
        ${w.connects.length ? html`<div class="wsp-names">${w.connects.map((n) => html`<span class="wsp-name">${icon('grid')}${n}</span>`)}</div>` : nothing}
        <div class="wsp-disc">${icon('xmark')}${w.disconnectsWords}</div>
        ${w.disconnects.length ? html`<ul class="wsp-dlist">${w.disconnects.map((d) => html`<li><b>${d.name}</b>${d.reason ? html` — ${d.reason}` : nothing}</li>`)}</ul>` : nothing}
        ${w.summary ? html`<div class="wsp-sum"><b>${w.summary}</b></div>` : nothing}
      </div>` : html`<div class="wsc-muted wsp-pad" role="status">${s.err || 'Asking xbin what would change…'}</div>`) : nothing}
    <div class="wsp-pop-f">
      <button @click=${() => act.back()}>Cancel</button>
      <button class="primary" ?disabled=${!w || !s.pick || s.pick === s.model || s.busy} @click=${() => act.switchModel()}>${w ? (w.allowed ? w.button : 'Switch model') : 'Switch model'}</button></div>
  </div>`;
}

function refusedView(r, act) {
  return html`<div class="wsp-pop wsp-refused" role="alertdialog" aria-labelledby="wsp-refused-h" @keydown=${(e) => act.key(e)}>
    <div class="wsp-pop-h">${icon('lock')}<b id="wsp-refused-h">${r.title}</b></div>
    <div class="wsp-pad"><p>${r.body}</p><p>${r.after}</p></div>
    <div class="wsp-pop-f col">
      <button class="primary" @click=${() => act.newOn(r.model)}>${r.start}</button>
      <button @click=${() => act.close()}>${r.stay}</button></div>
  </div>`;
}
