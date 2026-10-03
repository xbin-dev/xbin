/**
 * <bx-devices> — "my devices" (docs/auth.md §Device login): the xbin app's
 * devices enrolled for the signed-in user. Lists them (name, platform, last
 * sign-in) with each one's push registration (GET /devices/push: what it is
 * notified about, when it was last sent one, whether the relay wants a new
 * handle, its Live Activities — whether xbind may start one for a long agent
 * turn, and the cards it follows — removable on its own), removes one (its app sessions and push
 * registration end at once), and adds one: a
 * one-time enrollment code shown as a QR code of the xbin://enroll link plus
 * the raw link, valid five minutes — the panel watches the list and says so
 * when the phone has enrolled. Minting a code is a step-up: a sign-in older
 * than ten minutes is asked for the password first (or, with no password
 * sign-in on the account, to sign in again) — a device outlives the session
 * that adds it. The QR code carries the address the phone should use: the
 * server's origin by default, or one the user types ("address your phone
 * uses" — for a browser that reaches xbin through a tunnel or a proxy the
 * phone can't use), remembered per browser; the device still signs the
 * origin its enrollment answer names (docs/auth.md §Device login). Opened
 * from the shell's settings menu — "add a device" (openDevices({add: true}),
 * straight to the code) or my account → devices…; a modal over the
 * workspace that removes itself on close. The shell is chrome: these calls
 * ride the session cookie.
 */
import { LitElement, html, css, nothing } from 'lit';
import { scrollCss } from '/vendor/scroll-css.js';
import { xbinApi as call } from '/vendor/bx-kit.js';
import '/vendor/bx-icons.js';
import { baseCss } from './shell-css.js';

// openDevices({add}): open the panel — with add, straight on the add flow.
export function openDevices({ add = false } = {}) {
  const open = document.querySelector('bx-devices');
  if (open) { if (add) open.addDevice(); return; }
  const el = document.createElement('bx-devices');
  el.startAdd = add;
  document.body.append(el);
}

// The address the phone uses (per browser; '' = the server's origin).
const ADDR_KEY = 'xbin-phone-address';
function savedAddr() { try { return localStorage.getItem(ADDR_KEY) || ''; } catch { return ''; } }
function saveAddr(v) { try { if (v) localStorage.setItem(ADDR_KEY, v); else localStorage.removeItem(ADDR_KEY); } catch { /* storage off: not remembered */ } }
// phoneOrigin: v as an http(s) origin (scheme://host[:port], no path, query
// or credentials; https:// assumed when no scheme is typed) — or null.
export function phoneOrigin(v) {
  const t = String(v ?? '').trim();
  let u;
  try { u = new URL(t.includes('://') ? t : `https://${t}`); } catch { return null; }
  if (!/^https?:$/.test(u.protocol) || !u.hostname || u.username || u.password || u.search || u.hash) return null;
  if (u.pathname !== '/' || /^[a-z]+:\/\/[^/]*\/./i.test(t)) return null;
  return u.origin;
}
const enrollLink = (addr, code) => `xbin://enroll?u=${encodeURIComponent(addr)}&c=${code}`;

function ago(t) {
  if (!t) return 'never';
  const s = Math.max(0, Date.now() / 1000 - t);
  if (s < 60) return 'just now';
  if (s < 3600) return `${Math.round(s / 60)} min ago`;
  if (s < 86400) return `${Math.round(s / 3600)} h ago`;
  if (s < 30 * 86400) return `${Math.round(s / 86400)} d ago`;
  return new Date(t * 1000).toLocaleDateString();
}

// The QR code as one SVG path (qrcode-generator, vendored; loaded on first
// use). Dark modules on a light ground with the 4-module quiet zone scanners
// expect — whatever the theme (.qr: a fixed pair, not tokens).
async function qrPath(text) {
  const { default: qrcode } = await import('/vendor/qrcode.mjs');
  const qr = qrcode(0, 'M');
  qr.addData(text);
  qr.make();
  const n = qr.getModuleCount();
  let d = '';
  for (let r = 0; r < n; r++) {
    for (let c = 0; c < n; c++) if (qr.isDark(r, c)) d += `M${c + 4} ${r + 4}h1v1h-1z`;
  }
  return { d, size: n + 8 };
}

