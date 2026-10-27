/**
 * <bx-admin-apptunnel> — the "xbin app" tab's iroh tunneling section and
 * the workspace's device limit (D204, /docs/app-tunnel.md), drawn under the
 * native-runtime card by tabs/nativeapp.js.
 *
 * Iroh tunneling (GET/PUT /app-tunnel, admin): the switch; the relay — n0's
 * public relays (hobby-scale; the server's endpoint is published to n0 DNS)
 * or the workspace's own iroh-relay: its URL and the access token's two
 * slots, current and next (POST /app-tunnel/relay-tokens: generate-next,
 * set-next, promote, drop-next — a token is only ever shown as its hint,
 * and a generated one once, here); "Refuse app connections outside the
 * tunnel", warning how many devices aren't tunnel-paired; the status — the
 * sidecar's state, whether it is sandboxed, the endpoint id, the home
 * relay, Test (POST /app-tunnel/test), connected and synced devices.
 *
 * Devices per person (GET/PUT /devices/limits): the workspace default; a
 * person's own limit is set in users → devices….
 *
 * An xbind without app tunnels has neither route (404, or 405): the section
 * draws nothing of that part. Saves are the admin's; every open console hears the
 * `app-tunnel` event.
 */
import { LitElement, html, css, nothing } from 'lit';
import { base } from '../admin-css.js';
import { WithRouter } from '../shared.js';
import { sandboxed } from '/vendor/bx-kit.js';

// at(path, init) → {status, ok, body}: an admin call whose status matters (404: no tunnels here),
// with the credential the document has — bx-kit's api() rule: in the sandboxed admin tile, the
// frame token through the in-frame client (its opaque origin holds no cookie; a bare fetch is 401)
async function at(path, init = {}) {
  const f = sandboxed() && window.xbin?.fetch ? window.xbin.fetch : fetch;
  const r = await f(`/api/xbin${path}`, {
    ...init, headers: { Accept: 'application/json', ...(init.body ? { 'Content-Type': 'application/json' } : {}) },
  });
  return { status: r.status, ok: r.ok, body: (await r.json().catch(() => ({}))) ?? {} };
}
const send = (path, method, body) => at(path, { method, body: JSON.stringify(body) });
// an xbind from before D204 has no such route: 404 — or 405 for /devices/limits,
// which its DELETE /devices/{id} pattern matches
const absent = (r) => r.status === 404 || r.status === 405;
const plural = (n, one, many = `${one}s`) => `${n} ${n === 1 ? one : many}`;

// The sidecar's states, in words: [icon, class, words].
const STATE = {
  disabled: ['power', 'st-idle', 'off'],
  unavailable: ['warning', 'warn-ic', 'unavailable — this xbind was built without the iroh sidecar'],
  starting: ['hourglass', 'st-idle', 'starting…'],
  running: ['ok', 'st-healthy', 'running'],
  degraded: ['warning', 'warn-ic', 'degraded'],
  failed: ['error', 'st-failed', 'failed'],
};

