// harness-controls.js — driving a coding harness from the web's composer
// (D147 §4.2.4, §4.2.10, §4.3.12):
//   #hctl    one button beside the attach clip, "Accept edits · Opus · High ▾":
//            its popover switches the live mode (the adapter's modes; a
//            bypass mode is marked ⚠, the owner's only, and confirmed) and
//            the config options (model, effort…) — PATCH /runs/{id}/harness —
//            and holds your Auto / Always approve for that harness
//            (/prefs/harness-mode); at home, while a harness answers new
//            chats (app.harness.picked()), it holds that setting
//   /        typing "/" offers the harness's advertised commands (↑↓, Tab
//            or Enter picks, Esc dismisses)
//   ⌘/Ctrl+Enter  while its turn runs, sends with {interrupt: true} (Enter
//            steers or queues, as the placeholder says); Stop interrupts
//   steered  a message steered into the running turn says so for a moment
//            (from the stream: model/harness-ask.js steerTrack)
//   account  in a person's own partition, #hctl's label ends with the account
//            its coding agent uses ("using Work") and the popover's Account
//            section switches it — the default, another saved sign-in, the
//            sandbox's own — and opens Saved sign-ins… (D179:
//            model/harness-signins.js accountOf, harness-catalog.js openSignins)
// All of it through the web's seams (web-ext.js: paint) and elements of its
// own; the words are model/harness-ask.js's. The queued chips' label is
// agent.js's (queueTpl with steerWords).
import { html, render, nothing, live } from '/vendor/lit-all.min.js';
import { ext, ctx } from './web-ext.js';
import { harnessOf, nameOf } from './model/harness.js';
import { controls, settingOf, slashCommands, slashMatches, slashText, steerWords, steerTrack, ownerOf, modeConfirm } from './model/harness-ask.js';
import { access } from './model/rules.js';
import { barredWhy } from './model/harness-homes.js'; // one in the shared space isn't driven: its mode and options are shown, not switched
import { accountOf, switchWords } from './model/harness-signins.js';
import { partitionState } from './model/partition.js';
import { openSignins } from './harness-catalog.js';

const $ = (id) => document.getElementById(id);
const st = { open: false, busy: false, err: '', note: '', sel: 0, dismissed: null, shown: null, timer: 0 };
let hctl, pop, slash, steer, stopTitle = '';
const track = steerTrack();
const clip = (s, n) => { s = String(s ?? ''); return s.length > n ? s.slice(0, n - 1) + '…' : s; };

// mount makes the elements once: #hctl in the composer (hidden unless a
// harness is in play, so the composer is as before otherwise), the popover,
// the slash menu and the steered note floating above it.
function mount() {
  if (hctl) return;
  hctl = Object.assign(document.createElement('span'), { id: 'hctl', className: 'hctl', hidden: true });
  $('ssel').after(hctl); // the last of the pickers (index.html .cpicks)
  for (const [id, cls] of [['hctl-pop', 'hctlpop'], ['slash', 'hslash'], ['hsteer', 'hsteer']]) {
    document.body.append(Object.assign(document.createElement('div'), { id, className: cls, hidden: true }));
  }
  pop = $('hctl-pop'); slash = $('slash'); steer = $('hsteer');
  stopTitle = $('stop').title;
  $('msg').addEventListener('input', syncSlash);
  $('msg').addEventListener('blur', () => setTimeout(() => { if (document.activeElement !== $('msg')) hideSlash(); }, 150));
}

const repaint = () => ctx.paint();
const view = () => ctx.app && ctx.app.session.current();

ext.register({
  paint(v) {
    const app = ctx.app;
    if (!app) return;
    mount();
    const h = v ? harnessOf(v) : null;
    const home = !v && !app.page ? app.harness.picked() : null;
    if (h) app.harness.ensure();
    if (h && partitionState() === 'user') app.harness.ensureSignins(); // the account (D179)
    hctl.hidden = !(h || home);
    if (!h && !home) st.open = false;
    const acct = h ? accountOf(h, app.harness.signins) : null;
    render(h ? buttonTpl(controls(h, app.harness.find(h.provider), who(v)).label + (acct.shown && acct.label ? ` · ${acct.label}` : '')) : home ? buttonTpl(homeLabel(home)) : nothing, hctl);
    drawPop(v, h, home);
    // the composer's words on a harness run (null: the built-in ones stand)
    const w = steerWords(v);
    if (w && !$('msg').disabled) $('msg').placeholder = w.placeholder;
    $('stop').title = h ? `Stop — interrupts ${nameOf(h)}'s turn; messages still queued come back here` : stopTitle;
    drawSteered(v, h);
    syncSlash();
  },
});

