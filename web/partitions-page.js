/**
 * <bx-partitions-page> — the partitions page, /xbin/partitions
 * (docs/partitions.md §Your partitions page; PD-47). xbind's own page,
 * served as it is (no HTML transform) and only top-level (it refuses to
 * be framed, and this element renders nothing of the person's in a
 * frame): what a person sees and decides about
 * partitioned tiles, with their own session — their partitions (state,
 * usage, mail counts, stop, reset, log share, restore), each tile's trust
 * panel and ledger, consents (with the workspace policy on), personal
 * binds, credentials an admin made for them, notices, and — for tile
 * managers — partition mode switch decisions (keep, or switch after a dry
 * run and the tile's path typed). xbind judges every act again.
 *
 * Words and data: web/partitions-kit.js (pure; hack/partitions-page.test.mjs).
 * Sections: web/partitions-sections.js, web/partitions-more.js.
 */
import { LitElement, html } from '/vendor/lit-all.min.js';
import { onEvent, onReconnect } from '/vendor/events-socket.js';
import {
  loadPage, call, errorText, modeBody, switchedText, resetConfirm, credentialDone, plural,
} from '/vendor/partitions-kit.js';
import { pageCss } from '/vendor/partitions-css.js';
import { headerSection, credentialsSection, decisionsSection, partitionsSection } from '/vendor/partitions-sections.js';
import { consentsSection, bindsSection, noticesSection, ledgerSection } from '/vendor/partitions-more.js';

const q = encodeURIComponent;
const F = (u, i) => fetch(u, i); // the person's own session (same-origin cookie)

export class BxPartitionsPage extends LitElement {
  static properties = {
    _m: { state: true },
    _ui: { state: true },
    _loadErr: { state: true },
    _decided: { state: true },
  };

  static styles = pageCss;

  constructor() {
    super();
    this._m = null;
    this._ui = {};
    this._loadErr = '';
    this._decided = []; // this visit's switches: their tiles leave the decisions list
    this._framed = false;
    try { this._framed = window.top !== window.self; } catch { this._framed = true; }
    this._timer = 0;
    this._offs = [];
  }

  connectedCallback() {
    super.connectedCallback();
    if (this._framed) return; // a person's partitions are shown top-level only
    this.reload();
    // what changes a person's view: their partitions' events (state, notices,
    // consent prompts, mode changes of tiles they read) and the policies
    this._offs.push(onEvent((e) => { if (e?.type === 'partitions' || e?.type === 'policies') this.soon(); }));
    this._offs.push(onReconnect(() => this.soon()));
  }

  disconnectedCallback() {
    super.disconnectedCallback();
    for (const off of this._offs.splice(0)) off();
    clearTimeout(this._timer);
  }

  // ---- state ----

  soon() {
    clearTimeout(this._timer);
    this._timer = setTimeout(() => this.reload(), 300);
  }

  async reload() {
    try {
      this._m = await loadPage(F);
      this._loadErr = '';
    } catch (e) {
      this._loadErr = String(e?.message || e);
    }
  }

  ui(key) { return this._ui[key] ?? {}; }

  set(key, patch) { this._ui = { ...this._ui, [key]: { ...this.ui(key), ...patch } }; }

  clear(key) {
    const next = { ...this._ui };
    delete next[key];
    this._ui = next;
  }

  me() { return this._m?.me?.id ?? ''; }

  // decided: the switches made on this page since it opened, newest first
  decided() { return this._decided; }

  // readOnly: view-as — the page shows, and offers no act
  readOnly() { return !!this._m?.me?.readOnly; }

  // act(key, fn): runs one act, busy meanwhile; fn answers {done} or {err}
  // (or a patch); the page reloads after it.
  async act(key, busy, fn) {
    if (this.ui(key).busy) return;
    this.set(key, { busy, err: '', done: '' });
    let out;
    try {
      out = await fn();
    } catch (e) {
      out = { err: String(e?.message || e) };
    }
    this.set(key, { busy: '', ...out });
    this.soon();
  }

  // ---- switch decisions (tile managers) ----

