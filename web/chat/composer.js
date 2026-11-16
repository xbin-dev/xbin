/**
 * chat/composer.js — <bx-chat-composer>, the chat kit's message box
 * (/vendor/chat/composer.js): the text, the files that go with it, the
 * slash-command menu, the pickers over it (the agent, its mode, its model
 * and other settings), the messages held while a turn runs — each with a ×
 * that takes it back — and Send / Stop.
 *
 * It knows no host: what it shows comes in as properties, what the person
 * does goes out as events, and the host does the sending.
 *
 *   properties
 *     draft        the text (the host sets it back from 'bx-chat-draft', and
 *                  may change it: a command picked, a message taken back)
 *     placeholder  the box's words; sendLabel ('Send'), sendTitle, stopTitle
 *     label        what a screen reader calls the box (default: the placeholder; revision 5)
 *     disabled     nothing can be typed or sent (the session is over)
 *     sending      Send waits (a message is on its way, the agent is starting)
 *     busy         a turn runs: Stop shows; Ctrl/⌘+Enter sends `interrupt`
 *     commands     the agent's slash commands [{name, description, hint}]
 *     queue        the messages that wait for the turn's end [{id, text, files}]
 *     queueHint    the words after them
 *     attach       files may go with a message (false: no button, no paste)
 *     limits       {files, bytes, total}: at most that many files, each and
 *                  together at most that many bytes (the session API's: 10
 *                  files, 10 MiB each, 20 MiB together)
 *     pickers      [{id, label, title, value, options: [{value, name, title,
 *                  disabled}], note}] — a <select> each
 *     unavailable  [{name, reason}] what a picker cannot offer here, and why
 *     fit          the element whose height caps the box (40 % of it;
 *                  default: this element's offset parent)
 *
 *   events (bubbling, composed)
 *     bx-chat-draft      {text}
 *     bx-chat-send       {text, files: [{name, mime, size, data}], interrupt,
 *                        sent()} — data is standard base64; the host calls
 *                        sent() once the message went: the staged files go
 *     bx-chat-stop       {}
 *     bx-chat-take-back  {id}   ('' : all of them)
 *     bx-chat-pick       {id, value}
 *
 * Light DOM: it wears the stylesheet of the root it sits in (css.js), so the
 * Agent tab's shadow root and a tile's document style it alike.
 */
import { LitElement, html, nothing } from '/vendor/lit-all.min.js';
import '/vendor/bx-icons.js';
import { slashMenu, commandHint, slashText, stageFiles, fmtSize, ATTACH_LIMITS } from '/vendor/chat/ask.js';

// a File's bytes as standard base64
function base64Of(file) {
  return new Promise((resolve, reject) => {
    const r = new FileReader();
    r.onload = () => { const s = String(r.result || ''); resolve(s.slice(s.indexOf(',') + 1)); };
    r.onerror = () => reject(r.error || new Error('could not read the file'));
    r.readAsDataURL(file);
  });
}

export class BxChatComposer extends LitElement {
  static properties = {
    draft: { type: String }, placeholder: { type: String }, label: { type: String }, sendLabel: { type: String }, sendTitle: { type: String }, stopTitle: { type: String },
    disabled: { type: Boolean }, sending: { type: Boolean }, busy: { type: Boolean }, attach: { type: Boolean },
    commands: { attribute: false }, queue: { attribute: false }, queueHint: { type: String }, limits: { attribute: false },
    pickers: { attribute: false }, unavailable: { attribute: false }, fit: { attribute: false },
    _sel: { state: true }, _off: { state: true }, _files: { state: true }, _note: { state: true }, _drop: { state: true },
  };

  constructor() {
    super();
    this.draft = ''; this.placeholder = ''; this.label = ''; this.sendLabel = 'Send'; this.sendTitle = ''; this.stopTitle = '';
    this.disabled = false; this.sending = false; this.busy = false; this.attach = false;
    this.commands = []; this.queue = []; this.queueHint = ''; this.limits = ATTACH_LIMITS;
    this.pickers = []; this.unavailable = []; this.fit = null;
    this._sel = 0; // the highlighted slash command
    this._off = false; // Escape closed the menu (until the draft changes)
    this._files = []; // staged: {name, mime, size, file}
    this._note = ''; // why a file was left out
    this._drop = false;
  }

  createRenderRoot() { return this; } // light DOM: the stylesheet of the root it sits in

  _emit(name, detail = {}) { return this.dispatchEvent(new CustomEvent(name, { detail, bubbles: true, composed: true })); }

  get _ta() { return this.querySelector('.bxc-compose textarea'); }

  /** focus(): the box. */
  focus() { this._ta?.focus(); }

  // ---- the slash menu ----

  /** slashItems(): the menu's commands for the draft ([] closed). */
  slashItems() {
    if (this._off || this.disabled) return [];
    return slashMenu(this.commands, this.draft) || [];
  }

  /** pickSlash(command): its text into the box. */
  pickSlash(c) {
    this._sel = 0;
    this._setDraft(slashText(c));
    this.focus();
  }

