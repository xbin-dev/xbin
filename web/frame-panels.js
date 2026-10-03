/**
 * frame-panels.js — the terminal window's body, extracted from bx-frame (the
 * frame is at its size budget): which panel the window shows, whether the
 * terminal sits beside it, and the divider between the two (D129). `f` is
 * the BxFrame; this reads its state and sets it.
 *
 * The window shows its terminal host — the shell and agent tabs, or the
 * launcher while it has none — alone (layout 'term'), or one panel: code
 * (bx-code), logs (bx-logs), change proposals (bx-prs) or the tile's
 * deployments (bx-deployments). A panel fills the window unless the window's
 * `_beside` flag puts the terminal beside it: then a divider splits the two,
 * the panel taking `_paneW` percent. So a window without sessions shows the
 * launcher only in 'term' or beside a panel, never next to a panel it did
 * not ask for — the Deployments panel opens full width.
 *
 * The layout switcher's panel buttons keep the flag (switching what sits
 * beside the terminal), `>_` clears it, and `⇋` toggles it (from 'term' it
 * puts code beside the terminal, the old split). open(layout), the entry the
 * shell's menus use, shows a panel full width, and 'split' — the old code +
 * terminal layout — means code beside the terminal, as it always did.
 *
 * The divider drags (a shield keeps the frame's iframe from stealing the
 * pointer), takes the arrow keys, Home and End when focused, and resets on a
 * double-click; neither side goes below its pixel floor (a panel's 230 px —
 * the Deployments panel's side list — and 200 px of terminal), and a
 * window too narrow for both splits it evenly.
 *
 * Saved in the window pref (term:<tile>, web/term-sessions.js layoutToPref):
 * code beside the terminal as `layout: 'split'` (what older frames restore),
 * another panel beside it as `beside: true` (older frames show that panel
 * alone), the width as `paneW` (read from the older `codeW` when absent).
 * The Deployments layout is never saved: the window comes back on the
 * terminal.
 */
import { html, css, nothing } from 'lit';
import { repeat } from 'lit';
import { dragPointer, clampBox } from '/vendor/bx-kit.js';
import { launcher, resumeHistory } from '/vendor/frame-launcher.js';
import { sessionTarget } from '/vendor/deploy-state.js';
import { PANELS, layoutFromPref, layoutToPref } from '/vendor/term-sessions.js';

// The panels (PANELS, in the layout switcher's order: frame-titlebar.js
// layoutGroup), each one's name in the ⇋ button's title.
export { PANELS };
const NAME = { code: 'code', logs: 'logs', prs: 'proposals', deployments: 'deployments' };
export const PANE_W = 55; // the panel's default share, percent (the old split's)
const PANE_MIN = 230, TERM_MIN = 200, BAR = 5; // px: a panel's floor, the terminal's, the divider

// split(f): the terminal sits beside a panel.
export const split = (f) => f._layout !== 'term' && !!f._beside;
// termShown(f): the terminal host shows — alone, or beside a panel.
export const termShown = (f) => f._layout === 'term' || !!f._beside;

// setLayout(f, l, beside): show layout l ('term', a panel, or 'split' — code
// beside the terminal); a panel keeps the window's beside flag unless one is
// given. A panel that needs room widens a narrow window, keeping it reachable.
export function setLayout(f, l, beside = f._beside) {
  if (l === 'split') { l = 'code'; beside = true; }
  if (l !== 'term' && !PANELS.includes(l)) return;
  f._layout = l;
  f._beside = l !== 'term' && !!beside;
  if ((f._beside || l === 'code' || l === 'prs' || l === 'deployments') && f._pop && f._pop.w < 760) {
    const box = { ...f._popBox(), w: 960 };
    f._setPopBox(f._bounds() ? box : clampBox(box));
    f._saveTerm(); f._popChanged();
  }
}

