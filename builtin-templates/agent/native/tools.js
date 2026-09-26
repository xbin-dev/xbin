// native/tools.js — the screens pushed over a conversation: its memory
// blocks, its session files (a viewer/editor, share/export, the render
// preview), the skill library, the workflow tree (cost, stop), and one tool
// call in full. The web shows these as agent.js's settings tabs, workflow
// pane and render pane; both make the same calls (model/actions.js). Each
// screen is an entry of ui.stack ({kind, …}); what it loaded lives on the entry.
import { html, repeat, nothing, native } from '/vendor/xb-native.js';
import * as actions from '../model/actions.js';
import { ui, ctx, push, fmtN, clip, base, when, thumb, raw, IMAGE } from './ui.js';
import { renderDoc } from './render-doc.js';
import { settingsScreens } from './settings.js';

const isHtml = (p) => /\.html?$/i.test(p || '');
let seq = 0;

// load runs a screen's loader once (and again on refresh), saying why it failed.
function load(s, fn) {
  if (s.loaded) return;
  s.loaded = true;
  s.err = '';
  Promise.resolve().then(() => fn(s)).catch((e) => { s.err = e.message; }).finally(() => ctx.paint());
}
const reload = (s) => () => { s.loaded = false; ctx.paint(); };
const errTpl = (s) => (s.err ? html`<section><notice tone="danger" text=${s.err}/></section>` : nothing);

// act runs a change on a screen: its error stays on the screen.
const act = (s, fn) => async (...a) => {
  s.err = '';
  try { await fn(...a); } catch (e) { s.err = e.message; }
  ctx.paint();
};

// refreshView re-reads the open run's view after an edit the stream does not
// carry (memory blocks, session files) — the counts in the menu follow.
function refreshView() {
  const app = ctx.app;
  if (app.sel != null) app.session.fetchView(app.sel).then(() => ctx.paint()).catch(() => {});
}
const stale = (kind, run) => { for (const s of ui.stack) if (s.kind === kind && s.run === run) s.loaded = false; };

const SCREENS = { memory: memoryTpl, files: filesTpl, file: fileTpl, skills: skillsTpl, skill: skillTpl, tree: treeTpl, call: callTpl, render: renderTpl };

// toolScreens: ui.stack as nav screens (native.js puts them over the chat or home).
export function toolScreens() {
  return ui.stack.map((s, i) => {
    if (!s.id) s.id = ++seq;
    const tpl = SCREENS[s.kind] || settingsScreens[s.kind];
    return { key: `tool:${s.id}`, entry: s, tpl: () => (tpl ? tpl(s) : html`<screen title="?"/>`), leave: () => { ui.stack.length = Math.min(ui.stack.length, i); } };
  });
}

// --- memory ------------------------------------------------------------------------------

function memoryTpl(s) {
  load(s, async () => { s.memory = await actions.memory(s.run); s.edit = {}; s.key = ''; s.value = ''; });
  const entries = Object.entries(s.memory || {});
  const put = (key, value) => act(s, async () => {
    await actions.setMemory(s.run, key, value);
    s.loaded = false; refreshView();
  });
  return html`<screen title="Memory" subtitle=${`run ${s.run}`} style="form" refreshable @refresh=${reload(s)}>
    ${errTpl(s)}
    ${!s.memory ? html`<section><progress label="loading…"/></section>` : entries.length ? repeat(entries, ([k]) => k, ([k, v]) => html`
      <section title=${k}>
        <field kind="multiline" value=${s.edit[k] ?? String(v)} @input=${(e) => { s.edit[k] = e.value; }}/>
        <button @tap=${() => put(k, s.edit[k] ?? String(v))()}>Set</button>
        <button role="destructive" confirm=${{ title: `Delete the block "${k}"?`, label: 'Delete', destructive: true }}
          @tap=${act(s, async () => { await actions.deleteMemory(s.run, k); s.loaded = false; refreshView(); })}>Delete</button>
      </section>`) : html`<section><empty title="no memory blocks yet"/></section>`}
    <section title="Add a block">
      <field label="Key" placeholder="new key" value=${s.key || ''} @input=${(e) => { s.key = e.value; }}/>
      <field label="Value" kind="multiline" value=${s.value || ''} @input=${(e) => { s.value = e.value; }}/>
      <button @tap=${() => { const k = (s.key || '').trim(); if (k) put(k, s.value || '')(); }}>Add</button>
    </section>
  </screen>`;
}

// --- files ---------------------------------------------------------------------------------

