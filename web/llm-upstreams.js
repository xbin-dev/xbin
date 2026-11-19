/**
 * <bx-llm-upstreams .cfg .client> — the LLM connector's providers (D226,
 * docs/llm.md): each upstream's name, base URL, whether it has a key (never
 * the key: its last four characters while the vault is open), its models
 * (a list, or every model the provider lists), the `private` mark, and
 * Test — a one-line call through it, answered inline. Add, edit and remove
 * are PUT / DELETE /llm/upstreams/<name>; the answer is the new
 * configuration, handed up in an `llm-config` event.
 *
 * A key is typed into a masked field and sent once: after the save the
 * field is gone and nothing reads the key back. An upstream the host's
 * file defines is drawn read-only with "Set by your host"; when the host
 * locks every setting there is no Add. Changing an upstream's scheme, host
 * or port drops its stored key (xbind's rule): the form says so and asks
 * for the key again, or for "remove the key".
 *
 * "reasoning…" opens a provider's models with what is known of their
 * reasoning effort (D249: "Reasoning: Low · High · Max (default High) ·
 * from the upstream", or "not described") and, unless the host holds the
 * provider, "describe…" per model: the levels it takes ticked, whether it
 * can be turned off, its default — a preset pre-ticks them — or "forget"
 * the description. No JSON, no request patches.
 */
import { LitElement, html, css, nothing } from '/vendor/lit-all.min.js';
import '/vendor/bx-icons.js';
import { limitsCss } from '/vendor/bx-limits.js';
import { llmCss, lockTpl, errTpl } from '/vendor/llm-css.js';
import { lockOf, fullyLocked, keyWords, modelsWords, testWords, upstreamBody, upstreamDraft, addressChanged, vaultShut, vaultWords, plural,
  effortLine, upstreamModels, REASONING_PRESETS, EFFORT_LEVELS, effortDraft, effortRecord, effortDraftWords } from '/vendor/llm-kit.js';

const NEW = '+';

export class BxLlmUpstreams extends LitElement {
  static properties = {
    cfg: { attribute: false },     // GET /llm/config
    client: { attribute: false },  // llmClient()
    _edit: { state: true },        // the upstream in the editor: its name, NEW, or null
    _d: { state: true },           // the editor's draft (upstreamDraft)
    _rm: { state: true },          // the upstream whose removal is being confirmed
    _tests: { state: true },       // name → the test's answer, {running} meanwhile
    _reason: { state: true },      // the upstream whose models' reasoning is shown, or null
    _models: { state: true },      // GET /v1/models while that is open ({data}), null before it came
    _desc: { state: true },        // the model being described ({model, draft}), or null
    _busy: { state: true }, _err: { state: true },
  };
  static styles = [limitsCss, llmCss, css`
    td.name { font-weight: 600; white-space: nowrap; }
    td.url { max-width: 22em; }
    .prices { display: grid; grid-template-columns: minmax(8em, 1.6fr) repeat(3, minmax(5em, 1fr)) max-content; gap: 4px 8px; align-items: center; }
    .result { display: flex; gap: 6px; align-items: baseline; }
    .reason { display: grid; grid-template-columns: minmax(8em, max-content) 1fr max-content; gap: 4px 12px; align-items: center; }
    .describe { grid-column: 1 / -1; display: flex; flex-wrap: wrap; gap: 6px 14px; align-items: center; padding: 6px 0 10px; }
    .describe .levels { display: flex; flex-wrap: wrap; gap: 4px 10px; }
    .describe select { max-width: 100%; }
    @media (max-width: 640px) { .reason { grid-template-columns: 1fr; } }
  `];
  constructor() { super(); this._edit = null; this._tests = {}; this._err = ''; }

  _open(name) {
    const u = (this.cfg?.upstreams ?? []).find((x) => x.name === name) ?? null;
    this._edit = u ? name : NEW; this._d = upstreamDraft(u); this._err = ''; this._rm = null;
  }
  _set(patch) { this._d = { ...this._d, ...patch }; }
  _price(i, patch) { this._set({ prices: this._d.prices.map((r, j) => (j === i ? { ...r, ...patch } : r)) }); }

