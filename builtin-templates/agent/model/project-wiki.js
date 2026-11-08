// model/project-wiki.js — a project's wiki for both views (API.md §Project
// wiki): app.projects' companion for the Wiki tab — its pages (the index
// first), a search, one page and its history with a revision's diff, and
// a person's edit with compare-and-swap: a save sends the version the edit
// began at, and a 409 keeps the editor open with the conflict's words and
// Reload (the page read again, what was typed kept beside it to copy from).
//
//   const w = projectWiki(app);   // one per app, made on first use
//   w.ensure(pid) → the tab's state; w.search(pid, q); w.open(pid, slug)
//   w.history(pid); w.showRev(pid, v); w.edit(pid, slug?); w.save(pid)
//   w.reload(pid); w.archive(pid)
//
// The `project` stream event with change `wiki` reads the list (and the
// open page) again — app.projects.take is wrapped once, as the feed's is.
// Page text is the project's notes: markdown, drawn sanitized by the view
// (chat-md.js); [[slug]] links are this model's (wikiLinks). Emits
// `projects`. No lit, no DOM.
import { projCall, qs, projectHome } from './project-api.js';

const enc = encodeURIComponent;

/** WIKI_TIP: the Wiki tab's tooltip. */
export const WIKI_TIP = 'Wiki. The project\'s shared notes — how to build and test, where things are, traps and decisions — kept by the coordinator, its research and the tasks as they finish. Every version is kept.';

/** wikiApi(pid): the wiki's routes of one project, at its home. */
export function wikiApi(pid) {
  const home = projectHome(pid);
  const p = `/projects/${pid}/wiki`;
  const call = (path, method, body) => projCall(home, path, method, body);
  return {
    list: (q = '', archived = false) => call(`${p}${qs({ q, archived })}`),
    get: (slug, v = 0) => call(`${p}/${enc(slug)}${qs({ v })}`),
    history: (slug, v = 0) => call(`${p}/${enc(slug)}/history${qs({ v })}`),
    put: (slug, body) => call(`${p}/${enc(slug)}`, 'PUT', body),
    archive: (slug, baseVersion, note = '') => call(`${p}/${enc(slug)}${qs({ baseVersion })}`, 'DELETE', note ? { note } : undefined),
  };
}

/** SLUG: a page's slug as the backend takes it. */
export const SLUG = /^[a-z0-9](?:[a-z0-9-]{0,62}[a-z0-9])?$/;

/** slugOf(text): a slug made of a title ('' when nothing is left). */
export const slugOf = (s) => String(s || '').toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '').slice(0, 64).replace(/-+$/, '');

/** wikiLinks(html): rendered markdown with its [[slug]] links made links (a.wlink[data-slug]) — the
 * slug's characters are a slug's, so nothing else of the text becomes markup. */
export const wikiLinks = (html) => String(html || '').replace(/\[\[([a-z0-9][a-z0-9-]{0,63})\]\]/g, '<a class="wlink" data-slug="$1" href="#" role="link">$1</a>');

/** linkedSlugs(body): the pages a page links ([[slug]]), each once, in order. */
export function linkedSlugs(body) {
  const out = [];
  for (const m of String(body || '').matchAll(/\[\[([a-z0-9][a-z0-9-]{0,63})\]\]/g)) if (!out.includes(m[1])) out.push(m[1]);
  return out;
}

/** plainLinks(body): a page's [[slug]] links as plain words (for a renderer that takes only web links). */
export const plainLinks = (body) => String(body || '').replace(/\[\[([a-z0-9][a-z0-9-]{0,63})\]\]/g, '‹$1›');

/** kb(n): a size in words. */
export const kb = (n) => (n >= 10240 ? `${Math.round(n / 1024)} KB` : n >= 1024 ? `${(n / 1024).toFixed(1)} KB` : `${n | 0} B`);

/** tokens(n): a token count in words. */
const tokens = (n) => (n >= 1e6 ? `${(n / 1e6).toFixed(1)}M` : n >= 1e3 ? `${Math.round(n / 1e3)}k` : String(n | 0));

