// ws/panels.js — the workspace agent's page beside its conversation: the
// session switcher, the empty state with its starters, the notebook, and
// the banners of the page's states (docs/workspace-agent.md §The agent's
// page). Views only: the page (ws/page.js) holds the state and does the
// calls.
import { html, nothing } from '/vendor/lit-all.min.js';
import { sessionRow, day, pageTag, pageClosed, W } from './words.js';

const icon = (name) => (name ? html`<bx-icon name=${name} aria-hidden="true"></bx-icon>` : nothing);

// ---- sessions ------------------------------------------------------------------------------

/**
 * sessionsView(s, act): the switcher — {list, current, renaming, older}:
 * Main first and always there, the others with their name, scope, model
 * and status; New session…, Rename, Archive (not Main); archived ones fold
 * under Older.
 */
export function sessionsView(s, act) {
  const now = Date.now();
  const live = s.list.filter((x) => !x.archivedAt || x.id === s.current);
  const old = s.list.filter((x) => x.archivedAt && x.id !== s.current);
  const cur = s.list.find((x) => x.id === s.current);
  const row = (x) => {
    const w = sessionRow(x, now);
    const on = x.id === s.current;
    return html`<li class="wsp-srow ${on ? 'on' : ''}" role="option" aria-selected=${on ? 'true' : 'false'} data-session=${x.id}>
      ${s.renaming === x.id ? html`<input class="wsp-rename" aria-label="the session’s name" .value=${w.name}
          @keydown=${(e) => { if (e.key === 'Enter') act.rename(x.id, e.target.value); if (e.key === 'Escape') { e.stopPropagation(); act.renaming(null); } }}
          @blur=${(e) => act.rename(x.id, e.target.value)}>`
        : html`<button class="wsp-sbtn" @click=${() => act.pick(x.id)}>
          <span class="wsp-sname"><b>${w.name}</b>${w.status.word ? html`<span class="wsp-sst ${w.status.tone}">${icon(w.status.glyph)}${w.status.word}</span>` : nothing}</span>
          <span class="wsc-muted">${w.scope}${w.main ? ' · always here' : ''}</span></button>`}
    </li>`;
  };
  return html`<div class="wsp-pop wsp-sessions" role="dialog" aria-label="Sessions" @keydown=${(e) => act.key(e)}>
    <ul class="wsp-slist" role="listbox" aria-label="sessions">${live.map(row)}</ul>
    ${old.length ? html`<details class="wsp-older"><summary>${icon('caret-right')}Older (${old.length})</summary><ul class="wsp-slist" role="listbox" aria-label="older sessions">${old.map(row)}</ul></details>` : nothing}
    <div class="wsp-sacts">
      <button @click=${() => act.create()}>${icon('plus')}New session…</button>
      <button class="quiet" ?disabled=${!cur} @click=${() => act.renaming(s.current)}>Rename</button>
      ${cur && cur.archivedAt ? html`<button class="quiet" @click=${() => act.archive(cur.id, false)}>Restore</button>`
        : html`<button class="quiet" ?disabled=${!cur || cur.main} title=${cur && cur.main ? 'Main is always here: it can be renamed, not archived' : ''} @click=${() => act.archive(cur.id, true)}>Archive</button>`}
    </div>
    ${s.err ? html`<div class="wsc-err" role="alert">${s.err}</div>` : nothing}
    <p class="wsp-foot">${W.sessionsFoot}</p>
  </div>`;
}

// ---- the empty state -------------------------------------------------------------------------

/**
 * emptyView(e, act): what a new session shows until its first message —
 * one heading, one sentence, what the agent can and can't do in this
 * session's own numbers, and starters built from the person's own tiles
 * (GET /ws/empty). A starter fills the composer; one with a blank puts the
 * cursor in it.
 */
export function emptyView(e, act) {
  if (!e) return html`<div class="wsp-empty wsc-muted" role="status">Loading…</div>`;
  return html`<section class="wsp-empty" aria-labelledby="wsp-empty-h">
    <h2 id="wsp-empty-h">${e.heading}</h2>
    <p>${e.line}</p>
    <div class="wsp-cols">
      <div><h3>It can</h3><ul class="can">${(e.can || []).map((x) => html`<li>${icon('check')}<span>${x}</span></li>`)}</ul></div>
      <div><h3>It can’t</h3><ul class="cant">${(e.cant || []).map((x) => html`<li>${icon('xmark')}<span>${x}</span></li>`)}</ul></div>
    </div>
    ${(e.starters || []).length ? html`<h3>${e.startersTitle}</h3>
      <ul class="wsp-starters">${e.starters.map((s) => html`<li><button class="wsp-starter" data-kind=${s.kind} @click=${() => act.starter(s)}>
        <span class="wsp-kind">${s.kind}</span><span>${s.text}</span></button></li>`)}</ul>` : nothing}
    ${e.startersError ? html`<p class="wsc-muted">${e.startersError}</p>` : nothing}
  </section>`;
}

// ---- the notebook ------------------------------------------------------------------------------

/**
 * notebookView(n, act): "what the agent remembers" — {pages, open, page,
 * editing, draft, confirm, err, busy}: the list (a page this session may
 * not read is listed, closed, with the reason), one page to read, edit or
 * delete, and Add a note.
 */
