// native/settings.js — the operators' Settings tab in the native view: the
// isolation mode, the sandbox networks, the sizes, the quotas and the rest
// (layout, idle stop, mounts), each saved as the web's are (PUT /ops/config
// with its own top-level fields; model/ops.js).
import { html, nothing, repeat } from '/vendor/xb-native.js';
import * as O from '../model/ops.js';
import * as F from '../model/format.js';
import { ui, ctx, act, push, back, set } from './ui.js';

export function settingsSections() {
  const app = ctx.app;
  if (app.opsErr) return html`<section><notice tone="danger" text=${app.opsErr}/></section>`;
  if (!app.ops) return html`<section><progress label="loading…"/></section>`;
  const m = app.mode();
  const pick = ui.forms.mode ?? m.value;
  const b = app.backend();
  const cfg = app.ops.config;
  return html`
    <section title="Isolation" footer=${m.now ? `Now ${m.nowText}; existing ones keep theirs.` : ''}>
      <picker label="Mode" value=${pick} options=${m.options.map((o) => ({ value: o.value, label: o.why ? `${o.label} (unavailable)` : o.label }))}
        @change=${(e) => { ui.forms.mode = e.value; ctx.paint(); }}/>
      ${m.blocked ? html`<notice tone="danger" title="No new sandbox can be made now" text=${m.blocked}/>` : nothing}
      ${pick !== m.value ? html`<button icon="check" role="primary" ?busy=${ui.busy === 'mode'}
        @tap=${() => act('mode', () => app.saveConfig({ mode: pick }).then(() => { ui.forms.mode = undefined; }), 'the mode is saved')}>Save the mode</button>` : nothing}
    </section>
    <section title="Networks" footer="A sandbox-net class of this tile's, bound by an approver (the binding panel, or bx bind); unbound, sandboxes get no network.">
      ${repeat(b.classes, (c) => c.slot, (c) => html`<row title=${c.slot} subtitle=${c.ref ? `bound to ${c.ref}` : c.bind} mono="subtitle"
        detail=${c.offered ? `offered · ${F.EGRESS[c.reach] || c.reach}` : 'not offered'} tone=${c.offered ? 'ok' : 'muted'} icon="network"/>`)}
    </section>
    <section title="Sizes">
      ${repeat(cfg.sizes || [], (s) => s.id, (s) => html`<row title=${s.title || s.id} subtitle=${F.sizeText(s)} badge=${s.default ? 'default' : undefined} nav
        @tap=${() => { ui.forms.size = { ...s, was: s.id }; push({ kind: 'size' }); }}/>`)}
      <button icon="plus" @tap=${() => { ui.forms.size = { id: '', title: '', memMiB: 2048, vcpus: 2, diskGiB: 20, was: '' }; push({ kind: 'size' }); }}>Add a size</button>
    </section>
    <section title="Quotas" footer="0 is no limit. The substrate's own limits for this tile bind too.">
      ${repeat(app.quotas(), (r) => r.kind + ':' + r.key, (r) => html`<row title=${r.label} nav
        subtitle=${Object.entries(O.QUOTA_LABELS).map(([k, l]) => `${r.values[k] || '∞'} ${l}`).join(' · ')}
        @tap=${() => { ui.forms.quota = { kind: r.kind, key: r.key, ...r.values }; push({ kind: 'quota', label: r.label, removable: r.removable }); }}/>`)}
      <button icon="plus" @tap=${() => { ui.forms.quota = { kind: 'consumer', key: '', fresh: true, ...defaultQuota(cfg, 'consumer') }; push({ kind: 'quota', label: 'Its own quota', removable: false }); }}>A consumer's or a person's own quota</button>
    </section>
    <section title="Where people work">
      <row title="Layout, idle stop, mounts" subtitle=${`${cfg.layout.user} in ${cfg.layout.workdir} · ${(cfg.mounts || []).length} mount(s)`} nav
        @tap=${() => { ui.forms.adv = { ...cfg.layout, autoStopMin: String(cfg.autoStopMin || 0), mounts: [...(cfg.mounts || [])], mount: '' }; push({ kind: 'advanced' }); }}/>
      <row title="Backend" detail=${b.name} subtitle=${`registered: ${((app.ops.backend && app.ops.backend.registered) || []).join(', ')}`}/>
    </section>`;
}

// sizeScreen: one size (ui.forms.size), saved into config.sizes.
export function sizeScreen() {
  const app = ctx.app;
  const f = ui.forms.size || {};
  const sizes = O.sizeForms(app.ops.config.sizes);
  const save = (remove = false) => {
    let next = remove ? sizes.filter((s) => s.id !== f.was) : f.was ? sizes.map((s) => (s.id === f.was ? f : s)) : [...sizes, f];
    if (f.default && !remove) next = next.map((s) => (s === f ? s : { ...s, default: false }));
    const r = O.applySizes(next.map(({ was, ...s }) => s));
    if (r.error) { ui.err = r.error; ctx.paint(); return; }
    act('size', async () => { await app.saveConfig({ sizes: r.sizes }); ui.forms.size = null; back(); }, 'the sizes are saved');
  };
  return html`<screen title=${f.was ? `Size ${f.was}` : 'New size'} style="form">
    ${ui.err ? html`<section><notice tone="danger" text=${ui.err}/></section>` : nothing}
    <section footer="A size over the substrate's per-sandbox caps isn't offered.">
      <field label="Id" value=${f.id} @input=${set('size', 'id')}/>
      <field label="Title" value=${f.title || ''} @input=${set('size', 'title')}/>
      <field label="Memory, MiB" kind="number" value=${String(f.memMiB ?? '')} @input=${set('size', 'memMiB')}/>
      <field label="vCPUs" kind="number" value=${String(f.vcpus ?? '')} @input=${set('size', 'vcpus')}/>
      <field label="Disk, GiB" kind="number" value=${String(f.diskGiB ?? '')} @input=${set('size', 'diskGiB')}/>
      <toggle label="The default" value=${!!f.default} @change=${(e) => { ui.forms.size = { ...f, default: e.value }; ctx.paint(); }}/>
    </section>
    <section>
      <button icon="check" role="primary" ?busy=${ui.busy === 'size'} @tap=${() => save()}>Save</button>
      ${f.was ? html`<button icon="trash" role="destructive" confirm=${{ title: `Remove ${f.was}?`, message: 'Consumers are no longer offered it.', label: 'Remove', destructive: true }}
        @tap=${() => save(true)}>Remove</button>` : nothing}
    </section>
  </screen>`;
}

