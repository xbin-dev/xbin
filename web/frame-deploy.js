/**
 * frame-deploy.js — the terminal window's live reload glue, beside bx-frame
 * (the frame is near its size budget): it loads the tile's deployments state
 * (GET /api/xbin/deployments?tile=, docs/protocol.md), follows the tile's
 * `deployments` events, and draws what web/deploy-state.js decides — the
 * title bar's live-reload chip and Reload now offer (the degraded bar's
 * compact chip in the title row), the zero state's one entry point, the
 * chip's menu, the launcher's banner, and the "📌 pinned" chip over the tile
 * for people whose saves it concerns. Every operation confirms from a dry
 * run of the exact request, through the frame's own dialog (f._ask), and
 * sends the state's seq with it, so what the dialog showed is what happens.
 * Grey lines in the tile's open terminals (bx-terminal note()) say when the
 * tile-wide state changes.
 *
 * `f` is the BxFrame. Nothing here decides who may do what: the state's
 * `can`/`why` and the dry run's `impact` do, rendered by deploy-state.js.
 * A state of null — an xbind without tile deployments (a plain 404/405), a
 * route not built yet (501), any failure — draws nothing, exactly today's
 * window; the zero state draws only the entry point. The state is loaded when
 * the window opens, on every relist, after the events socket reconnects (it
 * may be another binary now), and on this tile's `deployments` events; the
 * chip over the tile starts from the /components summary, so a tile in the
 * zero state costs no request while its window is closed.
 */
import { html, css, nothing } from 'lit';
import * as events from '/vendor/events-socket.js';
import { infoFor } from '/vendor/frame-info.js';
import {
  viewModel, chipItems, toMenu, confirmation, refusal, conflict, applyEvent, notice,
  frameChip as chipOverTile, apiOptions,
} from '/vendor/deploy-state.js';

const SHEET = typeof matchMedia === 'function' ? matchMedia('(max-width: 820px)') : { matches: false };
const OP_PATH = { pause: 'live-reload/pause', resume: 'live-reload/resume', reloadNow: 'live-reload/now', attach: 'live-reload/attach' };
const TAKES_DEPLOYMENT = new Set(['resume', 'attach']);
// what makes a terminal line: a record change of these fields, a final deploy
const NOTED = ['liveReload', 'primary', 'protectedPrimary'];

// per frame: {state, loaded, gen, pinned (the /components summary said so),
// busy, timer, queue, seen (the state the terminals were last told about:
// set by the first load and by each event burst, never by an operation's own
// answer, which may land before its event)}
const per = new WeakMap();
const rec = (f) => { let r = per.get(f); if (!r) per.set(f, (r = { state: null, loaded: false, gen: 0, queue: [] })); return r; };

// a view-as session: the viewed user's name, for "<user> may do this" (whoami)
let viewing;
function learnViewing() {
  if (viewing !== undefined) return;
  viewing = '';
  fetch('/api/xbin/whoami').then((r) => (r.ok ? r.json() : null)).then((w) => {
    viewing = w?.readOnly ? String(w.id || '') : '';
    for (const f of events.mountedFrames) if (per.has(f)) f.requestUpdate?.();
  }).catch(() => { });
}

// A reconnected events socket may be another binary: every window that holds
// a state loads it again (and drops it if the feature is gone), when
// events-socket.js reports reconnects (onReconnect). Without it the state
// still reloads on open, on relist and when the page becomes visible again.
events.onReconnect?.(() => { for (const f of events.mountedFrames) if (per.get(f)?.loaded) loadDeploy(f); });

// the active tab's echo, for the API select's entries (the tab owns it)
const echo = (f, i = f._active) => { const t = f._sessions?.[i]; return t ? { api: t.api !== false } : {}; };
const opts = (f) => ({ now: Date.now(), viewing: viewing || '', panel: false, session: echo(f) });
const vmOf = (f) => viewModel(per.get(f)?.state ?? null, opts(f));

// loadDeploy(f) → the state, fetched; null when this xbind can't answer it.
export async function loadDeploy(f) {
  const r = rec(f);
  const gen = ++r.gen;
  let s = null;
  try {
    const res = await fetch(`/api/xbin/deployments?tile=${encodeURIComponent(f.src)}`);
    const j = res.ok ? await res.json() : null;
    if (j && typeof j === 'object' && j.tile === f.src) s = j;
  } catch { /* no state: today's window */ }
  if (gen !== r.gen) return r.state;
  r.state = s;
  if (!r.loaded) r.seen = s;
  if (!s?.record) r.pinned = false; // the page's summary is older than this answer
  r.loaded = true;
  if (s?.caller?.readOnly) learnViewing();
  f.requestUpdate();
  return s;
}

