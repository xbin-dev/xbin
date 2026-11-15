// ws/cards.js — the workspace agent's own cards in its transcript, drawn
// inside the chat kit's `host` frame (docs/frontend-kit.md: the kit frames a
// host block, the host draws it): a tile agent the agent started, a request
// that waits for the person's approval and what came of it, an admin
// change set, the editable brief, a refusal of xbin's, quick replies.
//
// None of them grants anything. A request's one button is Review…, which
// asks xbin's chrome to open it by id (ws/dock.js); the chrome draws the
// grant from xbind's own reading of the request. The brief's button is the
// backend's: "Review and start…" opens xbind's approval of the start, and
// only where the person's own preference lets tile agents start without
// asking (the backend decides) is it "Start".
import { html, nothing } from '/vendor/lit-all.min.js';
import { agentCard, requestCard, briefCard, W } from './words.js';

const icon = (name, label) => (name ? html`<bx-icon name=${name} aria-hidden=${label ? nothing : 'true'} label=${label || nothing}></bx-icon>` : nothing);

/**
 * hostCard(b, act): the inside of a host block — b.type: wsession,
 * request, brief, refusal, slow, replies, notice. act is the page's:
 * {now(), tileName(path), offline, bag(k), openSession(a), review(q),
 * send(text), fill(text), briefSave(b, vals), briefStart(b),
 * startDefaults(b) (the brief's start-defaults, read once), openScope(o),
 * whatCan(), repaint(), cards()}.
 */
export function hostCard(b, act) {
  switch (b.type) {
    case 'wsession': return sessionCard(b.data, act);
    case 'request': return requestView(b.data, act);
    case 'brief': return briefView(b, act);
    case 'refusal': return refusalView(b.data, act);
    case 'slow': return html`<div class="wsc wsc-quiet" role="status">${icon('clock')}Slowing down — the agent is doing a lot at once.</div>`;
    case 'replies': return repliesView(b.data, act);
    case 'notice': return noticeCard(b.data);
  }
  return null;
}

// ---- a tile agent the agent started ---------------------------------------------------------

function sessionCard(a, act) {
  const w = agentCard(a, act.now(), act.tileName);
  return html`<div class="wsc wsc-agent ${w.waits ? 'wsc-waits' : ''}" data-agent=${a.id} data-status=${a.status || ''} data-tile=${a.tile || ''}>
    <div class="wsc-row">
      <span class="wsc-name">${icon('grid')}<b>${w.tile}</b><span class="wsc-muted"> · ${w.harness}</span></span>
      <span class="wsc-st ${w.tone ? 'wsc-' + w.tone : ''}">${icon(w.glyph)}${w.word}</span>
      <button class="wsc-open ${w.waits ? 'primary' : ''}" ?disabled=${!a.session} title=${a.session ? `show this session in ${w.tile}` : 'its session isn’t open yet'}
        @click=${() => act.openSession(a)}>${w.button}${icon('external')}</button>
    </div>
    ${w.line || w.note ? html`<div class="wsc-line">${w.line ? html`<span class="wsc-last">${w.line}</span>` : nothing}${w.note ? html` <b>${w.note}</b>` : nothing}</div>` : nothing}
  </div>`;
}

// ---- a request for the person's approval -------------------------------------------------------

function requestView(q, act) {
  const w = requestCard(q, act.now(), act.tileName);
  const local = w.local && w.waits; // a start the brief's own button confirms (the person's preference)
  const head = local ? 'Ready to start' : w.head;
  const btn = local ? '' : w.button;
  const off = act.offline && btn === W.review;
  return html`<div class="wsc wsc-req ${w.waits ? 'wsc-waits' : ''}" data-req=${q.id} data-ref=${q.ref || ''} data-state=${q.state || ''} data-kind=${w.kind}>
    <div class="wsc-row"><span class="wsc-name">${icon(w.waits ? 'shield' : w.glyph)}<b>${head}</b></span>${w.line ? html`<span class="wsc-muted">${w.line}</span>` : nothing}</div>
    <div class="wsc-what"><span class="wsc-quote" title="the agent’s words">${w.what}</span>${w.tile && !w.what.includes(w.tile) ? html` — <b>${w.tile}</b>` : nothing}</div>
    ${w.call && w.waits ? html`<div class="wsc-call wsc-mono">${w.call}</div>` : nothing}
    ${w.result ? html`<div class="wsc-line">${w.result}</div>` : nothing}
    ${local ? html`<div class="wsc-line">Start it from the brief.</div>` : nothing}
    ${btn ? html`<div class="wsc-btns"><button class=${btn === W.review ? 'primary' : ''} ?disabled=${off} title=${off ? W.needsConnection : btn === W.review ? 'xbin shows what exactly would happen, and asks you' : ''}
      @click=${() => (btn === W.review ? act.review(q) : act.send(`Please ask again: ${q.title || w.what}`))}>${btn}</button>
      ${off ? html`<span class="wsc-muted">${W.needsConnection}</span>` : nothing}</div>` : nothing}
  </div>`;
}

// ---- the brief ("scope, then hand off") ----------------------------------------------------------

