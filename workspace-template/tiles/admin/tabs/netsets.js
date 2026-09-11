/**
 * <bx-admin-netsets> — the admin console's network sets tab (D54): named
 * reach rules attached to organisations by reference, with the typed-row
 * editor (bx-netrules) and its live reach preview. A tab element of
 * tiles/admin (see admin.js): data arrives as properties, writes go
 * through the API and are reported to the router with composed events
 * (bx-admin-err / bx-admin-refresh).
 */
import { LitElement, html, nothing } from 'lit';
import { xbinApi as api } from '/vendor/bx-kit.js';
import { RULE_KINDS, parseRule, fmtRule, ruleProblem, ruleLabel, setSummary } from '/vendor/bx-netrules.js';
import { base } from '../admin-css.js';
import { targetDatalist, WithDrafts, WithRouter } from '../shared.js';

export class BxAdminNetsets extends WithRouter(WithDrafts(LitElement)) {
  static properties = {
    netsets: { attribute: false }, // {sets: {name: {rules, created}}, attachedTo} from /net-sets
    targets: { attribute: false }, // tile-target datalist options (shared.targetOptions)
    _drafts: { state: true }, // editor drafts keyed by context ("netset:<name>", …)
    _err: { state: true },    // the last API refusal (also reported to the router's slot)
  };
  static styles = [base];

  // The harness surface (routed here by the admin router's testApi).
  testApi() { return this.draftApi(); }

  // Named reach rules attached to orgs by reference. A separate tab from
  // permission sets on purpose: these answer "what can this org reach", those
  // "who may approve what" — a founder shouldn't hunt for the first behind the
  // second's allowance grammar.
  render() {
    const sets = this.netsets?.sets ?? {};
    const attached = this.netsets?.attachedTo ?? {};
    const bound = this.netsets?.boundBy ?? {}; // tiles bound to set:<name> (D65)
    const editKey = (n) => `netset:${n}`;
    return html`
      ${targetDatalist(this.targets)}
      <p class="muted" style="max-width:72ch">Named <b>network reach</b>, attached to organisations by reference
        (organisations tab → network). For an org's <b>own tiles</b> the union of its sets is the ceiling on
        <span class="mono">net</span> bindings, what its admins may bind without asking, and the default egress —
        the builtin <span class="mono">org</span> — of those tiles and of terminals opened on them, where the
        scope menu also offers each attached set by name. Personal and workspace tiles are unaffected (their
        terminals follow <b>term-net</b>) — except that a workspace admin may bind any tile's
        <span class="mono">net</span> slot to one set (<span class="mono">set:&lt;name&gt;</span>, binding tab /
        tile popover) and pick any set in any terminal (D65). Edits restart the affected org tiles and every
        tile bound to the set; terminals pick the change up when reopened.</p>
      ${Object.entries(sets).sort(([a], [b]) => a.localeCompare(b)).map(([name, ns]) =>
        this._netSetCard(name, ns, attached[name] ?? [], bound[name] ?? [], editKey(name)))}
      ${!Object.keys(sets).length ? html`<p class="muted">No network sets yet — every org tile's
        <span class="mono">net</span> slot is bound by hand today.</p>` : nothing}
      <h4>add set</h4>
      <form class="inline" @submit=${async (e) => { e.preventDefault();
        const f = e.target; const name = f.name_.value.trim();
        if (!name) return;
        await this._orgAPI('PUT', `/net-sets/${encodeURIComponent(name)}`, { rules: ['internet'] });
        if (!this._err) { f.reset(); this._setDraft(editKey(name), [{ kind: 'internet', value: '' }]); } }}>
        <input name="name_" placeholder="set name (devs-net)" size="16" required>
        <button class="act go">create</button>
        <span class="muted" style="font-size:10.5px">starts as 🌐 all internet and opens for editing — add LAN ranges or destinations there</span>
      </form>
      <p class="muted" style="font-size:10.5px; margin-top:6px">Rule grammar:
        <span class="mono">internet · internet:&lt;host|*.glob|ip|cidr&gt;[:port] · lan:&lt;ip|cidr&gt;[:port] · host · provider:&lt;tile-glob&gt;</span>
        — the <span class="mono">net:</span> allowance forms without the prefix. Hostnames are DNS-pinned by the
        relay (one <span class="mono">*</span> per glob); same-org provider tiles need no rule. A workspace or org
        <span class="mono">deny net</span> ceiling row still beats everything.</p>`;
  }

