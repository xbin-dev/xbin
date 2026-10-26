/**
 * shell-prompts.js — the decisions waiting for the person, on the window of
 * the tile that asked (D195): grant requests, interfaces to bind and
 * partition consent asks. They used to fill a strip above every screen;
 * now a tile with something waiting carries a key badge in its title bar
 * and a popover over the top of its window holding that tile's
 * <bx-grants>, <bx-bindings> and <bx-part-consent> (each with `tile`). Its
 * ⌃ folds it to the badge, and the badge opens it again (a new decision
 * opens it too). It never covers more than the top of the window, so the
 * tile stays usable; on a phone, and on a Document-mode card (only as
 * tall as its page), it sits in the card's flow above the tile.
 * A tile that isn't on the screen shows nothing but its sidebar flag
 * (bx-side; prompts.js says which tile each decision belongs to).
 *
 * The shell's end (followPrompts) reads the page's one copy of the
 * requests and dismissals (/vendor/bx-requests.js) and the consent asks
 * (bx-part-consent.js), keeps `_prompts` (path → counts) for the canvas and
 * the sidebar and `_pendingN` for the organisations badge, and says so
 * when a dismissal couldn't be saved (#11).
 */
import { html, css, nothing } from 'lit';
import '/vendor/bx-grants.js';
import '/vendor/bx-bindings.js';
import '/vendor/bx-icons.js';
import { grantKey, bindingKey, isDismissed, pendingCount } from '/vendor/bx-dismiss.js';
import { grantsStore, bindingsStore, dismissalStore } from '/vendor/bx-requests.js';
import { consentContext, consentAsks, onConsents } from './bx-part-consent.js';
import { promptsByTile, promptWords } from './prompts.js';

const KEY = { grants: grantKey, bindings: bindingKey };

// ---- the shell's end (s is <bx-shell>) ----
export function followPrompts(s) {
  const update = () => {
    const d = dismissalStore().value;
    s._prompts = promptsByTile({ grants: grantsStore().value, bindings: bindingsStore().value, consents: consentAsks() },
      (kind, p) => isDismissed(d, kind, KEY[kind](p)));
    s._pendingN = pendingCount(grantsStore().value, bindingsStore().value, s._accessReqs ?? null, d);
  };
  const offs = [grantsStore().subscribe(update), bindingsStore().subscribe(update), dismissalStore().subscribe(update), onConsents(update),
    dismissalStore().onError(() => s._pushToast('dismissed', { level: 'error',
      message: 'Couldn’t save your dismissal, so it shows again — try again (it is kept on your devices once saved).' }))];
  s._promptsUpdate = update;
  loadAccessRequests(s);
  return () => offs.forEach((f) => f());
}

// The organisations badge also counts people's access requests the person
// can grant (D36): read on start and on `grants`/`users` events.
export async function loadAccessRequests(s) {
  try {
    const r = await fetch('/api/xbin/access-requests');
    s._accessReqs = r.ok ? await r.json() : null;
  } catch { s._accessReqs = null; }
  s._promptsUpdate?.();
}

// The consent asks follow what the shell knows (who, which tiles).
export const syncPrompts = (s) => consentContext(s._components, s._who);

// ---- on a tile's window (c is <bx-canvas>) ----
const shut = (c) => (c._pqShut ??= new Map()); // path → the count it was folded at

// promptBadge(c, o): the title bar's key badge, when the tile has something waiting.
export function promptBadge(c, o) {
  const e = c.prompts?.get(o.path);
  if (!e?.n) return nothing;
  const folded = (shut(c).get(o.path) ?? -1) >= e.n;
  return html`<button class="wc pqb ${folded ? '' : 'on'}" data-prompts=${e.n} title=${`${promptWords(e)} — ${folded ? 'show' : 'fold'}`}
    aria-label=${promptWords(e)} aria-expanded=${folded ? 'false' : 'true'}
    @pointerdown=${(ev) => ev.stopPropagation()}
    @click=${(ev) => { ev.stopPropagation(); togglePrompts(c, o.path, e.n, folded); }}><bx-icon name="key"></bx-icon><span>${e.n}</span></button>`;
}