export class BxAdminAppTunnel extends WithRouter(LitElement) {
  static properties = {
    _v: { state: true },        // GET /app-tunnel; null = loading, false = this xbind has none
    _lim: { state: true },      // GET /devices/limits; null = loading, false = none
    _mode: { state: true },     // the relay radio's draft ('n0' | 'custom'), '' = as saved
    _url: { state: true },      // the custom relay URL's draft
    _setNext: { state: true },  // the "set next token" form is open
    _token: { state: true },    // a generated next token, shown once
    _test: { state: true },     // POST /app-tunnel/test's answer, or {error}
    _busy: { state: true },
    _err: { state: true },
  };
  static styles = [base, css`
    :host { margin-top: 16px; }
    .card { max-width: 560px; border: 1px solid var(--bx-border); border-radius: var(--bx-radius); padding: var(--bx-pad); background: var(--bx-panel); margin-bottom: 16px; }
    .card h4 { margin: 0 0 8px; }
    .card h5 { margin: 16px 0 6px; font: var(--bx-font-micro); letter-spacing: var(--bx-tracking-micro); text-transform: uppercase; color: var(--bx-muted); }
    label.sw { display: flex; gap: 8px; align-items: flex-start; margin-top: 8px; }
    label.sw input { margin-top: 2px; flex: none; }
    .hint { color: var(--bx-muted); }
    .orow { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; margin-top: 6px; }
    .orow input[type=url], .orow input[type=password] { flex: 1; min-width: 14em; }
    .slot { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; min-height: var(--bx-row); }
    .slot .lbl { min-width: 7em; }
    .once { margin-top: 8px; padding: 8px 10px; border: 1px solid var(--bx-warn); background: var(--bx-warn-bg); border-radius: var(--bx-radius); }
    .once code { display: block; margin: 6px 0; padding: 4px 6px; overflow-wrap: anywhere; background: var(--bx-code-bg); border: 1px solid var(--bx-border); border-radius: var(--bx-radius); }
    .kv { display: grid; grid-template-columns: max-content minmax(0, 1fr); gap: 4px 12px; margin-top: 8px; }
    .kv > .k { color: var(--bx-muted); }
    .kv > .v { overflow-wrap: anywhere; }
    .state { display: flex; gap: 6px; align-items: center; font-weight: 600; }
    .lock { display: flex; gap: 6px; align-items: flex-start; margin: 6px 0 0 24px; color: var(--bx-warn); }
    .lock bx-icon { flex: none; margin-top: 1px; }
    input[type=number] { width: 6em; }
  `];

  constructor() { super(); this._v = null; this._lim = null; this._mode = ''; this._busy = false; }

  connectedCallback() {
    super.connectedCallback();
    this._load();
    this._off = window.xbin?.events.on((e) => { if (e.type === 'app-tunnel') this._load(); });
  }
  disconnectedCallback() { super.disconnectedCallback(); this._off?.(); }

  async _load() {
    try {
      const [t, l] = await Promise.all([at('/app-tunnel'), at('/devices/limits')]);
      this._v = t.ok ? t.body : absent(t) ? false : this._v;
      this._lim = l.ok ? l.body : absent(l) ? false : this._lim;
      if (!t.ok && !absent(t)) this._fail(t.body.error || `app tunnel: error ${t.status}`);
    } catch (e) { this._fail(e); }
  }

  // One write. Its answer is the new view (a generated token rides beside
  // it, shown once); anything else, the next load brings. A refusal reloads
  // (a switch's caller puts the box back: its binding never saw the click).
  async _write(path, method, body, notice) {
    this._busy = true;
    let out = null;
    try {
      const r = await send(path, method, body);
      if (!r.ok) throw new Error(r.body.error || `error ${r.status}`);
      out = r.body;
      const { token: _, ...view } = out;
      if (path.startsWith('/app-tunnel') && 'enabled' in view) this._v = view;
      else if (path === '/devices/limits' && 'default' in view) this._lim = { ...this._lim, ...view };
      else this._load();
      this._ok();
      if (notice) this._emit('bx-admin-notice', notice);
    } catch (e) { this._fail(e); await this._load(); }
    this._busy = false;
    return out;
  }

