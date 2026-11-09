// project-repos.js — the new-project wizard's repo picker on the web (API.md
// §Projects in the UI, "A new project"): an ARIA combobox over the repos
// you can reach through the form's provider (GET /projects/scm/repos). It
// opens on focus with your repos (the first page), filters as you type (a
// search 250 ms after the last keystroke: app.projects.setRepoQuery; the
// latest answer wins), and ends with "more" while there is a next page.
// Each option says owner/name, private, your permission and archived — an
// archived repo is disabled, saying why. Choosing one adds it at once
// (no Add step); the list stays open for the next. The chosen repos are
// rows below it, each removable, its setup script a folded "setup script
// (optional)". Keys: ↓/↑ move, Home/End, Enter chooses, Escape closes (a
// second Escape clears the words), Tab leaves.
import { html, nothing } from '/vendor/lit-all.min.js';
import { repoSlug } from './model/projects.js';

// the picker's own state (open, the active option), per form
const views = new WeakMap();
const viewOf = (f) => { let v = views.get(f); if (!v) { v = { open: false, active: -1 }; views.set(f, v); } return v; };

// options: what the list offers, in order — each repo, then "more"
function optionsOf(f) {
  const out = f.results.map((r) => {
    const name = `${r.owner}/${r.name}`;
    const added = f.repos.some((x) => x.repo === name);
    return { key: name, r, added, why: r.archived ? 'archived: read-only, a task can\'t push to it' : added ? 'chosen' : '' };
  });
  if (f.next && !f.loading) out.push({ key: '', more: true });
  return out;
}
const usable = (o) => o.more || !o.why;

/** repoPickerTpl(p, f): the combobox and the chosen repos below it. */
export function repoPickerTpl(p, f) {
  const v = viewOf(f);
  const opts = optionsOf(f);
  if (v.active >= opts.length) v.active = opts.length - 1;
  const paint = () => p.changed();
  const open = () => { if (!v.open) { v.open = true; v.active = -1; if (!f.results.length && !f.loading) p.searchRepos(); paint(); } };
  const close = () => { if (v.open) { v.open = false; v.active = -1; paint(); } };
  const choose = (o) => {
    if (!o) return;
    if (o.more) { p.searchRepos(true); return; }
    if (o.why) return;
    p.addFormRepo(o.key);
    if (f.q) p.setRepoQuery(''); // the next pick starts from all your repos again
  };
  const move = (d) => {
    if (!opts.length) return;
    let i = v.active < 0 && d < 0 ? opts.length : v.active; // ↑ with none active: the last one
    for (let n = 0; n < opts.length; n++) {
      i = d === 'first' ? n : d === 'last' ? opts.length - 1 - n : (i + d + opts.length) % opts.length;
      if (usable(opts[i])) break;
    }
    v.active = i;
    paint();
    requestAnimationFrame(() => document.getElementById(`pn-opt-${i}`)?.scrollIntoView({ block: 'nearest' }));
  };
  const key = (e) => {
    switch (e.key) {
      case 'ArrowDown': e.preventDefault(); if (!v.open) open(); if (!e.altKey) move(1); break;
      case 'ArrowUp': e.preventDefault(); if (!v.open) open(); move(-1); break;
      case 'Home': if (v.open) { e.preventDefault(); move('first'); } break;
      case 'End': if (v.open) { e.preventDefault(); move('last'); } break;
      case 'Enter': e.preventDefault(); if (v.open && v.active >= 0) choose(opts[v.active]); else open(); break;
      case 'Escape': if (v.open) { e.preventDefault(); e.stopPropagation(); close(); } else if (f.q) { e.preventDefault(); p.setRepoQuery(''); } break;
      case 'Tab': close(); break;
    }
  };
  const activeId = v.open && v.active >= 0 ? `pn-opt-${v.active}` : '';
  return html`<div class="pncombo">
      <input type="text" id="pn-q" role="combobox" autocomplete="off" spellcheck="false" aria-autocomplete="list" aria-haspopup="listbox"
        aria-expanded=${v.open ? 'true' : 'false'} aria-controls="pn-list" aria-activedescendant=${activeId || nothing} aria-describedby="pn-q-hint"
        placeholder="Find a repo you can reach…" .value=${f.q}
        @focus=${open} @click=${open} @keydown=${key}
        @input=${(e) => { v.open = true; v.active = -1; p.setRepoQuery(e.target.value); }}
        @blur=${() => setTimeout(() => { if (document.activeElement && document.activeElement.id === 'pn-q') return; close(); }, 120)}>
      <div class="muted small" id="pn-q-hint">${f.repos.length ? 'Choose another to add it too.' : 'Choose a repo to add it; you can add several.'}</div>
      <ul class="pnlist" id="pn-list" role="listbox" aria-label="Repos you can reach" ?hidden=${!v.open} @mousedown=${(e) => e.preventDefault()}>
        ${!v.open ? nothing : opts.map((o, i) => (o.more
          ? html`<li role="option" id=${`pn-opt-${i}`} class="pnopt more" data-more aria-selected=${i === v.active ? 'true' : 'false'} @click=${() => choose(o)}>more…</li>`
          : html`<li role="option" id=${`pn-opt-${i}`} class="pnopt" data-repo=${o.key} aria-selected=${i === v.active ? 'true' : 'false'}
              aria-disabled=${o.why ? 'true' : 'false'} title=${o.why && !o.added ? o.why : nothing} @click=${() => choose(o)} @mousemove=${() => { if (v.active !== i) { v.active = i; paint(); } }}>
              <span class="pnck">${o.added ? html`<bx-icon name="check" label="chosen"></bx-icon>` : nothing}</span>
              <span class="mono pnname">${o.key}</span>
              ${o.r.private ? html`<span class="badge">private</span>` : nothing}
              ${o.r.permission ? html`<span class="muted small">${o.r.permission}</span>` : nothing}
              ${o.r.archived ? html`<span class="badge">archived</span><span class="muted small pnwhy">read-only: a task can't push to it</span>` : nothing}
            </li>`))}
        ${!v.open ? nothing : f.loading ? html`<li class="pnnote muted small" role="presentation">loading…</li>`
          : f.err ? html`<li class="pnnote err" role="presentation">${f.err}</li>`
          : !f.results.length ? html`<li class="pnnote muted small" role="presentation">${f.q ? `No repo you can reach matches “${f.q}”.` : 'No repos found.'}</li>` : nothing}
      </ul>
      <div class="sr" aria-live="polite">${v.open && !f.loading ? `${f.results.length} repo${f.results.length === 1 ? '' : 's'}${f.next ? ', more to load' : ''}` : ''}</div>
    </div>
    ${f.repos.length ? html`<ul class="pnrepos" aria-label="Chosen repos">${f.repos.map((r) => html`<li class="pnrepo" data-repo=${r.repo}>
        <div class="pnrh"><b class="mono">${r.repo}</b><span class="muted small">→ ${repoSlug(r.repo)}</span><span style="flex:1"></span>
          <button type="button" class="btn ghost btnsm icon" title=${`Remove ${r.repo}`} aria-label=${`Remove ${r.repo}`} @click=${() => p.dropFormRepo(r.repo)}><bx-icon name="xmark"></bx-icon></button></div>
        <details class="pnsetup" ?open=${!!r.setup}><summary>setup script (optional)${r.setup ? html` <span class="muted small">— set</span>` : nothing}</summary>
          <textarea rows="2" class="mono" aria-label=${`Setup script for ${r.repo}`} placeholder="run in each task's checkout first, e.g. npm ci" .value=${r.setup}
            @input=${(e) => p.setFormSetup(r.repo, e.target.value)}></textarea></details></li>`)}</ul>` : nothing}`;
}