// toggleBeside(f): the switcher's ⇋ — the terminal beside the panel, or not;
// from 'term', code beside the terminal.
export function toggleBeside(f) {
  if (f._layout === 'term') setLayout(f, 'code', true);
  else setLayout(f, f._layout, !f._beside);
}

// besideTitle(f): the ⇋ button's title, naming the panel it puts beside.
export function besideTitle(f) {
  const what = NAME[f._layout] || 'code';
  return `${what[0].toUpperCase()}${what.slice(1)} beside the ${f._isAgent ? 'agent' : 'terminal'}`;
}

// revealTerm(f): a session started or opened must be seen: the terminal host
// shows (a panel beside it stays).
export function revealTerm(f) {
  if (!termShown(f)) { f._layout = 'term'; f._beside = false; }
}

// restoreLayout(f, w): the window pref's layout, beside flag and width
// (term-sessions.js layoutFromPref: a layout this frame doesn't restore
// leaves the terminal).
export function restoreLayout(f, w) {
  const r = layoutFromPref(w);
  if (r.layout) { f._layout = r.layout; f._beside = r.beside; }
  if (r.paneW) f._paneW = r.paneW;
}

// layoutPref(f): the layout part of the window pref (term-sessions.js layoutToPref).
export const layoutPref = (f) => layoutToPref({ layout: f._layout, beside: split(f), paneW: f._paneW });

// the panel's share for a divider at x px of a `total` px wide body, within
// both floors (an even split when the body can't hold them)
function share(x, total) {
  if (!(total > 0)) return PANE_W;
  const lo = Math.min(PANE_MIN, (total - BAR) / 2), hi = Math.max(lo, total - BAR - TERM_MIN);
  return Math.round((Math.max(lo, Math.min(hi, x)) / total) * 1000) / 10;
}

// Drag the divider: the panel's share follows the pointer (the body's box
// is fixed for the drag). updated() saves the window as the width changes.
// The drag — and its shield — starts once the pointer moves: a shield up
// between a click's press and release would take the release, and the
// double-click (the reset) would never reach the divider. Until then the
// divider holds the pointer (capture), so neither the tile's iframe nor the
// terminal takes it.
function splitStart(f, e) {
  if (e.button !== 0) return;
  e.preventDefault();
  const bar = e.currentTarget, rect = bar.parentElement.getBoundingClientRect(), x0 = e.clientX;
  const to = (x) => { f._paneW = share(x - rect.left, rect.width); };
  try { bar.setPointerCapture(e.pointerId); } catch { /* not an active pointer */ }
  const done = () => { for (const t of ['pointermove', 'pointerup', 'pointercancel']) bar.removeEventListener(t, t === 'pointermove' ? move : done); };
  const move = (ev) => {
    if (Math.abs(ev.clientX - x0) < 3) return;
    done();
    to(ev.clientX);
    dragPointer({ cursor: 'col-resize', onMove: (m) => to(m.clientX) });
  };
  bar.addEventListener('pointermove', move);
  bar.addEventListener('pointerup', done);
  bar.addEventListener('pointercancel', done);
}

// The focused divider: ←/→ move it 2 % (10 % with Shift), Home and End to
// either floor.
function splitKey(f, e) {
  const total = e.currentTarget.parentElement.getBoundingClientRect().width;
  const step = e.shiftKey ? 10 : 2;
  const px = { ArrowLeft: -step, ArrowRight: step }[e.key];
  let x;
  if (px !== undefined) x = ((f._paneW + px) / 100) * total;
  else if (e.key === 'Home') x = 0;
  else if (e.key === 'End') x = total;
  else return;
  e.preventDefault();
  f._paneW = share(x, total);
}

function panel(f) {
  switch (f._layout) {
    case 'code': return html`<bx-code class="pane" src=${f.src}></bx-code>`;
    case 'logs': return html`<bx-logs class="pane" component=${f.src}></bx-logs>`;
    case 'prs': return html`<bx-prs class="pane" component=${f.src}></bx-prs>`;
    // the active tab's target as a property, so the panel's Dev API tag
    // follows tab switches and restarts
    case 'deployments': return html`<bx-deployments class="pane" component=${f.src} .frame=${f}
        .target=${sessionTarget(f._sessions[f._active])}></bx-deployments>`;
    default: return nothing;
  }
}

