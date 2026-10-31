// project-coord.js — the coordinator on a project's overview page, and the
// page's height (D214; API.md
// §Projects in the UI), on the web:
//
//   coordSecTpl(p, pv, it)  the coordinator's section (#pcoord): a header —
//                           its state, model and tokens today, Stop while it
//                           works (POST /runs/{id}/interrupt, as the chat's
//                           Stop: what it had not read yet comes back into
//                           the input), Open chat ↗ —; its goal in one line
//                           (#pcoord-goal; a click shows all of it; idle:
//                           what wakes it, then the goal); its newest
//                           messages in a window of fixed height
//                           (#pcoord-chat: more read than fit, the oldest
//                           clipped at the top under a fade with "earlier
//                           messages are in its chat", tool steps one line
//                           each, no scrollbar of its own, nothing below it
//                           moves as one arrives); on a short tile its input
//                           (#pcoord-msg) ends the section
//   splitTpl(pv)            the bar between the board and it (#pov-split,
//                           a separator): drag it, Arrow keys ±24 px (Shift
//                           ±96), double-click resets; the chat keeps 100 px,
//                           the board 120; kept per person (o.setChatHeight)
//   pageBar(p)              the page's bottom bar (page-composer.js): the
//                           coordinator's (project-feed.js coordComposer) —
//                           none on a short tile's overview, where the input
//                           ends the coordinator's section instead
//   onOverview(p)           the project's page is drawn as its overview: a
//                           backend that serves it (model/project-overview.js
//                           `supported`), a project with tasks of its own
//
// The height (the paint hook, ext.paint): on the overview of a tile at least
// CHAT.tall (600) px tall the page fits the tile (#main.pov-fit): the
// timeline never scrolls, the board takes what is left and scrolls inside
// it, the chat window gives way before the board goes under 120 px, and the
// input is the bar at the bottom, always in view. Shorter — or too short for
// the rest with the board's 120 px and the chat's 100 (a strip of two or
// three rows on a narrow tile) — the page scrolls as one (#main.pov-on
// only), nothing clipped. A ResizeObserver on #main says which. Leaving the
// board (another tab, a conversation, another page) takes the classes off,
// and the overview's refresh (o.show / o.hide) with them.
import { html, nothing, repeat, live } from '/vendor/lit-all.min.js';
import { ext, ctx } from './web-ext.js';
import { can } from './model/projects.js';
import { projectFeed, COORD_EMPTY } from './model/project-feed.js';
import { projectOverview, coordWords, goalWords, wakeTip, fmtTokens, TIPS, CHAT } from './model/project-overview.js';
import { blocksTpl } from './chat-cards.js';
import { coordComposer } from './project-feed.js';
import { draftOf, setDraft } from './page-composer.js';
import { syncTips } from './tooltips.js';

const $ = (id) => document.getElementById(id);
const feed = () => projectFeed(ctx.app);
const ov = () => projectOverview(ctx.app);
const caret = (open) => html`<bx-icon name=${open ? 'caret-down' : 'caret-right'}></bx-icon>`;

let mainH = 0;        // #main's height: the tile's
let ro = null;
let laid = null;      // the layout drawn last: null (not the overview's board), 'fit', 'short'
let shortAs = null;   // the layout the templates draw: true (short), false (fit), null (not yet decided: by the tile's height)
let barH = 0;         // the bottom bar's height, when it last showed
const goalOpen = new Set(); // projects whose goal line shows all of it

ext.register({ paint: (v) => sync(v) });

/** onOverview(p): the open project's page is drawn as its overview (any tab). */
export function onOverview(p) {
  if (!p || p.form || p.opened == null) return false;
  const pv = p.view();
  return !!pv && pv.kind !== 'team' && ov().item(pv.id).supported;
}
const onBoard = (p) => onOverview(p) && p.tab === 'board';

