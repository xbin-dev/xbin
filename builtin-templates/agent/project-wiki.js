// project-wiki.js — a project's Wiki tab on the web (API.md §Project wiki),
// drawn in a project's page (projects.js): the pages on the left — a
// search, the index first, each with its version, size, who wrote it last
// and how often agents read it — and on the right one page (markdown as
// the chat renders it, sanitized; [[slug]] links open the page), its
// history (a version picked shows its change against the one before), or
// the editor. A save carries the version the edit began at: when someone
// changed the page meanwhile it is refused with the conflict's words, and
// Reload reads the page again (what you typed is kept beside it to merge).
// Archive retires a page (its history stays). The footer says the wiki's
// size, its reads and what dream rounds cost (model/project-wiki.js
// statsWords). The state is model/project-wiki.js (projectWiki(app)).
import { html, nothing, repeat, unsafeHTML } from '/vendor/lit-all.min.js';
import { ctx } from './web-ext.js';
import { md } from './chat-md.js';
import { can } from './model/projects.js';
import { projectWiki, pageWords, statsWords, wikiLinks, diffCount, ago } from './model/project-wiki.js';

const wiki = () => projectWiki(ctx.app);

// linkClick: a [[slug]] link in a page opens that page.
const linkClick = (pid) => (e) => {
  const a = e.target.closest && e.target.closest('a.wlink');
  if (!a) return;
  e.preventDefault();
  wiki().open(pid, a.dataset.slug);
};

/** wikiTpl(p, pv): the Wiki tab. */
export function wikiTpl(p, pv) {
  const w = wiki();
  const s = w.ensure(pv.id);
  const write = can(pv).act;
  const list = s.hits || s.pages;
  return html`<div class="pwiki" id="pwiki">
    <div class="pbar"><span class="muted small">The project's shared notes: what its agents verified — how to build and test, where things are, traps and decisions.
      The coordinator and its research keep it; tasks read it and add what they learned when their work goes to review${s.dream ? '' : ' (dream rounds are off in this project\'s policy)'}.</span></div>
    ${s.err ? html`<div class="err" id="pwiki-err">${s.err}</div>` : nothing}
    <div class="pwgrid">
      <div class="pwlist" id="pwiki-list">
        <input type="search" class="pwq" id="pwiki-q" aria-label="Search the wiki" placeholder="Search the wiki…" .value=${s.q}
          @change=${(e) => w.search(pv.id, e.target.value)}>
        ${write ? html`<button class="btn ghost btnsm" id="pwiki-new" @click=${() => w.edit(pv.id, null)}>New page</button>` : nothing}
        ${repeat(list, (x) => x.slug, (x) => {
          const pw = s.hits ? { slug: x.slug, title: x.title, meta: `v${x.version}` } : pageWords(x);
          return html`<div class="pwrow ${s.slug === x.slug && s.view !== 'edit' ? 'on' : ''}" data-slug=${x.slug} role="button" tabindex="0"
              @click=${() => w.open(pv.id, x.slug)} @keydown=${(e) => { if (e.key === 'Enter') w.open(pv.id, x.slug); }}>
            <div class="pwt">${pw.title}</div>
            <div class="muted small pwm">${x.slug} · ${pw.meta}</div>
            ${s.hits && x.snippet ? html`<div class="small pwsnip">${x.snippet}</div>` : nothing}
          </div>`;
        })}
        ${s.loading && !list.length ? html`<div class="muted small">loading…</div>`
          : !list.length ? html`<div class="muted small empty-line" id="pwiki-empty">${s.q ? 'No page matches.' : 'No pages yet — the coordinator, its research and tasks write them as they learn.'}</div>` : nothing}
      </div>
      <div class="pwmain" id="pwiki-main">${s.view === 'edit' && s.edit ? editTpl(pv, s, w) : s.view === 'history' ? historyTpl(pv, s, w) : pageTpl(pv, s, w, write)}</div>
    </div>
    ${s.stats ? html`<div class="muted small pwfoot" id="pwiki-stats" title="size, reads and the tokens dream rounds used — the wiki's own measures">${statsWords(s.stats)}</div>` : nothing}
  </div>`;
}

