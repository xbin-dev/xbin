/**
 * xb/render-agent.js — the workspace agent's nodes in the reference renderer
 * (D233; docs/native.md §The workspace agent's nodes): `card`, `connection`,
 * `request`, `session`, and a transcript's `empty` slot.
 *
 * `request` and `session` are placed by a tile BY ID and drawn by the xbin
 * app from xbind's own reading — what waits, for which tile, what came of
 * it, and the one button. A tile's tree can't describe a grant, and this
 * renderer has no xbind to ask: it draws an honest placeholder that names
 * the kind and the id and says who draws the real thing. It never draws a
 * Review, an Allow or anything else that looks like a decision.
 */
import { css } from '/vendor/lit-all.min.js';
import { html, nothing, P, cls, tone, icon, str } from '/vendor/xb/render-base.js';

const TONES = new Set(['info', 'muted', 'accent', 'ok', 'warn', 'danger']);

// card: a title line (icon, title, badge), a subtitle, its children one under
// another, a quiet note, and its buttons in a row at the bottom.
function card(n, cx) {
  const p = P(n);
  const t = TONES.has(p.tone) ? p.tone : '';
  const all = n.c || [];
  const buttons = all.filter((c) => c.t === 'button');
  const body = all.filter((c) => c.t !== 'button');
  const inner = cx.in('free');
  return html`<xb-card data-k=${n.k} class=${cls('card', 'wcard', t && `w-${t}`)}>
    ${p.title || p.icon || p.badge ? html`<div class="wc-head">
      ${p.icon ? html`<span class=${cls('wc-ic', tone(t === 'info' ? 'accent' : t))}>${icon(p.icon)}</span>` : nothing}
      <span class="wc-title">${str(p.title)}</span>
      ${p.badge ? html`<span class=${cls('pill', t && t !== 'info' && t !== 'muted' && `pill-${t}`)}>${p.badge}</span>` : nothing}
    </div>` : nothing}
    ${p.subtitle ? html`<div class="wc-sub">${p.subtitle}</div>` : nothing}
    ${body.length ? html`<div class="wc-body">${body.map((c) => inner.node(c))}</div>` : nothing}
    ${p.note ? html`<div class="wc-note">${p.note}</div>` : nothing}
    ${buttons.length ? html`<div class="wc-actions">${buttons.map((c) => inner.node(c))}</div>` : nothing}
  </xb-card>`;
}

// connection: one line, three segments — what is connected, what is not, the
// model. The line reports `tap`; the model segment `model` when the tile
// listens to it (else it is part of the line).
function connection(n, cx) {
  const p = P(n);
  const dis = !!p.disabled;
  const tap = cx.on(n, 'tap') && !dis;
  const model = cx.on(n, 'model') && !dis;
  const lead = html`<span class="cn-seg">${icon('plug')}<span>${str(p.connected)}</span></span>
    ${p.not ? html`<span class="cn-seg cn-not">${icon('lock')}<span>${p.not}</span></span>` : nothing}`;
  const mod = p.model ? html`<span class="cn-model-t">${p.model}</span>${p.private ? html`<span class="cn-priv">· private</span>` : nothing}` : nothing;
  return html`<xb-connection data-k=${n.k} class=${cls('connection', dis && 'disabled')} role="group" aria-label=${str(p.label) || nothing}>
    <button class="cn-main" ?disabled=${!tap} @click=${() => cx.emit(n, 'tap', {})}>${lead}${model ? nothing : html`<span class="cn-seg cn-model">${mod}</span>`}</button>
    ${model ? html`<button class="cn-seg cn-model tap" aria-label=${`Model ${str(p.model)}`} @click=${() => cx.emit(n, 'model', {})}>${mod}${icon('expand', 'cn-chev')}</button>` : nothing}
  </xb-connection>`;
}

// What the app draws in their place (see the header): the kind and the id,
// nothing of the request itself.
const REQUEST = {
  call: ['shield', 'Approval request'], session: ['terminal', 'Tile agent start'],
  template: ['box', 'Tile creation'], changeset: ['gear', 'Admin changes'],
};
const APP_DRAWN = 'Drawn by the xbin app from xbind’s own reading.';

