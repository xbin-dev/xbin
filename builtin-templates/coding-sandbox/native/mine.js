// native/mine.js — "Your sandboxes" in the native view: the page as a
// consumer of its own — the list, the create sheet, one sandbox's screen
// (lifecycle, who may use it, sharing), its files (browse, view, download
// through the share sheet, a folder, remove) and a terminal: the app's
// `terminal` on this tile's own route, attached to a shell the view starts
// (so a reconnect is the same shell). Uploads stay on the web (a D96
// difference: model/features.js).
import { html, nothing, repeat, native } from '/vendor/xb-native.js';
import * as F from '../model/format.js';
import * as M from '../model/mine.js';
import { ui, ctx, act, push, back, set, fail } from './ui.js';
import { sharesSection } from './ops.js';

const ACT_ICON = { start: 'play', stop: 'stop', delete: 'trash' };
const confirmOf = (r, a) => (a.confirm ? { title: `Delete ${r.name}?`, message: a.confirm, label: 'Delete', destructive: true } : undefined);

export function mineSections() {
  const app = ctx.app;
  const rows = app.myRows();
  const c = app.createForm({});
  return html`
    ${app.helloErr ? html`<section><notice tone="danger" text=${app.helloErr}/></section>` : nothing}
    ${app.mineErr ? html`<section><notice tone="danger" text=${app.mineErr}/></section>` : nothing}
    <section title=${app.operator ? 'Yours' : 'Your sandboxes'}
      footer=${app.operator ? '' : 'Sandboxes you make here are yours; the tile\'s operators see every consumer\'s.'}>
      ${rows.length ? repeat(rows, (r) => r.id, (r) => html`<row title=${r.name} subtitle=${[r.image, r.egressText, r.lastText].filter(Boolean).join(' · ')}
          detail=${r.stateLabel} tone=${r.tone} nav @tap=${() => push({ kind: 'mine', id: r.id })}>
          <actions>${r.actions.map((a) => html`<button icon=${ACT_ICON[a.id]} role=${a.danger ? 'destructive' : 'secondary'} confirm=${confirmOf(r, a)}
            @tap=${() => act(`${r.id}:${a.id}`, () => app.act(r.id, a.id))}>${a.label}</button>`)}</actions></row>`)
        : app.mine ? html`<empty icon="box" title="No sandbox here yet" text="Make one: New sandbox."/>` : html`<progress label="loading…"/>`}
      <button icon="plus" role="primary" ?disabled=${!!c.cant} @tap=${() => { ui.forms.create = { ...c.f }; ui.sheet = { kind: 'create' }; ctx.paint(); }}>New sandbox</button>
      ${c.cant ? html`<text style="footnote" tone="muted" text=${c.cant}/>` : nothing}
    </section>`;
}

// createSheet: the create form (ui.forms.create).
export function createSheet() {
  const app = ctx.app;
  const open = !!(ui.sheet && ui.sheet.kind === 'create');
  const vm = app.createForm(open ? ui.forms.create : {});
  const pick = (k) => (e) => { ui.forms.create = { ...vm.f, ...ui.forms.create, [k]: e.value }; ctx.paint(); };
  const go = () => {
    const now = app.createForm(ui.forms.create);
    if (now.error) { fail(now.error); return; }
    act('create', async () => {
      const s = await app.create(now.f);
      ui.sheet = null; ui.forms.create = null;
      push({ kind: 'mine', id: s.id });
    }, `${now.f.name} is made`);
  };
  return html`<sheet open=${open} title="New sandbox" @dismiss=${() => { ui.sheet = null; ctx.paint(); }}>
    <screen title="New sandbox" style="form">
      <toolbar><button role="plain" @tap=${() => { ui.sheet = null; ctx.paint(); }}>Cancel</button>
        <button role="primary" ?busy=${ui.busy === 'create'} @tap=${go}>Create</button></toolbar>
      ${ui.err ? html`<section><notice tone="danger" text=${ui.err}/></section>` : nothing}
      ${vm.notes.length ? html`<section>${vm.notes.map((n) => html`<notice tone="info" text=${n}/>`)}</section>` : nothing}
      <section footer="An image with a setup script builds on its first use: that one takes a while.">
        <field label="Name" placeholder="api-dev" value=${vm.f.name} submit="go" @input=${pick('name')} @submit=${go}/>
        <picker label="Image" value=${vm.f.image} options=${vm.images.map(({ value, label }) => ({ value, label }))} @change=${pick('image')}/>
        <picker label="Size" value=${vm.f.size} options=${vm.sizes} @change=${pick('size')}/>
        <picker label="Network" value=${vm.f.egress} options=${vm.egress} @change=${pick('egress')}/>
        <picker label="Who may use it" value=${vm.f.visibility} options=${VIS} @change=${pick('visibility')}/>
      </section>
    </screen>
  </sheet>`;
}