function filesTpl(s) {
  load(s, async () => { s.files = await actions.files(s.run); });
  const files = s.files || [];
  return html`<screen title="Files" subtitle=${`run ${s.run}`} style="list" refreshable @refresh=${reload(s)}>
    <toolbar><button icon="plus" @tap=${() => push({ kind: 'file', run: s.run, path: null })}>New file</button></toolbar>
    ${errTpl(s)}
    <section title="Session files" footer="Text lives in this run's database; attachments in the tile's blob store. Deleting the run deletes both.">
      ${!s.files ? html`<progress label="loading…"/>` : files.length ? repeat(files, (f) => f.path, (f) => html`
        <row title=${f.path} mono="title" icon=${f.binary ? (IMAGE.test(f.mime || '') ? 'photo' : 'file') : isHtml(f.path) ? 'code' : 'doc'}
          subtitle=${`${f.binary ? f.mime : f.mime || 'text'} · ${fmtN(f.bytes)} B · v${f.version || 0}`} nav
          @tap=${() => push({ kind: 'file', run: s.run, path: f.path })}>
          <actions>
            ${isHtml(f.path) ? html`<button icon="eye" @tap=${() => openRender(s.run, f.path, f.version, false)}>Render</button>` : nothing}
            <button icon="trash" role="destructive" confirm=${{ title: `Delete "${f.path}"?`, label: 'Delete', destructive: true }}
              @tap=${act(s, async () => {
                await actions.deleteFile(s.run, f.path);
                s.loaded = false; refreshView();
              })}>Delete</button>
          </actions>
        </row>`) : html`<empty title="no files yet" text="the agent writes these with its file tools; attach your own from the composer"/>`}
    </section>
  </screen>`;
}

function fileTpl(s) {
  load(s, async () => {
    s.meta = s.path ? (await actions.files(s.run)).find((f) => f.path === s.path) || { path: s.path } : null;
    if (s.meta && !s.meta.binary) {
      const f = await actions.file(s.run, s.path);
      s.text = f.content || '';
      s.version = f.version;
    } else if (!s.meta) { s.text = s.text ?? ''; s.version = 0; s.newPath = s.newPath ?? ''; }
  });
  const m = s.meta;
  if (m && m.binary) {
    const img = IMAGE.test(m.mime || '');
    return html`<screen title=${base(s.path)} subtitle="attachment" style="scroll">
      ${s.err ? html`<notice tone="danger" text=${s.err}/>` : nothing}
      ${img ? html`<image src=${/webp$/.test(m.mime) ? raw(s.run, s.path) : thumb(s.run, s.path, 1024)} alt=${base(s.path)} height="xl" preview/>` : nothing}
      <text tone="muted">${`${m.mime || 'binary'} · ${fmtN(m.bytes)} B — the agent ${img ? 'sees it with file_view' : 'can list it but not read it as text'}.`}</text>
      <button icon="share" role="primary" @tap=${() => native.share({ file: raw(s.run, s.path) })}>Share / export</button>
    </screen>`;
  }
  const save = act(s, async () => {
    const path = s.path || (s.newPath || '').trim();
    if (!path) throw new Error('need a path');
    const r = await actions.saveFile(s.run, { path, content: s.text || '', version: s.path ? s.version : 0 });
    s.path = path; s.version = r.version; s.meta = { path }; s.saved = true;
    stale('files', s.run);
    for (const x of ui.stack) if (x.kind === 'render' && x.run === s.run && x.path === path) { x.ver = r.version; x.loaded = false; }
    refreshView();
  });
  return html`<screen title=${s.path ? base(s.path) : 'New file'} subtitle=${s.path ? `v${s.version || 0}${s.saved ? ' · saved' : ''}` : nothing} style="form">
    <toolbar><button role="primary" @tap=${save}>Save</button></toolbar>
    ${errTpl(s)}
    <section>
      ${s.path ? html`<row title=${s.path} mono="title" subtitle="path"/>`
        : html`<field label="Path" placeholder="report.html" value=${s.newPath || ''} @input=${(e) => { s.newPath = e.value; }}/>`}
      <field label="Content" kind="multiline" value=${s.text ?? ''} @input=${(e) => { s.text = e.value; s.saved = false; }}/>
    </section>
    ${s.path ? html`<section>
      ${isHtml(s.path) ? html`<button icon="eye" @tap=${() => openRender(s.run, s.path, s.version, false)}>Render</button>` : nothing}
      <button icon="copy" copy=${s.text || ''}>Copy</button>
      <button icon="share" @tap=${() => native.share({ file: raw(s.run, s.path) })}>Share / export</button>
    </section>` : nothing}
  </screen>`;
}