/** statsWords(stats): the tab's footer — size, cost and reads (the article's measures). */
export function statsWords(s) {
  if (!s) return '';
  const parts = [`${s.pages | 0} page${s.pages === 1 ? '' : 's'}`, kb(s.bytes | 0)];
  const per = s.pages ? (s.reads / s.pages) : 0;
  parts.push(`${s.reads | 0} read${s.reads === 1 ? '' : 's'}${s.pages ? ` (${per >= 10 ? Math.round(per) : per.toFixed(1)} a page)` : ''}`);
  if (s.readers) parts.push(`by ${s.readers} run${s.readers === 1 ? '' : 's'}`);
  if (s.revisions) parts.push(`${s.revisions} version${s.revisions === 1 ? '' : 's'}`);
  if (s.dreams) parts.push(`${s.dreams} dream round${s.dreams === 1 ? '' : 's'}${s.dreamTokens ? `, ${tokens(s.dreamTokens)} tokens` : ''}`);
  return parts.join(' · ');
}

/** ago(ms, now): how long ago, in words. */
export function ago(ms, now = Date.now()) {
  if (!ms) return '';
  const d = Math.max(0, now - ms) / 1000;
  if (d < 60) return 'just now';
  if (d < 3600) return `${Math.floor(d / 60)}m ago`;
  if (d < 172800) return `${Math.floor(d / 3600)}h ago`;
  return `${Math.floor(d / 86400)}d ago`;
}

/** pageWords(page, now): a page's row — {slug, title, meta}. */
export const pageWords = (p, now = Date.now()) => ({
  slug: p.slug, title: p.title || p.slug,
  meta: [`v${p.version}`, kb(p.size | 0), p.updatedBy ? `by ${p.updatedBy}` : '', ago(p.updatedMs, now), p.reads ? `${p.reads} read${p.reads === 1 ? '' : 's'}` : '']
    .filter(Boolean).join(' · '),
});

/**
 * lineDiff(a, b): b against a by line — [{op: ' ' | '-' | '+', text}] (the
 * longest common subsequence; texts past DIFF_MAX lines each compare as two
 * blocks, all of a removed and all of b added).
 */
export const DIFF_MAX = 1500;
export function lineDiff(a, b) {
  const x = String(a ?? '').split('\n');
  const y = String(b ?? '').split('\n');
  if (x.length > DIFF_MAX || y.length > DIFF_MAX) return [...x.map((t) => ({ op: '-', text: t })), ...y.map((t) => ({ op: '+', text: t }))];
  const n = x.length; const m = y.length;
  const L = Array.from({ length: n + 1 }, () => new Uint16Array(m + 1));
  for (let i = n - 1; i >= 0; i--) for (let j = m - 1; j >= 0; j--) L[i][j] = x[i] === y[j] ? L[i + 1][j + 1] + 1 : Math.max(L[i + 1][j], L[i][j + 1]);
  const out = [];
  let i = 0; let j = 0;
  while (i < n && j < m) {
    if (x[i] === y[j]) { out.push({ op: ' ', text: x[i] }); i++; j++; } else if (L[i + 1][j] >= L[i][j + 1]) { out.push({ op: '-', text: x[i++] }); } else { out.push({ op: '+', text: y[j++] }); }
  }
  while (i < n) out.push({ op: '-', text: x[i++] });
  while (j < m) out.push({ op: '+', text: y[j++] });
  return out;
}

/** diffCount(d): how many lines a diff adds and removes. */
export const diffCount = (d) => ({ add: d.filter((l) => l.op === '+').length, del: d.filter((l) => l.op === '-').length });

class Wiki {
  constructor(app) {
    this.app = app;
    this.tabs = new Map(); // pid → the tab's state
    this.pages = new Map(); // `${pid}:${slug}` → {page, loading, err}: pages read by name (the native view's stacked screens)
    const pj = app.projects;
    if (pj) {
      const take = pj.take.bind(pj);
      pj.take = (ev) => { take(ev); this.take(ev); };
    }
  }

  changed() { this.app.emit('projects'); }

