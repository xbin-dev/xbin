/**
 * <bx-pair> — adding a phone to the xbin app, after a code is minted
 * (D204, /docs/app-tunnel.md; device login: docs/auth.md §Device login).
 * Hosted by the shell's devices panel and xbind's /xbin/devices page, which
 * mint the code (POST /api/xbin/devices/enroll-code, step-up included) and
 * hand the answer in as `.code`.
 *
 * A tunnel code (the answer carries `tunnel`, the workspace has iroh
 * tunneling on): the big QR code of the xbin://enroll link (level M, at
 * least 288 px, whole pixels and at least 4 per module — the link is long),
 * then GET /devices/pairing/{id} every second. When the phone has paired
 * (state confirm) the QR code gives way to the six-digit confirmation code,
 * large, with the phone's name and platform: "Only confirm if YOUR phone
 * shows this code", Codes match / They don't (POST …/confirm {match}).
 * At the person's device limit the step also asks which device the new one
 * replaces ({match, replace: <deviceId>}). Then: enrolled, or what added
 * nothing — rejected (They don't burns the code), abandoned (the phone
 * stopped first), expired — each with "make a new code". While the
 * workspace doesn't require the tunnel, "Older app? Show a classic code".
 *
 * A classic code (no `tunnel`): the same QR code and link; the list of
 * devices is watched for the new one.
 *
 * Both: the "address your phone uses" field rewrites only the link's u=
 * (pair-kit withOrigin — a tunnel link keeps its tv, t, k, p, r, rq),
 * remembered per browser under the shell's own key; it and the raw link
 * sit folded under the QR code (open when an address is remembered).
 *
 * Events (bubbling, composed): bx-pair-state {state, deviceId?, name?} on
 * every change of what it shows; bx-pair-again (make a new code);
 * bx-pair-classic (mint a classic code instead). Everything is read and
 * written with the person's own session; xbind judges every act.
 */
import { LitElement, html, css, nothing } from '/vendor/lit-all.min.js';
import qrcode from '/vendor/qrcode.mjs';
import '/vendor/bx-icons.js';
import {
  formatSas, pairState, isFinal, phoneOrigin, withOrigin, qrModel, expiresAt, clock, limitInfo, confirmBody,
  endWords, ago, platformName,
} from '/vendor/pair-kit.js';

// The address the phone uses (per browser; '' = the server's origin) — the
// shell's devices panel's key, so both surfaces remember the same one.
const ADDR_KEY = 'xbin-phone-address';
function savedAddr() { try { return localStorage.getItem(ADDR_KEY) || ''; } catch { return ''; } }
function saveAddr(v) { try { if (v) localStorage.setItem(ADDR_KEY, v); else localStorage.removeItem(ADDR_KEY); } catch { /* storage off: not remembered */ } }

// req(path, init) → {status, ok, body}: an /api/xbin call with the
// session cookie, its answer whatever the status (a 409 says why).
async function req(path, init = {}) {
  const r = await fetch(`/api/xbin${path}`, {
    ...init, headers: { Accept: 'application/json', ...(init.body ? { 'Content-Type': 'application/json' } : {}) },
  });
  const body = await r.json().catch(() => ({}));
  return { status: r.status, ok: r.ok, body: body ?? {} };
}