const VIS = [{ value: 'private', label: 'you' }, { value: 'team', label: 'anyone who may open this page' }];

// myScreen: one of your sandboxes.
export function myScreen(s) {
  const app = ctx.app;
  const r = app.myRows().find((x) => x.id === s.id);
  if (!r) return html`<screen title="Sandbox" style="form"><section><notice tone="muted" text="It is gone."/></section></screen>`;
  const shk = 'share:mine-' + r.id;
  return html`<screen title=${r.name} subtitle=${r.id} style="form" refreshable @refresh=${() => app.load()}>
    ${ui.err ? html`<section><notice tone="danger" text=${ui.err}/></section>` : nothing}
    ${ui.msg ? html`<section><notice tone="ok" text=${ui.msg}/></section>` : nothing}
    <section title="Sandbox">
      <row title="State" detail=${r.stateLabel} tone=${r.tone} subtitle=${r.stateDetail || undefined}/>
      <row title="Image" detail=${r.image}/>
      <row title="Size" detail=${r.size} subtitle=${r.sizeText}/>
      <row title="Network" detail=${r.egressText}/>
      <row title="Isolation" detail=${r.isolation}/>
      <row title="Owner" detail=${r.owner}/>
      <row title="Working directory" detail=${r.workdir} mono="detail"/>
    </section>
    <section>
      <row title="Files" icon="folder" nav ?disabled=${!r.canFiles} subtitle=${r.canFiles ? r.workdir : 'not now'}
        @tap=${() => { if (r.canFiles) { push({ kind: 'files', id: r.id, path: r.workdir }); app.browse(r.id, r.workdir); } }}/>
      <row title="Terminal" icon="terminal" nav ?disabled=${!r.canTerminal} subtitle=${r.canTerminal ? `a shell as ${r.user || 'the sandbox user'}` : 'not now'}
        @tap=${() => { if (r.canTerminal) openTerminal(r); }}/>
    </section>
    <section>
      ${r.actions.map((a) => html`<button icon=${ACT_ICON[a.id]} role=${a.danger ? 'destructive' : 'primary'} ?busy=${ui.busy === `${r.id}:${a.id}`} confirm=${confirmOf(r, a)}
        @tap=${() => act(`${r.id}:${a.id}`, async () => { await app.act(r.id, a.id); if (a.id === 'delete') back(); })}>${a.label}</button>`)}
    </section>
    ${r.canShare ? html`<section title="Who may use it">
      <picker label="Here" value=${r.visibility} options=${VIS} @change=${(e) => act('vis', () => app.setVisibility(r.id, e.value))}/>
    </section>
    ${sharesSection(r.shares, (x) => app.setShares(r.id, x), shk, ui.forms[shk] || (ui.forms[shk] = { consumer: '', users: '' }))}` : nothing}
  </screen>`;
}

// --- files ---------------------------------------------------------------------------

// filesScreen: a directory of sandbox s.id (the model's browser follows the
// screen on top: a pushed directory reads its own path).
export function filesScreen(s) {
  const app = ctx.app;
  const fs = app.files && app.files.id === s.id && app.files.asked === s.path ? app.files : null;
  const rows = fs ? M.fileRows(fs.listing) : [];
  const into = (e) => {
    if (e.dir) { push({ kind: 'files', id: s.id, path: e.path }); app.browse(s.id, e.path); } else { push({ kind: 'file', id: s.id, path: e.path }); app.readFile(s.id, e.path); }
  };
  return html`<screen title=${F.baseName(s.path)} subtitle=${s.path} style="list" refreshable @refresh=${() => app.browse(s.id, s.path)}>
    <toolbar><button icon="folder" @tap=${() => { ui.forms.mkdir = { name: '' }; ui.sheet = { kind: 'mkdir', id: s.id, path: s.path }; ctx.paint(); }}>New folder</button></toolbar>
    ${ui.err ? html`<section><notice tone="danger" text=${ui.err}/></section>` : nothing}
    ${fs && fs.err ? html`<section><notice tone="danger" text=${fs.err}/></section>` : nothing}
    <section>
      ${!fs || (fs.busy && !fs.listing) ? html`<progress label="reading…"/>` : nothing}
      ${fs && fs.listing && !rows.length ? html`<empty icon="folder" title="Empty"/>` : nothing}
      ${repeat(rows, (e) => e.name, (e) => html`<row title=${e.name} icon=${e.icon} detail=${e.detail} subtitle=${e.when} nav=${e.dir} @tap=${() => into(e)}>
        <actions>
          ${e.dir ? nothing : html`<button icon="download" @tap=${() => download(s.id, e.path)}>Download</button>`}
          <button icon="trash" role="destructive" confirm=${{ title: `Remove ${e.name}?`, message: e.dir ? 'It and everything in it.' : e.path, label: 'Remove', destructive: true }}
            @tap=${() => act('remove', () => app.remove(s.id, e.path, e.dir).then(() => app.browse(s.id, s.path)))}>Remove</button>
        </actions></row>`)}
    </section>
  </screen>`;
}

