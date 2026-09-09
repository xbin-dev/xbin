/**
 * <bx-admin-vault> — the admin console's vault tab: the encryption barrier
 * (unseal / set passphrase / rekey / seal) and every component's secret key
 * names (values are never readable here — set/rotate/delete only). A tab
 * element of tiles/admin (see admin.js); data arrives as properties, writes
 * report through bx-admin-err / bx-admin-refresh.
 */
import { LitElement, html, nothing } from 'lit';
import { xbinApi as api, jbody } from '/vendor/bx-kit.js';
import { base } from '../admin-css.js';

export class BxAdminVault extends LitElement {
  static properties = {
    vaults: { attribute: false },      // [{component, keys}] (null while sealed)
    vaultStatus: { attribute: false }, // {initialized, sealed, mode, insecure}
    components: { attribute: false },  // the roster (for the component datalist)
    _secretEdit: { state: true },      // {comp, key} being re-set inline
    _err: { state: true }, // the last refusal (reported to the router's slot)
  };
  static styles = [base];

  _emit(type, detail) { this.dispatchEvent(new CustomEvent(type, { detail, bubbles: true, composed: true })); }
  // fail(e) reports a refusal to the router's global slot; ok() clears it.
  _fail(e) { this._err = String(e?.message ?? e); this._emit('bx-admin-err', this._err); }
  _ok() { this._err = ''; this._emit('bx-admin-err', ''); }