// panels(f): the window's body — the panel, the divider, and the terminal
// host with the launcher or the tabs. The host stays mounted (hidden while a
// panel fills the window) so its sessions survive; a panel mounts when shown.
export function panels(f) {
  const s = split(f), who = f._isAgent ? 'agent' : 'terminal';
  return html`<div class=${'panels' + (s ? ' split' : '')} style=${s ? `--pane-w:${f._paneW}%` : nothing}>
    ${panel(f)}
    ${s ? html`<div class="vsplit" role="separator" tabindex="0" aria-orientation="vertical" aria-label=${`resize the panel beside the ${who}`}
        aria-valuemin="0" aria-valuemax="100" aria-valuenow=${Math.round(f._paneW)}
        title="drag to resize · double-click to reset" @pointerdown=${(e) => splitStart(f, e)} @keydown=${(e) => splitKey(f, e)}
        @dblclick=${() => { f._paneW = PANE_W; }}></div>` : nothing}
    <div class="term-host" style="display:${termShown(f) ? 'flex' : 'none'}">
      ${f._sessions.length === 0 ? launcher(f) : nothing}
      ${repeat(f._sessions, (t) => t.key, (t, i) => t.kind === 'agent'
        ? html`<bx-agent style="height:100%; display:${i === f._active ? 'flex' : 'none'}"
            component=${f.src} session=${t.id ?? nothing} provider=${t.history || t.restarting ? nothing : (t.provider || nothing)} ?ended=${!!t.ended} ?restarting=${!!t.restarting} ?vm=${!!t.vm}
            history=${t.history || nothing} resume=${t.resume || nothing}
            @bx-session=${(ev) => f._gotSession(t.key, ev)}
            @bx-resume=${(ev) => resumeHistory(f, ev.detail, t.key)} @bx-new-agent=${(ev) => f._startKind('agent', ev.detail.provider)}
            @bx-open-terminal=${(ev) => f._signIn(ev)}
            @bx-exit=${() => f._endTab(t.key)}></bx-agent>`
        : html`<bx-terminal style="height:100%; display:${i === f._active ? 'block' : 'none'}"
            cwd=${f.src} session=${t.id ?? nothing} net=${t.net || nothing} gpu=${t.gpu || 'none'} deployment=${t.deployment || nothing} api=${t.api === false ? '0' : '1'} vm=${t.vm ? '1' : '0'} run=${t.run || nothing}
            @bx-session=${(ev) => f._gotSession(t.key, ev)}
            @bx-exit=${() => f._closeTerm(t.key, true)}></bx-terminal>`)}
    </div>
  </div>`;
}

// The body's styles (adopted by bx-frame beside the bar's).
export const panelsCss = css`
  .panels { display: flex; flex: 1; min-height: 0; }
  .panels > .pane { flex: 1; min-width: 0; overflow: hidden; }
  .panels.split > .pane { flex: 0 0 var(--pane-w, 55%); min-width: min(230px, calc(50% - 3px)); }
  .vsplit { flex: none; width: 5px; position: relative; z-index: 1; cursor: col-resize; touch-action: none;
    background: var(--bx-border, #33353F); }
  /* a wider grab zone than the 5 px line (a finger, a quick mouse) */
  .vsplit::before { content: ''; position: absolute; inset: 0 -5px; }
  .vsplit:hover { background: var(--bx-border-strong, #666A7E); }
  .vsplit:focus-visible { background: var(--bx-focus, #3DD6F5); outline: none; box-shadow: none; }
  .term-host { flex: 1; flex-direction: column; min-height: 0; min-width: 0; background: var(--bx-term-bg, #0B0C12); }
  .panels.split > .term-host { min-width: min(200px, calc(50% - 3px)); }
`;