function togglePrompts(c, path, n, folded) {
  if (folded) shut(c).delete(path); else shut(c).set(path, n);
  c.requestUpdate();
}

// promptPop(c, o, flow): the popover over the top of the window — open
// unless folded at this count or more (a new decision opens it again). flow:
// in the card's flow above the tile instead (a phone; a Document-mode card,
// which is only as tall as its page).
export function promptPop(c, o, flow = false) {
  const e = c.prompts?.get(o.path);
  if (!e?.n || (shut(c).get(o.path) ?? -1) >= e.n) return nothing;
  return html`<div class="pqpop ${flow ? 'flow' : ''}" role="region" aria-label=${`decisions waiting for ${o.path}`} data-prompts-for=${o.path}
      @pointerdown=${(ev) => ev.stopPropagation()}>
    <div class="pqh"><bx-icon name="key"></bx-icon><span>${promptWords(e)}</span>
      <button class="pqx" title="fold — the key badge in the title bar shows it again" aria-label="fold"
        @click=${() => togglePrompts(c, o.path, e.n, false)}><bx-icon name="chevron-up"></bx-icon></button></div>
    ${e.grants ? html`<bx-grants tile=${o.path}></bx-grants>` : nothing}
    ${e.bindings ? html`<bx-bindings tile=${o.path}></bx-bindings>` : nothing}
    ${e.consents ? html`<bx-part-consent tile=${o.path} .components=${c.components} .who=${c.who}></bx-part-consent>` : nothing}
  </div>`;
}

export const promptsCss = css`
  .card .head .pqb { width: auto; min-width: 28px; padding: 0 6px; gap: 4px; color: var(--bx-accent, #8C9BFF); }
  .card .head .pqb span { font: var(--bx-font-micro, 600 11px/14px "Instrument Sans", system-ui, sans-serif); }
  .card .head .pqb.on { background: var(--bx-hover, #2A2B34); }
  .pqpop {
    position: absolute; top: 8px; left: 8px; right: 8px; z-index: 14; box-sizing: border-box;
    max-height: calc(100% - 16px); overflow: auto; padding: 0 12px 12px;
    background: var(--bx-panel, #1F2028); color: var(--bx-text, #E9EAF0);
    border: 1px solid var(--bx-accent, #8C9BFF); border-radius: var(--bx-radius, 2px);
    box-shadow: var(--bx-shadow-rest, 0 10px 28px rgba(0, 0, 0, 0.4));
  }
  .pqpop .pqh {
    position: sticky; top: 0; z-index: 1; display: flex; align-items: center; gap: 8px; margin: 0 -12px; padding: 6px 4px 6px 12px;
    background: var(--bx-panel, #1F2028); border-bottom: 1px solid var(--bx-border, #33353F);
    color: var(--bx-muted, #A3A6B6); font: var(--bx-font-meta, 400 12px/16px "Instrument Sans", system-ui, sans-serif);
  }
  .pqpop .pqh bx-icon:first-child { color: var(--bx-accent, #8C9BFF); }
  .pqpop .pqh span { flex: 1; min-width: 0; }
  .pqpop .pqx {
    display: inline-flex; align-items: center; justify-content: center; width: 28px; height: 28px; padding: 0; cursor: pointer;
    background: none; border: 0; color: var(--bx-muted, #A3A6B6);
  }
  .pqpop .pqx:hover { color: var(--bx-text, #E9EAF0); background: var(--bx-hover, #2A2B34); }
  .pqpop > * + * { margin-top: 8px; }
  .pqpop.flow { position: static; flex: none; max-height: none; margin: 8px; box-shadow: none; }
`;