  keep(t) {
    return this.act(`mode:${t.tile}`, 'keep', async () => {
      const res = await call(F, 'POST', '/partitions/mode', modeBody(t.tile, 'keep', t.from, t.to));
      return res.ok ? { done: `Kept ${t.tile} as it is: it runs again, and nothing was deleted.`, open: false } : { err: errorText(res) };
    });
  }

  switchCount(t) {
    return this.act(`mode:${t.tile}`, 'count', async () => {
      const res = await call(F, 'POST', '/partitions/mode', modeBody(t.tile, 'switch', t.from, t.to, { dryRun: true }));
      return res.ok ? { open: true, dry: res.body, typed: '', yes: false } : { err: errorText(res) };
    });
  }

  switchGo(t) {
    const st = this.ui(`mode:${t.tile}`);
    const typed = String(st.typed ?? '').trim();
    if (typed !== t.tile) { this.set(`mode:${t.tile}`, { err: `Type the tile's path exactly (${t.tile}) to switch.` }); return undefined; }
    const managers = st.dry?.managers ?? [];
    if (managers.length && !st.yes) { this.set(`mode:${t.tile}`, { err: 'Tick "switch anyway" to switch with those sandbox managers bound.' }); return undefined; }
    return this.act(`mode:${t.tile}`, 'switch', async () => {
      const extra = managers.length ? { confirm: typed, yes: true } : { confirm: typed };
      const res = await call(F, 'POST', '/partitions/mode', modeBody(t.tile, 'switch', t.from, t.to, extra));
      if (res.ok) {
        this._decided = [{ tile: t.tile, text: switchedText(t.tile, t.to, res.body) }, ...this._decided.filter((d) => d.tile !== t.tile)];
        return { open: false, done: '' };
      }
      if (res.status === 409 && Array.isArray(res.body?.managers)) return { dry: { ...st.dry, managers: res.body.managers }, err: errorText(res) };
      return { err: errorText(res), open: res.status !== 409 };
    });
  }

  // ---- the person's own partition ----

  stop(t) {
    return this.act(`part:${t.tile}`, 'stop', async () => {
      const res = await call(F, 'POST', '/partitions/stop', { tile: t.tile, partition: `user:${this.me()}` });
      return res.ok ? { done: 'Stopped: your data stays, and the next request starts it again.' } : { err: errorText(res) };
    });
  }

  reset(t) {
    const key = `reset:${t.tile}`, want = resetConfirm(t.tile, this.me());
    const typed = String(this.ui(key).typed ?? '').trim();
    if (typed !== want) { this.set(key, { err: `Type ${want} exactly to reset.` }); return undefined; }
    return this.act(key, 'reset', async () => {
      const res = await call(F, 'POST', '/partitions/reset', { tile: t.tile, partition: `user:${this.me()}`, confirm: typed });
      if (!res.ok) return { err: errorText(res) };
      const d = res.body?.deleted ?? {};
      return { open: false, typed: '', done: `Reset: your data in ${t.tile} was deleted (${plural(d.namespaces || 0, 'data namespace')}, ${plural(d.subkeys || 0, 'backup key')} erased).` };
    });
  }

  shareLog(t) {
    const key = `share:${t.tile}`;
    const days = Math.min(14, Math.max(1, Number(this.ui(key).days) || 7));
    return this.act(key, 'share', async () => {
      const res = await call(F, 'POST', '/partitions/share-log', { tile: t.tile, days });
      return res.ok ? { done: `Shared for ${plural(days, 'day')}: the tile's managers and admins can read your partition's log.` } : { err: errorText(res) };
    });
  }

  unshareLog(t) {
    return this.act(`share:${t.tile}`, 'unshare', async () => {
      const res = await call(F, 'DELETE', '/partitions/share-log', { tile: t.tile });
      return res.ok ? { done: 'No longer shared.' } : { err: errorText(res) };
    });
  }

  backups(t) {
    return this.act(`restore:${t.tile}`, 'list', async () => {
      const res = await call(F, 'GET', `/partitions/backups?tile=${q(t.tile)}`);
      if (!res.ok) return { err: errorText(res) };
      const versions = Array.isArray(res.body.versions) ? res.body.versions : [];
      return { open: true, archiver: res.body.archiver || '', versions, version: versions[0]?.version ?? '', dry: null, typed: '' };
    });
  }