const pairCss = css`
  :host { display: block; color: var(--bx-text, #E9EAF0); font: var(--bx-font, 13px/18px system-ui, sans-serif); }
  :focus-visible { outline: var(--bx-focus-outline, 3px solid #3DD6F5); outline-offset: var(--bx-focus-offset, 2px); box-shadow: var(--bx-focus-halo, 0 0 0 2px #0B0C12); }
  .pair { padding: 12px; border-radius: var(--bx-radius, 2px); background: var(--bx-panel-2, #262730); border: 1px solid var(--bx-border, #33353F); }
  .qrwrap { display: flex; justify-content: center; }
  /* theme-ok: a QR code is dark modules on a light ground for every scanner, in either theme */
  .qr { max-width: 100%; aspect-ratio: 1; background: #FFFFFF; border-radius: var(--bx-radius, 2px); }
  .qr svg { display: block; width: 100%; height: 100%; }
  ol { margin: 12px 0 8px; padding-left: 18px; }
  ol li { margin: 2px 0; }
  .status { display: flex; align-items: center; gap: 6px; color: var(--bx-muted, #A3A6B6); }
  .status.on { color: var(--bx-text, #E9EAF0); }
  .timer { font: var(--bx-font-meta, 400 12px/16px system-ui, sans-serif); color: var(--bx-muted, #A3A6B6); margin-top: 6px; font-variant-numeric: tabular-nums; }
  details.more { margin-top: 10px; }
  details.more summary { cursor: pointer; color: var(--bx-link, #8C9BFF); }
  label.addr { display: block; margin-top: 8px; font-weight: 600; }
  .row { display: flex; gap: 6px; margin-top: 6px; }
  input { flex: 1; min-width: 0; box-sizing: border-box; height: var(--bx-control-h, 28px); padding: 0 8px;
    font: var(--bx-font-code, 12px/18px ui-monospace, monospace); border-radius: var(--bx-radius, 2px);
    border: 1px solid var(--bx-border-strong, #666A7E); background: var(--bx-panel, #1F2028); color: var(--bx-text, #E9EAF0); }
  input[type=radio] { flex: none; height: auto; margin: 2px 0 0; }
  .hint { font: var(--bx-font-meta, 400 12px/16px system-ui, sans-serif); color: var(--bx-muted, #A3A6B6); margin-top: 4px; }
  .hint.bad { color: var(--bx-danger, #FF7A7A); }
  .or { margin-top: 10px; }
  .classic { margin-top: 10px; }
  a { color: var(--bx-link, #8C9BFF); }
  button { box-sizing: border-box; min-height: var(--bx-control-h, 28px); padding: 0 12px; cursor: pointer; font: inherit; font-weight: 600;
    border: 1px solid var(--bx-border-strong, #666A7E); border-radius: var(--bx-radius, 2px);
    background: var(--bx-panel, #1F2028); color: var(--bx-text, #E9EAF0); }
  button:hover { background: var(--bx-hover, #2A2B34); }
  button:disabled { opacity: .5; cursor: default; }
  button.primary { background: var(--bx-accent, #8C9BFF); border-color: var(--bx-accent, #8C9BFF); color: var(--bx-accent-ink, #0B0C12); }
  button.primary:hover:not(:disabled) { background: var(--bx-accent-hover, #A9B4FF); border-color: var(--bx-accent-hover, #A9B4FF); }
  button.rm { color: var(--bx-danger, #FF7A7A); border-color: var(--bx-danger, #FF7A7A); }
  .big { min-height: 40px; padding: 0 20px; font: var(--bx-font-title, 600 16px/22px system-ui, sans-serif); }
  /* the confirmation step */
  .lbl { font: var(--bx-font-micro, 600 11px/14px system-ui, sans-serif); letter-spacing: var(--bx-tracking-micro, 0.06em);
    text-transform: uppercase; color: var(--bx-muted, #A3A6B6); }
  .confirm { text-align: center; }
  .sas { margin: 4px 0 8px; font-family: var(--bx-mono, "JetBrains Mono", ui-monospace, monospace); font-size: 44px; line-height: 52px;
    font-weight: 700; letter-spacing: 0.06em; font-variant-numeric: tabular-nums; white-space: nowrap; }
  .dev { display: inline-flex; align-items: center; gap: 6px; max-width: 100%; }
  .dev b { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .only { display: flex; gap: 8px; align-items: flex-start; text-align: left; margin: 12px 0; padding: 8px 10px; border-radius: var(--bx-radius, 2px);
    border: 1px solid var(--bx-warn, #F2994A); background: var(--bx-warn-bg, #382F2C); }
  .only > bx-icon { flex: none; color: var(--bx-warn, #F2994A); }
  .acts { display: flex; gap: 8px; justify-content: center; flex-wrap: wrap; margin-top: 8px; }
  fieldset.replace { text-align: left; margin: 12px 0 0; padding: 8px 10px; border: 1px solid var(--bx-border-strong, #666A7E); border-radius: var(--bx-radius, 2px); }
  fieldset.replace legend { padding: 0 4px; font-weight: 600; }
  fieldset.replace label { display: flex; gap: 8px; align-items: flex-start; padding: 4px 0; cursor: pointer; }
  fieldset.replace .sub { font: var(--bx-font-meta, 400 12px/16px system-ui, sans-serif); color: var(--bx-muted, #A3A6B6); }
  .err { display: flex; align-items: flex-start; gap: 6px; margin: 8px 0 0; padding: 6px 10px; border-radius: var(--bx-radius, 2px); text-align: left;
    color: var(--bx-danger, #FF7A7A); border: 1px solid var(--bx-danger, #FF7A7A); background: var(--bx-danger-bg, #3A2B32); }
  /* how it ended */
  .ok { display: inline-flex; align-items: center; gap: 6px; color: var(--bx-ok, #A3CF5E); font-weight: 600; }
  .end { display: flex; gap: 8px; align-items: flex-start; }
  .end > bx-icon { flex: none; margin-top: 1px; }
  .end.danger > bx-icon, .end.danger b { color: var(--bx-danger, #FF7A7A); }
  .end.warn > bx-icon, .end.warn b { color: var(--bx-warn, #F2994A); }
  .end p { margin: 4px 0 8px; }
  .muted { color: var(--bx-muted, #A3A6B6); }
`;