function request(n) {
  const p = P(n);
  const [ic, title] = REQUEST[p.kind] || REQUEST.call;
  return html`<xb-request data-k=${n.k} class="card wstub">
    <span class="ws-ic">${icon(ic)}</span>
    <span class="ws-text"><span class="ws-title">${title}</span><span class="ws-id">${str(p.id)}</span><span class="ws-note">${APP_DRAWN}</span></span>
  </xb-request>`;
}

function session(n) {
  const p = P(n);
  return html`<xb-session data-k=${n.k} class="card wstub">
    <span class="ws-ic">${icon('agent')}</span>
    <span class="ws-text"><span class="ws-title">Tile agent${p.tile ? html` · ${p.tile}` : nothing}</span><span class="ws-id">${str(p.id)}</span><span class="ws-note">${APP_DRAWN}</span></span>
  </xb-session>`;
}

/**
 * transcriptEmpty(n, cx): what a transcript with no children shows (rev 3
 * `empty`): a heading, a sentence, short titled lists and suggestions — a
 * kind in small caps and a line each — that report `suggestion {id}`.
 */
export function transcriptEmpty(n, cx) {
  const e = P(n).empty;
  if (!e || typeof e !== 'object') return nothing;
  const lists = (Array.isArray(e.lists) ? e.lists : []).filter((l) => l && typeof l === 'object');
  const sugg = (Array.isArray(e.suggestions) ? e.suggestions : []).filter((s) => s && typeof s === 'object');
  const can = cx.on(n, 'suggestion');
  return html`<div class="tr-empty">
    ${e.title ? html`<h2 class="te-title">${e.title}</h2>` : nothing}
    ${e.text ? html`<p class="te-text">${e.text}</p>` : nothing}
    ${lists.map((l) => html`<div class="te-list">
      ${l.title ? html`<div class="te-cap">${l.title}</div>` : nothing}
      <ul>${(Array.isArray(l.items) ? l.items : []).map((i) => html`<li>${str(i)}</li>`)}</ul>
    </div>`)}
    ${sugg.length ? html`<div class="te-sugg">
      ${e.suggestionsTitle ? html`<div class="te-cap">${e.suggestionsTitle}</div>` : nothing}
      ${sugg.map((s) => html`<button class="te-s" ?disabled=${!can} @click=${() => cx.emit(n, 'suggestion', { id: str(s.id) })}>
        ${s.kind ? html`<span class="te-kind">${s.kind}</span>` : nothing}<span class="te-line">${str(s.text)}</span></button>`)}
    </div>` : nothing}
  </div>`;
}

export const AGENT = { card, connection, request, session };