  _done(r, what) {
    this._busy = false;
    if (!r.ok) { this._err = `${what}: ${r.error}`; return false; }
    this._err = ''; this._edit = null; this._d = null; this._rm = null;
    this.dispatchEvent(new CustomEvent('llm-config', { detail: r.data, bubbles: true, composed: true }));
    return true;
  }
  async _save() {
    const cur = this._edit === NEW ? null : (this.cfg?.upstreams ?? []).find((x) => x.name === this._edit) ?? null;
    const { name, body, error } = upstreamBody(this._d, cur);
    if (error) { this._err = error; return; }
    if (!cur && (this.cfg?.upstreams ?? []).some((x) => x.name === name)) { this._err = `name: a provider named ${name} exists — edit it instead`; return; }
    if (cur && !Object.keys(body).length) { this._edit = null; return; }
    this._busy = true;
    this._done(await this.client.putUpstream(name, body), name);
  }
  async _remove(name) {
    this._busy = true;
    if (this._done(await this.client.deleteUpstream(name), name)) { const { [name]: _, ...rest } = this._tests; this._tests = rest; }
  }
  async _openReason(name) {
    if (this._reason === name) { this._reason = null; return; }
    this._reason = name; this._models = null; this._err = '';
    const r = await this.client.models();
    this._models = r.ok ? r.data : { data: [], error: r.error };
  }
  async _setReason(u, model, rec) {
    this._busy = true;
    const r = await this.client.putUpstream(u.name, { reasoning: { [model]: rec } });
    this._busy = false;
    if (!r.ok) { this._err = `${u.name}: ${r.error}`; return; }
    this._err = ''; this._desc = null;
    this.dispatchEvent(new CustomEvent('llm-config', { detail: r.data, bubbles: true, composed: true }));
    const m = await this.client.models();
    if (m.ok) this._models = m.data;
  }
  _draft(patch) { this._desc = { ...this._desc, draft: { ...this._desc.draft, ...patch } }; }
  // _describeTpl: the levels a model takes, ticked; whether it can be turned off; its default — a preset pre-ticks them
  _describeTpl(u, model) {
    const d = this._desc.draft, ticked = EFFORT_LEVELS.filter((w) => d.levels[w]);
    const presets = this.cfg?.reasoningPresets ?? [];
    return html`<div class="describe" data-describe=${model}>
      <span class="k">presets</span>${REASONING_PRESETS.filter((p) => p.id).map((p) => html`<button type="button" class="small quiet" data-preset=${p.id}
        @click=${() => { this._desc = { ...this._desc, draft: effortDraft(presets.find((x) => x.name === p.id)?.effort) }; }}>${p.label}</button>`)}
      <span class="levels" role="group" aria-label=${`the levels ${model} takes`}>${EFFORT_LEVELS.map((w) => html`<label class="sw"><input type="checkbox" data-level=${w}
        .checked=${!!d.levels[w]} @change=${(e) => this._draft({ levels: { ...d.levels, [w]: e.target.checked } })}><span>${w}</span></label>`)}</span>
      <label class="sw"><input type="checkbox" data-off .checked=${d.off} @change=${(e) => this._draft({ off: e.target.checked })}><span>can be turned off (none)</span></label>
      <label>default <select data-default aria-label="its default" @change=${(e) => this._draft({ def: e.target.value })}>
        <option value="" ?selected=${!d.def}>unknown</option>
        ${d.off ? html`<option value="none" ?selected=${d.def === 'none'}>off</option>` : nothing}
        ${ticked.map((w) => html`<option value=${w} ?selected=${d.def === w}>${w}</option>`)}</select></label>
      <span class="meta" data-describe-words>${effortDraftWords(d)}</span>
      <button class="small primary" data-describe-save ?disabled=${this._busy} @click=${() => this._setReason(u, model, effortRecord(d))}>Save</button>
      <button class="small quiet" @click=${() => { this._desc = null; }}>Cancel</button>
    </div>`;
  }
  // _reasonTpl: the provider's models, each with its reasoning and (unless locked) describe… / forget; then the
  // admin's own entries that name no listed model (a family: "deepseek*").
  _reasonTpl(u, lock) {
    if (!this._models) return html`<div class="meta">loading the models…</div>`;
    const models = upstreamModels(this._models, u.name);
    const own = u.reasoning ?? {};
    const acts = (model, e) => html`<span class="line">
      <button class="small" data-describe-open=${model} ?disabled=${lock.locked || this._busy} title=${lock.locked ? 'set by your host — it can’t be changed here' : 'the levels it takes, whether it can be turned off, its default'}
        @click=${() => { this._desc = { model, draft: effortDraft(e) }; }}>describe…</button>
      ${own[model] ? html`<button class="small quiet" data-forget=${model} ?disabled=${lock.locked || this._busy} title="forget your description: the upstream's or xbin's applies"
        @click=${() => this._setReason(u, model, null)}>forget</button>` : nothing}</span>
      ${this._desc?.model === model ? this._describeTpl(u, model) : nothing}`;
    const rows = models.map((m) => html`<span class="mono">${m.id}</span><span data-reason-line=${m.id}>${effortLine(m.effort)}</span>
      ${acts(m.id, own[m.id] ?? m.effort)}`);
    const families = Object.keys(own).filter((k) => !models.some((m) => m.id === k)).sort().map((k) => html`<span class="mono">${k}</span>
      <span data-reason-line=${k}>${effortLine(own[k])}</span>${acts(k, own[k])}`);
    return html`<div class="wide">
      <div class="meta">What the connector knows of each model's reasoning effort: what you describe here, else the provider's own list, else xbin's table. A model nothing describes gets no effort control, and the efforts callers send pass to it untouched.${this._models.error ? ` (The model list could not be read: ${this._models.error}.)` : ''}</div>
      ${rows.length || families.length ? html`<div class="reason" data-reason=${u.name}>${rows}${families}</div>` : html`<div class="meta">The provider lists no models.</div>`}
      ${errTpl(this._err)}</div>`;
  }
  async _test(u) {
    this._tests = { ...this._tests, [u.name]: { running: true } };
    const r = await this.client.testUpstream(u.name);
    this._tests = { ...this._tests, [u.name]: r.ok ? r.data : { ok: false, error: r.error } };
  }