  /** state(pid): the tab's state — {pages, stats, dream, q, hits, loading, loaded, err, slug, page, view: page | history | edit, revs, rev, edit}. */
  state(pid) {
    let s = this.tabs.get(+pid);
    if (!s) {
      s = { pages: [], stats: null, dream: true, q: '', hits: null, loading: false, loaded: false, err: '', slug: null, page: null, view: 'page',
        revs: null, rev: null, prev: null, edit: null, busy: '', seq: 0 };
      this.tabs.set(+pid, s);
    }
    return s;
  }

  /** ensure(pid): the tab's state, read once when a view shows it. */
  ensure(pid) {
    const s = this.state(pid);
    if (!s.loaded && !s.loading && !s.err) this.load(pid);
    return s;
  }

  // load reads the pages (and the search's hits); the index opens first.
  async load(pid) {
    const s = this.state(pid);
    const seq = ++s.seq;
    s.loading = true;
    try {
      const r = await wikiApi(pid).list(s.q);
      if (seq !== s.seq) return;
      s.pages = (r && r.pages) || [];
      s.stats = (r && r.stats) || null;
      s.dream = !(r && r.dream === false);
      s.hits = s.q ? (r && r.hits) || [] : null;
      s.err = '';
      if (s.slug == null && !s.edit && s.pages.length) await this.open(pid, s.pages[0].slug, true);
      else if (s.slug != null && s.view !== 'edit') await this.open(pid, s.slug, true);
    } catch (e) { if (seq === s.seq) s.err = e.status === 404 && !s.loaded ? 'This project\'s backend has no wiki yet (an older version).' : e.message; }
    if (seq !== s.seq) return;
    s.loading = false;
    s.loaded = true;
    this.changed();
  }

  search(pid, q) { const s = this.state(pid); s.q = String(q || '').trim(); return this.load(pid); }

  // open shows a page (quiet: a read again, the view kept as it is).
  async open(pid, slug, quiet = false) {
    const s = this.state(pid);
    try {
      const r = await wikiApi(pid).get(slug);
      s.slug = slug;
      s.page = r && r.page;
      if (!quiet) { s.view = 'page'; s.revs = null; s.rev = null; s.prev = null; s.edit = null; }
      s.err = '';
    } catch (e) {
      if (e.status === 404 && quiet) { s.slug = null; s.page = null; s.view = 'page'; } else s.err = e.message;
    }
    if (!quiet) this.changed();
  }

  // history reads the open page's versions, and shows the newest one's change.
  async history(pid) {
    const s = this.state(pid);
    if (!s.slug) return;
    try {
      const r = await wikiApi(pid).history(s.slug);
      s.revs = (r && r.revisions) || [];
      s.view = 'history';
      s.err = '';
      if (s.revs.length) await this.showRev(pid, s.revs[0].version);
    } catch (e) { s.err = e.message; }
    this.changed();
  }

  // showRev reads version v and the one before it: the diff between them.
  async showRev(pid, v) {
    const s = this.state(pid);
    const api = wikiApi(pid);
    try {
      const [cur, prev] = await Promise.all([api.history(s.slug, v), v > 1 ? api.history(s.slug, v - 1).catch(() => null) : null]);
      s.rev = cur && cur.revision;
      s.prev = prev && prev.revision;
      s.err = '';
    } catch (e) { s.err = e.message; }
    this.changed();
  }

  /** diff(pid): the shown version against the one before it (none before: all added). */
  diff(pid) {
    const s = this.state(pid);
    if (!s.rev) return [];
    return lineDiff(s.prev ? s.prev.body : '', s.rev.body);
  }

  // edit opens the editor: the open page at its version, or a new page (slug null).
  edit(pid, slug = undefined) {
    const s = this.state(pid);
    const p = slug === null ? null : s.page;
    s.edit = p ? { slug: p.slug, isNew: false, title: p.title, body: p.body || '', note: '', base: p.version, err: '', conflict: '', mine: '' }
      : { slug: '', isNew: true, title: '', body: '', note: '', base: 0, err: '', conflict: '', mine: '' };
    s.view = 'edit';
    this.changed();
  }