const KINDS = [{ value: 'consumer', label: 'a consumer' }, { value: 'person', label: 'a person' }];

// defaultQuota: a new override starts from its kind's default (as the web's).
const defaultQuota = (cfg, kind) => ({ sandboxes: 1, ...(((cfg && cfg.quotas) || {})[kind] || {}) });

// quotaScreen: one quota row (ui.forms.quota): the defaults or an override.
export function quotaScreen(s) {
  const app = ctx.app;
  const f = ui.forms.quota || {};
  const save = (remove = false) => {
    const key = String(f.key || '').trim();
    if (f.fresh && !key) { ui.err = 'whose quota: a consumer tile (apps/…) or a user id'; ctx.paint(); return; }
    const quotas = O.setQuota(app.ops.config.quotas, f.kind, key, remove ? null : f);
    act('quota', async () => { await app.saveConfig({ quotas }); ui.forms.quota = null; back(); }, 'the quotas are saved');
  };
  return html`<screen title=${s.label} style="form">
    ${ui.err ? html`<section><notice tone="danger" text=${ui.err}/></section>` : nothing}
    ${f.fresh ? html`<section title="Whose">
      <picker label="For" value=${f.kind} options=${KINDS} @change=${(e) => { ui.forms.quota = { ...f, kind: e.value, ...defaultQuota(app.ops.config, e.value) }; ctx.paint(); }}/>
      <field label=${f.kind === 'person' ? 'User id' : 'Consumer tile'} placeholder=${f.kind === 'person' ? 'alice' : 'apps/agent'} value=${f.key} @input=${set('quota', 'key')}/>
    </section>` : nothing}
    <section title="Limits" footer="0 is no limit.">
      ${Object.entries(O.QUOTA_LABELS).map(([k, l]) => html`<field label=${l} kind="number" value=${String(f[k] ?? 0)} @input=${set('quota', k)}/>`)}
    </section>
    <section>
      <button icon="check" role="primary" ?busy=${ui.busy === 'quota'} @tap=${() => save()}>Save</button>
      ${s.removable ? html`<button icon="trash" role="destructive" @tap=${() => save(true)}>Remove its own quota</button>` : nothing}
    </section>
  </screen>`;
}

// advancedScreen: the layout, the idle stop and the mounts (ui.forms.adv).
export function advancedScreen() {
  const app = ctx.app;
  const f = ui.forms.adv || {};
  const addMount = () => {
    const r = O.parseMount(f.mount);
    if (r.error) { ui.err = r.error; ctx.paint(); return; }
    ui.forms.adv = { ...f, mounts: [...f.mounts, r.mount], mount: '' };
    ctx.paint();
  };
  const save = () => {
    const layout = { workdir: f.workdir, home: f.home, user: f.user, uid: Number(f.uid), gid: Number(f.gid), shell: f.shell };
    act('adv', async () => { await app.saveConfig({ layout, autoStopMin: Number(f.autoStopMin) || 0, mounts: f.mounts }); ui.forms.adv = null; back(); }, 'saved: new sandboxes get it');
  };
  return html`<screen title="Layout, idle stop, mounts" style="form">
    ${ui.err ? html`<section><notice tone="danger" text=${ui.err}/></section>` : nothing}
    <section title="Layout" footer="Where people work in every new sandbox (on a substrate that runs everything as root, root at /root).">
      ${['workdir', 'home', 'user', 'uid', 'gid', 'shell'].map((k) => html`<field label=${k} kind=${k === 'uid' || k === 'gid' ? 'number' : 'text'}
        value=${String(f[k] ?? '')} @input=${set('adv', k)}/>`)}
    </section>
    <section title="Idle stop"><field label="Minutes (0: the substrate's)" kind="number" value=${String(f.autoStopMin ?? '0')} @input=${set('adv', 'autoStopMin')}/></section>
    <section title="Mounts" footer="Filesystem resources this tile holds, in every new sandbox.">
      ${repeat(f.mounts || [], (m) => m.res + m.at, (m, i) => html`<row title=${O.mountText(m)} mono="title">
        <actions><button icon="trash" role="destructive" @tap=${() => { ui.forms.adv = { ...f, mounts: f.mounts.filter((_, j) => j !== i) }; ctx.paint(); }}>Remove</button></actions></row>`)}
      <field label="A mount" placeholder=${`res:${app.self}/cache:go /cache ro`} value=${f.mount || ''} @input=${set('adv', 'mount')}/>
      <button icon="plus" @tap=${addMount}>Add the mount</button>
    </section>
    <section><button icon="check" role="primary" ?busy=${ui.busy === 'adv'} @tap=${save}>Save</button></section>
  </screen>`;
}