  _setDraft(text) {
    this.draft = text;
    this._emit('bx-chat-draft', { text });
  }

  _input(e) {
    this._off = false;
    this._sel = 0;
    this._setDraft(e.target.value);
    this._fit();
  }

  _key(ev) {
    const items = this.slashItems();
    if (items.length && !ev.isComposing) {
      const n = items.length, sel = Math.min(this._sel, n - 1);
      if (ev.key === 'ArrowDown' || ev.key === 'ArrowUp') { ev.preventDefault(); this._sel = (sel + (ev.key === 'ArrowDown' ? 1 : n - 1)) % n; return; }
      if (ev.key === 'Tab' || (ev.key === 'Enter' && !ev.shiftKey)) { ev.preventDefault(); this.pickSlash(items[sel]); return; }
      if (ev.key === 'Escape') { ev.preventDefault(); this._off = true; return; }
    }
    if (ev.key === 'Enter' && !ev.shiftKey && !ev.isComposing) { ev.preventDefault(); this.send(ev.ctrlKey || ev.metaKey); } // isComposing: don't send on an IME confirm
  }

  // ---- files ----

  /** files: the staged files' {name, mime, size}. */
  get files() { return this._files.map((f) => ({ name: f.name, mime: f.mime, size: f.size })); }

  /** addFiles(list): stage File objects (the picker's, a paste's, a drop's) for the next message; what is past a limit is said. */
  addFiles(list) {
    if (!this.attach || this.disabled) return;
    const incoming = [...(list || [])].map((file) => ({ name: file.name || 'file', mime: file.type || '', size: file.size || 0, file }));
    if (!incoming.length) return;
    const { files, refused } = stageFiles(this._files, incoming, this.limits || ATTACH_LIMITS);
    this._files = files;
    this._note = refused.join(' · ');
  }

  /** removeFile(i): take a staged file out. */
  removeFile(i) { this._files = this._files.filter((_, k) => k !== i); this._note = ''; }

  _paste(ev) {
    const fs = [...(ev.clipboardData?.files || [])];
    if (!fs.length || !this.attach) return; // text pastes as text
    ev.preventDefault();
    this.addFiles(fs);
  }

  _dropped(ev) {
    this._drop = false;
    if (!this.attach || !ev.dataTransfer?.files?.length) return;
    ev.preventDefault();
    this.addFiles(ev.dataTransfer.files);
  }

  // ---- sending ----

  /**
   * send(interrupt): the message goes to the host ('bx-chat-send') — the
   * text trimmed, the staged files read. Nothing is cleared here: the host
   * empties `draft` and calls detail.sent() once it went, so a message that
   * was refused stays to be sent again.
   */
  async send(interrupt = false) {
    if (this.disabled || this.sending || this._reading) return;
    const text = String(this.draft || '').trim();
    const staged = this._files;
    let files = [];
    if (staged.length) {
      this._reading = true;
      try { files = await Promise.all(staged.map(async (f) => ({ name: f.name, mime: f.mime, size: f.size, data: await base64Of(f.file) }))); } catch (e) { this._note = String(e.message || e); return; } finally { this._reading = false; }
    }
    this._emit('bx-chat-send', { text, files, interrupt: !!interrupt, sent: () => { if (this._files === staged) { this._files = []; this._note = ''; } } });
  }

  // the box is as tall as its text — its border counted, so a paste never
  // leaves a sliver to scroll — up to 40 % of the element it fits in, and
  // shrinks again when the text goes
  _fit() {
    const ta = this._ta;
    if (!ta) return;
    ta.style.height = 'auto';
    const border = ta.offsetHeight - ta.clientHeight; // border-box: the border (and a scrollbar) the content height leaves out
    const host = (this.fit || this.offsetParent || this).getBoundingClientRect().height || window.innerHeight;
    const cap = Math.max(48, host * 0.4);
    const want = ta.scrollHeight + border;
    ta.style.height = Math.min(want, cap) + 'px';
    ta.style.overflowY = want > cap ? 'auto' : 'hidden';
  }

  updated(ch) {
    if (ch.has('draft')) {
      const ta = this._ta;
      if (ta && ta.value !== this.draft) ta.value = this.draft; // the host changed it
      this._fit(); // a send, a pick, a take-back: its height follows
    }
  }

  // ---- render ----