function watch() {
  if (ro) return;
  const m = $('main');
  if (!m) return;
  mainH = m.clientHeight;
  ro = new ResizeObserver(() => {
    mainH = m.clientHeight;
    if (!laid) return;
    const s = decide();
    if (s !== shortAs) { shortAs = s; ctx.paint(); } else measure();
  });
  ro.observe(m);
}
/** tall(): the tile is at least CHAT.tall px high. */
export function tall() { watch(); return mainH >= CHAT.tall; }
const isShort = () => shortAs ?? !tall();

// fits: the overview's least — its fixed parts, the board's 120 px, the coordinator's header and goal and
// the chat's 100 px — within the tile less its top bar (and notices) and the bottom bar. Its parts are
// measured as they are: none of them depends on the layout (the board and the chat are counted at their least)
function fits() {
  const main = $('main'), tl = $('timeline'), pg = $('pov');
  if (!main || !tl || !pg) return true;
  const bar = main.querySelector(':scope > .composer');
  if (bar && !bar.hidden && bar.offsetHeight) barH = barH ? Math.min(barH, bar.offsetHeight) : bar.offsetHeight; // its least: one line
  let avail = main.clientHeight - (barH || 52);
  for (const el of main.children) if (el !== tl && el !== bar && el.offsetParent !== null && getComputedStyle(el).position !== 'absolute') avail -= el.offsetHeight;
  const ps = getComputedStyle(pg);
  let need = parseFloat(ps.paddingTop) + parseFloat(ps.paddingBottom);
  for (const el of pg.children) {
    if (el.id === 'pov-board') need += CHAT.boardMin;
    else if (el.id === 'pcoord') need += 2 + CHAT.min + [...el.children].filter((c) => c.id !== 'pcoord-chat' && c.id !== 'pcoord-in').reduce((n, c) => n + c.offsetHeight, 0);
    else { const cs = getComputedStyle(el); need += el.offsetHeight + parseFloat(cs.marginTop) + parseFloat(cs.marginBottom); }
  }
  return avail >= need;
}
const decide = () => !(tall() && fits()); // short?

/** pageBar(p): the bottom bar's job on the Projects page. */
export function pageBar(p) {
  const pc = coordComposer(p);
  return pc && onBoard(p) && isShort() ? null : pc;
}

// --- the coordinator's section ---------------------------------------------------------------

const WORD = { working: ['working', 'run'], waiting: ['waiting for you', 'warn'], failed: ['failed', 'bad'], idle: ['idle', 'idle'], none: ['not started', 'idle'] };

// its state: the run's own (the session's, live) once the preview knows it, else the overview's
function stateOf(pr, cw) {
  const key = pr.shown && pr.run ? pr.state.key : pr.shown && !pr.loading ? 'none' : cw.state;
  const [word, tone] = WORD[key] || WORD.none;
  return { key, word, tone };
}

