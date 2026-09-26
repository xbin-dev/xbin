/**
 * xb/render-controls.js — controls of the reference renderer (button, toggle,
 * field, picker, menu) and the view's overlays (a button's confirmation
 * dialog, a menu's popover, an image preview).
 *
 * Controlled props follow native/spec/tree.md §6 through the view: a field
 * shows the tile's `value` while it is bound, reports every keystroke as
 * `input {value}` (the view writes it into its copy first, so re-renders
 * never fight the typing) and applies a tile `set` whenever it differs from
 * what the field shows — that is how `draft = ''` resets work.
 *
 * Buttons look like their place: prominent full-width buttons in a free
 * layout (sheet, scroll screen), accent-text cells in an inset group, bare
 * items in a toolbar, compact pills among a row's actions, rows in a menu.
 */
import { css, live } from '/vendor/lit-all.min.js';
import { html, nothing, P, cls, icon, spinner, str } from '/vendor/xb/render-base.js';

// press(n, view): a button's tap — its confirmation first, then the native
// copy, then the tile's `tap`.
export async function press(n, view) {
  const p = P(n);
  if (p.disabled || p.busy) return;
  if (p.confirm && typeof p.confirm === 'object') {
    const ok = await view.confirm({ label: p.label, ...p.confirm });
    if (!ok) return;
  }
  if (p.copy != null) {
    await view.copy(str(p.copy));
    const u = view.uiOf(n.k); u.copied = true; view.requestUpdate();
    setTimeout(() => { u.copied = false; view.requestUpdate(); }, 1200);
  }
  if (Array.isArray(n.e) && n.e.includes('tap')) view.emit(n, 'tap', {});
}

function button(n, cx) {
  const p = P(n);
  const place = cx.place === 'screen' ? 'free' : cx.place;
  const role = ['primary', 'secondary', 'destructive', 'plain'].includes(p.role) ? p.role : 'default';
  const copied = cx.ui(n.k).copied;
  const label = copied ? 'Copied' : str(p.label);
  const ic = p.busy ? spinner() : copied ? icon('check') : p.icon ? icon(p.icon) : nothing;
  const iconOnly = place === 'toolbar' && (p.icon || p.busy) && label;
  const inner = place === 'menu'
    ? html`<span class="b-label">${label}</span>${ic}`
    : html`${ic}${iconOnly ? nothing : label ? html`<span class="b-label">${label}</span>` : nothing}`;
  return html`<xb-button data-k=${n.k} class=${cls('button', `in-${place}`, place === 'group' && 'cell')}>
    <button class=${cls('btn', `b-${place}`, `r-${role}`, p.busy && 'busy')} ?disabled=${!!p.disabled || !!p.busy}
      aria-label=${iconOnly ? label : nothing} aria-busy=${p.busy ? 'true' : nothing}
      @click=${() => { if (place === 'menu') cx.v.closeOverlay(); press(n, cx.v); }}>${inner}</button>
  </xb-button>`;
}

function toggle(n, cx) {
  const p = P(n);
  const on = !!cx.val(n, 'value', false);
  const flip = () => { if (!p.disabled) cx.emit(n, 'change', { value: !on }); };
  return html`<xb-toggle data-k=${n.k} class=${cls('toggle', cx.place === 'group' ? 'cell' : 'free', p.disabled && 'disabled')}>
    <span class="tg-label" @click=${flip}>${str(p.label)}</span>
    <button role="switch" aria-checked=${on ? 'true' : 'false'} aria-label=${str(p.label) || nothing}
      class=${cls('switch', on && 'on')} ?disabled=${!!p.disabled} @click=${flip}><span class="knob"></span></button>
  </xb-toggle>`;
}

const INPUT = { text: 'text', secure: 'password', number: 'text', email: 'email', url: 'url', search: 'search', date: 'date', time: 'time' };

