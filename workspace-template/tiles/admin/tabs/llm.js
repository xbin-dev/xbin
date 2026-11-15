/**
 * <bx-admin-llm> — the admin console's workspace → LLM tab (D226,
 * /docs/llm.md): xbind's built-in LLM connector — its status, providers
 * (masked keys, Test), default models, who may call, quotas and usage — a
 * thin wrapper around <bx-llm-admin> from /vendor/llm-admin.js, which
 * xbind's own /xbin/llm page shares, read and written through xbin.fetch
 * (the console's identity: an admin driving it). A setting the host's
 * deployment file holds is read-only there, "Set by your host".
 *
 * hasLLM() is the question the users and organisations tabs ask before
 * they offer a quota: is there a connector, with a provider.
 */
import { LitElement, html } from 'lit';
import '/vendor/llm-admin.js';
import '/vendor/llm-quotas.js'; // <bx-llm-quota>: one person's or org's quota (the users and organisations tabs)
import { base } from '../admin-css.js';

export const xfetch = (u, o) => window.xbin.fetch(u, o);

// hasLLM() → true when this xbind has the connector and a provider exists
// (GET /llm: 404 before the connector). Asked once per page; the tab
// below keeps the answer current as providers come and go.
let has = null;
export const hasLLM = () => (has ??= xfetch('/api/xbin/llm').then(async (r) => r.ok && !!(await r.json()).configured).catch(() => false));

export class BxAdminLlm extends LitElement {
  static styles = [base];
  render() {
    return html`<p class="muted hint">One OpenAI-compatible endpoint in front of the model providers this workspace uses: the keys in one place,
      every call metered to the person it was made for, quotas where you want them. It is off until a provider exists.
      <a class="link" href="/xbin/llm" target="_blank" rel="noopener">/xbin/llm</a> is the same page outside the console, where everyone else reads
      their own usage; <code>bx llm</code> does the same from a terminal (<a class="link" href="/docs/llm.md" target="_blank" rel="noopener">how it works</a>).</p>
      <bx-llm-admin .fetcher=${xfetch} @llm-loaded=${(e) => { has = Promise.resolve(!!e.detail?.configured); }}></bx-llm-admin>`;
  }
}
customElements.define('bx-admin-llm', BxAdminLlm);
