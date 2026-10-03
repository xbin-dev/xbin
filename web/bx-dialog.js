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
 *     error?,              // plain text, shown as an alert box (e.g. why a
 *                          // submit failed — re-open the dialog with it set)
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
import { scrollCss } from '/vendor/scroll-css.js';
import '/vendor/bx-icons.js';

export class BxDialog extends LitElement {
  static properties = {
    spec: { attribute: false },
    open: { type: Boolean, reflect: true },
    from: { type: String }, // originating component (verified, shell-set) — shown
                            // so a tile can't pass its modal off as system chrome
  };

  // product-ui §6 (D184): square, 1px border-strong, the title type, the
  // pop-over shadow over the scrim; controls 28px with the focus ring; the
  // primary button in the accent with the accent ink, a destructive one the
  // danger outline. Show the plan, then ask: the spec's message is the plan.
  static styles = [scrollCss, css`
    :host { position: fixed; inset: 0; z-index: 4000; display: none; }
    :host([open]) { display: block; }
    .backdrop { position: absolute; inset: 0; background: var(--bx-scrim, rgba(0, 0, 0, 0.55)); }
    .box {
      position: absolute; left: 50%; top: 42%; transform: translate(-50%, -50%);
      width: min(440px, 92vw); max-height: 82vh; overflow: auto; box-sizing: border-box;
      background: var(--bx-panel, #1F2028); color: var(--bx-text, #E9EAF0);
      border: 1px solid var(--bx-border-strong, #666A7E); border-radius: var(--bx-radius, 2px);
      box-shadow: var(--bx-shadow-pop, 0 12px 32px rgba(0, 0, 0, 0.6));
      padding: 16px;
      font: var(--bx-font, 13px/18px "Instrument Sans", system-ui, sans-serif);
    }
    .attrib { display: flex; align-items: center; gap: 6px; margin: 0 0 8px; color: var(--bx-muted, #A3A6B6);
      font: var(--bx-font-meta, 400 12px/16px "Instrument Sans", system-ui, sans-serif); }
    .attrib .from { font-family: var(--bx-mono, "JetBrains Mono", ui-monospace, monospace); }
    h3 { margin: 0 0 8px; font: var(--bx-font-title, 600 16px/22px "Instrument Sans", system-ui, sans-serif); }
    .msg { white-space: pre-wrap; margin: 0 0 8px; color: var(--bx-text, #E9EAF0); }
    .err { white-space: pre-wrap; margin: 8px 0; padding: 8px 12px; border-radius: var(--bx-radius, 2px);
      color: var(--bx-danger, #FF7A7A); border: 1px solid var(--bx-danger, #FF7A7A); background: var(--bx-danger-bg, #3A2B32); }
    .err bx-icon { margin-right: 6px; }
    label { display: block; margin: 12px 0 4px; color: var(--bx-muted, #A3A6B6);
      font: var(--bx-font-micro, 600 11px/14px "Instrument Sans", system-ui, sans-serif); letter-spacing: var(--bx-tracking-micro, 0.06em); text-transform: uppercase; }
    input, textarea, select {
      width: 100%; box-sizing: border-box; min-height: var(--bx-control-h, 28px); font: inherit; padding: 4px 8px;
      border: 1px solid var(--bx-border-strong, #666A7E); border-radius: var(--bx-radius, 2px);
      background: var(--bx-panel, #1F2028); color: var(--bx-text, #E9EAF0);
    }
    input::placeholder, textarea::placeholder { color: var(--bx-subtle, #8E91A2); opacity: 1; }
    textarea { resize: vertical; }
    :focus-visible { outline: var(--bx-focus-outline, 3px solid #3DD6F5); outline-offset: var(--bx-focus-offset, 2px);
      box-shadow: var(--bx-focus-halo, 0 0 0 2px #0B0C12); }
    label.chk { display: flex; align-items: center; gap: 8px; margin: 12px 0 4px; font: inherit;
      text-transform: none; letter-spacing: 0; color: var(--bx-text, #E9EAF0); }
    label.chk input { width: auto; min-height: 0; accent-color: var(--bx-accent, #8C9BFF); }
    .btns { display: flex; justify-content: flex-end; gap: 8px; margin-top: 16px; }
    button { box-sizing: border-box; min-height: var(--bx-control-h, 28px); font: inherit; font-weight: 600; padding: 4px 11px;
      border-radius: var(--bx-radius, 2px); cursor: pointer;
      border: 1px solid var(--bx-border-strong, #666A7E); background: var(--bx-panel, #1F2028); color: var(--bx-text, #E9EAF0); }
    button:hover { background: var(--bx-hover, #2A2B34); }
    button.primary { background: var(--bx-accent, #8C9BFF); border-color: var(--bx-accent, #8C9BFF); color: var(--bx-accent-ink, #0B0C12); }
    button.primary:hover { background: var(--bx-accent-hover, #A9B4FF); border-color: var(--bx-accent-hover, #A9B4FF); }
    button.danger { color: var(--bx-danger, #FF7A7A); border-color: var(--bx-danger, #FF7A7A); }
  `];

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
        ${this.from ? html`<div class="attrib"><bx-icon name="window"></bx-icon><span class="from">${this.from}</span></div>` : nothing}
        ${s.title ? html`<h3>${s.title}</h3>` : nothing}
        ${s.message ? html`<p class="msg">${s.message}</p>` : nothing}
        ${s.error ? html`<p class="err" role="alert"><bx-icon name="error"></bx-icon>${s.error}</p>` : nothing}
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
