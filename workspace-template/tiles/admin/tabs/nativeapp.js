/**
 * <bx-admin-nativeapp> — the admin console's "xbin app" tab: the
 * workspace's native-runtime switch (docs/elements.md §Native app UI). On,
 * the xbin app draws tiles that ship a native.js with native controls; off,
 * it opens every tile as its web page (whoami says native.runtime 0 and the
 * runtime documents answer 410). Saves go through PUT /native-runtime
 * (admin); every open console and app hears the `native` event.
 */
import { LitElement, html, css } from 'lit';
import { xbinApi as api, jbody } from '/vendor/bx-kit.js';
import { base } from '../admin-css.js';
import { WithRouter } from '../shared.js';

export class BxAdminNativeApp extends WithRouter(LitElement) {
  static properties = {
    _state: { state: true }, // {enabled, runtime, version} from GET /native-runtime (null until loaded)
    _busy: { state: true },
    _err: { state: true },
  };
  static styles = [base, css`
    .card { max-width: 560px; border: 1px solid var(--bx-border, #363c45); border-radius: 8px; padding: 12px 14px; background: var(--bx-panel, #23272e); }
    .card h4 { margin: 0 0 6px; }
    label.sw { display: flex; gap: 8px; align-items: flex-start; font-size: 12px; margin-top: 8px; }
    .hint { color: var(--bx-muted, #868f9a); font-size: 12px; }
    .state { margin-top: 8px; font-size: 12px; }
  `];

  constructor() { super(); this._state = null; this._busy = false; }

  connectedCallback() {
    super.connectedCallback();
    this._load();
    this._off = window.xbin?.events.on((e) => { if (e.type === 'native') this._load(); });
  }
  disconnectedCallback() { super.disconnectedCallback(); this._off?.(); }

  async _load() {
    try { this._state = await api('/native-runtime'); } catch (e) { this._fail(e); }
  }

  async _set(enabled) {
    this._busy = true;
    try {
      this._state = await api('/native-runtime', jbody({ enabled }, 'PUT'));
      this._ok();
      this._emit('bx-admin-notice', enabled ? 'native tile UIs are on' : 'native tile UIs are off — the app shows web pages');
    } catch (e) { this._fail(e); this._load(); }
    this._busy = false;
  }

  render() {
    const s = this._state;
    if (!s) return html`<div class="hint">loading…</div>`;
    return html`<div class="card" data-native-runtime=${s.enabled ? 'on' : 'off'}>
      <h4>Native tile UIs in the xbin app</h4>
      <div class="hint">Tiles can ship a <span class="mono">native.js</span> that the xbin app (iPhone, iPad)
        draws with native controls; every tile also works in the app as its web page. Turn this off to have the
        app open every tile as its web page — for example while a native UI misbehaves. Browsers are not affected,
        and builders' previews (<span class="mono">bx native tree</span>, <span class="mono">bx preview --native</span>)
        keep working.</div>
      <label class="sw">
        <input type="checkbox" .checked=${!!s.enabled} ?disabled=${this._busy} @change=${(e) => this._set(e.target.checked)}>
        <span><b>Open tiles natively in the app</b> where they have a native UI.</span>
      </label>
      <div class="state">
        <span class="dot" style="background:${s.enabled ? 'var(--bx-green, #4caf50)' : 'var(--bx-amber, #f2a71b)'}"></span>
        ${s.enabled ? html`on — the app is told native runtime ${s.version}`
          : html`off — the app is told native runtime 0 and opens web pages; tiles already open switch when reopened`}
      </div>
    </div>`;
  }
}
customElements.define('bx-admin-nativeapp', BxAdminNativeApp);