const who = (v) => ({ owner: ownerOf(v, ctx.app.me), talk: access(v).talk && !barredWhy(v.run) });

// --- #hctl ------------------------------------------------------------------------------

const buttonTpl = (label) => html`<button class="btn ghost hctlb" type="button" title="the coding agent's mode, options and your setting"
  @click=${(e) => { e.stopPropagation(); st.open = !st.open; st.err = ''; repaint(); }}>${label} ▾</button>`;

function homeLabel(entry) {
  const s = settingOf(entry, ctx.app.harness.setting(entry.id));
  return `${s.name}: ${s.choices.find((c) => c.value === s.value).label}`;
}

function drawPop(v, h, home) {
  if (!st.open || (!h && !home)) { pop.hidden = true; render(nothing, pop); return; }
  const r = hctl.getBoundingClientRect();
  pop.hidden = false;
  pop.style.left = Math.max(8, Math.min(r.left, innerWidth - 348)) + 'px';
  pop.style.bottom = Math.max(8, innerHeight - r.top + 6) + 'px';
  const app = ctx.app;
  if (!h) { render(html`${settingTpl(home)}`, pop); return; }
  const c = controls(h, app.harness.find(h.provider), who(v));
  const runId = v.run.id;
  render(html`
    <div class="hsec">Mode</div>
    ${c.modes.length ? c.modes.map((m) => html`<label class="hmode ${m.explicit ? 'hwarn' : ''} ${m.allowed ? '' : 'off'}" data-mode=${m.id}
        title=${!m.allowed && m.explicit ? 'only the owner can switch to a bypass mode' : m.description || nothing}>
      <input type="radio" name="hctl-mode" .checked=${live(m.current)} ?disabled=${!m.allowed || st.busy} @change=${() => pickMode(runId, c, m)}>
      <span><span class="hmn">${m.explicit ? '⚠ ' : ''}${m.name}</span>${m.description ? html`<span class="muted small"> — ${m.description}</span>` : nothing}</span></label>`)
      : html`<div class="muted small">${c.name} advertises no modes</div>`}
    ${c.options.map((o) => html`<div class="hsec" title=${o.description || nothing}>${o.name}</div>
      <select class="hsel" data-opt=${o.id} ?disabled=${!c.talk || st.busy} .value=${live(String(o.value))}
        @change=${(e) => pickOption(runId, o, e.target.value)}>
        ${o.choices.map((ch) => html`<option value=${String(ch.value)} ?selected=${String(ch.value) === String(o.value)}
          title=${ch.description || nothing}>${ch.name}</option>`)}</select>`)}
    ${accountTpl(runId, h, c)}
    ${st.busy ? html`<div class="muted small">switching…</div>` : nothing}
    ${st.err ? html`<div class="err small">${st.err}</div>` : nothing}
    ${st.note && !st.err ? html`<div class="muted small" id="hctl-note">${st.note}</div>` : nothing}
    ${settingTpl(app.harness.find(h.provider), true)}`, pop);
}

// the account its coding agent uses (D179): the default, another saved
// sign-in or the sandbox's own; a switch restarts it with that one at the
// next message, resuming the session
function accountTpl(runId, h, c) {
  const app = ctx.app;
  const acct = accountOf(h, app.harness.signins);
  if (!acct.shown) return nothing;
  const pick = (ch) => {
    if (ch.current || ch.disabled) return;
    st.note = '';
    patch(async () => { await app.harness.pickSignin(runId, ch.value); st.note = switchWords(c.name, ch); });
  };
  return html`<div class="hsec">Account</div>
    ${acct.choices.map((ch) => html`<label class="hmode hacct ${ch.disabled ? 'off' : ''}" data-signin=${ch.value} title=${ch.why || nothing}>
      <input type="radio" name="hctl-acct" .checked=${live(ch.current)} ?disabled=${ch.disabled || st.busy || !c.talk} @change=${() => pick(ch)}>
      <span>${ch.label}${ch.why ? html`<span class="muted small"> — ${ch.why}</span>` : nothing}</span></label>`)}
    ${acct.warn ? html`<div class="err small" id="hctl-acctwarn">${acct.warn}</div>` : nothing}
    <button class="btn btnsm ghost" type="button" id="hctl-signins" @click=${() => { st.open = false; repaint(); openSignins(app); }}>Saved sign-ins…</button>`;
}