export class BxPair extends LitElement {
  static properties = {
    code: { attribute: false },  // the enroll-code answer {code, origin, expires, url, tunnel?: {pairing, required, …}}
    known: { attribute: false }, // classic: the device ids there before the code (the new one is the one not in it)
    _st: { state: true },        // the pairing as xbind says it: {state, sas, device, expires, reason, error, deviceId}
    _link: { state: true },
    _addr: { state: true },
    _addrErr: { state: true },
    _qr: { state: true },
    _now: { state: true },
    _busy: { state: true },
    _err: { state: true },
    _devs: { state: true },      // GET /devices at the confirmation: {devices, limit, count}
    _replace: { state: true },   // the device id the new phone replaces
  };
  static styles = pairCss;

  constructor() {
    super();
    this.code = null;
    this.known = null;
    this._view = '';
    this._timer = 0;
    this._n = 0;
  }

  connectedCallback() {
    super.connectedCallback();
    if (this.code && !this._timer && !isFinal(this._view)) this._run();
  }
  disconnectedCallback() {
    super.disconnectedCallback();
    this._stop();
  }
  willUpdate(changed) {
    if (changed.has('code') && this.code) this._start();
  }

  get _tunnel() { return this.code?.tunnel ?? null; }

  // A new code: its link at the remembered address, its QR code, the poll.
  _start() {
    const c = this.code;
    this._stop();
    this._st = { state: c.tunnel ? 'minted' : 'waiting', expires: c.expires };
    this._view = pairState(this._st.state);
    this._err = '';
    this._devs = null;
    this._replace = '';
    this._addrErr = '';
    this._busy = false;
    const addr = phoneOrigin(savedAddr()) || c.origin;
    this._setLink(addr);
    this._moreOpen = addr !== c.origin; // a remembered address: shown, so nobody wonders what the QR code carries
    if (!c.tunnel && !this.known) this._knownFromList();
    this._run();
  }
  _setLink(addr) {
    const c = this.code;
    this._addr = addr;
    this._link = addr === c.origin ? c.url : withOrigin(c.url, addr);
    this._qr = qrModel(qrcode, this._link);
  }
  _run() {
    this._now = Date.now() / 1000;
    clearInterval(this._timer);
    this._timer = setInterval(() => this._tick(), 1000);
    if (this._tunnel) this._poll();
  }
  _stop() { clearInterval(this._timer); this._timer = 0; }

