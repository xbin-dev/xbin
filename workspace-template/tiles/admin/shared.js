// shared.js — helpers several admin tabs (and the router) render with:
// the tile-target and service datalists, the typed allow rows (permission
// sets and an org's extra entries), and the draft plumbing every editor
// uses. Pure functions over data the caller passes in — no element state.
import { html, nothing } from 'lit';
import { xbinApi as api, jbody } from '/vendor/bx-kit.js';
import { ALLOW_KINDS, ROLE_CAPS, KNOWN_CAPS, allowKind, fmtAllow, allowProblem, describeAllow } from '/vendor/bx-allow.js';

// targetOptions(ov): every real component path (minus chrome), a pattern per
// top dir, and * — the row editors' <datalist> of tile targets.
export function targetOptions(ov) {
  const comps = (ov?.components ?? []).map((c) => c.path)
    .filter((p) => p !== 'root' && p !== 'shell');
  const pats = new Set(['*']);
  for (const p of comps) {
    const s = p.split('/');
    if (s.length > 1) pats.add(`${s[0]}/*`);
  }
  return [...comps.sort(), ...[...pats].sort()];
}
export const targetDatalist = (opts) => html`<datalist id="tile-targets">
    ${(opts ?? []).map((t) => html`<option value=${t}></option>`)}
  </datalist>`;

// serviceOptions(ifaces): the http services tiles provide or request — the
// "bind an interface" row's datalist. ifaces = the /bindings payload.
export function serviceOptions(ifaces) {
  const svcs = new Set();
  for (const c of ifaces?.components ?? []) {
    for (const p of Object.values(c.provides ?? {})) if (p.service) svcs.add(p.service);
    for (const p of Object.values(c.interfaces ?? {})) if (p.service) svcs.add(p.service);
  }
  return [...svcs].sort();
}
export const serviceDatalist = (svcs) => html`<datalist id="iface-services">${(svcs ?? []).map((s) => html`<option value=${s}></option>`)}</datalist>`;

// WithDrafts(Base): the click-through editors' draft plumbing, keyed by a
// context id ("netset:<name>", "permset:new", "user:bob:tiles", …). The
// element declares `_drafts: { state: true }` itself.
export const WithDrafts = (Base) => class extends Base {
  _draft(k) { return this._drafts?.[k]; }
  _setDraft(k, v) { this._drafts = { ...(this._drafts ?? {}), [k]: v }; }
  _dropDraft(k) { const d = { ...(this._drafts ?? {}) }; delete d[k]; this._drafts = d; }
  _toggleDraft(k, seed) { this._draft(k) ? this._dropDraft(k) : this._setDraft(k, seed()); }
  // the harness surface: read/write drafts by key
  draftApi() { const a = this; return { draft: (k) => a._draft(k), setDraft: (k, v) => a._setDraft(k, v), dropDraft: (k) => a._dropDraft(k) }; }
};

