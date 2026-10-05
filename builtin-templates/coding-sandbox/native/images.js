// native/images.js — the operators' Images tab in the native view: the
// images and their builds, one image's screen (its script, its last build's
// output, build now, remove) and the image editor. The same model as the
// web's (model/ops.js imageRows, imageForm, applyImage, removeImage).
import { html, nothing, repeat } from '/vendor/xb-native.js';
import * as O from '../model/ops.js';
import { ui, ctx, act, push, back, set } from './ui.js';

export function imagesSections() {
  const app = ctx.app;
  if (app.opsErr) return html`<section><notice tone="danger" text=${app.opsErr}/></section>`;
  if (!app.ops) return html`<section><progress label="loading…"/></section>`;
  const rows = app.images();
  return html`<section title="Images" footer="An image is the substrate's base plus an optional setup script, run once as root and snapshotted; every later sandbox of it is a clone.">
      ${repeat(rows, (im) => im.id, (im) => html`<row title=${im.title} subtitle=${[im.id, im.sudo ? 'sudo' : '', im.tools.join(', ')].filter(Boolean).join(' · ')}
        detail=${im.buildText} tone=${im.tone === 'muted' ? undefined : im.tone} badge=${im.default ? 'default' : undefined} icon="box" nav
        @tap=${() => push({ kind: 'image', id: im.id })}/>`)}
    </section>
    <section><button icon="plus" role="primary" @tap=${() => { ui.forms.image = O.imageForm(); push({ kind: 'imageForm' }); }}>New image</button></section>`;
}

// imageScreen: one image — its build, script and output; build, edit, remove.
export function imageScreen(s) {
  const app = ctx.app;
  const im = app.images().find((x) => x.id === s.id);
  if (!im) return html`<screen title="Image" style="form"><section><notice tone="muted" text="It is gone."/></section></screen>`;
  const images = app.ops.config.images || [];
  const remove = () => {
    const r = O.removeImage(images, im.id);
    if (r.error) { ui.err = r.error; ctx.paint(); return; }
    act('image', async () => { await app.saveConfig({ images: r.images }); back(); }, `image ${im.id} removed`);
  };
  return html`<screen title=${im.title} subtitle=${im.id} style="form" refreshable @refresh=${() => app.load()}>
    ${ui.err ? html`<section><notice tone="danger" text=${ui.err}/></section>` : nothing}
    <section title="Image">
      <row title="Build" detail=${im.buildText} tone=${im.tone === 'muted' ? undefined : im.tone}/>
      <row title="Offered to consumers" detail=${im.offered ? 'yes' : 'no'}/>
      <row title="Tools" detail=${im.tools.join(', ') || '—'}/>
      ${im.agents.length ? html`<row title="Coding agents" detail=${im.agents.join(', ')}/>` : nothing}
      <row title="Its user may sudo" detail=${im.sudo ? 'yes, in VM sandboxes on KVM' : 'no'} tone=${im.sudoWhy ? 'warn' : undefined}/>
      ${im.sudoWhy ? html`<notice tone="warn" text=${`sudo: ${im.sudoWhy}`}/>` : nothing}
      ${im.default ? html`<row title="The default image"/>` : nothing}
      ${im.built && im.built.detail ? html`<notice tone="danger" text=${im.built.detail}/>` : nothing}
      ${im.kept ? html`<notice tone="info" text=${im.kept}/>` : nothing}
    </section>
    ${im.setup ? html`<section title=${`Setup script${im.buildEgress ? ` · network while it builds: ${im.buildEgress}` : ''}`}><code copy wrap text=${im.setup}/></section>` : nothing}
    ${im.built && im.built.log ? html`<section title="The last build's output"><code wrap text=${im.built.log}/></section>` : nothing}
    <section>
      ${im.setup ? html`<button icon="wrench" role="primary" ?disabled=${!im.canBuild} ?busy=${ui.busy === 'build'}
        @tap=${() => act('build', () => app.build(im.id), `building ${im.id}…`)}>${im.built ? 'Rebuild' : 'Build now'}</button>` : nothing}
      <button icon="pencil" @tap=${() => { ui.forms.image = O.imageForm(images.find((x) => x.id === im.id)); push({ kind: 'imageForm' }); }}>Edit</button>
      <button icon="trash" role="destructive" confirm=${{ title: `Remove ${im.id}?`, message: 'Its sandboxes keep running; no new one is made from it.', label: 'Remove', destructive: true }}
        @tap=${remove}>Remove</button>
    </section>
  </screen>`;
}

const EGRESS_OPTS = [{ value: '', label: 'internet where bound, else none' }, { value: 'none', label: 'none' },
  { value: 'internet', label: 'internet' }, { value: 'open', label: 'open' }];

// imageFormScreen: the image editor (ui.forms.image).
export function imageFormScreen() {
  const app = ctx.app;
  const f = ui.forms.image || O.imageForm();
  const save = () => {
    const r = O.applyImage(app.ops.config.images || [], f);
    if (r.error) { ui.err = r.error; ctx.paint(); return; }
    act('image', async () => { await app.saveConfig({ images: r.images }); ui.forms.image = null; back(); }, `image ${f.id} saved`);
  };
  return html`<screen title=${f.was ? `Edit ${f.was}` : 'New image'} style="form">
    ${ui.err ? html`<section><notice tone="danger" text=${ui.err}/></section>` : nothing}
    <section>
      <field label="Id" value=${f.id} @input=${set('image', 'id')}/>
      <field label="Title" value=${f.title} @input=${set('image', 'title')}/>
      <field label="Tools" placeholder="git, node, pnpm" value=${f.tools} @input=${set('image', 'tools')}/>
      <toggle label="The default" value=${f.default} @change=${(e) => { ui.forms.image = { ...f, default: e.value }; ctx.paint(); }}/>
    </section>
    <section footer=${O.SUDO_HELP}>
      <toggle label="Its user may sudo (VM sandboxes)" value=${f.sudo} @change=${(e) => { ui.forms.image = { ...f, sudo: e.value }; ctx.paint(); }}/>
    </section>
    <section title="Setup script" footer="Run as root in the workdir, once; empty: the substrate's base. A changed script, or sudo, rebuilds the image at its next use.">
      <field kind="multiline" label="Script" placeholder="apt-get update && apt-get install -y nodejs npm" value=${f.setup} @input=${set('image', 'setup')}/>
      <picker label="Network while it builds" value=${f.buildEgress} options=${EGRESS_OPTS} @change=${(e) => { ui.forms.image = { ...f, buildEgress: e.value }; ctx.paint(); }}/>
    </section>
    <section><button icon="check" role="primary" ?busy=${ui.busy === 'image'} @tap=${save}>Save</button></section>
  </screen>`;
}