// Called with the window's other tile state (frame-launcher.js loadTileState):
// while the window is open, or when the tile is out of the zero state (the
// chip over the tile follows it). A closed window on a zero-state tile costs
// nothing.
export function loadDeployIfShown(f) {
  const r = per.get(f);
  if (f._termOpen || r?.pinned || r?.state?.record) loadDeploy(f);
}

// deployMount(f): a frame connected. The /components summary (the tile's
// primary is pinned) is what starts a load while the window is closed.
export function deployMount(f) {
  infoFor(f.src).then((info) => {
    if (info?.path !== f.src || !info.deployments?.pinned || !f.isConnected) return;
    rec(f).pinned = true;
    loadDeploy(f);
  }).catch(() => { });
}

// onDeployEvent(f, e): a `deployments` event (the tile's bare path; the
// deployment is named inside data). op work-tree moves the count in place;
// record, deploy and data changes load the state again (debounced), then
// each open terminal gets its line.
export function onDeployEvent(f, e) {
  const d = e?.data;
  if (e.component !== f.src || !d) return;
  const r = rec(f);
  if (r.state) {
    const a = applyEvent(r.state, d);
    if (a.state !== r.state) { r.state = a.state; f.requestUpdate(); }
    if (!a.refetch) return;
  } else if (d.op !== 'record' && d.op !== 'deploy' && d.op !== 'data') return;
  if ((d.op === 'record' && (d.what || []).some((w) => NOTED.includes(w))) || (d.op === 'deploy' && (d.result === 'ok' || d.result === 'failed'))) r.queue.push(d);
  clearTimeout(r.timer);
  r.timer = setTimeout(async () => {
    const evs = r.queue;
    r.timer = null; r.queue = [];
    const next = await loadDeploy(f);
    const prev = r.seen;
    r.seen = next;
    for (const ev of evs) noteTerminals(f, prev, next, ev);
  }, 250);
}

// A grey line in each of the window's open terminals, except the one whose
// own session acted (bx printed the result there already).
function noteTerminals(f, prev, next, ev) {
  for (const el of f.renderRoot?.querySelectorAll('bx-terminal') ?? []) {
    if (ev.session && el.getAttribute('session') === ev.session) continue;
    const line = notice(prev, next, ev, { target: el.getAttribute('api') === '0' ? 'off' : 'primary', panel: false });
    if (line) el.note?.(line);
  }
}

// ---- operations ----

async function post(path, body) {
  try {
    const r = await fetch(`/api/xbin/deployments/${path}`, { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify(body) });
    const j = await r.json().catch(() => ({}));
    return r.ok ? { status: r.status, body: j } : { status: r.status, error: j.error || `${r.status} ${r.statusText}` };
  } catch (e) {
    return { status: 0, error: String(e?.message || e) };
  }
}

async function refused(f, op, error) {
  const r = refusal(op, error);
  await f._confirm(r.title, r.message, r.ok);
  loadDeploy(f);
}

// act(f, op, deployment): pause | resume | reloadNow | attach, as the chip's
// menu, the offer and the launcher's button start it. A dry run of the exact
// request renders the confirmation; the confirmed request carries the dry
// run's seq (and, for Reload now, the checkpoint it showed as expect). A 409
// because the record or the code moved loads the state and asks again with
// the new facts; any other refusal shows the server's text verbatim.
export async function act(f, op, deployment) {
  const r = rec(f);
  if (r.busy || !OP_PATH[op]) return;
  r.busy = true;
  try {
    for (let attempt = 0; attempt < 3; attempt++) {
      const s = r.state;
      if (!s) return;
      const body = { tile: f.src };
      if (deployment && TAKES_DEPLOYMENT.has(op)) body.deployment = deployment;
      if (Number.isInteger(s.seq)) body.seq = s.seq;
      const dry = await post(OP_PATH[op], { ...body, dryRun: true });
      if (dry.error) {
        if (conflict(dry.status, dry.error) && attempt < 2) { await loadDeploy(f); continue; }
        await refused(f, op, dry.error);
        return;
      }
      const cur = dry.body.state?.tile === f.src ? dry.body.state : s;
      if (cur !== s) { r.state = cur; f.requestUpdate(); }
      const c = confirmation(op, { state: cur, impact: dry.body.impact, deployment }, opts(f));
      const answer = await f._ask(c.spec);
      if (answer?.button !== 'ok') return;
      if (Number.isInteger(cur.seq)) body.seq = cur.seq;
      if (c.expect) body.expect = c.expect;
      const res = await post(OP_PATH[op], body);
      if (res.error) {
        if (conflict(res.status, res.error) && attempt < 2) { await loadDeploy(f); continue; }
        await refused(f, op, res.error);
        return;
      }
      if (res.body.state?.tile === f.src) { r.state = res.body.state; f.requestUpdate(); } else loadDeploy(f);
      return;
    }
  } finally {
    r.busy = false;
  }
}