export function notebookView(n, act) {
  if (n.open) return pageView(n, act);
  const pages = n.pages || [];
  return html`<section class="wsp-nb" aria-labelledby="wsp-nb-h">
    <div class="wsp-nb-h"><button class="quiet" aria-label="back to the conversation" @click=${() => act.close()}>${icon('chevron-left')}</button>
      <h2 id="wsp-nb-h">Notebook</h2><span class="wsc-muted">what the agent remembers · ${pages.length} page${pages.length === 1 ? '' : 's'}</span>
      <span class="grow"></span><button @click=${() => act.add()}>${icon('plus')}Add a note</button></div>
    ${n.err ? html`<div class="wsc-err" role="alert">${n.err}</div>` : nothing}
    <ul class="wsp-pages">${pages.map((p) => {
      const c = pageClosed(p);
      const meta = [p.summary, [p.updatedBy ? `by ${p.updatedBy}` : '', day(p.updatedMs)].filter(Boolean).join(' ')].filter(Boolean).join(' · ');
      return html`<li class="wsp-pg ${c.closed ? 'closed' : ''}" data-slug=${p.slug}>
        <button ?disabled=${c.closed} aria-disabled=${c.closed ? 'true' : 'false'} @click=${() => act.read(p.slug)}>
          ${icon('doc')}<span class="wsp-pgt"><b>${p.title || p.slug}</b><span class="wsc-muted">${c.closed ? c.why : meta}</span></span>
          <span class="wsp-tag ${c.closed ? 'warn' : ''}">${c.closed ? icon('lock') : nothing}${pageTag(p)}</span></button></li>`;
    })}</ul>
    ${pages.length ? nothing : html`<p class="wsc-muted">Nothing yet. The agent writes here what should outlast a session; so can you.</p>`}
    <p class="wsp-foot">${W.notebookFoot}</p>
  </section>`;
}

function pageView(n, act) {
  const p = n.page;
  const fresh = n.open === '+';
  const editing = n.editing || fresh;
  const d = n.draft || {};
  return html`<section class="wsp-nb" aria-labelledby="wsp-pg-h">
    <div class="wsp-nb-h"><button class="quiet" aria-label="back to the notebook" @click=${() => act.list()}>${icon('chevron-left')}</button>
      <h2 id="wsp-pg-h">${fresh ? 'Add a note' : p ? p.title || p.slug : 'Notebook'}</h2>
      <span class="grow"></span>
      ${p && !editing ? html`<button @click=${() => act.edit()}>${icon('pencil')}Edit</button>
        <button class="danger" @click=${() => act.confirm(true)}>${icon('trash')}Delete</button>` : nothing}</div>
    ${n.err ? html`<div class="wsc-err" role="alert">${n.err}</div>` : nothing}
    ${n.confirm === true ? html`<div class="wsp-confirm" role="alertdialog" aria-label="Delete this page?">
      <span>Delete this page? The agent forgets it; its history is kept.</span>
      <button class="danger" @click=${() => act.drop()}>Delete</button><button @click=${() => act.confirm(false)}>Keep</button></div>` : nothing}
    ${editing ? html`<div class="wsp-edit">
        <label>Title<input .value=${d.title ?? (p ? p.title : '')} @input=${(e) => { d.title = e.target.value; }}></label>
        <label>Text<textarea rows="12" .value=${d.body ?? (p ? p.body || '' : '')} @input=${(e) => { d.body = e.target.value; }}></textarea></label>
        <div class="wsc-btns"><button class="primary" ?disabled=${n.busy} @click=${() => act.save()}>Save</button><button ?disabled=${n.busy} @click=${() => act.cancel()}>Cancel</button></div>
      </div>`
      : p ? html`<div class="wsp-pmeta wsc-muted">${[p.updatedBy ? `edited by ${p.updatedBy}` : '', day(p.updatedMs), p.tile ? `about ${p.tile}${p.rev ? ' at ' + p.rev : ''}` : '', pageTag(p)].filter(Boolean).join(' · ')}
</div>
        ${(p.orgs || []).length ? html`<p class="wsp-marks wsc-muted" data-marks>${W.marksNote(p.orgs)}</p>` : nothing}
        <div class="wsp-pbody bxc-md" .innerHTML=${act.md(p.body || '')}></div>` : html`<div class="wsc-muted" role="status">Loading…</div>`}
  </section>`;
}

// ---- banners ------------------------------------------------------------------------------------

/** bannersView(list): the page's states, each a glyph, its words and what still works: [{key, tone, icon, bold, text, button: {label, act}}]. */
export function bannersView(list) {
  return (list || []).map((b) => html`<div class="wsp-banner ${b.tone || ''}" data-state=${b.key} role=${b.tone === 'bad' || b.tone === 'warn' ? 'alert' : 'status'}>
    ${icon(b.icon)}<div class="wsp-bt"><b>${b.bold}</b> ${b.text || ''}${b.more ? html`<div class="wsc-muted">${b.more}</div>` : nothing}</div>
    ${[...(b.button ? [b.button] : []), ...(b.buttons || [])].map((x) => html`<button data-act=${x.key || nothing} @click=${x.act}>${x.label}</button>`)}
  </div>`);
}
