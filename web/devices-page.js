/**
 * <bx-devices-page> — xbind's devices page, /xbin/devices[?add=1] (D204,
 * /docs/app-tunnel.md; device login: docs/auth.md §Device login): the
 * surface that is there at all times — it ships with xbind, so a workspace
 * whose shell predates app tunnels can still pair a phone over one (an old
 * shell can't show the confirmation code), and the refusals that need it
 * name it. Served as it is (no HTML transform) and only top-level: framed,
 * it renders nothing of the person's.
 *
 * The person's xbin app devices (name, platform, whether it pairs over an
 * iroh tunnel, last sign-in) with remove, "N of M devices", and add: the
 * code is minted here (POST /devices/enroll-code {tunnel: true}, the
 * step-up included — a password, or signing in again) and <bx-pair> draws
 * it and follows the pairing. At the limit, add gives way to remove — or,
 * with tunnels on, to "replace a device…" (the confirmation names the one
 * replaced). Everything is read and written with the person's own session;
 * xbind judges every act.
 */
import { LitElement, html, css, nothing } from '/vendor/lit-all.min.js';
import '/vendor/bx-icons.js';
import '/vendor/bx-pair.js';
import { limitInfo, ago, platformName } from '/vendor/pair-kit.js';

const pageCss = css`
  :host { display: block; color: var(--bx-text, #E9EAF0); font: var(--bx-font, 13px/18px "Instrument Sans", system-ui, sans-serif); }
  :focus-visible { outline: var(--bx-focus-outline, 3px solid #3DD6F5); outline-offset: var(--bx-focus-offset, 2px); box-shadow: var(--bx-focus-halo, 0 0 0 2px #0B0C12); }
  main { max-width: 640px; margin: 0 auto; padding: 24px 16px 64px; box-sizing: border-box; }
  header { display: flex; align-items: baseline; gap: 12px; flex-wrap: wrap; margin-bottom: 8px; }
  header .grow { flex: 1; }
  h1 { font: var(--bx-font-heading, 600 20px/26px "Bricolage Grotesque", "Arial Black", system-ui, sans-serif);
    letter-spacing: var(--bx-tracking-heading, -0.01em); margin: 0; display: flex; align-items: center; gap: 8px; }
  .lead { color: var(--bx-muted, #A3A6B6); margin: 8px 0 16px; }
  a { color: var(--bx-link, #8C9BFF); text-decoration: none; }
  a:hover { text-decoration: underline; }
  .small { font: var(--bx-font-meta, 400 12px/16px "Instrument Sans", system-ui, sans-serif); }
  .banner { display: flex; gap: 6px; align-items: flex-start; border: 1px solid var(--bx-danger, #FF7A7A); border-radius: var(--bx-radius, 2px);
    padding: 8px 12px; margin: 12px 0; background: var(--bx-danger-bg, #3A2B32); color: var(--bx-danger, #FF7A7A); }
  .card { border: 1px solid var(--bx-border, #33353F); border-radius: var(--bx-radius, 2px); background: var(--bx-panel, #1F2028); padding: 12px; }
  ul { list-style: none; margin: 0; padding: 0; }
  li { display: flex; align-items: center; gap: 12px; padding: 8px 0; border-top: 1px solid var(--bx-border, #33353F); flex-wrap: wrap; }
  li:first-child { border-top: 0; }
  .ico { flex: none; display: inline-flex; color: var(--bx-muted, #A3A6B6); }
  .who { flex: 1; min-width: 12em; }
  .name { font-weight: 600; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .sub { font: var(--bx-font-meta, 400 12px/16px system-ui, sans-serif); color: var(--bx-muted, #A3A6B6); }
  .empty { color: var(--bx-muted, #A3A6B6); padding: 6px 0 2px; }
  .acts { display: flex; gap: 6px; }
  button { box-sizing: border-box; min-height: var(--bx-control-h, 28px); padding: 0 12px; cursor: pointer; font: inherit; font-weight: 600;
    border: 1px solid var(--bx-border-strong, #666A7E); border-radius: var(--bx-radius, 2px);
    background: var(--bx-panel, #1F2028); color: var(--bx-text, #E9EAF0); }
  button:hover { background: var(--bx-hover, #2A2B34); }
  button.primary { background: var(--bx-accent, #8C9BFF); border-color: var(--bx-accent, #8C9BFF); color: var(--bx-accent-ink, #0B0C12); }
  button.primary:hover { background: var(--bx-accent-hover, #A9B4FF); border-color: var(--bx-accent-hover, #A9B4FF); }
  button.rm { color: var(--bx-danger, #FF7A7A); border-color: var(--bx-danger, #FF7A7A); }
  .foot { display: flex; gap: 8px; align-items: center; justify-content: flex-end; flex-wrap: wrap; margin-top: 12px; }
  .count { margin-right: auto; font: var(--bx-font-meta, 400 12px/16px system-ui, sans-serif); color: var(--bx-muted, #A3A6B6); font-variant-numeric: tabular-nums; }
  .count.at { color: var(--bx-warn, #F2994A); }
  .step { margin-top: 12px; padding: 12px; border-radius: var(--bx-radius, 2px); background: var(--bx-panel-2, #262730); border: 1px solid var(--bx-border, #33353F); }
  .row { display: flex; gap: 6px; margin-top: 6px; flex-wrap: wrap; }
  input { flex: 1; min-width: 10em; box-sizing: border-box; height: var(--bx-control-h, 28px); padding: 0 8px; font: inherit;
    border-radius: var(--bx-radius, 2px); border: 1px solid var(--bx-border-strong, #666A7E); background: var(--bx-panel, #1F2028); color: var(--bx-text, #E9EAF0); }
  .hint { font: var(--bx-font-meta, 400 12px/16px system-ui, sans-serif); color: var(--bx-muted, #A3A6B6); margin-top: 4px; }
  .hint.bad { color: var(--bx-danger, #FF7A7A); }
  bx-pair { margin-top: 12px; }
`;

