/**
 * <bx-llm-page> — xbind's page for its LLM connector, /xbin/llm (D226,
 * docs/llm.md): the surface that is there at all times — it ships with
 * xbind, so a workspace whose admin console predates the connector still
 * sets it up, and the connector's alerts name it. Served as it is (no HTML
 * transform) and only top-level: framed, it renders nothing of the
 * person's.
 *
 * A workspace admin gets <bx-llm-admin>: status, providers, default
 * models, who may call, quotas and usage — every setting the host's
 * deployment file holds read-only, "Set by your host". Anyone else gets
 * <bx-llm-me>: their own quota and their own usage, nothing more. Which of
 * the two is xbind's answer to GET /llm/config, read with the person's own
 * session; xbind judges every act again.
 */
import { LitElement, html, css } from '/vendor/lit-all.min.js';
import '/vendor/bx-icons.js';
import '/vendor/llm-admin.js';
import '/vendor/llm-usage.js';
import { llmClient } from '/vendor/llm-client.js';

const pageCss = css`
  :host { display: block; color: var(--bx-text, #E9EAF0); font: var(--bx-font, 13px/18px "Instrument Sans", system-ui, sans-serif); }
  :focus-visible { outline: var(--bx-focus-outline, 3px solid #3DD6F5); outline-offset: var(--bx-focus-offset, 2px); box-shadow: var(--bx-focus-halo, 0 0 0 2px #0B0C12); }
  main { max-width: 1080px; margin: 0 auto; padding: 24px 16px 64px; box-sizing: border-box; }
  main.one { max-width: 760px; }
  header { display: flex; align-items: baseline; gap: 12px; flex-wrap: wrap; margin-bottom: 8px; }
  header .grow { flex: 1; }
  h1 { font: var(--bx-font-heading, 600 20px/26px "Bricolage Grotesque", "Arial Black", system-ui, sans-serif);
    letter-spacing: var(--bx-tracking-heading, -0.01em); margin: 0; display: flex; align-items: center; gap: 8px; }
  .lead { color: var(--bx-muted, #A3A6B6); max-width: 76ch; margin: 8px 0 20px; }
  a { color: var(--bx-link, #8C9BFF); text-decoration: none; }
  a:hover { text-decoration: underline; }
  .small { font: var(--bx-font-meta, 400 12px/16px "Instrument Sans", system-ui, sans-serif); }
  .banner { border: 1px solid var(--bx-danger, #FF7A7A); border-radius: var(--bx-radius, 2px); padding: 8px 12px; margin: 12px 0; background: var(--bx-danger-bg, #3A2B32); }
`;

export class BxLlmPage extends LitElement {
  static properties = { _mode: { state: true } }; // admin (tried first) | me | absent
  static styles = pageCss;
  constructor() {
    super();
    this._framed = false;
    try { this._framed = window.top !== window.self; } catch { this._framed = true; }
    this._mode = 'admin';
    this._client = llmClient();
  }
  render() {
    if (this._framed) {
      return html`<main><div class="banner" role="alert"><bx-icon name="error"></bx-icon> This page shows the LLM connector only when it is opened on its own, never inside another page.
        Open <code>/xbin/llm</code> in a tab of its own.</div></main>`;
    }
    const m = this._mode;
    return html`<main class=${m === 'admin' ? '' : 'one'} data-mode=${m}>
      <header>
        <h1><bx-icon name="chat"></bx-icon>${m === 'me' ? 'Your LLM usage' : 'LLM connector'}</h1>
        <span class="grow"></span>
        <a class="small" href="/docs/llm.md" target="_blank" rel="noopener">how it works</a>
        <a class="small" href="/"><bx-icon name="arrow-left"></bx-icon> workspace</a>
      </header>
      ${m === 'absent' ? html`<div class="banner" role="alert"><bx-icon name="error"></bx-icon> This xbind has no LLM connector.</div>`
        : m === 'me' ? html`<p class="lead">This workspace’s tiles, agents and coding tools reach their models through one connector, and every call is
            counted to the person it was made for. Here is what was counted to you, and the limits that apply to you.</p>
          <bx-llm-me .client=${this._client}></bx-llm-me>`
          : html`<p class="lead">One OpenAI-compatible endpoint in front of the model providers this workspace uses: the keys in one place, every call
            metered to the person it was made for, quotas where you want them. It is off until a provider exists
            (<code>bx llm</code> does the same from a terminal).</p>
          <bx-llm-admin @llm-denied=${() => { this._mode = 'me'; }} @llm-absent=${() => { this._mode = 'absent'; }}></bx-llm-admin>`}
    </main>`;
  }
  testApi() { const s = this; return { framed: () => s._framed, get mode() { return s._mode; } }; }
}
customElements.define('bx-llm-page', BxLlmPage);