function field(n, cx) {
  const p = P(n);
  const kind = Object.hasOwn(INPUT, p.kind) || p.kind === 'multiline' ? p.kind : 'text';
  const v = str(cx.val(n, 'value', ''));
  const report = (type) => (e) => cx.emit(n, type, { value: e.target.value });
  const key = (e) => {
    if (e.key === 'Enter' && !e.isComposing && kind !== 'multiline') { e.preventDefault(); cx.emit(n, 'submit', { value: e.target.value }); }
  };
  const ph = str(p.placeholder) || nothing;
  const common = { dis: !!p.disabled };
  const input = kind === 'multiline'
    ? html`<textarea class="f-input" rows="3" placeholder=${ph} ?disabled=${common.dis} .value=${live(v)}
        @input=${report('input')} @change=${report('change')}></textarea>`
    : html`<input class="f-input" type=${INPUT[kind]} placeholder=${ph} ?disabled=${common.dis} .value=${live(v)}
        inputmode=${kind === 'number' ? 'decimal' : nothing} enterkeyhint=${p.submit ? str(p.submit) : nothing}
        autocomplete=${kind === 'secure' ? 'new-password' : 'off'} spellcheck="false" autocapitalize="off"
        @input=${report('input')} @change=${report('change')} @keydown=${key}>`;
  return html`<xb-field data-k=${n.k} class=${cls('field', cx.place === 'group' ? 'cell' : 'free', p.error && 'invalid', p.disabled && 'disabled', `k-${kind}`)}>
    <label class="f-wrap">${p.label ? html`<span class="f-label">${p.label}</span>` : nothing}
      <span class="f-box">${kind === 'search' ? icon('search') : nothing}${input}</span></label>
    ${p.error ? html`<div class="f-error">${p.error}</div>` : p.hint ? html`<div class="f-hint">${p.hint}</div>` : nothing}
  </xb-field>`;
}

const same = (a, b) => a === b || (a != null && b != null && typeof a === typeof b && JSON.stringify(a) === JSON.stringify(b));

function picker(n, cx) {
  const p = P(n);
  const opts = (Array.isArray(p.options) ? p.options : []).filter((o) => o && typeof o === 'object');
  const val = cx.val(n, 'value', undefined);
  const idx = opts.findIndex((o) => same(o.value, val));
  const lbl = (o) => str(o.label ?? o.value);
  const choose = (i) => { const o = opts[i]; if (o && !same(o.value, val)) cx.emit(n, 'change', { value: o.value }); };
  const style = ['menu', 'segmented', 'inline'].includes(p.style) ? p.style : 'menu';
  const place = cx.place === 'group' || cx.place === 'toolbar' ? cx.place : 'free';
  const group = place === 'group';
  if (style === 'segmented') {
    return html`<xb-picker data-k=${n.k} class=${cls('picker', 'pk-seg', group ? 'cell' : 'free')}>
      ${p.label ? html`<div class="pk-label">${p.label}</div>` : nothing}
      <div class="seg" role="radiogroup">${opts.map((o, i) => html`<button role="radio" aria-checked=${i === idx ? 'true' : 'false'}
        class=${cls('seg-item', i === idx && 'on')} @click=${() => choose(i)}>${o.icon ? icon(o.icon) : nothing}<span>${lbl(o)}</span></button>`)}</div>
    </xb-picker>`;
  }
  if (style === 'inline') {
    const rows = opts.map((o, i) => html`<button class="cell pk-opt" role="radio" aria-checked=${i === idx ? 'true' : 'false'} @click=${() => choose(i)}>
      ${o.icon ? icon(o.icon, 'pk-oic') : nothing}<span class="pk-ot">${lbl(o)}</span>${i === idx ? icon('check', 'pk-check') : nothing}</button>`);
    return html`<xb-picker data-k=${n.k} class=${cls('picker', 'pk-inline', group ? 'in-group' : 'free')}>
      ${p.label && !group ? html`<div class="pk-label">${p.label}</div>` : nothing}
      ${group ? rows : html`<div class="group">${rows}</div>`}</xb-picker>`;
  }
  const cur = idx >= 0 ? opts[idx] : null;
  const select = html`<select class="pk-native" aria-label=${str(p.label) || 'choose'} @change=${(e) => choose(Number(e.target.value))}>
    ${idx < 0 ? html`<option value="-1" selected hidden></option>` : nothing}
    ${opts.map((o, i) => html`<option value=${i} ?selected=${i === idx}>${lbl(o)}</option>`)}</select>`;
  return html`<xb-picker data-k=${n.k} class=${cls('picker', 'pk-menu', `in-${place}`, group && 'cell')}>
    ${p.label && place !== 'toolbar' ? html`<span class="pk-label">${p.label}</span>` : nothing}
    <span class="pk-val">${cur?.icon ? icon(cur.icon) : nothing}<span>${cur ? lbl(cur) : '—'}</span>${icon('ui-updown', 'pk-ud')}</span>
    ${select}
  </xb-picker>`;
}