  _tick() {
    this._now = Date.now() / 1000;
    const exp = expiresAt(this._st?.expires) ?? this.code?.expires;
    // nobody scanned it in time: expired here too (xbind says so on its next answer)
    if (this._view === 'waiting' && exp && this._now > exp + 1) { this._set({ ...this._st, state: 'expired' }); return; }
    if (this._tunnel) this._poll();
    else if (++this._n % 2 === 0) this._watchList();
  }

  // The tunnel: the pairing's state from xbind.
  async _poll() {
    const id = this._tunnel?.pairing;
    if (!id || this._inflight || isFinal(this._view)) return;
    this._inflight = true;
    try {
      const r = await req(`/devices/pairing/${encodeURIComponent(id)}`);
      if (this._tunnel?.pairing !== id || isFinal(this._view)) return; // a new code, or decided, meanwhile
      if (r.status === 404 || r.status === 410) this._set({ ...this._st, state: 'expired' });
      else if (r.ok) this._set(r.body);
    } catch { /* offline a moment: the next tick asks again */ } finally { this._inflight = false; }
  }

  // Classic: the list of devices, before and after.
  async _knownFromList() {
    try { this.known = ((await req('/devices')).body?.devices ?? []).map((d) => d.id); } catch { this.known = []; }
  }
  async _watchList() {
    if (!this.known || isFinal(this._view)) return;
    try {
      const fresh = ((await req('/devices')).body?.devices ?? []).find((d) => !this.known.includes(d.id));
      if (fresh && !isFinal(this._view)) this._set({ state: 'enrolled', deviceId: fresh.id, device: { name: fresh.name, platform: fresh.platform } });
    } catch { /* the next tick */ }
  }

  _set(st) {
    this._st = st;
    const v = pairState(st?.state);
    if (v === this._view) return;
    this._view = v;
    if (v === 'confirm') this._loadDevs();
    if (isFinal(v)) this._stop();
    const name = st?.device?.name ?? st?.name;
    this.dispatchEvent(new CustomEvent('bx-pair-state', { bubbles: true, composed: true,
      detail: { state: v, ...(st?.deviceId ? { deviceId: st.deviceId } : {}), ...(name ? { name } : {}) } }));
  }

  // At the confirmation: the person's devices and limit — at the limit, the
  // new phone has to replace one.
  async _loadDevs(atLimit = false) {
    try {
      const r = await req('/devices');
      if (r.ok) this._devs = atLimit && !limitInfo(r.body)?.at ? { ...r.body, forced: true } : r.body;
    } catch { /* no list: no picker; xbind still refuses a confirm over the limit */ }
  }
  get _mustReplace() {
    const d = this._devs;
    return !!d && (d.forced || !!limitInfo(d)?.at) && (d.devices ?? []).length > 0 && limitInfo(d)?.limit !== 0;
  }

  async _confirm(match) {
    const id = this._tunnel?.pairing;
    if (!id || this._busy) return;
    this._busy = true;
    this._err = '';
    try {
      const r = await req(`/devices/pairing/${encodeURIComponent(id)}/confirm`, {
        method: 'POST', body: JSON.stringify(confirmBody(match, this._mustReplace ? this._replace : '')) });
      if (r.ok) {
        if (r.body?.state) this._set({ ...this._st, ...r.body });
        else if (!match) this._set({ ...this._st, state: 'rejected', reason: 'mismatch' });
        else this._poll();
      } else if (r.status === 409 && Number.isInteger(r.body?.limit)) {
        this._err = r.body.error || 'you are at your device limit — pick the device this phone replaces';
        await this._loadDevs(true);
      } else {
        this._err = r.body?.error || `error ${r.status}`;
        if (r.body?.state) this._set({ ...this._st, ...r.body });
      }
    } catch (e) { this._err = e.message; }
    this._busy = false;
  }

