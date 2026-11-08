// native/project-wiki.js — a project's wiki in the native view (API.md
// §Project wiki), read-only: editing pages of Markdown, with a merge on a
// conflict, is the web's (model/features.js DIFFERENCES); agents write it
// either way. Pushed screens of the stack (native/nav.js):
//
//   'project-wiki'       the pages, the index first, a search; the wiki's
//                        measures as the footer (model/project-wiki.js statsWords)
//   'project-wiki-page'  one page as markdown — its [[slug]] links as plain
//                        words (the runtime opens web links only) and a row
//                        per page it links, who changed it and when
//
// The entry is the project screen's Wiki row (native/projects.js). The state
// is model/project-wiki.js (projectWiki(app)).
import { html, nothing, repeat } from '/vendor/xb-native.js';
import { ext } from './ext.js';
import { ctx, push } from './ui.js';
import { projectWiki, pageWords, statsWords, linkedSlugs, plainLinks, ago } from '../model/project-wiki.js';

const wiki = () => projectWiki(ctx.app);

ext.register({
  screen(s) {
    switch (s.kind) {
      case 'project-wiki': return listTpl(s);
      case 'project-wiki-page': return pageTpl(s);
    }
    return null;
  },
});

/** openWiki(pid): the project's wiki screen. */
export const openWiki = (pid) => push({ kind: 'project-wiki', pid });

function listTpl(s) {
  const w = wiki();
  const st = w.ensure(s.pid);
  const pv = ctx.app.projects.find(s.pid);
  const list = st.hits || st.pages;
  return html`<screen title="Wiki" subtitle=${pv ? pv.name : ''} style="list" refreshable @refresh=${() => w.load(s.pid)}>
    <section><field kind="search" placeholder="Search the wiki" submit="search" value=${st.q}
      @submit=${(e) => w.search(s.pid, e.value || '')} @change=${(e) => { if ((e.value || '') !== st.q) w.search(s.pid, e.value || ''); }}/></section>
    ${st.err ? html`<section><notice tone="danger" text=${st.err}/></section>` : nothing}
    <section footer=${st.stats ? statsWords(st.stats) : 'The project\'s shared notes — kept by its coordinator, its research and its tasks.'}>
      ${list.length ? repeat(list, (x) => x.slug, (x) => {
        const pw = st.hits ? { title: x.title, meta: x.snippet || `v${x.version}` } : pageWords(x);
        return html`<row title=${pw.title} subtitle=${st.hits ? pw.meta : `${x.slug} · ${pw.meta}`} icon="doc" nav
          @tap=${() => push({ kind: 'project-wiki-page', pid: s.pid, slug: x.slug })}/>`;
      }) : html`<empty title=${st.loading ? 'loading…' : st.q ? 'No page matches' : 'No pages yet'}/>`}
    </section>
  </screen>`;
}

function pageTpl(s) {
  const c = wiki().peek(s.pid, s.slug);
  const pg = c.page;
  if (!pg) {
    return html`<screen title=${s.slug} style="list">
      <section>${c.err ? html`<notice tone="danger" text=${c.err}/>` : html`<empty title="loading…"/>`}</section></screen>`;
  }
  const links = linkedSlugs(pg.body);
  const meta = [`version ${pg.version}`, pg.updatedBy ? `by ${pg.updatedBy}` : '', ago(pg.updatedMs), pg.archived ? 'archived' : ''].filter(Boolean).join(' · ');
  return html`<screen title=${pg.title} subtitle=${pg.slug} style="list">
    <section footer=${meta}><markdown source=${plainLinks(pg.body)}/></section>
    ${(pg.sources || []).length ? html`<section title="Sources">${repeat(pg.sources, (x) => x, (x) => html`<row title=${x}/>`)}</section>` : nothing}
    ${links.length ? html`<section title="Pages it links">${repeat(links, (x) => x, (x) => html`<row title=${x} icon="link" nav
      @tap=${() => push({ kind: 'project-wiki-page', pid: s.pid, slug: x })}/>`)}</section>` : nothing}
  </screen>`;
}