// the chip's menu as <bx-menu> items: disabled items keep their reason as the
// hint, and every item its tooltip (the sentence is cut to the row's width)
function menuItems(f) {
  const items = chipItems(per.get(f)?.state ?? null, opts(f));
  const titled = (src, out) => out.map((m, i) => {
    const it = src[i];
    if (it.kind) return m;
    const t = it.title || (it.op || it.items ? '' : it.label);
    return { ...m, ...(t ? { title: t } : {}), ...(m.items ? { items: titled(it.items, m.items) } : {}) };
  });
  return titled(items, toMenu(items, (op, dep) => act(f, op, dep)));
}
function openMenu(f, e) {
  f._menu = { items: menuItems(f), anchor: e.currentTarget.getBoundingClientRect(), sheet: SHEET.matches };
}

// ---- the title bar ----

// a label that starts with its glyph: the glyph is aria-hidden (the
// control's aria-label carries the words)
const glyphed = (text) => {
  const sp = text.indexOf(' ');
  const g = sp < 0 ? text : text.slice(0, sp), rest = sp < 0 ? '' : text.slice(sp);
  return html`<span aria-hidden="true">${g}</span>${rest}`;
};

function chipButton(f, c, compact) {
  const base = compact ? c.baseCompact : c.base;
  return html`<button class=${'lr' + (compact ? ' compact' : '')} title=${c.title} aria-label=${c.title}
      @click=${(e) => openMenu(f, e)}>${glyphed(base)}${c.failed ? html`<span class="bad">${compact ? '!' : ' · deploy failed'}</span>` : nothing}</button>`;
}

// barDeploy(f): the window's live reload controls among the settings — the
// zero state's entry point, or the chip and the Reload now offer (on the
// full bar; the degraded bar's chip is in the title row, titleChip).
export function barDeploy(f) {
  const vm = vmOf(f);
  if (!vm.feature) return nothing;
  if (vm.zero) {
    return html`<button class="dentry" title=${vm.entry.title} aria-label=${vm.entry.title}
        @click=${(e) => openMenu(f, e)}>${glyphed(vm.entry.text)}</button>`;
  }
  if (f._narrow) return nothing;
  return html`${vm.chip ? chipButton(f, vm.chip, false) : nothing}${vm.offer ? html`<button class="offer" title=${vm.offer.title}
      aria-label=${vm.offer.label.slice(2)} @click=${() => act(f, 'reloadNow')}>${glyphed(vm.offer.label)}</button>` : nothing}`;
}

// titleChip(f): the degraded bar's compact chip, in the title row itself
// (between + and ⋯), so the state never hides behind ⋯.
export function titleChip(f) {
  const c = vmOf(f).chip;
  return c ? chipButton(f, c, true) : nothing;
}

// deployKey(f): what the bar's width depends on here (frame-titlebar barKey).
export const deployKey = (f) => vmOf(f).barKey;

// ---- the launcher (the empty window) ----

export function launchBanner(f) {
  const l = vmOf(f).launcher;
  if (!l) return nothing;
  const b = l.banner;
  return html`${b ? html`<div class=${'ldep ' + b.tone}><span>${b.text}</span>${b.reloadNow ? html`
      <button class="lreload" aria-label="Reload now" @click=${() => act(f, 'reloadNow')}><span aria-hidden="true">⇡</span> Reload now</button>` : nothing}
    </div>` : nothing}${l.note ? html`<div class="ldepnote">${l.note}</div>` : nothing}`;
}

// ---- the chip over the tile ----

// the /components summary, from the state when it is loaded (it is fresher
// than the page's cache)
const summaryOf = (s) => (s?.record
  ? { primary: s.primary, pinned: (s.deployments || []).some((d) => d.primary && d.liveReload === false), protected: !!s.protectedPrimary }
  : null);
const overTile = (f) => { const s = per.get(f)?.state; return s ? chipOverTile(summaryOf(s), s) : null; };

// frameChip(f): "📌 pinned" at the tile's top right, for viewers with
// terminal level while the primary is pinned; hover or focus says to what
// and why; a click opens the tile's terminal window.
export function frameChip(f) {
  const c = overTile(f);
  if (!c) return nothing;
  return html`<button class="dchip" title=${c.title} aria-label=${c.title} @click=${() => f.open('term')}>
    <span class="rest"><span aria-hidden="true">📌</span> pinned</span><span class="full">${c.title}</span></button>`;
}