/** coordSecTpl(p, pv, it): the coordinator's section of the overview (it: the overview's item). */
export function coordSecTpl(p, pv, it) {
  if (!can(pv).act) return nothing;
  const f = feed();
  const c = f.coord(pv.id);
  if (c.missing) return nothing;
  const pr = f.preview(pv.id);
  const cw = coordWords(it.data);
  const st = stateOf(pr, cw);
  const job = coordComposer(p);
  const open = async () => { const run = await f.openCoordinator(pv.id); if (run && run.id) ctx.app.select(run.id); };
  const meta = [cw.model, cw.tokensToday ? `${fmtTokens(cw.tokensToday)} tokens today` : ''].filter(Boolean).join(' · ');
  const short = isShort();
  return html`<section class="povco" id="pcoord" data-state=${st.key} aria-label="Coordinator">
    <div class="pch"><h3 data-tip=${TIPS.coordinator}>Coordinator</h3>
      <span class="chip pcst" id="pcoord-state" data-tone=${st.tone} data-tip=${TIPS.coordinator}><span class="pdot" data-tone=${st.tone}></span>${st.word}</span>
      ${meta ? html`<span class="pcmeta">${meta}</span>` : nothing}
      ${c.busy === 'send' ? html`<span class="pcnote" id="pcoord-note">Sending…</span>`
        : c.err ? html`<span class="err small" id="pcoord-err">${c.err}</span>` : c.note ? html`<span class="pcnote" id="pcoord-note">${c.note}</span>` : nothing}
      <span class="sp"></span>
      ${st.key === 'working' && pr.run ? html`<button class="btn ghost btnsm" id="pcoord-stop" data-tip=${TIPS.stop} ?disabled=${!!c.busy}
        @click=${() => stop(pv.id, job)}>${c.busy === 'stop' ? 'Stopping…' : 'Stop'}</button>` : nothing}
      <button class="btn ghost btnsm" id="pcoord-open" data-tip=${TIPS.open} ?disabled=${!!c.busy} @click=${open}>${c.busy === 'open' ? 'Opening…' : html`Open chat<bx-icon name="popout"></bx-icon>`}</button>
    </div>
    ${goalTpl(pv.id, it.data, pr)}
    ${chatTpl(pv, pr, f, open, short)}
    ${short && job ? inputTpl(job) : nothing}
  </section>`;
}

// stop interrupts its turn; what it had not read comes back into the input (the bar's, or the section's)
async function stop(pid, job) {
  const text = await feed().stopCoordinator(pid);
  if (!text) return;
  const key = job ? job.key : `coord:${pid}`;
  setDraft(key, [text, draftOf(key)].filter(Boolean).join('\n\n'));
  ctx.paint();
  const box = $('pcoord-msg') || $('msg');
  if (box) box.focus();
}

function goalTpl(pid, d, pr) {
  if (!d) return nothing;
  const g = goalWords(d);
  if (g.kind === 'none') return nothing;
  const open = goalOpen.has(pid);
  const toggle = (e) => {
    if (e.target.closest && e.target.closest('.chip')) return;
    if (open) goalOpen.delete(pid); else goalOpen.add(pid);
    ctx.paint();
  };
  const now = g.now || pr.line || '';
  return html`<div class="pgoal ${open ? 'open' : ''}" id="pcoord-goal" data-kind=${g.kind} role="button" tabindex="0" aria-expanded=${open ? 'true' : 'false'}
      @click=${toggle} @keydown=${(e) => { if (e.target === e.currentTarget && (e.key === 'Enter' || e.key === ' ')) { e.preventDefault(); toggle(e); } }}>
    ${g.kind === 'idle' ? html`<span class="gl" data-tip=${TIPS.idle}>Idle — wakes on</span>
        <span class="waits">${g.wakes.map((w) => html`<span class="chip" data-kind=${w.kind} data-tip=${wakeTip(w)}>${w.label}</span>`)}</span>
        ${g.text ? html`<span class="now">Goal: ${g.text}</span>` : nothing}`
      : html`<span class="gl" data-tip=${TIPS.goal}>Goal</span><span class="gt">${g.text}</span>${now ? html`<span class="now">Now: ${now}</span>` : nothing}`}
    <span class="exp">${caret(open)}</span></div>`;
}

const STEP = new Set(['tool', 'agent', 'notice']);
const RUNNING = new Set(['running', 'writing', 'waiting']);

// a tool step, a subagent or a notice: one line (the whole call is behind Open chat)
function stepTpl(b) {
  if (b.k === 'notice') return html`<div class="pcstep" data-k=${b.id} data-kind="notice">${b.text.split('\n')[0].replace(/^\[|\]$/g, '').replace(/ — .*$/, '')}</div>`;
  const name = b.k === 'agent' ? 'subagent' : b.name;
  const what = b.k === 'agent' ? b.task || b.headline : b.headline;
  return html`<div class="pcstep" data-k=${b.id} data-kind=${b.k} data-state=${b.state || ''}><b>${name}</b>${what && what !== name ? html`<span>${what}</span>` : nothing}
    ${RUNNING.has(b.state) ? html`<span class="run">· running</span>` : b.state === 'error' ? html`<span class="bad">· failed</span>` : nothing}</div>`;
}