export class BxDevices extends LitElement {
  static properties = {
    _devices: { state: true }, // null = loading
    _push: { state: true },    // GET /devices/push {enabled, devices:[{deviceId, kinds, lastSent, needsNewHandle, pushToStart, activities}]}; null = unavailable
    _enroll: { state: true },  // {code, url, base, origin, addr, expires, qr, known:Set, added} — url/qr carry addr
    _addrErr: { state: true }, // the typed phone address isn't an http(s) origin
    _stepUp: { state: true },  // {mode: 'password'|'signin', msg, retry} — the server asked to re-prove it's you
    _confirm: { state: true }, // device id awaiting "remove?" confirmation
    _err: { state: true },
    _now: { state: true },
  };

  static styles = [scrollCss, baseCss, css`
    :host { position: fixed; inset: 0; z-index: 4000; display: block; }
    .backdrop { position: absolute; inset: 0; background: var(--bx-scrim, rgba(0, 0, 0, 0.55)); }
    /* a dialog (product-ui 6): square, 1 px border_strong, the title type */
    .box {
      position: absolute; left: 50%; top: 45%; transform: translate(-50%, -50%);
      width: min(470px, calc(100vw - 32px)); max-height: 86vh; overflow: auto; box-sizing: border-box;
      background: var(--bx-panel, #1F2028); color: var(--bx-text, #E9EAF0);
      border: 1px solid var(--bx-border-strong, #666A7E); border-radius: var(--bx-radius, 2px);
      box-shadow: var(--bx-shadow-pop, 0 12px 32px rgba(0, 0, 0, 0.6));
      padding: 16px; font: var(--bx-font, 13px/18px system-ui, sans-serif);
    }
    h3 { margin: 0 0 4px; font: var(--bx-font-title, 600 16px/22px system-ui, sans-serif); display: flex; align-items: center; gap: 8px; }
    h3 .x { margin-left: auto; }
    .lede { margin: 0 0 12px; color: var(--bx-muted, #A3A6B6); }
    ul.devs { list-style: none; margin: 0; padding: 0; }
    ul.devs > li { display: flex; align-items: center; gap: 12px; padding: 8px 0; border-top: 1px solid var(--bx-border, #33353F); }
    ul.devs > li:first-child { border-top: 0; }
    .ico { flex: none; display: inline-flex; color: var(--bx-muted, #A3A6B6); }
    .who { flex: 1; min-width: 0; }
    .name { font-weight: 600; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    .sub { font: var(--bx-font-meta, 400 12px/16px system-ui, sans-serif); color: var(--bx-muted, #A3A6B6); }
    .empty { color: var(--bx-muted, #A3A6B6); padding: 6px 0 2px; }
    button { box-sizing: border-box; min-height: var(--bx-control-h, 28px); padding: 0 12px; cursor: pointer; font-weight: 600;
      border: 1px solid var(--bx-border-strong, #666A7E); border-radius: var(--bx-radius, 2px);
      background: var(--bx-panel, #1F2028); color: var(--bx-text, #E9EAF0); }
    button:hover { background: var(--bx-hover, #2A2B34); }
    button.primary { background: var(--bx-accent, #8C9BFF); border-color: var(--bx-accent, #8C9BFF); color: var(--bx-accent-ink, #0B0C12); }
    button.primary:hover { background: var(--bx-accent-hover, #A9B4FF); border-color: var(--bx-accent-hover, #A9B4FF); }
    button.rm { color: var(--bx-danger, #FF7A7A); border-color: var(--bx-danger, #FF7A7A); }
    button.x { display: inline-flex; align-items: center; justify-content: center; width: 28px; padding: 0; border: 0; background: none; color: var(--bx-muted, #A3A6B6); }
    button.x:hover { background: var(--bx-close-hover, #FF7A7A); color: var(--bx-close-hover-ink, #0B0C12); }
    .foot { display: flex; justify-content: flex-end; gap: 8px; margin-top: 12px; }
    .err { display: flex; align-items: flex-start; gap: 6px; margin: 8px 0 0; padding: 6px 10px; border-radius: var(--bx-radius, 2px);
      color: var(--bx-danger, #FF7A7A); border: 1px solid var(--bx-danger, #FF7A7A); background: var(--bx-danger-bg, #3A2B32); }
    .enroll { margin-top: 12px; padding: 12px; border-radius: var(--bx-radius, 2px); background: var(--bx-panel-2, #262730);
      border: 1px solid var(--bx-border, #33353F); display: flex; gap: 16px; align-items: flex-start; flex-wrap: wrap; }
    /* theme-ok: a QR code is dark modules on a light ground for every scanner, in either theme */
    .qr { width: 176px; height: 176px; flex: none; background: #FFFFFF; border-radius: var(--bx-radius, 2px); }
    .qr svg { display: block; width: 100%; height: 100%; }
    .steps { flex: 1; min-width: 180px; }
    .steps ol { margin: 0 0 8px; padding-left: 18px; }
    .link { display: flex; gap: 6px; margin-top: 6px; }
    .link input { flex: 1; min-width: 0; box-sizing: border-box; height: var(--bx-control-h, 28px); padding: 0 8px;
      font: var(--bx-font-code, 12px/18px ui-monospace, monospace); border-radius: var(--bx-radius, 2px);
      border: 1px solid var(--bx-border-strong, #666A7E); background: var(--bx-panel, #1F2028); color: var(--bx-text, #E9EAF0); }
    .timer { font: var(--bx-font-meta, 400 12px/16px system-ui, sans-serif); color: var(--bx-muted, #A3A6B6); margin-top: 6px; font-variant-numeric: tabular-nums; }
    .ok { display: inline-flex; align-items: center; gap: 6px; color: var(--bx-ok, #A3CF5E); font-weight: 600; }
    .push { display: flex; align-items: center; gap: 6px; flex-wrap: wrap; margin-top: 4px; font: var(--bx-font-meta, 400 12px/16px system-ui, sans-serif); color: var(--bx-muted, #A3A6B6); }
    .push .warn { display: inline-flex; align-items: center; gap: 4px; color: var(--bx-warn, #F2994A); }
    .push button { min-height: 22px; padding: 0 8px; font-weight: 400; }
    h4 { margin: 12px 0 4px; font: var(--bx-font-micro, 600 11px/14px system-ui, sans-serif); letter-spacing: var(--bx-tracking-micro, 0.06em); text-transform: uppercase; color: var(--bx-muted, #A3A6B6); }
    .note { margin-top: 8px; font: var(--bx-font-meta, 400 12px/16px system-ui, sans-serif); color: var(--bx-muted, #A3A6B6); }
    .addr { display: block; margin-top: 8px; font-weight: 600; }
    .hint { font: var(--bx-font-meta, 400 12px/16px system-ui, sans-serif); color: var(--bx-muted, #A3A6B6); margin-top: 4px; }
    .hint.bad { color: var(--bx-danger, #FF7A7A); }
    .or { margin-top: 8px; }
    a { color: var(--bx-link, #8C9BFF); }
  `];