  _setAddr(v) {
    const c = this.code;
    if (!c) return;
    const addr = String(v).trim() ? phoneOrigin(v) : c.origin;
    if (!addr) { this._addrErr = 'an http(s) address the phone can reach, like https://xbin.example.com (no path)'; return; }
    this._addrErr = '';
    saveAddr(addr === c.origin ? '' : addr);
    this._setLink(addr);
  }

  _again() { this.dispatchEvent(new CustomEvent('bx-pair-again', { bubbles: true, composed: true })); }

  // ---- views ----

  _qrTpl() {
    const q = this._qr;
    if (!q) return nothing;
    return html`<div class="qrwrap"><div class="qr" data-qr style="width:${q.px}px" title="scan with the xbin app">
      <svg viewBox="0 0 ${q.span} ${q.span}" shape-rendering="crispEdges" role="img"
        aria-label="QR code to add a phone"><path d=${q.d} fill="#000"/></svg><!-- theme-ok: a QR code's modules stay dark on light for scanners -->
    </div></div>`;
  }

  _waitTpl() {
    const t = this._tunnel;
    const c = this.code;
    const exp = expiresAt(this._st?.expires) ?? c.expires;
    const left = Math.max(0, (exp ?? 0) - (this._now ?? 0));
    const connecting = this._view === 'connecting';
    return html`<div class="pair" data-pair="${this._view}" data-mode=${t ? 'tunnel' : 'classic'}>
      ${this._qrTpl()}
      <ol>
        <li>Open the <b>xbin</b> app on your phone.</li>
        <li>Tap <b>Log in</b> → <b>Scan QR code</b>, and point it here.</li>
        ${t ? html`<li>Your phone shows a six-digit code. The same code appears here: compare them, then confirm.</li>`
          : html`<li>Confirm with Face ID — done.</li>`}
      </ol>
      ${t ? html`<div class="status ${connecting ? 'on' : ''}" data-status><bx-icon name=${connecting ? 'signal' : 'hourglass'}></bx-icon>${connecting
        ? 'A phone is connecting — its code appears here in a moment.' : 'Waiting for your phone to scan the code…'}</div>` : nothing}
      <div class="timer">works once, for ${clock(left)} more · for your account only${t ? ' · over an iroh tunnel' : ''}</div>
      <details class="more" ?open=${this._moreOpen}><summary>No camera, or the phone uses another address?</summary>
        <label class="addr" for="addr">Address your phone uses</label>
        <div class="row"><input id="addr" data-addr .value=${this._addr} spellcheck="false" autocomplete="off" inputmode="url"
            @change=${(e) => this._setAddr(e.target.value)} @keydown=${(e) => { if (e.key === 'Enter') e.target.blur(); }}>
          ${this._addr !== c.origin ? html`<button title=${`back to ${c.origin}`} @click=${() => this._setAddr('')}>reset</button>` : nothing}</div>
        <div class="hint ${this._addrErr ? 'bad' : ''}">${this._addrErr
          || 'Change it when this browser reaches xbin through a tunnel or proxy the phone can\'t use.'}</div>
        <div class="or">No camera? Open this link on the phone:</div>
        <div class="row"><input readonly data-link .value=${this._link} @focus=${(e) => e.target.select()}>
          <button @click=${() => navigator.clipboard?.writeText(this._link)}>copy</button></div>
      </details>
      ${t && !t.required ? html`<div class="classic"><a href="#" data-classic @click=${(e) => { e.preventDefault();
        this.dispatchEvent(new CustomEvent('bx-pair-classic', { bubbles: true, composed: true })); }}>Older app? Show a classic code</a>
        <span class="hint">(an app from before iroh tunnels can't scan this one)</span></div>` : nothing}
    </div>`;
  }

  _replaceTpl() {
    if (!this._mustReplace) return nothing;
    const d = this._devs;
    const lim = limitInfo(d);
    return html`<fieldset class="replace" data-replace>
      <legend>${lim ? `You have ${lim.words}: the most you may have.` : 'You are at your device limit.'} This phone replaces:</legend>
      ${(d.devices ?? []).map((x) => html`<label data-replace-device=${x.id}>
        <input type="radio" name="replace" .value=${x.id} .checked=${this._replace === x.id} @change=${() => { this._replace = x.id; }}>
        <span><b>${x.name}</b><div class="sub">${platformName(x.platform) || 'device'} · added ${ago(x.created)} ·
          ${x.lastUsed ? `last sign-in ${ago(x.lastUsed)}` : 'not signed in yet'}</div></span></label>`)}
      <div class="hint">The one you pick is removed when you confirm: its app is signed out and forgets this workspace.</div>
    </fieldset>`;
  }

  _confirmTpl() {
    const st = this._st ?? {};
    const sas = formatSas(st.sas);
    const exp = expiresAt(st.expires);
    const need = this._mustReplace && !this._replace;
    return html`<div class="pair confirm" data-pair="confirm">
      <div class="lbl">Confirmation code</div>
      <div class="sas" data-sas>${sas ?? '— — —'}</div>
      <div class="dev"><bx-icon name="phone"></bx-icon><b title=${st.device?.name ?? ''}>${st.device?.name || 'a phone'}</b>
        ${st.device?.platform ? html`<span class="muted">· ${platformName(st.device.platform)}</span>` : nothing}</div>
      <div class="only" role="note"><bx-icon name="warning"></bx-icon><span><b>Only confirm if YOUR phone shows this code.</b>
        It says “Confirm on your computer” with these six digits. If it shows other digits, or you didn't just scan this
        QR code, press <b>They don't</b>.</span></div>
      ${this._replaceTpl()}
      <div class="acts">
        <button class="primary big" data-match ?disabled=${this._busy || need || !sas}
          title=${need ? 'pick the device this phone replaces first' : ''} @click=${() => this._confirm(true)}>Codes match</button>
        <button class="rm big" data-mismatch ?disabled=${this._busy} @click=${() => this._confirm(false)}>They don't</button>
      </div>
      ${exp ? html`<div class="timer">confirm within ${clock(exp - (this._now ?? 0))}</div>` : nothing}
    </div>`;
  }

  _endTpl() {
    const st = this._st ?? {};
    if (this._view === 'enrolled') {
      const name = st.device?.name ?? st.name ?? 'The phone';
      const gone = st.replaced ? (this._devs?.devices ?? []).find((x) => x.id === st.replaced)?.name ?? 'the device it replaces' : '';
      return html`<div class="pair" data-pair="enrolled"><span class="ok"><bx-icon name="ok" label="OK"></bx-icon>${name} was added.</span>
        <span class="muted">It signs in with Face ID from now on.${gone ? ` ${gone} was removed.` : ''}</span></div>`;
    }
    const w = endWords(this._view, st.reason, st.error);
    return html`<div class="pair end ${w.tone}" data-pair=${this._view}>
      <bx-icon name=${w.tone === 'danger' ? 'error' : 'warning'}></bx-icon>
      <div><b>${w.title}</b><p>${w.body}</p><button class="primary" data-again @click=${() => this._again()}>make a new code</button></div>
    </div>`;
  }

  render() {
    if (!this.code) return nothing;
    const v = this._view;
    const body = v === 'confirm' ? this._confirmTpl() : isFinal(v) ? this._endTpl() : this._waitTpl();
    return html`${body}${this._err ? html`<div class="err" role="alert"><bx-icon name="error" label="Error"></bx-icon><span>${this._err}</span></div>` : nothing}`;
  }

  // The harness surface: what it shows, the link, the QR code's geometry.
  testApi() {
    return { state: () => this._view, link: () => this._link, qr: () => (this._qr ? { n: this._qr.n, ppm: this._qr.ppm, px: this._qr.px } : null) };
  }
}
if (!customElements.get('bx-pair')) customElements.define('bx-pair', BxPair);