function menu(n, cx) {
  const p = P(n);
  const place = cx.place === 'screen' ? 'free' : cx.place;
  const label = str(p.label);
  const iconOnly = place === 'toolbar' && p.icon;
  return html`<xb-menu data-k=${n.k} class=${cls('menu', `in-${place}`, place === 'group' && 'cell')}>
    <button class=${cls('btn', `b-${place}`, 'r-default', 'menu-btn')} aria-haspopup="menu" aria-label=${iconOnly ? label || 'More' : nothing}
      @click=${(e) => cx.v.openMenu(n, e.currentTarget)}>
      ${p.icon ? icon(p.icon) : nothing}${iconOnly || !label ? nothing : html`<span class="b-label">${label}</span>`}
      ${place === 'toolbar' ? nothing : icon('ui-updown', 'menu-ud')}
    </button></xb-menu>`;
}

export const CONTROLS = { button, toggle, field, picker, menu };

// overlays(view): the view's one transient overlay, drawn above everything.
export function overlays(view) {
  const o = view._overlay;
  if (!o) return nothing;
  if (o.kind === 'confirm') {
    const c = o.opts;
    return html`<div class="ov">
      <div class="ov-scrim" @click=${() => view.closeOverlay(false)}></div>
      <div class="as" role="alertdialog" aria-label=${str(c.title) || 'Confirm'}>
        <div class="as-group">
          ${c.title || c.message ? html`<div class="as-head">${c.title ? html`<div class="as-t">${c.title}</div>` : nothing}${c.message ? html`<div class="as-m">${c.message}</div>` : nothing}</div>` : nothing}
          <button class=${cls('as-btn', c.destructive && 'danger')} @click=${() => view.closeOverlay(true)}>${str(c.label) || 'OK'}</button>
        </div>
        <button class="as-btn as-cancel" @click=${() => view.closeOverlay(false)}>Cancel</button>
      </div></div>`;
  }
  if (o.kind === 'menu') {
    const n = view.find(o.n.k) || o.n;
    const cx = view.cx('menu');
    const pos = o.at.left > o.at.right ? `right:${Math.max(8, o.at.right)}px` : `left:${Math.max(8, o.at.left)}px`;
    return html`<div class="ov">
      <div class="ov-catch" @click=${() => view.closeOverlay()}></div>
      <div class="pop" role="menu" style=${`top:${o.at.top}px;${pos}`}>${(n.c || []).map((c) => (c.t === 'divider'
        ? html`<div class="pop-sep"></div>` : cx.node(c)))}</div></div>`;
  }
  if (o.kind === 'image') {
    return html`<div class="ov ov-img" @click=${() => view.closeOverlay()}><img src=${o.url} alt=${o.alt || ''}></div>`;
  }
  return nothing;
}