  #onKey = (e) => { if (e.key === 'Escape') { e.stopPropagation(); this.close(); } };

  connectedCallback() {
    super.connectedCallback();
    document.addEventListener('keydown', this.#onKey, true);
    this._devices = null;
    // the list first: the add flow tells a new device from the ones known
    this._load().then(() => { if (this.startAdd) this.addDevice(); });
  }
  addDevice() { if (!this._enroll || this._enroll.added) this._add(); }
  disconnectedCallback() {
    super.disconnectedCallback();
    document.removeEventListener('keydown', this.#onKey, true);
    clearInterval(this._tick);
  }
  close() { this.remove(); }

  async _load() {
    const push = this._loadPush();
    try {
      this._devices = (await call('/devices'))?.devices ?? [];
    } catch (e) { this._err = e.message; this._devices ??= []; }
    await push;
  }
  // Push registrations: optional — an xbind without the push plane (or a
  // sign-in that can't hold one) just shows none.
  async _loadPush() {
    try { this._push = await call('/devices/push'); } catch { this._push = null; }
  }
  async _removePush(id) {
    try {
      await call(`/devices/push/${encodeURIComponent(id)}`, { method: 'DELETE' });
      this._err = null;
    } catch (e) { this._err = e.message; }
    this._loadPush();
  }

  // One registration's line: what it gets, when it last got one, its Live
  // Activities (push-to-start registered; cards it follows), whether the
  // relay wants a new handle, and remove.
  _pushLine(reg) {
    if (!reg) return html`<div class="push"><bx-icon name="bell-slash"></bx-icon>no push notifications</div>`;
    const kinds = reg.kinds?.length ? reg.kinds.join(', ') : 'all';
    const n = reg.activities?.length ?? 0;
    const live = [reg.pushToStart ? 'xbind may start one' : '', n ? `${n} following` : ''].filter(Boolean).join(', ');
    return html`<div class="push" data-push=${reg.deviceId}><bx-icon name="bell"></bx-icon>notifications: ${kinds}
      · ${reg.lastSent ? `last sent ${ago(reg.lastSent)}` : 'none sent yet'}
      ${live ? html`<span data-live title="agent turns on the lock screen and in the Dynamic Island (removing the registration ends them)">· Live Activities: ${live}</span>` : nothing}
      ${reg.needsNewHandle ? html`· <span class="warn" title=${reg.relayError ?? ''}><bx-icon name="warning" label="Warning"></bx-icon>needs a new handle — the app renews it when next opened</span>` : nothing}
      <button title="stop notifications to this device until its app is next opened — it registers again then (to stop them for good, turn the app's notifications off in the device's Settings, or remove the device)"
        @click=${() => this._removePush(reg.deviceId)}>remove</button></div>`;
  }

  // Mint a code, draw it, and watch the list (every 2 s while the code is
  // live) for the device it enrolls. A 403 carrying stepUp turns into the
  // password / sign-in-again prompt instead of an error.
  async _add(password) {
    this._err = null;
    try {
      const r = await fetch('/api/xbin/devices/enroll-code', {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: password ? JSON.stringify({ password }) : '',
      });
      const e = await r.json().catch(() => ({}));
      if (r.status === 403 && e.stepUp) {
        this._stepUp = { mode: e.stepUp, msg: e.error, retry: !!password };
        this._enroll = null;
        clearInterval(this._tick);
        return;
      }
      if (!r.ok) throw new Error(e.error || `error ${r.status}`);
      this._stepUp = null;
      const addr = phoneOrigin(savedAddr()) || e.origin;
      const url = addr === e.origin ? e.url : enrollLink(addr, e.code);
      this._addrErr = null;
      this._enroll = { ...e, base: e.url, addr, url, qr: await qrPath(url), known: new Set((this._devices ?? []).map((d) => d.id)), added: null };
      this._now = Date.now() / 1000;
      clearInterval(this._tick);
      let n = 0;
      this._tick = setInterval(() => this._poll(++n), 1000);
    } catch (err) { this._err = err.message; }
  }
  async _poll(n) {
    this._now = Date.now() / 1000;
    const en = this._enroll;
    if (!en) { clearInterval(this._tick); return; }
    if (this._now > en.expires || en.added) { clearInterval(this._tick); return; }
    if (n % 2) return;
    await this._load();
    const fresh = (this._devices ?? []).find((d) => !en.known.has(d.id));
    if (fresh && this._enroll === en) {
      this._enroll = { ...en, added: fresh };
      clearInterval(this._tick);
    }
  }

  // A new phone address: rebuild the link and its QR code; remembered for
  // this browser ('' or the server's own origin forgets it).
  async _setAddr(v) {
    const en = this._enroll;
    if (!en) return;
    const addr = String(v).trim() ? phoneOrigin(v) : en.origin;
    if (!addr) { this._addrErr = 'an http(s) address the phone can reach, like https://xbin.example.com (no path)'; return; }
    this._addrErr = null;
    saveAddr(addr === en.origin ? '' : addr);
    const url = addr === en.origin ? en.base : enrollLink(addr, en.code);
    const qr = await qrPath(url);
    Object.assign(en, { addr, url, qr }); // in place: the poll holds this object
    this.requestUpdate();
  }

  async _remove(d) {
    this._confirm = null;
    try {
      await call(`/devices/${encodeURIComponent(d.id)}`, { method: 'DELETE' });
      this._err = null;
    } catch (e) { this._err = e.message; }
    this._load();
  }

  _row(d) {
    return html`<li data-device=${d.id}>
      <bx-icon class="ico" name="device" size="20"></bx-icon>
      <span class="who">
        <div class="name" title=${d.name}>${d.name}</div>
        <div class="sub">${d.platform || 'device'} · added ${ago(d.created)} ·
          ${d.lastUsed ? html`last sign-in ${ago(d.lastUsed)}${d.lastIP ? ` from ${d.lastIP}` : ''}` : 'not signed in yet'}</div>
        ${this._push ? this._pushLine(this._push.devices?.find((r) => r.deviceId === d.id)) : nothing}
      </span>
      ${this._confirm === d.id ? html`
        <button class="rm" @click=${() => this._remove(d)}>remove</button>
        <button @click=${() => { this._confirm = null; }}>keep</button>`
        : html`<button class="rm" title="sign the app out on this device and forget its key" @click=${() => { this._confirm = d.id; }}>remove…</button>`}
    </li>`;
  }

  // Sign in again: out, then to the login page (password or SSO) — adding a
  // device works for ten minutes after.
  async _reauth() {
    try { await fetch('/logout', { method: 'POST' }); } catch { /* the login page tells */ }
    location.href = '/login';
  }

  _stepUpBox() {
    const s = this._stepUp;
    if (!s) return nothing;
    if (s.mode === 'password') {
      return html`<form class="enroll" data-stepup="password" @submit=${(e) => { e.preventDefault(); this._add(e.target.pw.value); }}>
        <div class="steps"><b>Confirm it's you.</b> A device keeps signing in after this browser signs out,
          so adding one needs your password (or a sign-in in the last ten minutes).
          <div class="link"><input name="pw" type="password" autocomplete="current-password" placeholder="your password" required>
            <button class="primary" type="submit">continue</button></div>
          ${s.retry ? html`<div class="hint bad">${s.msg}</div>` : nothing}</div>
      </form>`;
    }
    return html`<div class="enroll" data-stepup="signin"><div class="steps"><b>Sign in again first.</b> Adding a device
      needs a sign-in from the last ten minutes — sign in again, then add it.
      <div class="link"><button class="primary" @click=${() => this._reauth()}>sign in again</button>
        <button @click=${() => { this._stepUp = null; }}>not now</button></div></div></div>`;
  }

  _enrollBox() {
    const en = this._enroll;
    if (!en) return nothing;
    const left = Math.max(0, Math.round(en.expires - (this._now ?? 0)));
    if (en.added) {
      return html`<div class="enroll"><span class="ok"><bx-icon name="ok" label="OK"></bx-icon>${en.added.name} was added.</span>
        <span class="sub">It signs in with Face ID from now on.</span></div>`;
    }
    return html`<div class="enroll" data-enroll>
      <div class="qr" title="scan with the xbin app">${left ? html`<svg viewBox="0 0 ${en.qr.size} ${en.qr.size}"
          shape-rendering="crispEdges" role="img" aria-label="enrollment QR code"><path d=${en.qr.d} fill="#000"/></svg><!-- theme-ok: a QR code's modules stay dark on light for scanners -->` : nothing}</div>
      <div class="steps">
        <ol>
          <li>Open the <b>xbin</b> app on your phone.</li>
          <li>Tap <b>Log in</b> → <b>Scan QR code</b>, and point it here.</li>
          <li>Confirm with Face ID — done.</li>
        </ol>
        <label class="addr" for="addr">Address your phone uses</label>
        <div class="link"><input id="addr" data-addr .value=${en.addr} spellcheck="false" autocomplete="off" inputmode="url"
            @change=${(e) => this._setAddr(e.target.value)} @keydown=${(e) => { if (e.key === 'Enter') e.target.blur(); }}>
          ${en.addr !== en.origin ? html`<button title=${`back to ${en.origin}`} @click=${() => this._setAddr('')}>reset</button>` : nothing}</div>
        <div class="hint ${this._addrErr ? 'bad' : ''}">${this._addrErr
          || 'Change it when this browser reaches xbin through a tunnel or proxy the phone can\'t use.'}</div>
        <div class="or">No camera? Open this link on the phone:</div>
        <div class="link"><input readonly data-link .value=${en.url} @focus=${(e) => e.target.select()}>
          <button @click=${() => navigator.clipboard?.writeText(en.url)}>copy</button></div>
        <div class="timer">${left ? `works once, for ${Math.floor(left / 60)}:${String(left % 60).padStart(2, '0')} more · for your account only`
          : html`expired — <a href="#" @click=${(e) => { e.preventDefault(); this._add(); }}>make a new code</a>`}</div>
      </div>
    </div>`;
  }

  // Registrations under no enrolled device (the app before it enrolled, a
  // browser, the owner token) — they end with the sign-in that made them.
  _otherPush(list) {
    const p = this._push;
    if (!p || list == null) return nothing;
    const known = new Set(list.map((d) => d.id));
    const other = (p.devices ?? []).filter((r) => !known.has(r.deviceId));
    return html`${other.length ? html`<h4>Other notification registrations</h4>
      <ul class="devs">${other.map((r) => html`<li data-push-other=${r.deviceId}>
        <bx-icon class="ico" name="bell" size="20"></bx-icon>
        <span class="who"><div class="name" title=${r.deviceId}>${r.deviceId}</div>
          <div class="sub">registered ${ago(r.created)} by a sign-in without a device key — it ends with that sign-in</div>
          ${this._pushLine(r)}</span></li>`)}</ul>` : nothing}
      ${p.enabled === false && (p.devices ?? []).length ? html`<div class="note" data-push-off>Push notifications are off on this workspace — an admin turns them on; registrations wait until then.</div>` : nothing}`;
  }

  render() {
    const list = this._devices;
    return html`
      <div class="backdrop" @click=${() => this.close()}></div>
      <div class="box" role="dialog" aria-modal="true" aria-label="my devices">
        <h3>my devices <button class="x" title="close" aria-label="close" @click=${() => this.close()}><bx-icon name="xmark"></bx-icon></button></h3>
        <p class="lede">Phones and tablets signed in to this workspace with the xbin app — each with its own key, unlocked by Face ID.</p>
        ${list == null ? html`<div class="empty">loading…</div>`
          : list.length ? html`<ul class="devs">${list.map((d) => this._row(d))}</ul>`
          : html`<div class="empty">No devices yet.</div>`}
        ${this._otherPush(list)}
        ${this._stepUpBox()}
        ${this._enrollBox()}
        ${this._err ? html`<div class="err" role="alert"><bx-icon name="error" label="Error"></bx-icon><span>${this._err}</span></div>` : nothing}
        <div class="foot">
          ${(this._enroll && !this._enroll.added) || this._stepUp ? nothing
            : html`<button class="primary" @click=${() => this._add()}>add a device</button>`}
          <button @click=${() => this.close()}>close</button>
        </div>
      </div>`;
  }
}

customElements.define('bx-devices', BxDevices);