// --- skills --------------------------------------------------------------------------------

function skillsTpl(s) {
  load(s, async () => { s.skills = await actions.skills(); });
  const list = s.skills || [];
  return html`<screen title="Skills" style="list" refreshable @refresh=${reload(s)}>
    <toolbar><button icon="plus" @tap=${() => push({ kind: 'skill', name: null })}>New skill</button></toolbar>
    ${errTpl(s)}
    <section title="The skill library">
      ${!s.skills ? html`<progress label="loading…"/>` : list.length ? repeat(list, (k) => k.name, (k) => html`
        <row title=${k.name} mono="title" subtitle=${clip(k.description, 120)}
          badge=${k.owner ? `${k.owner}'s` : k.lane ? (k.lane === 'web' ? 'web' : 'internal') : nothing}
          detail=${k.updated ? when(k.updated * 1000) : nothing} nav @tap=${() => push({ kind: 'skill', name: k.name, skill: k })}>
          <actions><button icon="trash" role="destructive" confirm=${{ title: `Delete skill "${k.name}"?`, label: 'Delete', destructive: true }}
            @tap=${act(s, async () => { await actions.deleteSkill(k.name); s.loaded = false; })}>Delete</button></actions>
        </row>`) : html`<empty title="no skills yet" text="the agent authors these (Learn skill on a conversation), or add one"/>`}
    </section>
  </screen>`;
}

function skillTpl(s) {
  if (!s.form) { const k = s.skill || {}; s.form = { name: k.name || '', description: k.description || '', content: k.content || '' }; }
  const f = s.form;
  const save = act(s, async () => {
    const name = f.name.trim();
    if (!name || !f.content.trim()) throw new Error('need a name and content');
    await actions.saveSkill({ name, description: f.description.trim(), content: f.content });
    s.name = name;
    for (const x of ui.stack) if (x.kind === 'skills') x.loaded = false;
    ui.stack.pop();
  });
  return html`<screen title=${s.name ? 'Edit skill' : 'New skill'} style="form">
    <toolbar><button role="primary" @tap=${save}>Save</button></toolbar>
    ${errTpl(s)}
    <section>
      ${s.name ? html`<row title=${s.name} mono="title" subtitle="name"/>` : html`<field label="Name" value=${f.name} @input=${(e) => { f.name = e.value; }}/>`}
      <field label="Description" value=${f.description} @input=${(e) => { f.description = e.value; }}/>
      <field label="Content" kind="multiline" value=${f.content} @input=${(e) => { f.content = e.value; }}/>
    </section>
  </screen>`;
}

// --- the workflow tree ---------------------------------------------------------------------

const WF_WORDS = { dep: 'waiting on', slot: 'queued — at the concurrency limit', human: 'waiting on you', sleeping: 'sleeping', cancelling: 'cancelling…' };
const DOT = { running: 'accent', queued: 'muted', blocked: 'warn', done: 'ok', error: 'danger', cancelled: 'muted' };
const costOf = (n) => (Number(n.promptTokens) || 0) + (Number(n.completionTokens) || 0);

// treeDirty re-reads an open tree after the stream said something changed:
// one request in flight, one more if anything changed meanwhile.
export function treeDirty(s) {
  if (s.busy) { s.again = true; return; }
  s.busy = true;
  ctx.app.actions.tree(s.root).then((t) => { s.tree = t; }).catch(() => {}).finally(() => {
    s.busy = false;
    ctx.paint();
    if (s.again) { s.again = false; treeDirty(s); }
  });
}