  async _enable(el) {
    const on = el.checked;
    if (!await this._write('/app-tunnel', 'PUT', { enabled: on }, on ? 'iroh tunneling is on — new phones pair over the tunnel' : 'iroh tunneling is off')) {
      el.checked = !!this._v?.enabled;
    }
  }
  _relay(mode, url) {
    const relay = mode === 'n0' ? { mode } : { mode, url };
    this._write('/app-tunnel', 'PUT', { relay }, mode === 'n0' ? 'the tunnel uses n0\'s public relays' : `the tunnel uses ${url}`)
      .then((r) => { if (r || mode === 'n0') this._mode = ''; if (r) this._url = undefined; });
  }
  async _tunnelOnly(el, classic) {
    const on = el.checked;
    if (on && classic > 0 && !confirm(`${plural(classic, 'device')} ${classic === 1 ? "isn't" : "aren't"} tunnel-paired. Refusing app connections outside the tunnel locks ${classic === 1 ? 'it' : 'them'} out until ${classic === 1 ? 'it pairs' : 'they pair'} again with a new QR code.\n\nRefuse them now?`)) {
      el.checked = false;
      return;
    }
    if (!await this._write('/app-tunnel', 'PUT', { tunnelOnly: on }, on ? 'the app connects only through the tunnel now' : 'the app may connect outside the tunnel again')) {
      el.checked = !!this._v?.tunnelOnly;
    }
  }
  async _tokens(action, token) {
    const r = await this._write('/app-tunnel/relay-tokens', 'POST', token ? { action, token } : { action },
      { 'generate-next': 'a next token was generated — copy it now', 'set-next': 'the next token is set', promote: 'the next relay settings are current now', 'drop-next': 'the next token was dropped' }[action]);
    if (!r) return;
    this._token = action === 'generate-next' ? r.token ?? '' : '';
    if (action === 'set-next') this._setNext = false;
  }
  async _runTest() {
    this._test = { running: true };
    const r = await at('/app-tunnel/test', { method: 'POST', body: '{}' }).catch((e) => ({ ok: false, body: { error: e.message } }));
    this._test = r.ok ? r.body : { error: r.body.error || `error ${r.status}` };
  }
  _saveLimit(v) {
    const n = Number(v);
    if (!Number.isInteger(n) || n < 1 || n > (this._lim?.max ?? 32)) { this._fail(`a default limit is a whole number from 1 to ${this._lim?.max ?? 32}`); return; }
    this._write('/devices/limits', 'PUT', { default: n }, `each person may pair ${plural(n, 'device')} by default`);
  }

  // ---- views ----

  _relayTpl(v) {
    const r = v.relay ?? { mode: 'n0' };
    const mode = this._mode || r.mode || 'n0';
    const url = this._url ?? r.url ?? '';
    const next = r.next ?? null;
    const dv = v.devices ?? {};
    const env = v.env ?? ''; // XBIN_IROH_RELAY: this xbind's relay, over the choice below (a PUT of it is 409)
    return html`<h5>Relay</h5>
      ${env ? html`<div class="hint" data-relay-env><bx-icon name="info"></bx-icon> This xbind runs with
        <span class="mono">XBIN_IROH_RELAY=${env}</span>${env === 'disabled' ? ' — no relay: direct connections only' : ''}; it
        overrides the choice below until it is unset (<a href="/docs/config.md" target="_blank" rel="noopener">config</a>).</div>` : nothing}
      <label class="sw"><input type="radio" name="relay" value="n0" data-relay="n0" .checked=${mode === 'n0'} ?disabled=${this._busy || !!env}
          @change=${() => { this._mode = 'n0'; if (r.mode !== 'n0') this._relay('n0'); }}>
        <span><b>n0's public relays</b> <span class="muted">— hobby-scale; publishes this server's endpoint to n0 DNS</span></span></label>
      <label class="sw"><input type="radio" name="relay" value="custom" data-relay="custom" .checked=${mode === 'custom'} ?disabled=${this._busy || !!env}
          @change=${() => { this._mode = 'custom'; }}>
        <span><b>Your own relay</b> <span class="muted">— an iroh-relay you run, with an access token
          (<a href="/docs/app-tunnel.md" target="_blank" rel="noopener">how to run one</a>)</span></span></label>
      ${mode !== 'custom' ? nothing : html`
        <div class="orow"><input type="url" data-relay-url placeholder="https://relay.example.com" .value=${url}
            @input=${(e) => { this._url = e.target.value; }}>
          <button class="act ${r.mode === 'custom' && url === r.url ? '' : 'go'}" ?disabled=${this._busy || !url.trim() || (r.mode === 'custom' && url === r.url)}
            @click=${() => this._relay('custom', url.trim())}>${r.mode === 'custom' ? 'save' : 'use this relay'}</button></div>
        <div class="hint muted">Changing the relay restarts the tunnel. Phones learn the new one the next time they connect,
          so keep the old relay up until every device shows synced.</div>
        ${r.mode !== 'custom' ? nothing : html`
          <div class="slot" data-token="current"><span class="lbl">current token</span>
            <span class="mono">${r.tokenHint || html`<span class="muted sans">none</span>`}</span></div>
          <div class="slot" data-token="next"><span class="lbl">next token</span>
            <span class="mono">${next?.tokenHint || html`<span class="muted sans">none</span>`}</span>
            ${next?.url && next.url !== r.url ? html`<span class="muted">at <span class="mono">${next.url}</span></span>` : nothing}
            <span style="flex:1"></span>
            <button class="act" data-act="generate-next" ?disabled=${this._busy} @click=${() => this._tokens('generate-next')}>generate next</button>
            <button class="act" data-act="set-next" ?disabled=${this._busy} @click=${() => { this._setNext = !this._setNext; }}>set next…</button>
            ${next ? html`<button class="act go" data-act="promote" ?disabled=${this._busy}
                title="the next token becomes the current one: the tunnel restarts with it, and phones switch on their next connection"
                @click=${() => this._tokens('promote')}>promote</button>
              <button class="act quiet rm" data-act="drop-next" ?disabled=${this._busy} @click=${() => this._tokens('drop-next')}>drop next</button>` : nothing}
          </div>
          ${this._setNext ? html`<form class="orow" data-set-next @submit=${(e) => { e.preventDefault(); this._tokens('set-next', e.target.tok.value.trim()); }}>
            <input name="tok" type="password" autocomplete="off" placeholder="the relay's next token (16–256 characters)" required minlength="16" maxlength="256">
            <button class="act go" type="submit" ?disabled=${this._busy}>set</button>
            <button class="act" type="button" @click=${() => { this._setNext = false; }}>cancel</button></form>` : nothing}
          ${this._token ? html`<div class="once" data-generated><b>The new next token — shown this once:</b>
            <code>${this._token}</code>
            <div class="orow"><button class="act" @click=${() => navigator.clipboard?.writeText(this._token)}>copy</button>
              <button class="act quiet" @click=${() => { this._token = ''; }}>I've copied it</button></div>
            <div class="hint">Add it to the relay's <span class="mono">access.shared_token</span> list beside the current one, restart
              the relay, then promote it here.</div></div>` : nothing}
          ${next ? html`<div class="hint muted" data-synced>${dv.synced ?? 0} of ${plural(dv.tunnelPaired ?? 0, 'tunnel-paired device')} know the next
            relay settings — drop the old token from the relay once every one does.</div>` : nothing}`}`}`;
  }