// a call on the live session: the popover says it's on its way, then why it failed;
// its result comes back as the run's harness event (the stream)
async function patch(fn) {
  st.busy = true; st.err = ''; repaint();
  try { await fn(); } catch (e) { st.err = e.message; }
  st.busy = false; repaint();
}
function pickMode(runId, c, m) {
  if (m.current) return;
  if (m.explicit && !confirm(modeConfirm(c.name, m))) { repaint(); return; }
  patch(() => ctx.app.harness.setMode(runId, m.id));
}
function pickOption(runId, o, value) {
  const ch = o.choices.find((x) => String(x.value) === value);
  if (!ch || ch.value === o.value) return;
  patch(() => ctx.app.harness.setOptionOf(runId, o.id, ch.value));
}

// Auto / Always approve, the person's own for this harness
function settingTpl(entry, inConv = false) {
  if (!entry) return nothing;
  const s = settingOf(entry, ctx.app.harness.setting(entry.id));
  return html`<div class="hsec">Your setting for ${s.name}</div>
    <div class="hseg">${s.choices.map((c) => html`<button class="btn btnsm ${s.value === c.value ? 'on' : 'ghost'}" type="button" data-setting=${c.value}
      aria-pressed=${s.value === c.value ? 'true' : 'false'} title=${c.title} ?disabled=${c.disabled || st.busy}
      @click=${() => setSetting(s, c.value)}>${s.value === c.value ? '✓ ' : ''}${c.label}</button>`)}</div>
    <div class="muted small">${s.note}${inConv ? ' This conversation\'s own mode is switched above.' : ''}</div>`;
}
function setSetting(s, mode) {
  if (mode === s.value) return;
  patch(() => ctx.app.harness.setSetting(s.provider, mode));
}

// a click elsewhere or Esc closes the popover
document.addEventListener('click', (e) => {
  if (st.open && pop && !pop.contains(e.target) && !hctl.contains(e.target)) { st.open = false; repaint(); }
});

// --- the slash menu -------------------------------------------------------------------------

function syncSlash() {
  if (!slash) return;
  const v = view();
  const h = v ? harnessOf(v) : null;
  const text = $('msg').value;
  const list = h && access(v).talk ? slashMatches(slashCommands(h), text) : null;
  if (text !== st.dismissed) st.dismissed = null;
  st.shown = list && list.length && st.dismissed == null ? list : null;
  if (!st.shown) { hideSlash(); return; }
  st.sel = Math.min(st.sel, st.shown.length - 1);
  const r = $('msg').getBoundingClientRect();
  slash.hidden = false;
  slash.style.left = r.left + 'px';
  slash.style.bottom = (innerHeight - r.top + 4) + 'px';
  slash.style.width = Math.max(220, Math.min(r.width, 460)) + 'px';
  render(html`${st.shown.map((c, i) => html`<div class="hsl ${i === st.sel ? 'on' : ''}" data-cmd=${c.name}
      @mousedown=${(e) => { e.preventDefault(); pickSlash(c); }}>
    <span class="mono">/${c.name}</span>${c.hint ? html` <span class="muted">${c.hint}</span>` : nothing}
    ${c.description ? html`<span class="muted small hsd">${c.description}</span>` : nothing}</div>`)}`, slash);
}
function hideSlash() {
  if (!slash || slash.hidden) return;
  slash.hidden = true; st.shown = null; st.sel = 0;
  render(nothing, slash);
}
function pickSlash(c) {
  const m = $('msg');
  m.value = slashText(c);
  m.focus();
  m.setSelectionRange(m.value.length, m.value.length);
  m.dispatchEvent(new Event('input')); // the composer grows; the menu closes (a space follows the name)
}

