// effort-chip.js — the composer's reasoning-effort chip (D249), beside the
// model picker (#msel): "High ▾", "Default · High ▾". Hidden for a model
// with no record or no reasoning, a coding agent's conversation and the
// workspace agent. Its popover is a notched track over the model's own
// levels (Off first when it can be turned off; Off | On for a model that is
// only on or off), a Default button that sends nothing, "always thinks"
// under the track for a model that can't be turned off, and what a level
// becomes on this model when it lacks it. Keyboard: ←/→ notch to notch,
// Home/End the ends, 0 Default, Enter or Esc closes. In a conversation the
// pick is PATCH /runs/{id} {effort}, from its next turn; at home it is the
// next new chat's only (not remembered). The words and the rule are
// model/effort.js's.
import { html, render, nothing } from '/vendor/lit-all.min.js';
import { ext, ctx } from './web-ext.js';
import { chip, step, DEFAULT } from './model/effort.js';
import { access } from './model/rules.js';

const $ = (id) => document.getElementById(id);
const st = { open: false, busy: false, err: '' };
let el, pop;

function mount() {
  if (el) return;
  el = Object.assign(document.createElement('span'), { id: 'echip', className: 'echip', hidden: true });
  $('msel').after(el); // beside the model it is the model's (index.html .cpicks)
  pop = Object.assign(document.createElement('div'), { id: 'epop', className: 'epop', hidden: true });
  pop.setAttribute('role', 'dialog');
  pop.setAttribute('aria-label', 'Reasoning effort');
  document.body.append(pop);
  document.addEventListener('click', (e) => {
    if (st.open && !pop.contains(e.target) && !el.contains(e.target)) { st.open = false; ctx.paint(); }
  });
  pop.addEventListener('keydown', (e) => {
    if (e.key === 'Escape' || (e.key === 'Enter' && e.target.closest('.etrack'))) { e.preventDefault(); close(); }
  });
}

const view = () => ctx.app && ctx.app.session.current();

// current: the chip for what the composer is on now
function current(v) {
  const app = ctx.app;
  const cls = v ? v.class : app.classes && (app.classes.classes || []).find((c) => c.id === app.newClassId());
  return chip(v, { catalog: app.catalog, model: app.model, effort: app.effort, cls, tileDefault: app.catalog && app.catalog.defaultEffort,
    talk: v ? access(v).talk : true });
}

function close() {
  st.open = false;
  ctx.paint();
  el.querySelector('button')?.focus();
}

async function pick(value) {
  st.busy = true; st.err = '';
  ctx.paint();
  try { await ctx.app.pickEffort(value); } catch (e) { st.err = e.message || String(e); }
  st.busy = false;
  ctx.paint();
  pop.querySelector('[aria-checked="true"]')?.focus();
}

ext.register({
  paint(v) {
    const app = ctx.app;
    if (!app) return;
    mount();
    const c = current(v);
    // a coding agent picked for new chats hides the built-in model, and with it this
    const shown = c.shown && !$('msel').hidden && !(!v && app.harness && app.harness.picked && app.harness.picked());
    el.hidden = !shown;
    if (!shown) { st.open = false; render(nothing, el); drawPop(null); return; }
    render(html`<button class="btn ghost echipb" type="button" aria-haspopup="dialog" aria-expanded=${st.open ? 'true' : 'false'}
      title=${c.title} data-effort=${c.value} ?disabled=${c.disabled}
      @click=${(e) => { e.stopPropagation(); st.open = !st.open; st.err = ''; ctx.paint(); if (st.open) setTimeout(() => pop.querySelector('[aria-checked="true"]')?.focus(), 0); }}>
      <bx-icon name="thought"></bx-icon><span class="echipt">${c.label}</span><bx-icon name="caret-down"></bx-icon></button>`, el);
    drawPop(st.open ? c : null, v);
  },
});