export const CONTROLS_CSS = css`
  xb-button { display: block; min-width: 0; }
  xb-button.cell { padding: 0; }
  .btn { display: flex; align-items: center; justify-content: center; gap: 8px; min-width: 0; text-align: center; }
  .btn:disabled { opacity: 0.4; }
  .btn.busy:disabled { opacity: 0.75; }
  .btn .spin { color: currentColor; }
  .b-label { overflow: hidden; text-overflow: ellipsis; }
  /* free: prominent, full width */
  .b-free { width: 100%; min-height: 50px; padding: 0 20px; border-radius: 12px; font: var(--xb-font-headline);
    background: color-mix(in srgb, var(--xb-accent) 16%, transparent); color: var(--xb-accent-text); }
  .b-free.r-primary { background: var(--xb-accent); color: var(--xb-on-accent); }
  .b-free.r-destructive { background: color-mix(in srgb, var(--xb-danger) 14%, transparent); color: var(--xb-danger); }
  .b-free.r-plain { background: none; min-height: 44px; }
  .b-free:not(:disabled):active { filter: brightness(0.92); }
  xb-stack.h > xb-button, xb-stack.h > xb-menu { flex: 0 1 auto; }
  xb-stack.h .b-free { width: auto; min-height: 36px; padding: 0 14px; border-radius: 18px; font: var(--xb-font-subheadline); font-weight: 600; }
  /* group: a cell */
  .b-group { width: 100%; justify-content: flex-start; min-height: 44px; padding: 11px 16px; color: var(--xb-accent-text); text-align: left; }
  .b-group.r-primary { font-weight: 600; }
  .b-group.r-destructive { color: var(--xb-danger); }
  .b-group.r-secondary, .b-group.r-plain, .b-group.r-default { font-weight: 400; }
  .b-group:not(:disabled):active { background: var(--xb-fill); }
  .b-group .spin { margin-left: auto; order: 2; }
  /* toolbar: bare */
  .b-toolbar { min-width: 36px; height: 36px; padding: 0 8px; border-radius: 18px; color: var(--xb-accent-text); font: var(--xb-font-body); }
  .b-toolbar.r-primary { font-weight: 600; }
  .b-toolbar.r-destructive { color: var(--xb-danger); }
  .b-toolbar .ic { width: calc(var(--xb-icon) + 2px); height: calc(var(--xb-icon) + 2px); }
  /* actions: compact pills */
  .b-actions { height: 32px; padding: 0 13px; border-radius: 16px; font: var(--xb-font-subheadline); font-weight: 600; background: var(--xb-fill); color: var(--xb-text); gap: 5px; }
  .b-actions .ic { width: 16px; height: 16px; stroke-width: 2.4; }
  .b-actions.r-primary { background: var(--xb-accent); color: var(--xb-on-accent); }
  .b-actions.r-destructive { background: color-mix(in srgb, var(--xb-danger) 15%, transparent); color: var(--xb-danger); }
  .b-actions.r-secondary { background: color-mix(in srgb, var(--xb-accent) 16%, transparent); color: var(--xb-accent-text); }
  /* menu items */
  .b-menu { width: 100%; justify-content: space-between; padding: 11px 16px; min-height: 44px; text-align: left; }
  .b-menu.r-destructive { color: var(--xb-danger); }
  .b-menu:active { background: var(--xb-fill); }
  /* dock chips (composer) */
  .b-dock, .b-chips { height: 30px; padding: 0 12px; border-radius: 15px; font: var(--xb-font-footnote); font-weight: 600; background: var(--xb-surface2); color: var(--xb-text); }

  xb-toggle { display: flex; align-items: center; gap: 12px; }
  xb-toggle.cell { padding-top: 7px; padding-bottom: 7px; }
  xb-toggle.free { padding: 4px 0; }
  .tg-label { flex: 1; min-width: 0; cursor: pointer; overflow-wrap: anywhere; }
  .switch { position: relative; flex: none; width: 51px; height: 31px; border-radius: 16px; background: var(--xb-fill); transition: background 0.2s; }
  .switch.on { background: var(--xb-ok); }
  .knob { position: absolute; top: 2px; left: 2px; width: 27px; height: 27px; border-radius: 50%; background: #fff;
    box-shadow: 0 2px 6px rgba(0, 0, 0, 0.2), 0 0 0 0.5px rgba(0, 0, 0, 0.06); transition: transform 0.2s; }
  .switch.on .knob { transform: translateX(20px); }

  xb-field { display: block; }
  xb-field.cell { padding-top: 8px; padding-bottom: 9px; }
  .f-wrap { display: flex; flex-direction: column; gap: 2px; }
  .f-label { font: var(--xb-font-footnote); color: var(--xb-muted); }
  .f-box { display: flex; align-items: center; gap: 6px; min-width: 0; color: var(--xb-muted); }
  .f-box .ic { width: 18px; height: 18px; }
  .f-input { flex: 1; min-width: 0; width: 100%; border: 0; outline: 0; background: none; padding: 2px 0; font: var(--xb-font-body); color: var(--xb-text); }
  textarea.f-input { resize: none; field-sizing: content; min-height: 66px; max-height: 240px; line-height: 1.3; }
  .f-input::placeholder { color: color-mix(in srgb, var(--xb-muted) 70%, transparent); }
  .f-input:disabled { color: var(--xb-muted); }
  .k-secure .f-input:not(:placeholder-shown) { letter-spacing: 1.5px; }
  xb-field.free .f-label { padding: 0 4px 4px; }
  xb-field.free .f-box { background: var(--xb-surface); border-radius: 10px; padding: 11px 12px; box-shadow: inset 0 0 0 0.5px var(--xb-separator); }
  xb-field.invalid .f-box { box-shadow: inset 0 0 0 1px var(--xb-danger); }
  xb-field.cell.invalid .f-box { box-shadow: none; }
  .f-hint, .f-error { font: var(--xb-font-footnote); color: var(--xb-muted); margin-top: 4px; }
  .f-error { color: var(--xb-danger); }
  xb-field.free .f-hint, xb-field.free .f-error { padding: 0 4px; }

  xb-picker { display: block; min-width: 0; }
  .pk-menu { position: relative; display: flex; align-items: center; gap: 12px; }
  .pk-menu .pk-label { flex: 1 1 auto; min-width: 0; overflow-wrap: anywhere; }
  .pk-val { display: flex; align-items: center; gap: 4px; min-width: 0; margin-left: auto; color: var(--xb-muted); text-align: right; }
  .pk-val > span { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .pk-ud { width: 15px; height: 15px; flex: none; }
  .pk-native { position: absolute; inset: 0; width: 100%; height: 100%; opacity: 0; cursor: pointer; font-size: 16px; }
  .pk-menu.in-free { background: var(--xb-surface); border-radius: 10px; padding: 11px 12px; }
  .pk-menu.in-toolbar { padding: 0 8px; height: 36px; }
  .pk-menu.in-toolbar .pk-val { color: var(--xb-accent-text); font-weight: 600; max-width: 170px; }
  .pk-label { font: inherit; }
  .pk-seg .pk-label, .pk-inline.free .pk-label { font: var(--xb-font-footnote); color: var(--xb-muted); padding: 0 4px 6px; }
  .pk-seg.cell { padding-top: 8px; padding-bottom: 8px; }
  .pk-seg .seg-item .ic { width: 16px; height: 16px; }
  .pk-inline.in-group { display: contents; }
  .pk-opt { display: flex; align-items: center; gap: 12px; width: 100%; text-align: left; }
  .pk-ot { flex: 1; min-width: 0; }
  .pk-oic { color: var(--xb-accent-text); }
  .pk-check { color: var(--xb-accent-text); stroke-width: 2.6; }

  xb-menu { display: block; min-width: 0; }
  xb-menu.cell { padding: 0; }
  .menu-ud { width: 15px; height: 15px; opacity: 0.8; }
  .b-group.menu-btn { justify-content: flex-start; }
  .b-group.menu-btn .menu-ud { margin-left: auto; }
`;