// req(path, init) → {status, ok, body}: an /api/xbin call with the
// person's own session (the same-origin cookie).
async function req(path, init = {}) {
  const r = await fetch(`/api/xbin${path}`, {
    ...init, headers: { Accept: 'application/json', ...(init.body ? { 'Content-Type': 'application/json' } : {}) },
  });
  return { status: r.status, ok: r.ok, body: (await r.json().catch(() => ({}))) ?? {} };
}

export class BxDevicesPage extends LitElement {
  static properties = {
    _list: { state: true },    // GET /devices {devices, limit, count}; null until loaded
    _tunnelOn: { state: true },
    _code: { state: true },    // the enroll-code answer <bx-pair> draws
    _ended: { state: true },   // <bx-pair> ended (a phone added, or nothing): add again
    _stepUp: { state: true },  // {mode: 'password'|'signin', msg, retry, classic}
    _confirm: { state: true }, // the device id awaiting "remove?"
    _err: { state: true },
  };
  static styles = pageCss;

  constructor() {
    super();
    this._framed = false;
    try { this._framed = window.top !== window.self; } catch { this._framed = true; }
    this._list = null;
    this._wantAdd = new URLSearchParams(location.search).get('add') === '1';
  }

  connectedCallback() {
    super.connectedCallback();
    if (this._framed) return; // a person's devices are shown top-level only
    Promise.all([this._load(), this._loadTunnel()]).then(() => { if (this._wantAdd) this._addFirst(); });
  }

  async _load() {
    try {
      const r = await req('/devices');
      if (!r.ok) throw new Error(r.body.error || `error ${r.status}`);
      this._list = r.body;
    } catch (e) { this._err = e.message; this._list ??= { devices: [] }; }
  }
  async _loadTunnel() {
    try { this._tunnelOn = !!(await req('/login/methods')).body?.tunnel?.enabled; } catch { this._tunnelOn = false; }
  }
  get _lim() { return limitInfo(this._list); }
  get _canAdd() {
    const l = this._lim;
    return !l || !l.at || (this._tunnelOn && l.limit > 0);
  }
  _addFirst() { if (this._canAdd) this._add(); }

  // Mint a code ({tunnel: true}: xbind answers a tunnel code when tunnels
  // are on, a classic one otherwise; classic asks for one an older app scans).
  async _add(password, { classic = false } = {}) {
    this._err = '';
    const body = classic ? (password ? { password } : null) : { tunnel: true, ...(password ? { password } : {}) };
    try {
      const r = await req('/devices/enroll-code', { method: 'POST', body: body ? JSON.stringify(body) : '' });
      if (r.status === 403 && r.body.stepUp) {
        this._stepUp = { mode: r.body.stepUp, msg: r.body.error, retry: !!password, classic };
        this._code = null;
        return;
      }
      if (r.status === 409 && Number.isInteger(r.body.limit)) this._list = { ...this._list, limit: r.body.limit, count: r.body.count ?? this._list?.count };
      if (!r.ok) throw new Error(r.body.error || `error ${r.status}`);
      this._stepUp = null;
      this._ended = '';
      this._code = r.body;
    } catch (e) { this._err = e.message; }
  }

  async _remove(d) {
    this._confirm = null;
    try {
      const r = await req(`/devices/${encodeURIComponent(d.id)}`, { method: 'DELETE' });
      if (!r.ok) throw new Error(r.body.error || `error ${r.status}`);
      this._err = '';
    } catch (e) { this._err = e.message; }
    this._load();
  }

  _onPair({ state }) {
    if (state === 'enrolled') this._load();
    this._ended = ['enrolled', 'rejected', 'abandoned', 'expired'].includes(state) ? state : '';
  }