  restoreCheck(t) {
    const key = `restore:${t.tile}`;
    return this.act(key, 'check', async () => {
      const v = this.ui(key).version;
      const res = await call(F, 'POST', '/partitions/restore', { tile: t.tile, ...(v ? { version: v } : {}), dryRun: true });
      return res.ok ? { dry: res.body } : { err: errorText(res) };
    });
  }

  restore(t) {
    const key = `restore:${t.tile}`, want = resetConfirm(t.tile, this.me());
    const st = this.ui(key), typed = String(st.typed ?? '').trim();
    if (typed !== want) { this.set(key, { err: `Type ${want} exactly to restore.` }); return undefined; }
    return this.act(key, 'restore', async () => {
      const res = await call(F, 'POST', '/partitions/restore', { tile: t.tile, ...(st.version ? { version: st.version } : {}), confirm: typed });
      return res.ok ? { open: false, done: `Restored your partition of ${t.tile} from ${res.body.version || 'the latest version'}.` } : { err: errorText(res) };
    });
  }

  // ---- credentials, consents, personal binds ----

  credential(h, allow) {
    return this.act(`cred:${h.id}`, allow ? 'allow' : 'refuse', async () => {
      const res = await call(F, 'POST', '/partitions/credential-confirm', { id: h.id, allow });
      return res.ok || res.status === 409 ? { done: credentialDone(res.body), sure: false } : { err: errorText(res) };
    });
  }

  consent(from, to, allow, key = `consent:${from}→${to}`) {
    if (!from || !to) { this.set(key, { err: 'Pick both tiles.' }); return undefined; }
    return this.act(key, allow ? 'allow' : 'revoke', async () => {
      const res = await call(F, allow ? 'POST' : 'DELETE', '/partitions/consents', { from, to });
      if (res.ok && !allow) this.clear('consent:new'); // its "Allowed" line is history now
      return res.ok ? { done: allow ? `Allowed: ${from} may use your data in ${to}.` : `Taken back: ${from} no longer uses your data in ${to}.` } : { err: errorText(res) };
    });
  }

  bindAdd() {
    const key = 'bind:new', st = this.ui(key);
    const body = { requester: st.requester ?? '', slot: String(st.slot ?? '').trim(), provider: st.provider ?? '' };
    if (!body.requester || !body.slot || !body.provider) { this.set(key, { err: 'Pick the tile, name its slot and pick your tile.' }); return undefined; }
    return this.act(key, 'add', async () => {
      const res = await call(F, 'POST', '/partitions/binds', body);
      return res.ok ? { done: `Bound ${body.provider} into your partition of ${body.requester} (slot ${body.slot}).`, slot: '' } : { err: errorText(res) };
    });
  }

  bindRemove(b) {
    return this.act(`bind:${b.id}`, 'remove', async () => {
      const res = await call(F, 'DELETE', '/partitions/binds', { id: b.id });
      if (res.ok) this.clear('bind:new');
      return res.ok ? { done: 'Removed.' } : { err: errorText(res) };
    });
  }

  // The harness drives the page through this (hack/ui-harness: no private members).
  testApi() {
    return { model: () => this._m, ui: (k) => this.ui(k), reload: () => this.reload(), framed: () => this._framed };
  }

  render() {
    if (this._framed) {
      return html`<main><div class="banner err" role="alert">This page shows your partitions only when it is opened on its own, never inside another page.
        Open <code>/xbin/partitions</code> in a tab of its own.</div></main>`;
    }
    const m = this._m;
    if (!m) return html`<main>${this._loadErr ? html`<div class="banner err">${this._loadErr}</div>` : html`<p class="muted">Loading…</p>`}</main>`;
    return html`<main>
      ${headerSection(this, m)}
      ${credentialsSection(this, m)}
      ${decisionsSection(this, m)}
      ${partitionsSection(this, m)}
      ${consentsSection(this, m)}
      ${bindsSection(this, m)}
      ${noticesSection(this, m)}
      ${ledgerSection(this, m)}
    </main>`;
  }
}

customElements.define('bx-partitions-page', BxPartitionsPage);