/** closeRepoPicker(f): the list closes (a step change). */
export function closeRepoPicker(f) { if (f) { const v = viewOf(f); v.open = false; v.active = -1; } }

export const repoPickerCss = `
  .projs-page .pncombo { position: relative; }
  .projs-page .pnlist[hidden] { display: none; }
  .projs-page .pnlist { position: absolute; z-index: 20; left: 0; right: 0; top: 34px; max-height: 280px; overflow: auto; margin: 0; padding: 4px 0;
    list-style: none; background: var(--bx-panel); border: 1px solid var(--bx-border-strong); border-radius: var(--bx-radius);
    box-shadow: var(--bx-shadow-pop); }
  .projs-page .pnopt { display: flex; gap: 8px; align-items: center; min-height: var(--bx-row); padding: 4px 10px; cursor: pointer; min-width: 0; }
  .projs-page .pnopt[aria-selected="true"] { background: var(--bx-hover); box-shadow: inset 2px 0 0 var(--bx-accent); }
  .projs-page .pnopt[aria-disabled="true"] { cursor: default; color: var(--bx-muted); }
  .projs-page .pnopt .pnname { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; min-width: 0; }
  .projs-page .pnopt .pnck { width: 16px; flex: none; display: inline-flex; color: var(--bx-ok); }
  .projs-page .pnopt .pnwhy { white-space: nowrap; overflow: hidden; text-overflow: ellipsis; min-width: 0; }
  .projs-page .pnopt.more { color: var(--bx-link); padding-left: 34px; }
  .projs-page .pnnote { padding: 4px 10px 4px 34px; }
  .projs-page .pnrepos { display: grid; gap: 6px; margin: 8px 0 0; padding: 0; list-style: none; }
  .projs-page .pnrepo { border: 1px solid var(--bx-border); border-radius: var(--bx-radius); padding: 6px 8px; background: var(--bx-panel); }
  .projs-page .pnrh { display: flex; gap: 8px; align-items: center; min-width: 0; }
  .projs-page .pnrh b { overflow-wrap: anywhere; }
  .projs-page .pnsetup summary { cursor: pointer; color: var(--bx-muted); font-size: 12px; margin-top: 2px; }
  .projs-page .pnsetup textarea { margin-top: 4px; width: 100%; box-sizing: border-box; }
  .projs-page .sr { position: absolute; width: 1px; height: 1px; overflow: hidden; clip: rect(0 0 0 0); white-space: nowrap; }
`;