  _statusTpl(v) {
    const [icon, cls, words] = STATE[v.state] ?? ['question', 'st-idle', v.state || 'unknown'];
    const dv = v.devices ?? {};
    const t = this._test;
    return html`<h5>Status</h5>
      <div class="state" data-state=${v.state ?? ''}><bx-icon class=${cls} name=${icon}></bx-icon><span class=${cls}>${words}</span>
        ${v.restarts ? html`<span class="muted" style="font-weight:400">· restarted ${plural(v.restarts, 'time')}</span>` : nothing}</div>
      ${v.error ? html`<div class="err" data-state-error><bx-icon name="error"></bx-icon><span>${v.error}</span></div>` : nothing}
      <div class="kv">
        <span class="k">sandboxed</span><span class="v" data-confined>${v.confined ? 'yes — the iroh sidecar runs in a sandbox of its own'
          : html`<span class="warn-ic">no</span> <span class="muted">— this host can't sandbox it (<a href="/docs/isolation.md" target="_blank" rel="noopener">why</a>); it still sees only ciphertext</span>`}</span>
        <span class="k">endpoint id</span><span class="v mono" data-endpoint>${v.endpointId || '—'}</span>
        <span class="k">home relay</span><span class="v mono">${v.homeRelay || '—'}</span>
        <span class="k">devices</span><span class="v" data-devices>${dv.connected ?? 0} connected now · ${dv.tunnelPaired ?? 0} tunnel-paired ·
          ${dv.classic ?? 0} classic${dv.tunnelPaired ? ` · ${dv.synced ?? 0} of ${dv.tunnelPaired} synced` : ''}</span>
      </div>
      <div class="orow">
        <button class="act" data-test ?disabled=${t?.running || v.state === 'disabled' || v.state === 'unavailable'}
          title="an ephemeral endpoint dials this server through the relay" @click=${() => this._runTest()}>Test</button>
        ${!t ? nothing : t.running ? html`<span class="muted">testing…</span>`
          : t.error || !t.reachable ? html`<span class="err" data-test-result="fail"><bx-icon name="error"></bx-icon>not reachable${t.error ? `: ${t.error}` : ''}</span>`
          : html`<span class="st-healthy" data-test-result="ok"><bx-icon name="ok"></bx-icon> reachable${t.path ? ` via ${t.path}` : ''}${Number.isFinite(t.rttMs) ? ` · ${t.rttMs} ms` : ''}</span>`}
      </div>`;
  }

  _tunnelTpl() {
    const v = this._v;
    if (v === false) return nothing; // an xbind without app tunnels
    if (!v) return html`<div class="card"><div class="hint">loading…</div></div>`;
    const classic = v.devices?.classic ?? 0;
    return html`<div class="card" data-apptunnel=${v.enabled ? 'on' : 'off'}>
      <h4>Iroh tunneling</h4>
      <div class="hint">The xbin app reaches this workspace through an iroh tunnel: an encrypted connection, direct when it can be
        and through a relay when it can't, with xbin's own encryption inside — so a phone needs no VPN or public address, and
        no relay sees anything but ciphertext. Phones pair with a bigger QR code and a six-digit code confirmed on the
        computer. Browsers are not affected (<a href="/docs/app-tunnel.md" target="_blank" rel="noopener">how app tunnels work</a>).</div>
      <label class="sw"><input type="checkbox" data-enable .checked=${!!v.enabled} ?disabled=${this._busy} @change=${(e) => this._enable(e.target)}>
        <span><b>Enable iroh tunneling</b> for the xbin app</span></label>
      ${!v.enabled ? nothing : html`
        ${this._relayTpl(v)}
        <h5>Connections outside the tunnel</h5>
        <label class="sw"><input type="checkbox" data-tunnel-only .checked=${!!v.tunnelOnly} ?disabled=${this._busy}
            @change=${(e) => this._tunnelOnly(e.target, classic)}>
          <span><b>Refuse app connections outside the tunnel</b> <span class="muted">— the app's sign-ins and device sessions work
            only through iroh, so a phone off its VPN never sends them over the open network</span></span></label>
        ${classic > 0 ? html`<div class="lock" data-lockout><bx-icon name="warning"></bx-icon><span>${plural(classic, 'device')}
          ${classic === 1 ? "isn't" : "aren't"} tunnel-paired: ${v.tunnelOnly ? `${classic === 1 ? 'it is' : 'they are'} locked out until paired again with a new QR code`
            : `turning this on locks ${classic === 1 ? 'it' : 'them'} out until paired again with a new QR code. An updated xbin app moves onto the tunnel by itself the next time it opens while this is off`}.</span></div>` : nothing}
        ${this._statusTpl(v)}`}
    </div>`;
  }

  _limitTpl() {
    const l = this._lim;
    if (!l) return nothing; // loading, or an xbind without device limits
    const overrides = l.overrides ?? [];
    return html`<div class="card" data-device-limit>
      <h4>Devices per person</h4>
      <div class="hint">How many xbin app devices each person may pair. It applies when a device is added: devices paired
        before stay. A person's own limit is set in users → devices….</div>
      <form class="inline" @submit=${(e) => { e.preventDefault(); this._saveLimit(e.target.lim.value); }}>
        <label>default <input name="lim" type="number" min="1" max=${l.max ?? 32} step="1" .value=${String(l.default ?? 1)} data-limit-default></label>
        <button class="act go" type="submit" ?disabled=${this._busy}>save</button>
        <span class="muted" data-at-limit>${plural((l.atLimit ?? []).length, 'person', 'people')} at their limit${overrides.length
          ? ` · ${plural(overrides.length, 'person', 'people')} with a limit of their own` : ''}</span>
      </form>
    </div>`;
  }

  render() { return html`${this._tunnelTpl()}${this._limitTpl()}`; }
}
customElements.define('bx-admin-apptunnel', BxAdminAppTunnel);