function pageTpl(pv, s, w, write) {
  const pg = s.page;
  if (!pg) return html`<div class="muted small">${s.pages.length ? 'Pick a page.' : ''}</div>`;
  const archive = () => { if (confirm(`Archive “${pg.title}”? It leaves the list and search; its history stays.`)) w.archive(pv.id); };
  return html`<div class="pwpage" id="pwiki-page" data-slug=${pg.slug} data-version=${pg.version}>
    <div class="pwh"><h3>${pg.title}</h3><span style="flex:1"></span>
      <button class="btn ghost btnsm" id="pwiki-history" @click=${() => w.history(pv.id)}>History</button>
      ${write ? html`<button class="btn ghost btnsm" id="pwiki-edit" @click=${() => w.edit(pv.id)}>Edit</button>
        ${pg.archived ? nothing : html`<button class="btn ghost btnsm" id="pwiki-archive" ?disabled=${s.busy === 'archive'} @click=${archive}>Archive</button>`}` : nothing}</div>
    <div class="muted small pwmeta">${pg.slug} · version ${pg.version} · by ${pg.updatedBy || '—'} · ${ago(pg.updatedMs)}${pg.archived ? ' · archived' : ''}${pg.reads ? ` · read ${pg.reads}×` : ''}</div>
    ${(pg.sources || []).length ? html`<div class="muted small pwsrc">sources: ${pg.sources.join(' · ')}</div>` : nothing}
    <div class="md pwbody" @click=${linkClick(pv.id)}>${unsafeHTML(wikiLinks(md(pg.body)))}</div>
  </div>`;
}

function historyTpl(pv, s, w) {
  const d = w.diff(pv.id);
  const n = diffCount(d);
  return html`<div class="pwhist" id="pwiki-hist">
    <div class="pwh"><h3>${s.page ? s.page.title : s.slug} — history</h3><span style="flex:1"></span>
      <button class="btn ghost btnsm" @click=${() => w.open(pv.id, s.slug)}>Back to the page</button></div>
    <div class="pwrevs">${repeat(s.revs || [], (r) => r.version, (r) => html`<div class="pwrev ${s.rev && s.rev.version === r.version ? 'on' : ''}" data-v=${r.version}
        role="button" tabindex="0" @click=${() => w.showRev(pv.id, r.version)} @keydown=${(e) => { if (e.key === 'Enter') w.showRev(pv.id, r.version); }}>
      <b>v${r.version}</b> <span class="muted small">${r.by} · ${ago(r.atMs)}${r.archived ? ' · archived' : ''}</span>
      ${r.note ? html`<div class="small">${r.note}</div>` : nothing}</div>`)}</div>
    ${s.rev ? html`<div class="pwdiffh small"><b>v${s.rev.version}</b>${s.prev ? ` against v${s.prev.version}` : ' (the first)'}: <span class="pwadd">+${n.add}</span> <span class="pwdel">−${n.del}</span></div>
      <pre class="pwdiff" id="pwiki-diff">${d.map((l) => html`<span class="pwl" data-op=${l.op === ' ' ? 'same' : l.op === '+' ? 'add' : 'del'}>${l.op} ${l.text}
</span>`)}</pre>` : nothing}
  </div>`;
}

function editTpl(pv, s, w) {
  const e = s.edit;
  return html`<div class="pwedit" id="pwiki-editor">
    <div class="pwh"><h3>${e.isNew ? 'New page' : `Editing ${e.slug}`}</h3>${e.isNew ? nothing : html`<span class="muted small">from version ${e.base}</span>`}</div>
    ${e.conflict ? html`<div class="err pwconflict" id="pwiki-conflict" role="alert">${e.conflict}
      <button class="btn btnsm" id="pwiki-reload" @click=${() => w.reload(pv.id)}>Reload</button></div>` : nothing}
    ${e.err ? html`<div class="err" id="pwiki-edit-err">${e.err}</div>` : nothing}
    <label class="small">Title <input class="pwtitle" id="pwiki-title" .value=${e.title} @input=${(x) => w.set(pv.id, 'title', x.target.value)}></label>
    ${e.isNew ? html`<label class="small">Slug <input class="pwslug" id="pwiki-slug" .value=${e.slug} placeholder="build-and-test" @input=${(x) => w.set(pv.id, 'slug', x.target.value)}></label>` : nothing}
    <textarea class="pwtext" id="pwiki-body" rows="18" aria-label="The page, Markdown" placeholder="Markdown; [[slug]] links another page"
      .value=${e.body} @input=${(x) => w.set(pv.id, 'body', x.target.value)}></textarea>
    ${e.mine ? html`<details class="pwmine" open><summary class="small">What you had typed (the page above is as it is now: merge your change in)</summary>
      <pre class="pwminet">${e.mine}</pre></details>` : nothing}
    <label class="small">Why <input class="pwnote" id="pwiki-note" .value=${e.note} placeholder="what changed, in a line" @input=${(x) => w.set(pv.id, 'note', x.target.value)}></label>
    <div class="pwbtns"><button class="btn btnsm" id="pwiki-save" ?disabled=${s.busy === 'save'} @click=${() => w.save(pv.id)}>${s.busy === 'save' ? 'Saving…' : 'Save'}</button>
      <button class="btn ghost btnsm" id="pwiki-cancel" @click=${() => w.cancel(pv.id)}>Cancel</button>
      <span class="muted small">${e.body.length} characters (at most 16 KB)</span></div>
  </div>`;
}