export const AGENT_CSS = css`
  .wcard { padding: 12px 14px; display: flex; flex-direction: column; gap: 8px; }
  .wcard.w-accent { box-shadow: 0 0 0 1px color-mix(in srgb, var(--xb-accent) 45%, transparent); }
  .wcard.w-warn { box-shadow: inset 3px 0 0 var(--xb-warn), 0 0 0 0.5px var(--xb-separator); }
  .wcard.w-danger { box-shadow: inset 3px 0 0 var(--xb-danger), 0 0 0 0.5px var(--xb-separator); }
  .wcard.w-ok { box-shadow: inset 3px 0 0 var(--xb-ok), 0 0 0 0.5px var(--xb-separator); }
  .wcard.w-muted { background: var(--xb-surface2); }
  .wc-head { display: flex; align-items: flex-start; gap: 8px; }
  .wc-ic { display: flex; padding-top: 2px; color: var(--xb-muted); }
  .wc-ic .ic { width: 1.1em; height: 1.1em; }
  .wc-ic.tone-accent { color: var(--xb-accent-text); } .wc-ic.tone-ok { color: var(--xb-ok); }
  .wc-ic.tone-warn { color: var(--xb-warn); } .wc-ic.tone-danger { color: var(--xb-danger); }
  .wc-title { flex: 1; min-width: 0; font: var(--xb-font-headline); overflow-wrap: anywhere; }
  .wc-head > .pill { margin-top: 1px; }
  .wc-sub { font: var(--xb-font-footnote); color: var(--xb-muted); margin-top: -4px; }
  .wc-body { display: flex; flex-direction: column; gap: 8px; min-width: 0; font: var(--xb-font-subheadline); }
  .wc-body > xb-text { display: block; overflow-wrap: anywhere; }
  .wc-body xb-field.free .f-box, .wc-body .pk-menu.in-free { background: var(--xb-surface2); }
  .wc-body xb-row.lone { background: var(--xb-surface2); border-radius: var(--xb-radius); }
  .wc-body xb-badge { align-self: flex-start; }
  .wc-note { font: var(--xb-font-footnote); color: var(--xb-muted); }
  .wc-actions { display: flex; flex-wrap: wrap; gap: 8px; margin-top: 2px; }
  .wc-actions > xb-button { flex: 1 1 auto; }
  .wc-actions .b-free { min-height: 40px; padding: 0 14px; font: var(--xb-font-subheadline); font-weight: 600; }

  xb-connection { display: flex; align-items: stretch; flex: none; margin: 8px var(--xb-margin) 0; min-width: 0;
    background: var(--xb-surface); border-radius: var(--xb-radius); box-shadow: 0 0 0 0.5px var(--xb-separator); font: var(--xb-font-footnote); }
  .cn-main { flex: 1; min-width: 0; display: flex; flex-wrap: wrap; align-items: center; gap: 4px 12px; padding: 8px 10px; text-align: left; }
  .cn-main:disabled { opacity: 1; }
  .cn-seg { display: inline-flex; align-items: center; gap: 5px; min-width: 0; font-weight: 600; }
  .cn-seg .ic { width: 14px; height: 14px; color: var(--xb-muted); }
  .cn-not { color: var(--xb-muted); font-weight: 400; }
  .cn-model { margin-left: auto; font-family: var(--xb-mono); font-variant-ligatures: none; font-weight: 400; }
  .cn-model.tap { padding: 8px 10px; border-left: 0.5px solid var(--xb-separator); color: var(--xb-accent-text); }
  .cn-priv { color: var(--xb-ok); font-family: inherit; }
  .cn-chev { width: 12px; height: 12px; stroke-width: 2.6; }
  .cn-model.tap .ic { color: currentColor; }
  .connection.disabled { color: var(--xb-muted); }

  .wstub { display: flex; align-items: flex-start; gap: 10px; padding: 10px 12px; background: none; box-shadow: none;
    border: 1px dashed var(--xb-border); /* theme-ok: a placeholder for what the app draws */ }
  .ws-ic { display: flex; padding-top: 2px; color: var(--xb-muted); }
  .ws-text { display: flex; flex-direction: column; gap: 2px; min-width: 0; }
  .ws-title { font: var(--xb-font-subheadline); font-weight: 600; overflow-wrap: anywhere; }
  .ws-id { font: var(--xb-font-caption); font-family: var(--xb-mono); font-variant-ligatures: none; color: var(--xb-muted); overflow-wrap: anywhere; }
  .ws-note { font: var(--xb-font-caption); color: var(--xb-muted); }

  .tr-empty { display: flex; flex-direction: column; gap: 14px; padding: 12px 4px; }
  .te-title { margin: 0; font: var(--xb-font-title-2); letter-spacing: -0.01em; }
  .te-text { margin: -6px 0 0; font: var(--xb-font-subheadline); color: var(--xb-muted); }
  .te-cap { font: var(--xb-font-caption); font-weight: 700; letter-spacing: 0.06em; text-transform: uppercase; color: var(--xb-muted); margin-bottom: 4px; }
  .te-list ul { margin: 0; padding: 0; list-style: none; display: flex; flex-direction: column; gap: 4px; font: var(--xb-font-subheadline); }
  .te-list li { display: flex; gap: 8px; }
  .te-list li::before { content: ''; flex: none; width: 5px; height: 5px; margin-top: 0.55em; background: var(--xb-muted); }
  .te-sugg { display: flex; flex-direction: column; gap: 6px; }
  .te-s { display: flex; flex-direction: column; align-items: flex-start; gap: 2px; padding: 10px 12px; text-align: left; min-height: 44px;
    background: var(--xb-surface); border-radius: var(--xb-radius); box-shadow: 0 0 0 0.5px var(--xb-separator); }
  .te-s:disabled { opacity: 1; }
  .te-kind { font: var(--xb-font-caption2); font-weight: 700; letter-spacing: 0.08em; text-transform: uppercase; color: var(--xb-accent-text); }
  .te-line { font: var(--xb-font-subheadline); overflow-wrap: anywhere; }
`;