  // The admin console never reads secret values back — they're private to the
  // owning element (the vault lockdown). It can only list keys and set/rotate.
  async _setSecret(comp, key, value) {
    await api(`/vault/${comp}/${encodeURIComponent(key)}`,
      { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ value }) });
    this._emit('bx-admin-refresh');
  }
  async _delSecret(comp, key) {
    if (!confirm(`Delete secret ${comp} / ${key}?`)) return;
    await api(`/vault/${comp}/${encodeURIComponent(key)}`, { method: 'DELETE' });
    this._emit('bx-admin-refresh');
  }

  // ---- barrier (seal state / unseal / passphrase) ----
  async _unseal(pass) {
    try {
      await api('/vault-unseal', { method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ passphrase: pass }) });
      this._ok();
    } catch (e) { this._fail(e); }
    this._emit('bx-admin-refresh');
  }
  async _sealVault() {
    if (!confirm('Seal the vault? Encrypted resources unmount and stateful components stop until an admin unseals again.')) return;
    try { await api('/vault-seal', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{}' }); this._ok(); }
    catch (e) { this._fail(e); }
    this._emit('bx-admin-refresh');
  }
  async _rekeyVault(current, nw) {
    try {
      await api('/vault-rekey', { method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ current, new: nw }) });
      this._ok();
      alert('Passphrase changed (data key unchanged — nothing re-encrypted).');
    } catch (e) { this._fail(e); }
    this._emit('bx-admin-refresh');
  }

  _barrierView() {
    const st = this.vaultStatus;
    if (!st) return nothing;
    const badge = {
      unsealed:     ['unsealed — encryption at rest active', 'var(--bx-green, #4caf50)'],
      sealed:       ['sealed — encrypted and locked', 'var(--bx-amber, #f2a71b)'],
      unconfigured: ['unconfigured — no passphrase set, secret storage refused', 'var(--bx-red, #ef5350)'],
      plaintext:    ['plaintext — NO encryption at rest (dev mode)', 'var(--bx-red, #ef5350)'],
    }[st.mode] ?? [st.mode, 'var(--bx-muted, #868f9a)'];
    const firstTime = st.mode === 'unconfigured' || st.mode === 'plaintext';
    return html`
      <h4>encryption barrier</h4>
      <p style="margin:0 0 8px"><span class="dot" style="background:${badge[1]}"></span>${badge[0]}</p>

      ${st.mode === 'sealed' ? html`
        <form class="inline" @submit=${(e) => { e.preventDefault(); const f = e.target;
            if (f.pass.value) this._unseal(f.pass.value); f.reset(); }}>
          <input name="pass" type="password" placeholder="vault passphrase" size="24"
            autocomplete="off" required>
          <button class="act go">unseal</button>
        </form>
        <p class="muted" style="font-size:11px;margin-top:6px">Encrypted resources and secrets
          come back once unsealed. Also works from a terminal: <span class="mono">bx vault unseal</span>.</p>` : nothing}

      ${firstTime ? html`
        <form class="inline" @submit=${(e) => { e.preventDefault(); const f = e.target;
            if (f.pass.value !== f.confirm.value) { this._fail('passphrases do not match'); return; }
            this._unseal(f.pass.value); f.reset(); }}>
          <input name="pass" type="password" placeholder="new vault passphrase" size="20"
            autocomplete="new-password" required>
          <input name="confirm" type="password" placeholder="repeat" size="12"
            autocomplete="new-password" required>
          <button class="act go">${st.mode === 'plaintext' ? 'encrypt now' : 'set passphrase & unseal'}</button>
        </form>
        <p class="muted" style="font-size:11px;margin-top:6px">Creates the barrier and encrypts
          existing secrets. <b>The passphrase cannot be recovered</b> — losing it loses the data.
          To have xbind unseal itself on boot, put <span class="mono">XBIN_VAULT_PASSPHRASE</span>
          in <span class="mono">/etc/xbin/xbin.env</span> (mode 600).</p>` : nothing}

      ${st.mode === 'unsealed' ? html`
        <form class="inline" @submit=${(e) => { e.preventDefault(); const f = e.target;
            if (f.nw.value !== f.confirm.value) { this._fail('new passphrases do not match'); return; }
            this._rekeyVault(f.cur.value, f.nw.value); f.reset(); }}>
          <input name="cur" type="password" placeholder="current passphrase" size="17"
            autocomplete="off" required>
          <input name="nw" type="password" placeholder="new passphrase" size="15"
            autocomplete="new-password" required>
          <input name="confirm" type="password" placeholder="repeat" size="10"
            autocomplete="new-password" required>
          <button class="act">change passphrase</button>
          <button class="act rm" type="button" @click=${() => this._sealVault()}>seal now</button>
        </form>
        <p class="muted" style="font-size:11px;margin-top:6px">Changing the passphrase re-wraps the
          data key — nothing is re-encrypted. If auto-unseal is configured, update
          <span class="mono">/etc/xbin/xbin.env</span> to match.</p>` : nothing}`;
  }

  render() {
    const sealedOff = this.vaults == null && !!this.vaultStatus?.sealed;
    const vs = this.vaults ?? [];
    return html`
      ${this._barrierView()}
      ${sealedOff ? html`<h4>secrets</h4><span class="muted">unavailable while sealed — unseal above to browse and edit.</span>` : nothing}
      ${!sealedOff && vs.length === 0 ? html`<h4>secrets</h4><span class="muted">no vaults hold secrets yet — set one with
        <span class="mono">bx vault set &lt;component&gt; &lt;key&gt;</span> or below.</span>` : nothing}
      ${vs.length && !sealedOff ? html`<p class="muted" style="font-size:11px">
        Secret <b>values are private to the element that owns them</b> — the admin
        console can list and set/rotate secrets but can't read them back.</p>` : nothing}
      ${vs.map((v) => html`
        <h4>${v.component}</h4>
        <table>
          ${v.keys.map((k) => html`<tr>
              <td class="mono" style="width:30%">${k}</td>
              <td class="secret">${this._secretEdit?.comp === v.component && this._secretEdit?.key === k ? html`
                <form style="display:inline-flex; gap:4px" @submit=${(e) => { e.preventDefault();
                    const nv = e.target.nv.value; this._secretEdit = null;
                    if (nv) this._setSecret(v.component, k, nv); }}>
                  <input name="nv" type="password" size="16" placeholder="new value (can't read the old one)" autofocus>
                  <button class="act" type="submit">save</button>
                  <button class="act" type="button" @click=${() => { this._secretEdit = null; }}>cancel</button>
                </form>` : '••••••••'}</td>
              <td style="text-align:right; white-space:nowrap">
                ${this._secretEdit?.comp === v.component && this._secretEdit?.key === k ? nothing
                  : html`<button class="act" @click=${() => { this._secretEdit = { comp: v.component, key: k }; }}>set</button>`}
                <button class="act rm" @click=${() => this._delSecret(v.component, k)}>del</button>
              </td></tr>`)}
        </table>`)}
      ${sealedOff ? nothing : html`<form class="inline" @submit=${(e) => { e.preventDefault();
          const f = e.target;
          if (f.comp.value && f.key.value) this._setSecret(f.comp.value.trim(), f.key.value.trim(), f.val.value);
          f.reset(); }}>
        <input name="comp" placeholder="component" size="16" list="admin-comps">
        <input name="key" placeholder="key" size="12">
        <input name="val" placeholder="value" size="18" type="password">
        <button class="act go">set secret</button>
      </form>`}
      <datalist id="admin-comps">
        ${(this.components ?? []).map((k) => html`<option value=${k.path}></option>`)}
      </datalist>`;
  }
}

customElements.define('bx-admin-vault', BxAdminVault);