  set(pid, key, value) { const s = this.state(pid); if (s.edit) { s.edit[key] = value; if (key === 'title' && s.edit.isNew && !s.edit.slugTouched) s.edit.slug = slugOf(value); if (key === 'slug') s.edit.slugTouched = true; } this.changed(); }

  cancel(pid) { const s = this.state(pid); s.edit = null; s.view = 'page'; this.changed(); }

  // save PUTs the page at the version the edit began at: a 409 keeps the
  // editor with the conflict's words (Reload reads the page again).
  async save(pid) {
    const s = this.state(pid);
    const e = s.edit;
    if (!e || s.busy) return false;
    if (!SLUG.test(e.slug)) { e.err = 'The page needs a slug: lowercase letters, digits and dashes.'; this.changed(); return false; }
    if (!e.body.trim()) { e.err = 'The page needs some text (Archive retires a page).'; this.changed(); return false; }
    s.busy = 'save';
    e.err = '';
    this.changed();
    try {
      const r = await wikiApi(pid).put(e.slug, { title: e.title, body: e.body, note: e.note, baseVersion: e.base });
      s.busy = '';
      s.edit = null;
      s.slug = r.page.slug;
      s.page = r.page;
      s.view = 'page';
      await this.load(pid);
      return true;
    } catch (x) {
      s.busy = '';
      if (x.status === 409 && x.refusal === 'conflict') { e.conflict = x.message; e.current = x.data && x.data.current; } else e.err = x.message;
      this.changed();
      return false;
    }
  }

  // reload reads the page as it is now into the editor, at its version;
  // what was typed is kept beside it (mine) to merge from.
  async reload(pid) {
    const s = this.state(pid);
    const e = s.edit;
    if (!e) return;
    try {
      const r = await wikiApi(pid).get(e.slug);
      const p = r && r.page;
      e.mine = e.body;
      e.body = p.body || '';
      e.title = p.title;
      e.base = p.version;
      e.isNew = false;
      e.conflict = '';
      e.err = '';
      s.page = p;
      s.slug = p.slug;
    } catch (x) { e.err = x.message; }
    this.changed();
  }

  // archive retires the open page (at the version shown: a conflict says so).
  async archive(pid) {
    const s = this.state(pid);
    if (!s.page || s.busy) return false;
    s.busy = 'archive';
    this.changed();
    try {
      await wikiApi(pid).archive(s.page.slug, s.page.version);
      s.busy = '';
      s.slug = null;
      s.page = null;
      s.view = 'page';
      await this.load(pid);
      return true;
    } catch (x) {
      s.busy = '';
      s.err = x.status === 409 ? `${x.message} (Reload the page first.)` : x.message;
      this.changed();
      return false;
    }
  }

  /** peek(pid, slug): a page read by name, once (and again on a `wiki` event) — {page, loading, err}. */
  peek(pid, slug) {
    const key = `${+pid}:${slug}`;
    let c = this.pages.get(key);
    if (!c) {
      c = { page: null, loading: true, err: '' };
      this.pages.set(key, c);
      this.readPage(+pid, slug, c);
    }
    return c;
  }

  async readPage(pid, slug, c) {
    c.loading = true;
    try {
      const r = await wikiApi(pid).get(slug);
      c.page = r && r.page;
      c.err = '';
    } catch (e) { c.err = e.status === 404 ? 'This page is gone (archived or never written).' : e.message; }
    c.loading = false;
    this.changed();
  }

  forget(pid) { this.tabs.delete(+pid); }

  // take: a `project` event about the wiki reads the tab again (a held one only).
  take(ev) {
    if (!ev || ev.type !== 'project') return;
    const d = ev.data || {};
    if (d.change !== 'wiki') return;
    const s = this.tabs.get(+d.id);
    if (s && s.loaded) this.load(+d.id);
    for (const [key, c] of this.pages) if (key.startsWith(`${+d.id}:`)) this.readPage(+d.id, key.slice(key.indexOf(':') + 1), c);
  }
}

const wikis = new WeakMap();
/** projectWiki(app): the app's wiki store, made on first use. */
export function projectWiki(app) {
  let w = wikis.get(app);
  if (!w) { w = new Wiki(app); wikis.set(app, w); }
  return w;
}