  _editor(cur) {
    const d = this._d, shut = vaultShut(this.cfg);
    // a key is only ever sent to the address it was set for: a new scheme, host or port needs it again
    const moved = !!cur?.key?.set && addressChanged(cur.baseURL, d.baseURL);
    const keyHint = shut ? vaultWords(this.cfg.vault)
      : moved ? 'The address changed, so the stored key is not kept: a key is only ever sent to the address it was set for. Enter the key for the new address, or tick “remove the key”.'
      : cur?.key?.set ? `A key is set (${keyWords(cur.key)}). Leave this empty to keep it; a key typed here replaces it and is never shown again.`
        : 'Stored in xbind’s vault and never shown again — not here, not by any API.';
    return html`<div class="editor" data-editor=${cur?.name ?? 'new'}>
      <form class="form" @submit=${(e) => { e.preventDefault(); this._save(); }}>
        <label for="u-name">name</label>
        <input id="u-name" data-f="name" .value=${d.name} ?disabled=${!!cur} placeholder="openai" autocomplete="off" spellcheck="false"
          @input=${(e) => this._set({ name: e.target.value })}>
        <label for="u-url">base URL</label>
        <input id="u-url" data-f="baseURL" type="url" .value=${d.baseURL} placeholder="https://api.openai.com/v1" autocomplete="off" spellcheck="false"
          @input=${(e) => this._set({ baseURL: e.target.value })}>
        <span class="meta">The provider’s OpenAI-compatible API, with or without <code>/v1</code>.</span>
        <label for="u-key">API key</label>
        <input id="u-key" data-f="key" type="password" autocomplete="new-password" spellcheck="false" .value=${d.key} ?disabled=${shut || d.removeKey}
          placeholder=${moved ? 'the key for the new address' : cur?.key?.set ? 'unchanged' : 'the provider’s key'}
          @input=${(e) => this._set({ key: e.target.value })}>
        <span class="meta ${shut || moved ? 'warnc' : ''}" data-key-hint=${moved ? 'moved' : ''}>${keyHint}</span>
        ${cur?.key?.set && !shut ? html`<label class="sw wide"><input type="checkbox" data-f="removeKey" .checked=${d.removeKey}
          @change=${(e) => this._set({ removeKey: e.target.checked, key: '' })}><span>remove the key</span></label>` : nothing}
        <label for="u-models">models</label>
        <input id="u-models" data-f="models" .value=${d.models} placeholder="every model the provider lists" autocomplete="off" spellcheck="false"
          @input=${(e) => this._set({ models: e.target.value })}>
        <span class="meta">A list (commas or spaces) offers only those models. Empty passes the provider’s own list, and any model name, through.</span>
        <label class="sw wide"><input type="checkbox" data-f="private" .checked=${d.private} @change=${(e) => this._set({ private: e.target.checked })}>
          <span><b>private</b> <span class="muted">— prompts sent here stay inside your organisation (a mark org rules will filter on; nothing does yet)</span></span></label>
        <div class="wide">
          <div class="k">prices — USD per million tokens</div>
          <div class="meta">For the cost counters and cost quotas. <code>gpt-5*</code> prices a family; a provider that reports a call’s cost itself is believed when no price is set.</div>
          ${d.prices.length ? html`<div class="prices" data-prices>
            <span class="k">model</span><span class="k">input</span><span class="k">output</span><span class="k">cached input</span><span></span>
            ${d.prices.map((p, i) => html`
              <input aria-label="model" data-p="model" .value=${p.model} placeholder="gpt-5" spellcheck="false" @input=${(e) => this._price(i, { model: e.target.value })}>
              <input aria-label="input price" data-p="in" inputmode="decimal" .value=${p.in} placeholder="1.25" @input=${(e) => this._price(i, { in: e.target.value })}>
              <input aria-label="output price" data-p="out" inputmode="decimal" .value=${p.out} placeholder="10" @input=${(e) => this._price(i, { out: e.target.value })}>
              <input aria-label="cached input price" data-p="cachedIn" inputmode="decimal" .value=${p.cachedIn} placeholder="optional" @input=${(e) => this._price(i, { cachedIn: e.target.value })}>
              <button type="button" class="quiet small" title="remove this price" aria-label="remove this price"
                @click=${() => this._set({ prices: d.prices.filter((_, j) => j !== i) })}><bx-icon name="xmark"></bx-icon></button>`)}
          </div>` : nothing}
          <div class="line"><button type="button" class="small" data-add-price
            @click=${() => this._set({ prices: [...d.prices, { model: '', in: '', out: '', cachedIn: '' }] })}>+ price</button></div>
        </div>
        <div class="wide">${errTpl(this._err)}</div>
        <div class="line wide">
          <button class="primary" type="submit" data-save ?disabled=${this._busy}>${cur ? 'Save' : 'Add provider'}</button>
          <button type="button" class="quiet" @click=${() => { this._edit = null; this._err = ''; }}>Cancel</button>
        </div>
      </form>
    </div>`;
  }