// allowRows(rows, onChange, {gotoTab}): [what ▾] [that kind's fields] → the
// entry in words + the exact string, or the problem. Shared by permission
// sets and an org's extra allow entries.
// The typed rows: [what ▾] [that kind's fields] → the entry in words + the
// exact string, or the problem. Shared by permission sets and an org's
// extra allow entries.
export function allowRows(rows, onChange, { gotoTab } = {}) {
  const upd = (i, patch) => onChange(rows.map((r, j) => (j === i ? { ...r, ...patch } : r)));
  const rm = (i) => onChange(rows.filter((_, j) => j !== i));
  const roleSel = (r, i) => {
    const custom = !!r.role && !ROLE_CAPS.includes(r.role);
    return html`<select name="role" title="cap the delegable role — a bare entry delegates ANY role, prefer the cap"
        @change=${(e) => upd(i, { role: e.target.value === '__custom' ? 'custom' : e.target.value })}>
      <option value="" ?selected=${!r.role}>any role</option>
      ${ROLE_CAPS.map((x) => html`<option value=${x} ?selected=${r.role === x}>up to ${x}</option>`)}
      <option value="__custom" ?selected=${custom}>custom role…</option>
    </select>${custom ? html`<input name="rolename" size="10" placeholder="role name" .value=${r.role === 'custom' ? '' : r.role}
      @input=${(e) => upd(i, { role: e.target.value.trim() || 'custom' })}>` : nothing}`;
  };
  const val = (r, i, extra = {}) => html`<input name="value" size=${extra.size ?? 26} list=${extra.list ?? nothing}
    placeholder=${allowKind(r.kind).placeholder} title=${allowKind(r.kind).help} .value=${r.value ?? ''}
    @input=${(e) => upd(i, { value: e.target.value })}>`;
  const fields = (r, i) => {
    switch (r.kind) {
      case 'tile': return html`${val(r, i, { list: 'tile-targets', size: 28 })}${roleSel(r, i)}`;
      case 'res': return html`${val(r, i, { size: 28 })}${roleSel(r, i)}`;
      case 'iface': return html`${val(r, i, { list: 'iface-services', size: 14 })}
        <span class="muted">to</span>
        <input name="provider" list="tile-targets" size="24" placeholder="any provider tile" title="pin the provider tile (path or glob); empty = any provider of this service"
          .value=${r.provider ?? ''} @input=${(e) => upd(i, { provider: e.target.value })}>
        <input name="instance" size="10" placeholder="any instance" title="pin one provider instance (needs a provider tile)"
          .value=${r.instance ?? ''} @input=${(e) => upd(i, { instance: e.target.value })}>`;
      case 'cap': return html`${val(r, i, { list: 'cap-classes', size: 18 })}
        <datalist id="cap-classes">${KNOWN_CAPS.map((c) => html`<option value=${c}></option>`)}</datalist>`;
      case 'net': return html`${val(r, i, { list: 'tile-targets' })}
        <a class="link" style="font-size:11px" @click=${() => gotoTab?.('netsets')}>prefer a network set</a>`;
      default: return val(r, i);
    }
  };
  return html`
    ${rows.map((r, i) => {
      const entry = fmtAllow(r), problem = allowProblem(r);
      return html`<div class="orow allowrow" data-kind=${r.kind}>
        <select name="kind" title=${allowKind(r.kind).help}
          @change=${(e) => upd(i, { kind: e.target.value, role: (e.target.value === 'tile' || e.target.value === 'res') ? (r.role ?? '') : '' })}>
          ${ALLOW_KINDS.map((k) => html`<option value=${k.id} ?selected=${k.id === r.kind} title=${k.help}>${k.icon} ${k.label}</option>`)}
        </select>
        ${fields(r, i)}
        <button class="act rm" title="remove this entry" @click=${() => rm(i)}>✕</button>
        ${problem ? html`<span class="err-pill">${problem}</span>`
          : html`<span class="allow-desc muted">→ ${describeAllow(r)} <span class="mono">${entry}</span></span>`}
      </div>`;
    })}
    <div class="orow">
      <button class="act" data-add-entry @click=${() => onChange([...rows, { kind: 'tile', value: '', role: 'writer' }])}>+ entry</button>
      <span class="muted" style="font-size:10.5px">${rows.length ? allowKind(rows[rows.length - 1].kind).help : 'pick what to allow, fill the fields — the entry is built for you'}</span>
    </div>`;
}


// fmtBytes / fmtDur: the compact units the runtime and backup tables use.
export function fmtBytes(n) {
  n = n || 0; const u = ['B', 'K', 'M', 'G', 'T']; let i = 0;
  while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
  return (i === 0 ? Math.round(n) : n.toFixed(1)) + u[i];
}
export function fmtDur(s) {
  s = Math.max(0, s | 0);
  if (s < 60) return s + 's';
  if (s < 3600) return (s / 60 | 0) + 'm' + (s % 60) + 's';
  if (s < 86400) return (s / 3600 | 0) + 'h' + ((s % 3600) / 60 | 0) + 'm';
  return (s / 86400 | 0) + 'd' + ((s % 86400) / 3600 | 0) + 'h';
}

// setLifecycle(path, state): the offload confirm + the API write. Resolves
// false when the person declined (the caller re-renders to revert its
// <select>), true when the state was sent; throws on refusal.
export async function setLifecycle(path, state) {
  // Offload removes local bytes (after archiving) — confirm before the flip.
  if ((state === 'offloaded' || state === 'offloaded-full') &&
      !confirm(`Offload ${path}? Its ${state === 'offloaded-full' ? 'data + source' : 'data'} will be archived, then removed locally.`)) {
    return false;
  }
  await api('/lifecycle', jbody({ component: path, state }, 'POST'));
  return true;
}