export const OVERLAY_CSS = css`
  .ov { position: absolute; inset: 0; z-index: 50; }
  .ov-scrim { position: absolute; inset: 0; background: var(--xb-scrim); animation: xb-fade 0.18s ease-out; }
  .ov-catch { position: absolute; inset: 0; }
  .as { position: absolute; left: 8px; right: 8px; bottom: 12px; display: flex; flex-direction: column; gap: 8px; animation: xb-rise 0.22s cubic-bezier(0.2, 0.9, 0.3, 1); }
  .as-group { background: var(--xb-surface); border-radius: 14px; overflow: hidden; }
  .as-head { padding: 14px 16px 12px; text-align: center; border-bottom: 0.5px solid var(--xb-separator); }
  .as-t { font: var(--xb-font-footnote); font-weight: 600; color: var(--xb-muted); }
  .as-m { font: var(--xb-font-footnote); color: var(--xb-muted); margin-top: 2px; }
  .as-btn { width: 100%; min-height: 56px; font: var(--xb-font-title-3); color: var(--xb-accent-text); }
  .as-btn.danger { color: var(--xb-danger); }
  .as-cancel { background: var(--xb-surface); border-radius: 14px; font-weight: 600; }
  .pop { position: absolute; min-width: 220px; max-width: calc(100% - 16px); background: var(--xb-surface); border-radius: 13px;
    box-shadow: var(--xb-shadow), 0 0 0 0.5px var(--xb-separator); overflow: hidden; animation: xb-fade 0.12s ease-out; }
  .pop xb-button + xb-button { border-top: 0.5px solid var(--xb-separator); }
  .pop-sep { height: 8px; background: var(--xb-fill); }
  .ov-img { background: rgba(0, 0, 0, 0.92); display: flex; align-items: center; justify-content: center; cursor: zoom-out; }
  .ov-img img { max-width: 100%; max-height: 100%; object-fit: contain; }
`;
