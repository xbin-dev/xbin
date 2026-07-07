/**
 * <bx-dialog> — a modal dialog rendered from a plain data spec (no tile markup
 * runs here, so it's safe for the shell to render on a tile's behalf). Used two
 * ways: the shell renders it at top level when a tile calls `xbin.dialog(spec)`
 * (un-clipped over the whole workspace), and a standalone tile mounts it inside
 * its own frame as a fallback. All spec strings are shown as TEXT (Lit escapes;
 * never innerHTML) — a tile can't inject HTML/JS through it.
 *
 *   spec = {
 *     title?, message?,                                  // plain text
 *     fields?: [{ name, label?, type?, value?, placeholder?, options? }],
 *              // type: text|password|number|textarea|select|checkbox (default text)
 *     buttons?: [{ label, value, primary?, danger? }],   // default Cancel/OK
 *   }
 *
 * Fires `bx-dialog-resolve` (bubbles, composed) once with
 *   { button, values }  — button = the clicked button's value, or null if
 *   dismissed (Escape / backdrop / Cancel). values = { <field name>: value }.
 * The host (shell or tile) removes the element on resolve.
 */
import { LitElement, html, css, nothing } from 'lit';

export class BxDialog extends LitElement {
  static properties = {
    spec: { attribute: false },
    open: { type: Boolean, reflect: true },
    from: { type: String }, // originating component (verified, shell-set) — shown
                            // so a tile can't pass its modal off as system chrome
  };

  static styles = css`
    :host { position: fixed; inset: 0; z-index: 4000; display: none; }
    :host([open]) { display: block; }
    .backdrop { position: absolute; inset: 0; background: rgba(0, 0, 0, .45); }
    .box {
      position: absolute; left: 50%; top: 42%; transform: translate(-50%, -50%);
      width: min(440px, 92vw); max-height: 82vh; overflow: auto;
      background: var(--bx-panel, #fff); color: var(--bx-text, #33414e);
      border: 1px solid var(--bx-border, #e4e8ed); border-radius: 10px;
      box-shadow: 0 0 0 1px rgba(0, 0, 0, .4), 0 14px 44px rgba(0, 0, 0, .5);
      padding: 16px 18px;
      font: var(--bx-font, 13px/1.5 -apple-system, system-ui, sans-serif);
    }
    .attrib { font: 10px var(--bx-mono, monospace); color: var(--bx-muted, #8794a1);
      letter-spacing: .04em; margin: 0 0 6px; }
    h3 { margin: 0 0 8px; font-size: 14px; }
    .msg { white-space: pre-wrap; margin: 0 0 6px; font-size: 12.5px; color: var(--bx-text, #33414e); }
    label { display: block; font-size: 10.5px; font-weight: 600; letter-spacing: .05em;
      text-transform: uppercase; color: var(--bx-muted, #8794a1); margin: 10px 0 2px; }
    input, textarea, select {
      width: 100%; box-sizing: border-box; font: inherit; font-size: 12.5px; padding: 5px 8px;
      border: 1px solid var(--bx-border, #e4e8ed); border-radius: 6px;
      background: var(--bx-panel, #fff); color: var(--bx-text, #33414e);
    }
    textarea { resize: vertical; }
    input:focus, textarea:focus, select:focus {
      outline: 2px solid color-mix(in srgb, var(--bx-accent, #f5a623) 35%, transparent);
    }
    label.chk { display: flex; align-items: center; gap: 7px; font-size: 12.5px;
      text-transform: none; letter-spacing: 0; color: var(--bx-text, #33414e); font-weight: 400; }
    label.chk input { width: auto; accent-color: var(--bx-accent, #f5a623); }
    .btns { display: flex; justify-content: flex-end; gap: 8px; margin-top: 15px; }
    button { font: inherit; font-size: 12.5px; padding: 5px 14px; border-radius: 6px; cursor: pointer;
      border: 1px solid var(--bx-border, #e4e8ed); background: var(--bx-panel, #fff); color: var(--bx-text, #33414e); }
    button:hover { background: var(--bx-panel-2, #f7f8fa); }
    button.primary { background: var(--bx-accent, #f5a623); border-color: transparent; color: #23272e; font-weight: 600; }
    button.danger { color: var(--bx-red, #e5484d); border-color: color-mix(in srgb, var(--bx-red, #e5484d) 45%, transparent); }
  `;

  #onKey = (e) => { if (e.key === 'Escape') { e.stopPropagation(); this.#resolve(null); } };

  connectedCallback() {
    super.connectedCallback();
    document.addEventListener('keydown', this.#onKey, true);
    this.updateComplete.then(() =>
      this.renderRoot.querySelector('input, textarea, select, button.primary')?.focus());
  }
  disconnectedCallback() {
    super.disconnectedCallback();
    document.removeEventListener('keydown', this.#onKey, true);
  }

  #values() {
    const out = {};
    for (const f of this.spec?.fields ?? []) {
      const el = this.renderRoot.querySelector(`[name="${CSS.escape(f.name)}"]`);
      if (!el) continue;
      out[f.name] = el.type === 'checkbox' ? el.checked : el.value;
    }
    return out;
  }

  #resolve(button) {
    this.dispatchEvent(new CustomEvent('bx-dialog-resolve', {
      detail: { button, values: this.#values() }, bubbles: true, composed: true,
    }));
  }

  #field(f) {
    const t = f.type ?? 'text';
    if (t === 'checkbox') {
      return html`<label class="chk"><input type="checkbox" name=${f.name} ?checked=${!!f.value}>${f.label ?? f.name}</label>`;
    }
    return html`
      <label>${f.label ?? f.name}</label>
      ${t === 'textarea'
        ? html`<textarea name=${f.name} rows="4" placeholder=${f.placeholder ?? ''} .value=${f.value ?? ''}></textarea>`
        : t === 'select'
          ? html`<select name=${f.name}>${(f.options ?? []).map((o) => {
              const v = o.value ?? o;
              return html`<option value=${v} ?selected=${f.value === v}>${o.label ?? v}</option>`;
            })}</select>`
          : html`<input type=${t} name=${f.name} placeholder=${f.placeholder ?? ''} .value=${f.value ?? ''}>`}`;
  }

  render() {
    const s = this.spec ?? {};
    const buttons = s.buttons?.length ? s.buttons
      : [{ label: 'Cancel', value: null }, { label: 'OK', value: 'ok', primary: true }];
    const submit = (e) => {
      e.preventDefault();
      const b = buttons.find((x) => x.primary) ?? buttons[buttons.length - 1];
      this.#resolve(b.value);
    };
    return html`
      <div class="backdrop" @click=${() => this.#resolve(null)}></div>
      <div class="box" role="dialog" aria-modal="true">
        ${this.from ? html`<div class="attrib">▟ ${this.from}</div>` : nothing}
        ${s.title ? html`<h3>${s.title}</h3>` : nothing}
        ${s.message ? html`<p class="msg">${s.message}</p>` : nothing}
        <form @submit=${submit}>
          ${(s.fields ?? []).map((f) => this.#field(f))}
          <div class="btns">
            ${buttons.map((b) => html`
              <button type="button" class=${b.primary ? 'primary' : b.danger ? 'danger' : ''}
                      @click=${() => this.#resolve(b.value)}>${b.label}</button>`)}
          </div>
        </form>
      </div>`;
  }
}

customElements.define('bx-dialog', BxDialog);
