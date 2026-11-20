// ws/cards.js — the workspace agent's own cards in its transcript, drawn
// inside the chat kit's `host` frame (docs/frontend-kit.md: the kit frames a
// host block, the host draws it): a tile agent the agent started, a request
// that waits for the person's approval and what came of it, an admin
// change set, the editable brief, a refusal of xbin's, quick replies.
//
// None of them grants anything. ONE item a pending act (D250): a request
// that waits is a compact marker — "Waiting for your approval ↑ open it" —
// which asks xbin's chrome to open it by id (ws/dock.js); the chrome draws
// the grant from xbind's own reading of the request, and the call itself is
// the sheet's (its Details). A start that has a brief is the brief's card
// alone, carrying its request (ws/place.js): the brief's button is the
// backend's — "Review and start…" opens xbind's approval of the start, and
// only where the person's own preference lets tile agents start without
// asking (the backend decides) is it "Start". No fact is joined to another
// with "·": each is an element.
import { html, nothing } from '/vendor/lit-all.min.js';
import { agentCard, requestCard, briefCard, refusalCard, possessive, W } from './words.js';

const icon = (name, label) => (name ? html`<bx-icon name=${name} aria-hidden=${label ? nothing : 'true'} label=${label || nothing}></bx-icon>` : nothing);

/**
 * hostCard(b, act): the inside of a host block — b.type: wsession,
 * request, brief, refusal, redirect, cant, replies, notice. act is the page's:
 * {now(), tileName(path), offline, bag(k), openSession(a), review(q),
 * send(text), fill(text), handTo(tile, draft), briefSave(b, vals), briefStart(b),
 * startDefaults(b) (the brief's start-defaults, read once), signIn(harness)
 * (ask xbin's chrome for a coding agent's sign-in), openScope(o),
 * whatCan(), repaint(), cards()}.
 */