// download hands a file to the share sheet: the app downloads it with the
// frame token from this tile's own route.
function download(id, path) {
  const app = ctx.app;
  Promise.resolve(native.share({ file: `/api/${app.self}/sbx/sandboxes/${encodeURIComponent(id)}/files/content?path=${encodeURIComponent(path)}` })).catch(fail);
}

// fileScreen: a text file's first VIEW_MAX bytes.
export function fileScreen(s) {
  const app = ctx.app;
  const f = app.files && app.files.file && app.files.file.path === s.path ? app.files.file : null;
  return html`<screen title=${F.baseName(s.path)} subtitle=${s.path} style="scroll">
    <toolbar><button icon="share" @tap=${() => download(s.id, s.path)}>Download</button></toolbar>
    ${!f || f.busy ? html`<progress label="reading…"/>`
      : f.err ? html`<notice tone="danger" text=${f.err}/>`
      : f.binary ? html`<empty icon="file" title="A binary file" text="Download it."/>`
      : html`<code text=${f.text}/>${f.truncated ? html`<text style="footnote" tone="muted" text=${`The first ${F.bytes(M.VIEW_MAX)}; download it for the rest.`}/>` : nothing}`}
  </screen>`;
}

// mkdirSheet: a new folder in the directory the sheet was opened on.
export function mkdirSheet() {
  const app = ctx.app;
  const open = !!(ui.sheet && ui.sheet.kind === 'mkdir');
  const sh = ui.sheet || {};
  const f = ui.forms.mkdir || { name: '' };
  const go = () => {
    const name = String(f.name || '').trim();
    if (!name) return;
    act('mkdir', async () => { await app.mkdir(sh.id, F.joinPath(sh.path, name)); ui.sheet = null; await app.browse(sh.id, sh.path); });
  };
  return html`<sheet open=${open} title="New folder" detents="medium" @dismiss=${() => { ui.sheet = null; ctx.paint(); }}>
    <screen title="New folder" style="form">
      <toolbar><button role="plain" @tap=${() => { ui.sheet = null; ctx.paint(); }}>Cancel</button>
        <button role="primary" ?busy=${ui.busy === 'mkdir'} @tap=${go}>Make</button></toolbar>
      <section footer=${open ? `in ${sh.path}` : ''}><field label="Name" value=${f.name} submit="done" @input=${set('mkdir', 'name')} @submit=${go}/></section>
    </screen>
  </sheet>`;
}

// --- the terminal ----------------------------------------------------------------------

// openTerminal starts a shell as a tty exec and pushes its terminal.
function openTerminal(r) {
  const app = ctx.app;
  act('term', async () => {
    const t = await app.startShell(r.id, r.workdir);
    push({ kind: 'term', id: r.id, name: r.name, eid: t.eid, src: t.src });
  });
}

// termScreen: the app's terminal on the shell (tile-relative: this tile's own
// route, the frame token — the manager sees you).
export function termScreen(s) {
  const app = ctx.app;
  return html`<screen title=${`Terminal · ${s.name}`} style="scroll">
    <toolbar><button icon="stop" role="destructive" @tap=${() => { app.endShell(s.id, s.eid).catch(() => {}); s.ended = true; back(); }}>End</button></toolbar>
    <terminal src=${s.src} title=${s.name}/>
  </screen>`;
}

// leaveTerminal ends a terminal's shell when the person goes back from it: a
// shell left behind would run on unseen (the next terminal starts a new one).
export function leaveTerminal(s) {
  if (s && s.kind === 'term' && !s.ended) { s.ended = true; ctx.app.endShell(s.id, s.eid).catch(() => {}); }
}