// the window's height: the person's (dragged), else the default — a short tile's never taller than it allows
function chatHeight(short) {
  const pref = ov().chatHeight;
  const h = pref ?? (short ? CHAT.short : CHAT.def);
  return short ? Math.max(CHAT.min, Math.min(h, mainH - 140)) : h;
}

function chatTpl(pv, pr, f, open, short) {
  const ui = ctx.app.session.ui;
  const one = (text) => html`<div class="muted small pcempty">${text}</div>`;
  const body = !pr.shown ? one('Its conversation is behind Open chat.')
    : !pr.run ? (pr.err ? html`<div class="small pcempty"><span class="err">${pr.err}</span></div>` : one(pr.loading ? 'loading…' : COORD_EMPTY))
      : pr.recent ? (pr.recent.length ? repeat(pr.recent, (b) => b.id, (b) => (STEP.has(b.k) ? stepTpl(b) : blocksTpl([b], ui, 1))) : one('Nothing said yet.'))
        : pr.err ? html`<div class="small pcempty" role="status"><span class="err">Couldn't read its latest messages: ${pr.err}</span>
            <button class="lnk" data-act="retry" @click=${() => f.readTail(pv.id)}>Retry</button></div>`
          : one('loading…');
  return html`<div class="pcchat" id="pcoord-chat" data-older=${pr.older ? '1' : nothing} style=${`height:${chatHeight(short)}px`}>
    <div class="pcearlier">earlier messages are in <button class="lnk" data-tip=${TIPS.chat} @click=${open}>its chat</button></div>
    <div class="pcmsgs">${body}</div></div>`;
}

// the input, on a short tile: the bar's job (its draft moves between them)
function inputTpl(job) {
  const send = () => {
    const t = draftOf(job.key);
    if (!t.trim() || job.busy) return;
    job.send(t).then((sent) => { if (sent) { setDraft(job.key, ''); ctx.paint(); } });
  };
  const grow = (el) => { el.style.height = 'auto'; el.style.height = Math.min(el.scrollHeight, 160) + 'px'; };
  return html`<div class="pcin" id="pcoord-in">
    <textarea id="pcoord-msg" rows="1" aria-label="Message the coordinator" placeholder=${job.placeholder} .value=${live(draftOf(job.key))}
      @input=${(e) => { setDraft(job.key, e.target.value); grow(e.target); }}
      @keydown=${(e) => { if (e.key === 'Enter' && !e.shiftKey && !e.isComposing && e.keyCode !== 229) { e.preventDefault(); send(); } }}></textarea>
    <button class="btn" id="pcoord-sendbtn" data-tip=${TIPS.send} ?disabled=${job.busy} @click=${send}>Send</button></div>`;
}

// --- the bar between the board and the coordinator -------------------------------------------

// bounds: what the chat window may be — at least CHAT.min; fitting the tile, the board keeps CHAT.boardMin;
// a page that scrolls (a short tile), within the tile
function bounds() {
  const chat = $('pcoord-chat'), board = $('pov-board');
  const h0 = chat ? chat.offsetHeight : CHAT.def;
  if (!isShort() && board) return [CHAT.min, Math.max(CHAT.min, h0 + board.clientHeight - CHAT.boardMin)];
  return [CHAT.min, Math.max(CHAT.min, mainH - 140)];
}
const clamp = (v, [lo, hi]) => Math.round(Math.min(hi, Math.max(lo, v)));