// The composer's keys, before its own (capture, on the document): the slash
// menu's while it shows; ⌘/Ctrl+Enter interrupts a harness's running turn.
document.addEventListener('keydown', (e) => {
  if (e.key === 'Escape' && st.open) { st.open = false; repaint(); e.stopPropagation(); return; }
  if (e.target !== $('msg') || e.isComposing || e.keyCode === 229) return;
  const eat = () => { e.preventDefault(); e.stopPropagation(); };
  if (st.shown) {
    const n = st.shown.length;
    if (e.key === 'ArrowDown') { eat(); st.sel = (st.sel + 1) % n; syncSlash(); return; }
    if (e.key === 'ArrowUp') { eat(); st.sel = (st.sel + n - 1) % n; syncSlash(); return; }
    if ((e.key === 'Enter' && !e.shiftKey && !e.metaKey && !e.ctrlKey) || e.key === 'Tab') { eat(); pickSlash(st.shown[st.sel]); return; }
    if (e.key === 'Escape') { eat(); st.dismissed = $('msg').value; hideSlash(); return; }
  }
  if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
    const w = steerWords(view());
    if (!w || !w.busy) return; // not a turn to interrupt: Enter's usual send
    eat();
    const m = $('msg');
    ctx.app.send(m.value, () => { m.value = ''; m.dispatchEvent(new Event('input')); }, { interrupt: true });
  }
}, true);

// --- steered -----------------------------------------------------------------------------

function drawSteered(v, h) {
  const notes = h && h.steering ? track(v, ctx.app.session.shown().blocks, Date.now()) : track(null);
  clearTimeout(st.timer);
  if (!notes.length) { steer.hidden = true; render(nothing, steer); return; }
  const r = document.querySelector('.composer').getBoundingClientRect();
  steer.hidden = false;
  steer.style.left = (r.left + 12) + 'px';
  steer.style.bottom = (innerHeight - r.top + 4) + 'px';
  render(html`${notes.map((n) => html`<div class="hsn" role="status">↳ steered into ${nameOf(h)}'s running turn: <span class="mono">${clip(n.text, 60)}</span></div>`)}`, steer);
  st.timer = setTimeout(repaint, Math.max(50, Math.min(...notes.map((n) => n.until)) - Date.now() + 20));
}

const style = document.createElement('style');
style.textContent = `
  .hctl { align-self: flex-end; flex: 1 1 0; min-width: 2em; max-width: max-content; display: flex; } /* the pickers' row: what's left of it */
  .hctl[hidden] { display: none; }
  .hctl .hctlb { min-width: 0; max-width: 34ch; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .hctlpop .hacct.off { opacity: .55; }
  .hctlpop, .hslash, .hsteer { position: fixed; z-index: 30; }
  .hctlpop[hidden], .hslash[hidden], .hsteer[hidden] { display: none; }
  .hctlpop { box-sizing: border-box; width: min(340px, calc(100vw - 16px)); max-height: 70vh; overflow: auto; background: var(--bx-panel); border: 1px solid var(--bx-border);
    border-radius: 6px; padding: 8px 10px; box-shadow: 0 4px 18px rgba(0,0,0,.18); font-size: 12.5px; }
  .hctlpop .hsec { font-size: 10.5px; text-transform: uppercase; letter-spacing: .05em; color: var(--bx-muted); margin: 8px 0 3px; }
  .hctlpop .hsec:first-child { margin-top: 0; }
  .hctlpop .hmode { display: flex; gap: 6px; align-items: flex-start; padding: 2px 0; cursor: pointer; }
  .hctlpop .hmode.off { opacity: .55; cursor: default; }
  .hctlpop .hmode.hwarn .hmn { color: var(--bx-red, #ef5350); }
  .hctlpop .hsel { width: 100%; }
  .hctlpop .hseg { display: flex; gap: 4px; margin-bottom: 3px; }
  .hctlpop .hseg .btn.on { font-weight: 600; }
  .hslash { background: var(--bx-panel); border: 1px solid var(--bx-border); border-radius: 6px; padding: 3px 0; box-shadow: 0 4px 18px rgba(0,0,0,.18);
    max-height: 40vh; overflow: auto; font-size: 12.5px; }
  .hslash .hsl { padding: 3px 10px; cursor: pointer; display: flex; gap: 6px; align-items: baseline; white-space: nowrap; }
  .hslash .hsl.on { background: var(--bx-panel-2); }
  .hslash .hsd { overflow: hidden; text-overflow: ellipsis; }
  .hsteer .hsn { font-size: 11.5px; padding: 2px 8px; border-radius: 10px; background: var(--bx-panel-2); border: 1px solid var(--bx-border); margin-top: 3px; }
`;
document.head.append(style);