function briefView(blk, act) {
  const b = blk.data;
  const w = briefCard(b, act.tileName, act.startDefaults(b));
  const bag = act.bag(blk.k);
  const editing = !!bag.editing && w.editable;
  const vals = bag.vals || (bag.vals = Object.fromEntries(w.fields.map((f) => [f.key, f.value])));
  const save = async () => {
    bag.busy = true; bag.err = ''; act.repaint();
    const err = await act.briefSave(b, vals);
    bag.busy = false; bag.err = err || '';
    if (!err) { bag.editing = false; bag.vals = null; }
    act.repaint();
  };
  const start = async () => {
    bag.busy = true; bag.err = ''; act.repaint();
    bag.err = (await act.briefStart(b)) || '';
    bag.busy = false; act.repaint();
  };
  const id = `brief-${b.id}`;
  return html`<div class="wsc wsc-brief" data-brief=${b.id} data-state=${w.state} aria-labelledby=${id + '-h'}>
    <div class="wsc-row"><span class="wsc-name">${icon('doc')}<b id=${id + '-h'}>${w.title}</b></span>
      ${w.status ? html`<span class="wsc-st">${w.status}</span>` : nothing}
      ${w.editable && !editing ? html`<button class="quiet wsc-edit" @click=${() => { bag.editing = true; act.repaint(); }} aria-label="Edit the brief">${icon('pencil')}Edit</button>` : nothing}</div>
    <dl class="wsc-fields">${w.fields.map((f) => html`<div class="wsc-field" data-field=${f.key}>
      <dt id=${`${id}-${f.key}`}>${f.label}</dt>
      <dd>${editing ? html`<textarea rows="2" aria-labelledby=${`${id}-${f.key}`} .value=${vals[f.key] || ''} @input=${(e) => { vals[f.key] = e.target.value; }}></textarea>`
        : f.value || html`<span class="wsc-muted">—</span>`}</dd></div>`)}</dl>
    ${bag.err ? html`<div class="wsc-err" role="alert">${bag.err}</div>` : nothing}
    <div class="wsc-btns">
      ${editing ? html`<button class="primary" ?disabled=${bag.busy} @click=${save}>Save</button>
        <button ?disabled=${bag.busy} @click=${() => { bag.editing = false; bag.vals = null; act.repaint(); }}>Cancel</button>`
        : w.button ? html`<button class="primary wsc-start" ?disabled=${bag.busy || w.none || (act.offline && w.button !== W.start)} @click=${start}
          title=${w.button === W.start ? 'your preference lets tile agents start without asking: this click starts it' : 'xbin shows the start exactly as it will be sent, and asks you'}>${icon(w.button === W.start ? 'play' : 'shield')}${w.button}</button>` : nothing}
      ${w.harnesses.length > 1 && w.editable && !editing ? html`<label class="wsc-muted wsc-pick">Coding agent
          <select aria-label="the coding agent it starts on" ?disabled=${bag.busy} @change=${async (e) => { bag.busy = true; act.repaint(); bag.err = (await act.briefSave(b, { harness: e.target.value })) || ''; bag.busy = false; act.repaint(); }}>
            ${w.harnesses.map((h) => html`<option value=${h.id} ?selected=${h.id === w.picked}>${h.name}</option>`)}</select></label>` : nothing}
      ${w.runsOn ? html`<span class="wsc-muted wsc-runs">${w.runsOn}</span>` : nothing}
    </div>
    ${w.editable ? html`<div class="wsc-line wsc-wrap">${w.note}</div>` : nothing}
  </div>`;
}

// ---- refusals -----------------------------------------------------------------------------

function refusalView(r, act) {
  if (r.kind === 'cannot') {
    return html`<div class="wsc wsc-refusal" data-refusal="cannot">
      <div class="wsc-row"><span class="wsc-name">${icon('info')}<b>Can’t do this</b></span></div>
      <p class="wsc-p">${r.reason ? html`${r.reason}. ` : nothing}It’s yours to do yourself, in the tile.</p>
      <div class="wsc-btns"><button @click=${() => act.whatCan()}>${W.whatCanItDo}</button></div></div>`;
  }
  if (r.kind === 'hidden') {
    return html`<div class="wsc wsc-refusal" data-refusal="hidden">
      <div class="wsc-row"><span class="wsc-name">${icon('eye-slash')}<b>Not connected in this session</b></span></div>
      <p class="wsc-p">You left this tile out of this session’s scope. Widen the scope, or use another session.</p>
      <div class="wsc-btns"><button @click=${() => act.openScope({})}>Open scope</button></div></div>`;
  }
  return html`<div class="wsc wsc-refusal wsc-policy" data-refusal="policy">
    <div class="wsc-row"><span class="wsc-name">${icon('lock')}<b>Blocked by a rule</b></span></div>
    <p class="wsc-p">${r.reason}.</p>
    <p class="wsc-p wsc-muted">This is a rule set by a workspace admin, not something the agent can work around.</p>
    <div class="wsc-btns">${r.private ? html`<button class="primary" @click=${() => act.openScope({ models: true })}>See what a private model connects…</button>` : nothing}
      <button @click=${() => act.openScope({})}>Open scope</button></div></div>`;
}

// ---- quick replies ------------------------------------------------------------------------

function repliesView(d, act) {
  return html`<div class="wsc wsc-replies" role="group" aria-label="quick replies">${(d.choices || []).map((c) => html`<button class=${c.primary ? 'primary' : ''}
    @click=${() => (c.send ? act.send(c.send) : act.fill(c.fill || c.label))}>${c.label}</button>`)}</div>`;
}

function noticeCard(d) {
  return html`<div class="wsc wsc-quiet" role="status">${icon(d.icon || 'info')}<span>${d.bold ? html`<b>${d.bold}</b> ` : nothing}${d.text || ''}</span></div>`;
}