const style = document.createElement('style');
style.textContent = `
  .projs-page .pwiki .pwgrid { display: grid; grid-template-columns: minmax(200px, 280px) minmax(0, 1fr); gap: 12px; align-items: start; }
  @media (max-width: 800px) { .projs-page .pwiki .pwgrid { grid-template-columns: minmax(0, 1fr); } }
  .projs-page .pwlist { display: flex; flex-direction: column; gap: 4px; min-width: 0; }
  .projs-page .pwlist .pwq { width: 100%; box-sizing: border-box; }
  .projs-page .pwlist #pwiki-new { align-self: flex-start; }
  .projs-page .pwrow { padding: 6px 8px; border-radius: var(--bx-radius); cursor: pointer; border: 1px solid transparent; min-width: 0; }
  .projs-page .pwrow:hover { background: var(--bx-hover); }
  .projs-page .pwrow.on { background: var(--bx-selection); color: var(--bx-selection-text); box-shadow: inset 2px 0 0 var(--bx-accent); }
  .projs-page .pwrow .pwt { font-weight: 600; overflow-wrap: anywhere; }
  .projs-page .pwrow .pwm, .projs-page .pwrow .pwsnip { overflow-wrap: anywhere; }
  .projs-page .pwmain { min-width: 0; border: 1px solid var(--bx-border); border-radius: var(--bx-radius); padding: 10px 14px; background: var(--bx-panel); }
  .projs-page .pwh { display: flex; flex-wrap: wrap; align-items: center; gap: 6px; }
  .projs-page .pwh h3 { margin: 0; overflow-wrap: anywhere; }
  .projs-page .pwmeta, .projs-page .pwsrc { margin: 2px 0 8px; overflow-wrap: anywhere; }
  .projs-page .pwbody { overflow-wrap: anywhere; }
  .projs-page .pwbody a.wlink { color: var(--bx-link); cursor: pointer; }
  .projs-page .pwrevs { display: flex; flex-direction: column; gap: 2px; max-height: 220px; overflow: auto; margin: 8px 0; }
  .projs-page .pwrev { padding: 4px 6px; border-radius: var(--bx-radius); cursor: pointer; }
  .projs-page .pwrev:hover { background: var(--bx-hover); }
  .projs-page .pwrev.on { background: var(--bx-panel-2); box-shadow: inset 2px 0 0 var(--bx-accent); }
  .projs-page .pwdiff { font-family: var(--bx-mono); font-size: var(--bx-mono-size, 12px); white-space: pre-wrap; overflow-wrap: anywhere; margin: 4px 0 0;
    border: 1px solid var(--bx-border); border-radius: var(--bx-radius); padding: 6px; background: var(--bx-bg); max-height: 480px; overflow: auto; }
  .projs-page .pwl[data-op="add"] { color: var(--bx-ok); background: color-mix(in srgb, var(--bx-ok) 10%, transparent); }
  .projs-page .pwl[data-op="del"] { color: var(--bx-danger); background: color-mix(in srgb, var(--bx-danger) 10%, transparent); }
  .projs-page .pwl[data-op="same"] { color: var(--bx-muted); }
  .projs-page .pwadd { color: var(--bx-ok); }
  .projs-page .pwdel { color: var(--bx-danger); }
  .projs-page .pwedit { display: flex; flex-direction: column; gap: 6px; }
  .projs-page .pwedit label { display: flex; flex-direction: column; gap: 2px; }
  .projs-page .pwedit input, .projs-page .pwedit textarea { width: 100%; box-sizing: border-box; }
  .projs-page .pwedit textarea { font-family: var(--bx-mono); font-size: var(--bx-mono-size, 12px); resize: vertical; }
  .projs-page .pwconflict { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; }
  .projs-page .pwminet { white-space: pre-wrap; overflow-wrap: anywhere; font-family: var(--bx-mono); font-size: var(--bx-mono-size, 12px); max-height: 200px; overflow: auto;
    border-left: 2px solid var(--bx-border); padding-left: 8px; }
  .projs-page .pwbtns { display: flex; align-items: center; gap: 6px; }
  .projs-page .pwfoot { margin-top: 10px; border-top: 1px solid var(--bx-border); padding-top: 6px; }
`;
document.head.append(style);