function drag(e) {
  if (e.button !== 0) return;
  const bar = e.currentTarget, chat = $('pcoord-chat');
  if (!chat) return;
  e.preventDefault();
  const y0 = e.clientY, h0 = chat.offsetHeight, b = bounds();
  let v = h0;
  bar.setPointerCapture(e.pointerId);
  bar.classList.add('drag');
  bar.setAttribute('aria-valuemax', String(b[1]));
  const move = (ev) => { v = clamp(h0 - (ev.clientY - y0), b); chat.style.height = v + 'px'; bar.setAttribute('aria-valuenow', String(v)); };
  const up = () => {
    bar.classList.remove('drag');
    bar.removeEventListener('pointermove', move); bar.removeEventListener('pointerup', up); bar.removeEventListener('pointercancel', up);
    if (v !== h0) ov().setChatHeight(v);
  };
  bar.addEventListener('pointermove', move); bar.addEventListener('pointerup', up); bar.addEventListener('pointercancel', up);
}

function keys(e) {
  const step = e.shiftKey ? 96 : 24;
  const d = e.key === 'ArrowUp' ? step : e.key === 'ArrowDown' ? -step : 0;
  const chat = $('pcoord-chat');
  if (!d || !chat) return;
  e.preventDefault();
  const b = bounds();
  e.currentTarget.setAttribute('aria-valuemax', String(b[1]));
  ov().setChatHeight(clamp(chat.offsetHeight + d, b));
}

/** splitTpl(pv): the bar between the board and the coordinator (none without a coordinator section). */
export function splitTpl(pv) {
  if (!can(pv).act || feed().coord(pv.id).missing) return nothing;
  return html`<div class="povsplit" id="pov-split" role="separator" aria-orientation="horizontal" tabindex="0" aria-valuemin=${CHAT.min}
    aria-label="Share the height between the tasks and the coordinator" data-tip=${TIPS.split}
    @pointerdown=${drag} @keydown=${keys} @dblclick=${() => ov().setChatHeight(null)}><i></i></div>`;
}

// --- after each paint: the layout, the window's clip, the refresh ----------------------------------

function sync(v) {
  const app = ctx.app, main = $('main');
  if (!app || !main) return;
  const p = app.projects;
  const on = !v && app.page === 'projects' && onBoard(p);
  const short = on ? decide() : null;
  if (on && short !== isShort()) queueMicrotask(() => ctx.paint()); // drawn for the other layout: once more
  shortAs = short;
  main.classList.toggle('pov-on', on);
  main.classList.toggle('pov-fit', on && !short);
  if (on) ov().show(p.opened); else if (laid) ov().hide();
  laid = on ? (short ? 'short' : 'fit') : null;
  if (on) measure();
  syncTips();
}

// measure: the window says when it clips its oldest (the fade, "earlier messages…"); the bar, the window's height
function measure() {
  const ch = $('pcoord-chat');
  if (!ch) return;
  const m = ch.querySelector('.pcmsgs');
  const clip = !!m && m.offsetHeight > ch.clientHeight + 1;
  if ((ch.dataset.clip === '1') !== clip) ch.dataset.clip = clip ? '1' : '';
  const sp = $('pov-split');
  if (sp) sp.setAttribute('aria-valuenow', String(ch.offsetHeight));
}