  render() {
    return html`${this._pickers()}${this._slash()}${this._queued()}${this._staged()}
      <div class="bxc-compose ${this._drop ? 'bxc-drop' : ''}" @dragover=${(e) => { if (this.attach && e.dataTransfer?.types?.includes('Files')) { e.preventDefault(); this._drop = true; } }}
          @dragleave=${() => { this._drop = false; }} @drop=${this._dropped}>
        ${this.attach ? html`<button class="bxc-attach" type="button" ?disabled=${this.disabled} title="attach files to the message (or paste, or drop them on the box)" aria-label="attach files"
            @click=${() => this.querySelector('.bxc-compose input[type=file]')?.click()}><bx-icon name="paperclip"></bx-icon></button>
          <input type="file" multiple @change=${(e) => { this.addFiles(e.target.files); e.target.value = ''; }}>` : nothing}
        <textarea rows="1" ?disabled=${this.disabled} placeholder=${this.placeholder} aria-label=${this.label || this.placeholder || 'message'} .value=${this.draft} @input=${this._input} @keydown=${this._key} @paste=${this._paste}></textarea>
        ${this.busy ? html`<button class="bxc-stop" type="button" @click=${() => this._emit('bx-chat-stop')} title=${this.stopTitle || 'stop the running turn'}>Stop</button>` : nothing}
        <button class="bxc-send" type="button" @click=${() => this.send()} ?disabled=${this.disabled || this.sending} title=${this.sendTitle || 'send (Enter)'}>${this.sendLabel || 'Send'}</button>
      </div>`;
  }

  _pickers() {
    const ps = (this.pickers || []).filter(Boolean), un = (this.unavailable || []).filter((u) => u && u.name);
    if (!ps.length && !un.length) return nothing;
    return html`<div class="bxc-pickers">
      ${ps.map((p) => (p.note != null && !(p.options || []).length ? html`<span class="bxc-pnote">${p.note}</span>`
        : html`<label title=${p.title || p.label || ''} data-picker=${p.id}>${p.label ? html`<span class="bxc-lbl">${p.label}</span>` : nothing}
          <select ?disabled=${this.disabled || p.disabled} title=${p.label ? nothing : p.title || p.id} @change=${(e) => this._emit('bx-chat-pick', { id: p.id, value: e.target.value })}>
            ${(p.options || []).map((o) => html`<option value=${o.value} ?selected=${o.value === p.value} ?disabled=${!!o.disabled} title=${o.title || ''}>${o.name ?? o.value}</option>`)}
          </select></label>`))}
      ${un.length ? html`<ul class="bxc-unavailable">${un.map((u) => html`<li data-unavailable=${u.id || u.name}><b>${u.name}</b> isn’t offered here: ${u.reason}</li>`)}</ul>` : nothing}
    </div>`;
  }

  // the slash-command menu over the box, or the input hint of the command just completed
  _slash() {
    const items = this.slashItems();
    if (items.length) {
      const sel = Math.min(this._sel, items.length - 1);
      return html`<div class="bxc-slash" role="listbox">${items.map((c, i) => html`<div class="bxc-sc ${i === sel ? 'bxc-on' : ''}" role="option" aria-selected=${i === sel}
        @mousedown=${(e) => { e.preventDefault(); this.pickSlash(c); }}><b>/${c.name}</b><span class="bxc-d">${c.description || ''}</span>${c.hint ? html`<span class="bxc-hint">${c.hint}</span>` : nothing}</div>`)}</div>`;
    }
    const h = this.disabled ? null : commandHint(this.commands, this.draft);
    return h ? html`<div class="bxc-slash-hint">/${h.name} — ${h.hint}</div>` : nothing;
  }

  // the messages held for the turn's end: × takes one back into the box
  _queued() {
    const q = this.queue || [];
    if (!q.length || this.disabled) return nothing;
    return html`<div class="bxc-queued" role="list" aria-label="messages waiting for the turn to end">
      ${q.map((m) => html`<span class="bxc-qchip" role="listitem" title=${m.text || ''}>
        <span class="bxc-qt">${m.text || (m.files || []).join(', ')}</span>
        ${m.files?.length ? html`<span class="bxc-qf">+${m.files.length} ${m.files.length === 1 ? 'file' : 'files'}</span>` : nothing}
        <button class="bxc-qx" type="button" title="take it back into the message box" aria-label="take it back" @click=${() => this._emit('bx-chat-take-back', { id: m.id })}><bx-icon name="xmark"></bx-icon></button>
      </span>`)}
      <span class="bxc-qhint">${this.queueHint || `${q.length === 1 ? 'goes in' : 'go in together'} when the turn ends`}</span>
    </div>`;
  }

  // the files that go with the next message
  _staged() {
    if (!this._files.length && !this._note) return nothing;
    return html`<div class="bxc-staged" role="list" aria-label="files that go with the message">
      ${this._files.map((f, i) => html`<span class="bxc-qchip" role="listitem" title=${`${f.mime || 'file'} · ${fmtSize(f.size)}`}>
        <span class="bxc-qt">${f.name}</span><span class="bxc-qf">${fmtSize(f.size)}</span>
        <button class="bxc-qx" type="button" title="leave this file out" aria-label="leave it out" @click=${() => this.removeFile(i)}><bx-icon name="xmark"></bx-icon></button>
      </span>`)}
      ${this._note ? html`<span class="bxc-qhint bxc-err">${this._note}</span>` : nothing}
    </div>`;
  }
}
if (!customElements.get('bx-chat-composer')) customElements.define('bx-chat-composer', BxChatComposer);