function treeTpl(s) {
  load(s, async () => { s.tree = await ctx.app.actions.tree(s.root); });
  const t = s.tree || { nodes: [], totals: {} };
  const nodes = t.nodes || [];
  const tot = t.totals || {};
  const by = tot.byStatus || {};
  const counts = ['running', 'queued', 'blocked', 'done', 'error', 'cancelled'].filter((k) => by[k]).map((k) => `${by[k]} ${k}`).join(' · ') || 'idle';
  const root = nodes.find((n) => n.id === t.root);
  const maxCost = Math.max(1, ...nodes.map(costOf));
  const byParent = new Map();
  for (const n of nodes) {
    const k = n.id === t.root ? -1 : n.parentId;
    if (!byParent.has(k)) byParent.set(k, []);
    byParent.get(k).push(n);
  }
  const rows = [];
  const walk = (list) => { for (const n of (list || []).sort((a, b) => a.created - b.created)) { rows.push(n); walk(byParent.get(n.id)); } };
  walk(byParent.get(-1));
  const sub = (n) => {
    if (n.blockReason === 'dep' && (n.blockedOn || []).length) return `⛔ waiting on ${n.blockedOn.map((i) => '#' + i).join(', ')}`;
    if (n.blockReason) return '⏳ ' + (WF_WORDS[n.blockReason] || n.blockReason);
    if (n.status === 'error') return '⚠ ' + (n.result || 'failed');
    return n.lastStep || '';
  };
  const app = ctx.app;
  return html`<screen title=${root ? root.title || 'run ' + t.root : 'Workflow'} subtitle=${`${tot.nodes || 0} nodes · ${counts}`} style="list" refreshable @refresh=${reload(s)}>
    <toolbar><button icon="stop" role="destructive" confirm=${{ title: 'Cancel this workflow and every run below it?', label: 'Stop all', destructive: true }}
      @tap=${act(s, async () => { await app.actions.cancelTree(s.root); s.loaded = false; })}>Stop</button></toolbar>
    ${errTpl(s)}
    <section title="Cost">
      <row title=${`Σ ${fmtN(tot.promptTokens)}↑ ${fmtN(tot.completionTokens)}↓`} subtitle=${`${fmtN(tot.llmCalls)} calls · ${tot.active || 0}/${tot.limit || 0} running`}/>
    </section>
    <section title="Runs">${rows.length ? repeat(rows, (n) => n.id, (n) => html`
      <row title=${'· '.repeat(Math.min(Number(n.depth) || 0, 4)) + (n.title || 'run ' + n.id)} subtitle=${clip(sub(n), 160) || nothing}
        tone=${DOT[n.status] || 'muted'} detail=${costOf(n) ? fmtN(costOf(n)) : nothing} nav
        @tap=${() => { ui.stack.length = 0; app.select(n.id); }}>
        ${costOf(n) ? html`<progress value=${costOf(n) / maxCost}/>` : nothing}
      </row>`) : html`<empty title=${s.tree ? 'no background runs' : 'loading…'}/>`}</section>
  </screen>`;
}

// --- one tool call in full (a long result's ↗) ----------------------------------------------

function findBlock(blocks, id) {
  for (const b of blocks || []) {
    if (b.id === id) return b;
    const inner = b.blocks && findBlock(b.blocks, id);
    if (inner) return inner;
  }
  return null;
}

function callTpl(s) {
  const b = findBlock(ctx.app.session.shown().blocks, s.id) || s.last;
  if (b) s.last = b;
  if (!b) return html`<screen title="Tool call" style="scroll"><empty title="gone"/></screen>`;
  return html`<screen title=${b.headline} subtitle=${b.name} style="scroll">
    <text style="caption" tone="muted">arguments</text>
    <code text=${b.args || '{}'} copy wrap/>
    <text style="caption" tone="muted">${`result · ${fmtN((b.result || '').length)} characters`}</text>
    <code text=${b.result || ''} copy wrap/>
  </screen>`;
}

// --- the render preview ------------------------------------------------------------------------

// openRender shows an HTML session file as a no-script island (render-doc.js);
// live: it follows the run's newest render.
export function openRender(run, path, ver, live) {
  const cur = ui.stack.find((x) => x.kind === 'render');
  if (cur) Object.assign(cur, { run, path, ver: Number(ver) || 0, live: !!live, loaded: false });
  else push({ kind: 'render', run, path, ver: Number(ver) || 0, live: !!live });
  ctx.paint();
}

function renderTpl(s) {
  load(s, async () => {
    const f = await actions.file(s.run, s.path);
    const d = renderDoc(f.content);
    s.doc = d.html; s.blocked = d.blocked; s.version = f.version;
  });
  const staleV = s.ver && s.version && s.version !== s.ver;
  const warns = [];
  if (s.blocked) warns.push(`${s.blocked} external resource${s.blocked > 1 ? 's' : ''} blocked`);
  if (staleV) warns.push(`showing v${s.version} (the render was v${s.ver})`);
  return html`<screen title=${base(s.path)} subtitle=${s.version ? 'v' + s.version : nothing} style="scroll">
    <toolbar><button icon="doc" @tap=${() => push({ kind: 'file', run: s.run, path: s.path })}>Source</button></toolbar>
    ${s.err ? html`<notice tone="danger" text=${s.err}/>` : nothing}
    ${warns.length ? html`<notice tone="warn" text=${warns.join(' · ')}/>` : nothing}
    ${s.doc != null ? html`<canvas html=${s.doc} height="xl"/>` : html`<progress label="loading…"/>`}
  </screen>`;
}