function drawPop(c, v) {
  if (!pop) return;
  if (!c) { pop.hidden = true; render(nothing, pop); return; }
  const r = el.getBoundingClientRect();
  pop.hidden = false;
  pop.style.left = Math.max(8, Math.min(r.left, innerWidth - 348)) + 'px';
  pop.style.bottom = Math.max(8, innerHeight - r.top + 6) + 'px';
  const bare = c.model.includes('|') ? c.model.slice(c.model.indexOf('|') + 1) : c.model;
  const onKey = (e) => {
    const to = step(c, e.key);
    if (to == null) return;
    e.preventDefault();
    if (to !== c.value) pick(to);
  };
  const long = !!v && (v.messages || []).length > 12;
  const isDefault = c.value === DEFAULT;
  render(html`
    <div class="esec">Reasoning · <span class="mono">${bare}</span></div>
    <div class="etrack" role="radiogroup" aria-label="reasoning effort" @keydown=${onKey}>
      ${c.notches.map((n, i) => html`<button type="button" class="enotch ${n.value === c.value ? 'on' : ''}" role="radio" data-notch=${n.value}
          aria-checked=${n.value === c.value ? 'true' : 'false'} tabindex=${n.value === c.value || (isDefault && i === 0) ? '0' : '-1'}
          ?disabled=${st.busy || c.disabled} @click=${() => pick(n.value)}><span class="edot"></span><span class="elab">${n.label}</span></button>`)}
    </div>
    <div class="eends muted small"><span>faster, cheaper</span><span>slower, more thorough</span></div>
    <div class="erow">
      <button type="button" class="btn ghost btnsm edef ${isDefault ? 'on' : ''}" data-notch="default" aria-pressed=${isDefault ? 'true' : 'false'}
        ?disabled=${st.busy || c.disabled} title="send nothing: the model thinks as it does by default" @click=${() => pick(DEFAULT)}>${c.def}</button>
      ${c.always ? html`<span class="muted small" data-always>always thinks</span>` : nothing}
    </div>
    ${c.note ? html`<div class="muted small" data-effort-note>${c.note}</div>` : nothing}
    ${long ? html`<div class="muted small">Changing it can make the next call re-read the whole conversation (its cache).</div>` : nothing}
    ${v ? html`<div class="muted small">From the next turn of this conversation.</div>` : html`<div class="muted small">For your next new chat.</div>`}
    ${st.err ? html`<div class="err small" data-effort-err>${st.err}</div>` : nothing}`, pop);
}

const style = document.createElement('style');
style.textContent = `
  .echip { align-self: flex-end; display: flex; min-width: 0; }
  .echip .echipb { min-width: 0; max-width: 24ch; gap: 4px; }
  .echip .echipt { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .epop { position: fixed; z-index: 30; box-sizing: border-box; width: min(340px, calc(100vw - 16px)); background: var(--bx-panel);
    border: 1px solid var(--bx-border-strong); border-radius: var(--bx-radius); padding: 8px 12px 10px; box-shadow: var(--bx-shadow-pop); }
  .epop .esec { font: var(--bx-font-micro); letter-spacing: var(--bx-tracking-micro); text-transform: uppercase; color: var(--bx-muted); margin-bottom: 6px;
    overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .epop .esec .mono { text-transform: none; letter-spacing: 0; }
  .epop .etrack { position: relative; display: flex; justify-content: space-between; padding: 0 2px; }
  .epop .etrack::before { content: ''; position: absolute; left: 14px; right: 14px; top: 9px; border-top: 2px solid var(--bx-border-strong); }
  .epop .enotch { position: relative; display: flex; flex-direction: column; align-items: center; gap: 4px; background: none; border: 0; padding: 2px 0;
    color: var(--bx-muted); cursor: pointer; font: var(--bx-font-meta); min-width: 28px; }
  .epop .enotch .edot { width: 14px; height: 14px; box-sizing: border-box; border: 2px solid var(--bx-border-strong); border-radius: var(--bx-radius); background: var(--bx-panel); }
  .epop .enotch.on { color: var(--bx-text); }
  .epop .enotch.on .edot { background: var(--bx-accent); border-color: var(--bx-accent); }
  .epop .enotch:focus-visible { outline: var(--bx-focus-outline); outline-offset: var(--bx-focus-offset); }
  .epop .eends { display: flex; justify-content: space-between; margin: 2px 0 8px; }
  .epop .erow { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; margin-bottom: 4px; }
  .epop .edef.on { background: var(--bx-selection); color: var(--bx-selection-text); }
`;
document.head.append(style);