export function hostCard(b, act) {
  switch (b.type) {
    case 'wsession': return sessionCard(b.data, act);
    case 'request': return requestView(b.data, act);
    case 'brief': return briefView(b, act);
    case 'refusal': return refusalView(b.data, act);
    case 'redirect': return redirectView(b.data, act);
    case 'cant': return cantView(b.data, act);
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
      <span class="wsc-name">${icon('grid')}<b>${w.tile}</b>${w.harness ? html`<span class="wsc-chip" data-harness>${w.harness}</span>` : nothing}</span>
      <span class="wsc-st ${w.tone ? 'wsc-' + w.tone : ''}">${icon(w.glyph)}${w.word}</span>
      <button class="wsc-open ${w.waits ? 'primary' : ''}" ?disabled=${!a.session} title=${a.session ? `show this session in ${w.tile}` : 'its session isn’t open yet'}
        @click=${() => act.openSession(a)}>${w.button}${icon('external')}</button>
    </div>
    ${w.line || w.note ? html`<div class="wsc-line">${w.line ? html`<span class="wsc-last">${w.line}</span>` : nothing}${w.note ? html` <b>${w.note}</b>` : nothing}</div>` : nothing}
  </div>`;
}

// ---- a request for the person's approval -------------------------------------------------------

// marker: the compact control that opens the one item in xbin's chrome — a link, never a filled button, never an Allow
const marker = (act, label, open, off, data = 'approval') => html`<button class="link wsc-marker" ?disabled=${off}
  data-marker=${data} title=${off ? W.needsConnection : 'xbin shows it to you, in its own panel'} @click=${open}>${icon('shield')}<span>${label}</span><span aria-hidden="true">↑</span><span class="wsc-muted">${W.markerOpen}</span></button>`;
const parts = (list) => (list && list.length ? html`<span class="wsc-parts">${list.map((p) => html`<span>${p}</span>`)}</span>` : nothing);

function requestView(q, act) {
  const w = requestCard(q, act.now(), act.tileName);
  const local = w.local && w.waits; // a start the brief's own button confirms (the person's preference)
  const head = local ? 'Ready to start' : w.head;
  const btn = local ? '' : w.button;
  const off = act.offline && btn === W.review;
  if (btn === W.review) { // waiting: the compact marker, the what, when — the call is the sheet's (D250)
    return html`<div class="wsc wsc-req wsc-waits" data-req=${q.id} data-ref=${q.ref || ''} data-state=${q.state || ''} data-kind=${w.kind}>
      <div class="wsc-row">${marker(act, W.marker, () => act.review(q), off)}${parts(w.parts)}</div>
      <div class="wsc-what"><span class="wsc-quote" title="the agent’s words">${w.what}</span>${w.tile && !w.what.includes(w.tile) ? html` — <b>${w.tile}</b>` : nothing}</div></div>`;
  }
  const opens = btn === W.open; // of a decided plan, xbin's results
  return html`<div class="wsc wsc-req ${w.waits ? 'wsc-waits' : ''} ${w.todo ? 'wsc-open-fail' : ''}" data-req=${q.id} data-ref=${q.ref || ''} data-state=${q.state || ''} data-kind=${w.kind}>
    <div class="wsc-row"><span class="wsc-name">${icon(w.waits ? 'shield' : w.glyph)}<b>${head}</b></span>${parts(w.parts)}</div>
    <div class="wsc-what"><span class="wsc-quote" title="the agent’s words">${w.what}</span>${w.tile && !w.what.includes(w.tile) ? html` — <b>${w.tile}</b>` : nothing}</div>
    ${w.result ? html`<div class="wsc-line">${w.result}</div>` : nothing}
    ${w.lines && w.lines.length ? html`<ol class="wsc-lines" data-lines>${w.lines.map((l) => html`<li data-n=${l.n} data-result=${l.result}>
      <span class="wsc-ltext">${l.text}</span> <span class="wsc-st ${l.tone ? 'wsc-' + l.tone : ''}">${icon(l.glyph)}${l.word}</span>
      ${l.note ? html`<div class="wsc-err">${l.note}</div>` : nothing}</li>`)}</ol>` : nothing}
    ${w.todo ? html`<div class="wsc-line wsc-wrap" data-todo>${w.todo}</div>` : nothing}
    ${local ? html`<div class="wsc-line">Start it from the brief.</div>` : nothing}
    ${btn === W.retryMarker ? html`<div class="wsc-btns">${marker(act, btn, () => act.openNeeds(), act.offline, 'retry')}</div>`
      : btn ? html`<div class="wsc-btns"><button ?disabled=${off} title=${btn === W.open ? 'xbin shows each line’s result' : ''}
      @click=${() => (opens ? act.review(q) : act.send(`Please ask again: ${q.title || w.what}`))}>${btn}</button></div>` : nothing}
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
    <div class="wsc-muted wsc-by" data-brief-by>${w.by}</div>
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
      ${w.waiting && !editing ? marker(act, W.marker, () => act.review(w.request), act.offline) : nothing}
      ${w.retry && !editing ? marker(act, W.retryMarker, () => act.openNeeds(), act.offline, 'retry') : nothing}
      ${w.waiting && !editing ? parts(w.parts) : nothing}
    </div>
    ${w.chips.length && w.editable ? html`<div class="wsc-row wsc-runs" data-runs-on><span class="wsc-muted">Runs on</span>
      <span class="wsc-chips">${w.chips.map((c) => html`<span class="wsc-chip ${c.tone ? 'wsc-' + c.tone : ''}">${c.text}</span>`)}</span>
      ${w.pickNote ? html`<span class="wsc-muted">${w.pickNote}</span>` : nothing}</div>` : nothing}
    ${w.outcome ? html`<div class="wsc-line wsc-wrap" data-outcome>${w.outcome}</div>` : nothing}
    ${w.signin ? html`<div class="wsc-line wsc-wrap wsc-signin" data-signin=${w.signin.harness}>${icon('warning')}<span>Needs your ${w.signin.name} sign-in: without it the tile agent starts signed out. xbin shows the sign-in; nothing of it passes through your agent.</span>
      <button class="wsc-signin-go" ?disabled=${act.offline} @click=${() => act.signIn(w.signin.harness)}>${w.signin.button}</button></div>` : nothing}
    ${w.editable ? html`<div class="wsc-line wsc-wrap">${w.note}</div>` : nothing}
  </div>`;
}

// ---- refusals (cards.refusals) -------------------------------------------------------------------

const WAYS = {
  scope: (act, p) => html`<button class=${p ? 'primary' : ''} @click=${() => act.openScope({})}>Open scope</button>`,
  model: (act, p) => html`<button class=${p ? 'primary' : ''} @click=${() => act.openScope({ models: true })}>Change the model…</button>`,
  whatCan: (act, p) => html`<button class=${p ? 'primary' : ''} @click=${() => act.whatCan()}>${W.whatCanItDo}</button>`,
};
function refusalView(f, act) {
  const r = refusalCard(f, act.tileName);
  if (r.quiet) return html`<div class="wsc wsc-quiet" role="status" data-refusal=${r.kind}>${icon(r.icon)}${r.text}</div>`;
  return html`<div class="wsc wsc-refusal ${r.kind === 'policy' || r.kind === 'taint' ? 'wsc-policy' : ''}" data-refusal=${r.kind}>
    <div class="wsc-row"><span class="wsc-name">${icon(r.icon)}<b>${r.title}</b></span></div>
    ${r.text ? html`<p class="wsc-p">${r.text}</p>` : nothing}
    ${r.note ? html`<p class="wsc-p wsc-muted">${r.note}</p>` : nothing}
    ${r.ways.length ? html`<div class="wsc-btns">${r.ways.map((w, i) => WAYS[w](act, i === 0))}</div>` : nothing}</div>`;
}

// ---- the agent's question, when it is a redirect or a "can't" ----------------------------------------

// a request about code: the tile's own agent takes it as written, or the person says the outcome
function redirectView(d, act) {
  const tile = d.tile ? act.tileName(d.tile) || d.tile : '';
  return html`<div class="wsc wsc-redirect" data-ask="redirect">
    <div class="wsc-row"><span class="wsc-name">${icon('code')}<b>The workspace agent doesn’t open or edit code. Two ways forward:</b></span></div>
    ${d.question ? html`<p class="wsc-p wsc-quote">${d.question}</p>` : nothing}
    ${d.waits ? html`<div class="wsc-ways">
      ${tile ? html`<button class="wsc-way" @click=${() => act.handTo(d.tile, d.draft)}>${icon('external')}<span><b>Hand this to ${possessive(tile)} own agent</b>
        <span class="wsc-muted">It opens in ${tile} with your message as written.</span></span></button>` : nothing}
      <button class="wsc-way" @click=${() => act.fill('The outcome I want: ')}>${icon('pencil')}<span><b>Tell me the outcome instead</b>
        <span class="wsc-muted">Say what should be true when it’s done; the agent briefs the tile’s agent.</span></span></button></div>`
      : html`<div class="wsc-line">${d.answer != null ? 'You answered in the conversation.' : 'A newer question replaced this one.'}</div>`}
  </div>`;
}

// what it can't do, and the nearest things it can (each a reply)
function cantView(d, act) {
  return html`<div class="wsc wsc-refusal" data-ask="cant">
    <div class="wsc-row"><span class="wsc-name">${icon('info')}<b>Can’t do this</b></span></div>
    ${d.question ? html`<p class="wsc-p">${d.question}</p>` : nothing}
    ${d.waits && d.choices.length ? html`<div class="wsc-ways">${d.choices.map((c) => html`<button class="wsc-way" @click=${() => act.send(c)}>${icon('caret-right')}<span>${c}</span></button>`)}</div>` : nothing}
    ${d.answer != null ? html`<div class="wsc-line">You answered: ${d.answer}</div>` : nothing}
    <div class="wsc-btns">${WAYS.whatCan(act)}</div></div>`;
}

// ---- quick replies ------------------------------------------------------------------------
// The agent's suggestions: a click fills the composer, to edit or send —
// never a message by itself (xbin D241).

function repliesView(d, act) {
  return html`<div class="wsc wsc-replies" role="group" aria-label="quick replies">${(d.choices || []).map((c) => html`<button class=${c.primary ? 'primary' : ''}
    @click=${() => act.fill(c.send || c.fill || c.label)}>${c.label}</button>`)}</div>`;
}

function noticeCard(d) {
  return html`<div class="wsc wsc-quiet" role="status">${icon(d.icon || 'info')}<span>${d.bold ? html`<b>${d.bold}</b> ` : nothing}${d.text || ''}</span></div>`;
}