  // How this provider's Responses calls are served, when not by its own API: set so ("chat"), or found — with when,
  // and a way to have the provider asked again (setting responsesVia to auto forgets what was found).
  _responses(u, lock, busy) {
    if (u.responsesVia === 'chat') return html`<span class="badge" data-translating="set" title="Responses calls for this provider are translated to chat completions (responsesVia: chat)">Responses: translated</span> `;
    if (!u.translating) return nothing;
    const at = u.translatingSince ? new Date(u.translatingSince) : null;
    const when = at && !Number.isNaN(at.getTime()) ? ` — detected ${at.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })}` : '';
    return html`<span class="badge" data-translating="detected" title="This provider answered that it has no Responses API, and a translated call worked: Responses calls are translated to chat completions for it (docs: llm.md)">Responses: translated${when}</span>
      <button class="small quiet" data-redetect ?disabled=${lock.locked || busy} title="forget what was detected: the provider is asked for its Responses API again"
        @click=${async () => { this._done(await this.client.putUpstream(u.name, { responsesVia: 'auto' }), u.name); }}>reset</button> `;
  }

  _row(u) {
    const lock = u.locked ? { locked: true, reason: u.reason } : lockOf(this.cfg?.locks, `upstreams.${u.name}`);
    const t = this._tests[u.name], tw = testWords(t), busy = this._busy;
    const why = lock.locked ? 'set by your host — it can’t be changed here' : '';
    return html`<tr data-upstream=${u.name} ?data-host=${lock.locked}>
        <td class="name"><span class="mono">${u.name}</span></td>
        <td class="url mono" data-l="base URL">${u.baseURL}</td>
        <td data-l="key" data-key>${keyWords(u.key)}</td>
        <td data-l="models">${modelsWords(u.models)}${Object.keys(u.prices ?? {}).length ? html`<div class="meta">${plural(Object.keys(u.prices).length, 'price')}</div>` : nothing}</td>
        <td>${u.private ? html`<span class="badge">private</span> ` : nothing}${this._responses(u, lock, busy)}${lockTpl(lock)}</td>
        <td class="acts">
          <button class="small" data-test ?disabled=${t?.running} title="a one-line call through this provider (metered to you)" @click=${() => this._test(u)}>Test</button>
          <button class="small" data-edit ?disabled=${lock.locked || busy} title=${why} @click=${() => this._open(u.name)}>edit…</button>
          <button class="small quiet" data-reasoning aria-expanded=${this._reason === u.name ? 'true' : 'false'} title="each model's reasoning effort, and how you describe it"
            @click=${() => this._openReason(u.name)}>reasoning…</button>
          <button class="small quiet" data-remove ?disabled=${lock.locked || busy} title=${why} @click=${() => { this._rm = u.name; this._edit = null; }}>remove</button>
        </td>
      </tr>
      ${t ? html`<tr class="sub"><td colspan="6"><div class="result ${t.running ? 'muted' : tw.ok ? 'ok' : 'bad'}" data-test-result=${t.running ? 'running' : tw.ok ? 'ok' : 'fail'}>
        ${t.running ? nothing : html`<bx-icon name=${tw.ok ? 'ok' : 'error'}></bx-icon>`}<span>${tw.text}</span></div></td></tr>` : nothing}
      ${this._rm === u.name ? html`<tr class="sub"><td colspan="6"><div class="line">Remove ${u.name}? Its key goes with it; the usage already counted under its name stays.
        <button class="small primary" data-confirm-remove ?disabled=${busy} @click=${() => this._remove(u.name)}>Remove</button>
        <button class="small quiet" @click=${() => { this._rm = null; }}>Cancel</button></div>${errTpl(this._err)}</td></tr>` : nothing}
      ${this._edit === u.name ? html`<tr class="sub"><td colspan="6">${this._editor(u)}</td></tr>` : nothing}
      ${this._reason === u.name ? html`<tr class="sub"><td colspan="6">${this._reasonTpl(u, lock)}</td></tr>` : nothing}`;
  }

  render() {
    const ups = this.cfg?.upstreams ?? [], all = fullyLocked(this.cfg);
    return html`
      ${ups.length ? html`<table>
        <thead><tr><th>provider</th><th>base URL</th><th>key</th><th>models</th><th></th><th></th></tr></thead>
        <tbody>${ups.map((u) => this._row(u))}</tbody>
      </table>` : html`<div class="empty" data-empty>No provider yet. Add one — its base URL and key — and the connector is on.</div>`}
      ${this._edit === NEW ? this._editor(null) : html`<div class="line">
        <button class=${ups.length ? '' : 'primary'} data-add ?disabled=${all} title=${all ? 'set by your host — providers can’t be added here' : ''}
          @click=${() => this._open(NEW)}>Add a provider</button>
        ${all ? lockTpl({ locked: true }) : nothing}</div>`}`;
  }

  testApi() {
    const s = this;
    return {
      get editing() { return s._edit; },
      get reasoning() { return s._reason; },
      get describing() { return s._desc?.model ?? null; },
      openReasoning: (name) => s._openReason(name),
      get error() { return s._err; },
      test: (name) => s._tests[name] ?? null,
    };
  }
}
customElements.define('bx-llm-upstreams', BxLlmUpstreams);