  async _reauth() {
    try { await fetch('/logout', { method: 'POST' }); } catch { /* the login page tells */ }
    location.href = '/login?next=%2Fxbin%2Fdevices%3Fadd%3D1';
  }

  _row(d) {
    return html`<li data-device=${d.id}>
      <bx-icon class="ico" name="phone" size="20"></bx-icon>
      <span class="who">
        <div class="name" title=${d.name}>${d.name}</div>
        <div class="sub">${platformName(d.platform) || 'device'}${d.tunnel ? html` · <span data-tunnel>iroh tunnel</span>` : ''} · added ${ago(d.created)} ·
          ${d.lastUsed ? `last sign-in ${ago(d.lastUsed)}${d.lastIP ? ` from ${d.lastIP}` : ''}` : 'not signed in yet'}</div>
      </span>
      <span class="acts">${this._confirm === d.id ? html`
        <button class="rm" @click=${() => this._remove(d)}>remove</button>
        <button @click=${() => { this._confirm = null; }}>keep</button>`
        : html`<button class="rm" title="sign the app out on this device and forget its key" @click=${() => { this._confirm = d.id; }}>remove…</button>`}</span>
    </li>`;
  }

  _stepUpTpl() {
    const s = this._stepUp;
    if (!s) return nothing;
    if (s.mode === 'password') {
      return html`<form class="step" data-stepup="password" @submit=${(e) => { e.preventDefault(); this._add(e.target.pw.value, { classic: s.classic }); }}>
        <b>Confirm it's you.</b> A device keeps signing in after this browser signs out, so adding one needs your password
        (or a sign-in in the last ten minutes).
        <div class="row"><input name="pw" type="password" autocomplete="current-password" placeholder="your password" required>
          <button class="primary" type="submit">continue</button></div>
        ${s.retry ? html`<div class="hint bad">${s.msg}</div>` : nothing}
      </form>`;
    }
    return html`<div class="step" data-stepup="signin"><b>Sign in again first.</b> Adding a device needs a sign-in from the
      last ten minutes — sign in again, then add it.
      <div class="row"><button class="primary" @click=${() => this._reauth()}>sign in again</button>
        <button @click=${() => { this._stepUp = null; }}>not now</button></div></div>`;
  }

  _foot() {
    const l = this._lim;
    const busy = (this._code && !this._ended) || this._stepUp;
    const replace = l?.at && this._tunnelOn && l.limit > 0;
    return html`<div class="foot">
      ${l ? html`<span class="count ${l.at ? 'at' : ''}" data-limit>${l.words}${l.at
        ? (l.limit === 0 ? ' — this account may not add app devices' : replace ? ' — a new phone replaces one' : ' — remove one to add another') : ''}</span>` : nothing}
      ${busy || !this._canAdd ? nothing
        : html`<button class="primary" data-add @click=${() => this._add()}>${replace ? 'replace a device…' : 'add a device'}</button>`}
    </div>`;
  }

  render() {
    if (this._framed) {
      return html`<main><div class="banner" role="alert"><bx-icon name="error"></bx-icon><span>This page shows your devices only when it is opened on its own,
        never inside another page. Open <code>/xbin/devices</code> in a tab of its own.</span></div></main>`;
    }
    const list = this._list?.devices;
    return html`<main>
      <header>
        <h1><bx-icon name="phone"></bx-icon>Your devices</h1>
        <span class="grow"></span>
        <a class="small" href="/"><bx-icon name="arrow-left"></bx-icon> workspace</a>
      </header>
      <p class="lead">Phones and tablets signed in to this workspace with the xbin app — each with its own key, unlocked by
        Face ID. Add one by scanning a QR code with the app; with iroh tunneling on, the app connects through an encrypted
        tunnel and you confirm a six-digit code here (<a href="/docs/app-tunnel.md" target="_blank" rel="noopener">how it works</a>).</p>
      <div class="card">
        ${list == null ? html`<div class="empty">loading…</div>`
          : list.length ? html`<ul>${list.map((d) => this._row(d))}</ul>`
          : html`<div class="empty">No devices yet.</div>`}
        ${this._foot()}
      </div>
      ${this._stepUpTpl()}
      ${this._code ? html`<bx-pair .code=${this._code} @bx-pair-state=${(e) => this._onPair(e.detail)}
        @bx-pair-again=${() => this._add()} @bx-pair-classic=${() => this._add(undefined, { classic: true })}></bx-pair>` : nothing}
      ${this._err ? html`<div class="banner" role="alert"><bx-icon name="error"></bx-icon><span>${this._err}</span></div>` : nothing}
    </main>`;
  }

  testApi() { return { framed: () => this._framed, list: () => this._list, tunnelOn: () => this._tunnelOn }; }
}
customElements.define('bx-devices-page', BxDevicesPage);