  _netSetCard(name, ns, orgs, tiles, key) {
    const d = this._draft(key); // [{kind, value}] rows while editing
    const rules = ns.rules ?? [];
    const sum = setSummary(rules);
    const held = [...orgs.map((o) => `org ${o}`), ...tiles];
    return html`<div class="netsetcard" data-netset=${name} style="border:1px solid var(--bx-border, #363c45); border-radius:6px; padding:8px 10px; margin:8px 0">
      <div style="display:flex; align-items:baseline; gap:8px; flex-wrap:wrap">
        <b class="mono">⛭ ${name}</b>
        ${orgs.map((o) => html`<span class="pill">org ${o}</span>`)}
        ${tiles.map((t) => html`<span class="pill mono" title="bound to this set (set:${name}) by a workspace admin">${t}</span>`)}
        ${sum.host ? html`<span class="pill pol" title="every org-bound tile and terminal in attached orgs shares the host's network stack">⚠ host</span>` : nothing}
        <span style="flex:1"></span>
        <button class="act" @click=${() => this._toggleDraft(key, () => rules.map(parseRule))}>edit</button>
        <button class="act rm" ?disabled=${held.length > 0}
          title=${held.length ? `detach / unbind ${held.join(', ')} first` : 'delete this set'}
          @click=${() => confirm(`Delete network set ${name}?`) && this._orgAPI('DELETE', `/net-sets/${encodeURIComponent(name)}`)}>del</button>
      </div>
      <div style="margin-top:3px">${rules.length
        ? rules.map((r) => html`<span class="pill mono" title=${r}>${ruleLabel(r)}</span>`)
        : html`<span class="muted" style="font-size:11px">no rules — attached orgs' tiles reach nothing (airgapped, incl. DNS)</span>`}</div>
      ${d ? this._netSetEditor(name, key, d) : nothing}
    </div>`;
  }

  // Typed-row editor: [kind ▾][value][✕] per rule with an inline shape hint
  // (bx-netrules.ruleProblem — the server validates for real), a live "reach"
  // preview and a loud line whenever a row grants host networking.
  _netSetEditor(name, key, d) {
    const upd = (i, patch) => this._setDraft(key, d.map((r, j) => (j === i ? { ...r, ...patch } : r)));
    const wire = d.map(fmtRule);
    const problems = d.map((r, i) => (wire[i] ? ruleProblem(wire[i]) : 'value required'));
    const bad = problems.some(Boolean);
    const dup = new Set(wire).size !== wire.length;
    const host = d.some((r) => r.kind === 'host');
    const providerOnly = wire.length > 0 && d.every((r) => r.kind === 'provider');
    const sum = setSummary(wire.filter(Boolean));
    return html`<div class="editor" style="margin-top:6px">
      ${d.map((r, i) => {
        const k = RULE_KINDS.find((x) => x.id === r.kind) ?? RULE_KINDS[0];
        return html`<div class="orow">
          <select @change=${(e) => upd(i, { kind: e.target.value })}>
            ${RULE_KINDS.map((x) => html`<option value=${x.id} ?selected=${x.id === r.kind} title=${x.help}>${x.icon} ${x.label}</option>`)}
          </select>
          ${k.hasValue
            ? html`<input size="30" list=${r.kind === 'provider' ? 'tile-targets' : nothing} placeholder=${k.placeholder}
                title=${k.help} .value=${r.value ?? ''} @input=${(e) => upd(i, { value: e.target.value })}>`
            : html`<span class="muted" style="font-size:11px">${k.help}</span>`}
          ${problems[i] ? html`<span class="err-pill">${problems[i]}</span>` : nothing}
          <button class="act rm" title="remove rule" @click=${() => this._setDraft(key, d.filter((_, j) => j !== i))}>✕</button>
        </div>`;
      })}
      <div class="orow">
        <button class="act" @click=${() => this._setDraft(key, [...d, { kind: 'lan', value: '' }])}>+ rule</button>
        <span style="flex:1"></span>
        <button class="act go" ?disabled=${bad || dup} title=${bad ? 'fix the highlighted rules first' : dup ? 'remove the duplicate rule' : 'save (restarts affected org tiles)'}
          @click=${async () => {
            await this._orgAPI('PUT', `/net-sets/${encodeURIComponent(name)}`, { rules: wire });
            if (!this._err) this._dropDraft(key);
          }}>save</button>
        <button class="act" @click=${() => this._dropDraft(key)}>cancel</button>
      </div>
      <div class="muted" style="font-size:11px; margin-top:4px">tiles in attached orgs reach:
        ${wire.filter(Boolean).length ? sum.text : 'nothing (airgapped, incl. DNS)'}${providerOnly ? ' — provider-only: no relay egress until a provider tile is bound' : ''}</div>
      ${host ? html`<div class="warn-line">⚠ <b>host networking</b> shares the host's full network stack with EVERY
        org-bound tile and terminal in attached orgs — no relay, no filtering, no metering, no ingress splicing.
        Prefer a LAN range; keep host for a dedicated infra org.</div>` : nothing}
      ${dup ? html`<div class="warn-line">duplicate rules</div>` : nothing}
    </div>`;
  }
}

customElements.define('bx-admin-netsets', BxAdminNetsets);