// ---- the frame's test names (frame-testapi.js `deploy`) ----

function plain(items) {
  return items.map((it) => (it.kind ? { kind: it.kind, label: it.label }
    : { label: it.label, enabled: !!it.enabled, hint: it.hint || '', ...(it.items ? { items: plain(it.items) } : {}) }));
}

export function deployTestApi(f) {
  const r = () => per.get(f);
  return {
    get state() { return r()?.state ?? null; },
    get loaded() { return !!r()?.loaded; },
    get changed() { return vmOf(f).changed; },
    get chip() { const c = vmOf(f).chip; return c ? { text: c.text, compact: c.compact, title: c.title } : null; },
    get offer() { return !!vmOf(f).offer; },
    get entry() { const vm = vmOf(f); return vm.zero && vm.entry ? { ...vm.entry } : null; }, // drawn in the zero state only
    get banner() { const l = vmOf(f).launcher; return l?.banner ? { ...l.banner } : null; },
    chipItems() { return plain(vmOf(f).items); },
    // runs a chip-menu item ("Resume live reload on ▸/main" for a submenu's);
    // its confirmation shows as testApi().dialog. false: no such usable item.
    chipAction(label) {
      const [a, b] = String(label).split('/');
      let it = vmOf(f).items.find((x) => !x.kind && x.label === a);
      if (it && b !== undefined) it = (it.items || []).find((x) => x.label === b);
      if (!it?.enabled || !it.op) return false;
      act(f, it.op, it.deployment);
      return true;
    },
    get frameChip() { const c = overTile(f); return c ? { ...c } : null; },
    target(i = f._active) { const t = f._sessions?.[i]; return t ? (t.api === false ? 'off' : 'primary') : null; },
    apiOptions(i = f._active) { return apiOptions(r()?.state ?? null, echo(f, i)).options; },
    refresh: () => loadDeploy(f),
  };
}

// ---- styles (adopted by bx-frame beside the bar's and the launcher's) ----

export const deployCss = css`
  .titlebar button.lr, .toolsrow button.lr { color: var(--bx-text, #d4d9e0); }
  .titlebar button.lr.compact { max-width: 12ch; overflow: hidden; text-overflow: ellipsis; }
  button.lr .bad { color: var(--bx-red, #ef5350); font-weight: 600; }
  .titlebar button.offer, .toolsrow button.offer {
    border-color: var(--bx-accent, #f5a623); color: var(--bx-accent, #f5a623); font-weight: 600;
  }
  .titlebar button.offer:hover, .toolsrow button.offer:hover { background: var(--bx-accent, #f5a623); color: #1b1e24; }
  .dchip {
    position: absolute; top: 2px; right: 14px; z-index: 10; max-width: calc(100% - 28px);
    padding: 0 6px; height: 16px; border: 1px solid var(--bx-border, #363c45); border-radius: 8px;
    background: var(--bx-panel, #23272e); color: var(--bx-text, #d4d9e0);
    font: 10.5px/14px var(--bx-mono, ui-monospace, monospace); white-space: nowrap;
    overflow: hidden; text-overflow: ellipsis; opacity: 0.55; cursor: pointer;
  }
  .dchip .full { display: none; }
  .dchip:hover, .dchip:focus-visible { opacity: 1; }
  .dchip:hover .rest, .dchip:focus-visible .rest { display: none; }
  .dchip:hover .full, .dchip:focus-visible .full { display: inline; }
  @media (max-width: 820px), (pointer: coarse) {
    .dchip { overflow: visible; }
    .dchip::before { content: ''; position: absolute; inset: -14px -6px; }
  }
  .launcher .ldep { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; justify-content: center; max-width: 460px;
    padding: 8px 10px; border: 1px solid var(--bx-border, #363c45); border-radius: 6px; background: var(--bx-panel-2, #2b3038);
    color: var(--bx-text, #d4d9e0); font-size: 12px; }
  .launcher .ldep.paused { border-color: var(--bx-amber, #f2a71b); }
  .launcher .lreload { border: 1px solid var(--bx-accent, #f5a623); background: transparent; color: var(--bx-accent, #f5a623);
    border-radius: 6px; padding: 4px 10px; cursor: pointer; font: 12px var(--bx-sans, system-ui); font-weight: 600; white-space: nowrap; }
  .launcher .lreload:hover { background: var(--bx-accent, #f5a623); color: #1b1e24; }
  .launcher .ldepnote { color: var(--bx-muted, #868f9a); font-size: 11px; max-width: 460px; text-align: center; }
`;
