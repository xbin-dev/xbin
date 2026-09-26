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
 * that adds it. Opened from the account section of the shell's 🔧 menu
 * (openDevices); a modal over the workspace that removes itself on close.
 * The shell is chrome: these calls ride the session cookie.
 */
import { LitElement, html, css, nothing } from 'lit';
import { xbinApi as call } from '/vendor/bx-kit.js';

export function openDevices() {
  if (document.querySelector('bx-devices')) return;
  document.body.append(document.createElement('bx-devices'));
}

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
// use). Black on white with the 4-module quiet zone scanners expect —
// whatever the theme.
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
    _enroll: { state: true },  // {code, url, origin, expires, qr, known:Set, added}
    _stepUp: { state: true },  // {mode: 'password'|'signin', msg, retry} — the server asked to re-prove it's you
    _confirm: { state: true }, // device id awaiting "remove?" confirmation
    _err: { state: true },
    _now: { state: true },
  };

  static styles = css`
    :host { position: fixed; inset: 0; z-index: 4000; display: block; }
    .backdrop { position: absolute; inset: 0; background: rgba(0, 0, 0, .45); }
    .box {
      position: absolute; left: 50%; top: 45%; transform: translate(-50%, -50%);
      width: min(470px, calc(100vw - 32px)); max-height: 86vh; overflow: auto; box-sizing: border-box;
      background: var(--bx-panel, #23272e); color: var(--bx-text, #d4d9e0);
      border: 1px solid var(--bx-border, #363c45); border-radius: 10px;
      box-shadow: 0 0 0 1px rgba(0, 0, 0, .4), 0 14px 44px rgba(0, 0, 0, .5);
      padding: 16px 18px; font: var(--bx-font, 13px/1.5 -apple-system, system-ui, sans-serif);
    }
    h3 { margin: 0 0 2px; font-size: 14px; display: flex; align-items: center; gap: 8px; }
    h3 .x { margin-left: auto; }
    .lede { margin: 0 0 12px; font-size: 12px; color: var(--bx-muted, #868f9a); }
    ul.devs { list-style: none; margin: 0; padding: 0; }
    ul.devs > li { display: flex; align-items: center; gap: 10px; padding: 8px 0; border-top: 1px solid var(--bx-border, #363c45); }
    ul.devs > li:first-child { border-top: 0; }
    .ico { font-size: 18px; width: 22px; text-align: center; }
    .who { flex: 1; min-width: 0; }
    .name { font-weight: 600; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    .sub { font-size: 11px; color: var(--bx-muted, #868f9a); }
    .empty { font-size: 12px; color: var(--bx-muted, #868f9a); padding: 6px 0 2px; }
    button { font: inherit; font-size: 12px; padding: 4px 11px; border-radius: 6px; cursor: pointer;
      border: 1px solid var(--bx-border, #363c45); background: var(--bx-panel, #23272e); color: var(--bx-text, #d4d9e0); }
    button:hover { background: var(--bx-panel-2, #2b3038); }
    button.primary { background: var(--bx-accent, #f5a623); border-color: transparent; color: #23272e; font-weight: 600; }
    button.rm { color: var(--bx-red, #ef5350); border-color: color-mix(in srgb, var(--bx-red, #ef5350) 45%, transparent); }
    button.x { border: 0; background: none; font-size: 15px; padding: 0 4px; color: var(--bx-muted, #868f9a); }
    .foot { display: flex; justify-content: flex-end; gap: 8px; margin-top: 12px; }
    .err { margin: 8px 0 0; padding: 7px 10px; font-size: 12px; border-radius: 6px; color: var(--bx-red, #ef5350);
      border: 1px solid color-mix(in srgb, var(--bx-red, #ef5350) 55%, transparent);
      background: color-mix(in srgb, var(--bx-red, #ef5350) 12%, transparent); }
    .enroll { margin-top: 12px; padding: 12px; border-radius: 8px; background: var(--bx-panel-2, #2b3038);
      display: flex; gap: 14px; align-items: flex-start; flex-wrap: wrap; }
    .qr { width: 176px; height: 176px; flex: none; background: #fff; border-radius: 6px; }
    .qr svg { display: block; width: 100%; height: 100%; }
    .steps { flex: 1; min-width: 180px; font-size: 12px; }
    .steps ol { margin: 0 0 8px; padding-left: 18px; }
    .link { display: flex; gap: 6px; margin-top: 6px; }
    .link input { flex: 1; min-width: 0; font: 11px var(--bx-mono, monospace); padding: 4px 6px; border-radius: 5px;
      border: 1px solid var(--bx-border, #363c45); background: var(--bx-panel, #23272e); color: var(--bx-text, #d4d9e0); }
    .timer { font-size: 11px; color: var(--bx-muted, #868f9a); margin-top: 6px; }
    .ok { color: var(--bx-green, #4caf50); font-weight: 600; }
    .push { display: flex; align-items: center; gap: 6px; flex-wrap: wrap; margin-top: 3px; font-size: 11px; color: var(--bx-muted, #868f9a); }
    .push .warn { color: var(--bx-amber, #f2a71b); }
    .push button { font-size: 11px; padding: 0 7px; line-height: 18px; }
    h4 { margin: 12px 0 2px; font-size: 12px; }
    .note { margin-top: 8px; font-size: 11px; color: var(--bx-muted, #868f9a); }
  `;

  #onKey = (e) => { if (e.key === 'Escape') { e.stopPropagation(); this.close(); } };

  connectedCallback() {
    super.connectedCallback();
    document.addEventListener('keydown', this.#onKey, true);
    this._devices = null;
    this._load();
  }
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
    if (!reg) return html`<div class="push">🔕 no push notifications</div>`;
    const kinds = reg.kinds?.length ? reg.kinds.join(', ') : 'all';
    const n = reg.activities?.length ?? 0;
    const live = [reg.pushToStart ? 'xbind may start one' : '', n ? `${n} following` : ''].filter(Boolean).join(', ');
    return html`<div class="push" data-push=${reg.deviceId}>🔔 notifications: ${kinds}
      · ${reg.lastSent ? `last sent ${ago(reg.lastSent)}` : 'none sent yet'}
      ${live ? html`· <span data-live title="agent turns on the lock screen and in the Dynamic Island (removing the registration ends them)">Live Activities: ${live}</span>` : nothing}
      ${reg.needsNewHandle ? html`· <span class="warn" title=${reg.relayError ?? ''}>needs a new handle — the app renews it at its next sign-in</span>` : nothing}
      <button title="stop notifications to this device (the app registers again at its next sign-in)"
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
      const qr = await qrPath(e.url);
      this._enroll = { ...e, qr, known: new Set((this._devices ?? []).map((d) => d.id)), added: null };
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

  async _remove(d) {
    this._confirm = null;
    try {
      await call(`/devices/${encodeURIComponent(d.id)}`, { method: 'DELETE' });
      this._err = null;
    } catch (e) { this._err = e.message; }
    this._load();
  }

  _row(d) {
    const icon = /^(ios|android|iphone)/.test(d.platform ?? '') ? '📱' : /^(ipados|ipad)/.test(d.platform ?? '') ? '▭' : '◻';
    return html`<li data-device=${d.id}>
      <span class="ico" aria-hidden="true">${icon}</span>
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
          ${s.retry ? html`<div class="timer" style="color: var(--bx-red, #ef5350)">${s.msg}</div>` : nothing}</div>
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
      return html`<div class="enroll"><span class="ok">✓ ${en.added.name} was added.</span>
        <span class="sub">It signs in with Face ID from now on.</span></div>`;
    }
    return html`<div class="enroll" data-enroll>
      <div class="qr" title="scan with the xbin app">${left ? html`<svg viewBox="0 0 ${en.qr.size} ${en.qr.size}"
          shape-rendering="crispEdges" role="img" aria-label="enrollment QR code"><path d=${en.qr.d} fill="#000"/></svg>` : nothing}</div>
      <div class="steps">
        <ol>
          <li>Open the <b>xbin</b> app on your phone.</li>
          <li><b>Add workspace</b> → <b>scan code</b>, and point it here.</li>
          <li>Confirm with Face ID — done.</li>
        </ol>
        No camera? Open this link on the phone:
        <div class="link"><input readonly .value=${en.url} @focus=${(e) => e.target.select()}>
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
        <span class="ico" aria-hidden="true">🔔</span>
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
        <h3>my devices <button class="x" title="close" @click=${() => this.close()}>✕</button></h3>
        <p class="lede">Phones and tablets signed in to this workspace with the xbin app — each with its own key, unlocked by Face ID.</p>
        ${list == null ? html`<div class="empty">loading…</div>`
          : list.length ? html`<ul class="devs">${list.map((d) => this._row(d))}</ul>`
          : html`<div class="empty">No devices yet.</div>`}
        ${this._otherPush(list)}
        ${this._stepUpBox()}
        ${this._enrollBox()}
        ${this._err ? html`<div class="err" role="alert">${this._err}</div>` : nothing}
        <div class="foot">
          ${(this._enroll && !this._enroll.added) || this._stepUp ? nothing
            : html`<button class="primary" @click=${() => this._add()}>add a device</button>`}
          <button @click=${() => this.close()}>close</button>
        </div>
      </div>`;
  }
}

customElements.define('bx-devices', BxDevices);