const style = document.createElement('style');
style.textContent = `
  /* the overview (D214): its own padding; dense */
  .main.pov-on #timeline { padding: 0; }
  .projs-page.pov { max-width: none; margin: 0; padding: 6px 8px 8px; display: flex; flex-direction: column; box-sizing: border-box; min-width: 0;
    container: pov / inline-size; }
  .projs-page.pov > * { flex-shrink: 0; }
  /* a tile at least 600 px tall: the page fits it — the board takes what is left (and scrolls inside it), the
     chat window gives way before the board goes under 120 px; the timeline never scrolls */
  .main.pov-fit #timeline { overflow: hidden; display: flex; flex-direction: column; }
  .main.pov-fit .projs-page.pov { flex: 1 1 auto; min-height: 0; overflow: hidden; }
  .main.pov-fit .projs-page.pov .povboard { flex: 1 1 0; min-height: 120px; overflow: auto; }
  .main.pov-fit .projs-page.pov .povboard > .pboard { min-height: 100%; }
  .main.pov-fit .projs-page.pov .povco { flex: 0 1 auto; min-height: 0; }
  .main.pov-fit .projs-page.pov .pcchat { flex: 0 1 auto; min-height: 100px; }
  /* the top bar: the project's name, its repos, Status, the tabs */
  .top .title .prepos { font: var(--bx-font-meta); color: var(--bx-muted); margin-left: 8px; }
  .top .ptabs { display: inline-flex; gap: 2px; }
  .top .ptabs [aria-selected="true"] { background: var(--bx-panel-2); box-shadow: inset 0 -2px 0 var(--bx-accent); }
  .projs-page.povtab .pfeed, .projs-page.povtab .pset { margin-top: 0; }
  /* 2. the board: the columns side by side (sideways on a narrow page) */
  .projs-page.pov .pbar { margin: 6px 0 4px; gap: 4px; }
  .projs-page.pov .pbar h3 { margin: 0 8px 0 0; font: var(--bx-font-micro); letter-spacing: var(--bx-tracking-micro); text-transform: uppercase; color: var(--bx-muted); cursor: help; }
  .projs-page.pov .povboard { overflow-x: auto; }
  .projs-page.pov .pboard { grid-template-columns: repeat(5, minmax(170px, 1fr)); gap: 4px; align-items: stretch; }
  @container pov (max-width: 560px) { .projs-page.pov .pboard { grid-template-columns: repeat(5, 240px); } }
  .projs-page.pov .pcol { background: var(--bx-panel); border: 1px solid var(--bx-border); padding: 3px 4px 0; }
  .projs-page.pov .pcolh { margin: 1px 2px 3px; }
  .projs-page.pov .ptask { background: var(--bx-panel-2); padding: 3px 6px 4px; margin-bottom: 4px; }
  .projs-page.pov .ptm, .projs-page.pov .ptc { margin-top: 2px; }
  .projs-page.pov .pform { margin: 0 0 6px; }
  /* 3. the bar */
  .pov .povsplit { height: 9px; cursor: row-resize; display: flex; align-items: center; justify-content: center; touch-action: none; position: relative; }
  .pov .povsplit::before { content: ""; position: absolute; left: 0; right: 0; top: 4px; height: 1px; }
  .pov .povsplit:hover::before, .pov .povsplit:focus-visible::before, .pov .povsplit.drag::before { background: var(--bx-accent); }
  .pov .povsplit i { width: 32px; height: 3px; box-sizing: border-box; border-top: 1px solid var(--bx-muted); border-bottom: 1px solid var(--bx-muted); opacity: .7; position: relative; }
  .pov .povsplit:focus-visible { outline: none; }
  .pov .povsplit:focus-visible i, .pov .povsplit.drag i { border-color: var(--bx-accent); opacity: 1; }
  /* 4. the coordinator */
  .pov .povco { border: 1px solid var(--bx-border); border-radius: var(--bx-radius); background: var(--bx-panel); display: flex; flex-direction: column; overflow: hidden; }
  .pov .povco > * { flex-shrink: 0; }
  .pov .pch { display: flex; flex-wrap: wrap; gap: 4px 8px; align-items: center; padding: 3px 8px; border-bottom: 1px solid var(--bx-border); }
  .pov .pch h3 { margin: 0; font: var(--bx-font-micro); letter-spacing: var(--bx-tracking-micro); text-transform: uppercase; color: var(--bx-muted); cursor: help; }
  .pov .pch .sp { flex: 1; }
  .pov .pch .pcst { gap: 5px; }
  .pov .pch .pcmeta, .pov .pch .pcnote { font: var(--bx-font-meta); color: var(--bx-muted); }
  @container pov (max-width: 560px) { .pov .pch .pcmeta { display: none; } .projs-page.pov .pbar .pq { flex: 1 1 120px; width: auto; } }
  .pov .pgoal { display: flex; gap: 8px; padding: 3px 26px 3px 8px; border-bottom: 1px solid var(--bx-border); background: var(--bx-panel-2); align-items: center;
    white-space: nowrap; min-width: 0; overflow: hidden; cursor: pointer; position: relative; }
  .pov .pgoal .gl { font: var(--bx-font-micro); letter-spacing: var(--bx-tracking-micro); text-transform: uppercase; color: var(--bx-muted); flex: none; cursor: help; }
  .pov .pgoal .gt { flex: 0 1 auto; min-width: 0; overflow: hidden; text-overflow: ellipsis; }
  .pov .pgoal .now { flex: 1 1 0; min-width: 0; overflow: hidden; text-overflow: ellipsis; font: var(--bx-font-meta); color: var(--bx-muted); }
  .pov .pgoal .waits { display: inline-flex; gap: 4px; flex: none; }
  .pov .pgoal .exp { position: absolute; right: 6px; top: 5px; display: inline-flex; color: var(--bx-muted); }
  .pov .pgoal.open { white-space: normal; flex-wrap: wrap; align-items: baseline; }
  .pov .pgoal.open .gt, .pov .pgoal.open .now { overflow-wrap: anywhere; }
  .pov .pgoal.open .now { flex-basis: 100%; }
  .pov .pgoal.open .waits { flex-wrap: wrap; }
  /* the conversation: a window of fixed height on its newest messages — a new one pushes the oldest out at
     the top, nothing below it moves; no scrollbar of its own (the whole chat is behind Open chat) */
  .pov .pcchat { position: relative; overflow: hidden; overflow: clip; padding: 0 8px 4px; display: flex; flex-direction: column; justify-content: flex-end; box-sizing: border-box; }
  .pov .pcchat > .pcmsgs { flex: none; display: flex; flex-direction: column; min-width: 0; }
  .pov .pcchat[data-clip="1"]::before, .pov .pcchat[data-older]::before { content: ""; position: absolute; inset: 0 0 auto 0; height: 44px; z-index: 1; pointer-events: none;
    background: linear-gradient(var(--bx-panel), transparent); }
  .pov .pcearlier { position: absolute; top: 2px; left: 8px; z-index: 2; font: var(--bx-font-meta); color: var(--bx-muted); background: var(--bx-panel); padding: 0 4px; }
  .pov .pcchat:not([data-clip="1"]):not([data-older]) .pcearlier { display: none; }
  .pov .pcearlier .lnk, .pov .pcempty .lnk { border: 0; background: none; padding: 0; min-height: 0; font: inherit; color: var(--bx-link); cursor: pointer; }
  .pov .pcchat .msg { margin: 5px 0; max-width: 92ch; }
  .pov .pcchat .msg.user { align-self: flex-end; }
  .pov .pcchat .msg .acts { display: none; }
  .pov .pcstep { font: var(--bx-font-meta); color: var(--bx-muted); border-left: 2px solid var(--bx-border); padding: 0 8px; margin: 2px 0; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .pov .pcstep b { color: var(--bx-text); font-weight: 600; margin-right: 6px; }
  .pov .pcstep .run { color: var(--bx-accent); margin-left: 4px; }
  .pov .pcstep .bad { color: var(--bx-danger); margin-left: 4px; }
  .pov .pcempty { padding: 8px 0; }
  .pov .pcin { display: flex; gap: 6px; align-items: flex-end; padding: 5px 8px; border-top: 1px solid var(--bx-border); }
  .bx .pov .pcin textarea { flex: 1; min-width: 0; resize: none; min-height: var(--bx-control-h); max-height: 160px; box-sizing: border-box; }
`;
document.head.append(style);
