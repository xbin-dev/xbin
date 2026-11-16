/**
 * agent-signin-list.js — <bx-agent-signin>'s list face and the element's
 * styles (D230; docs/agent-signins.md): a person's saved sign-ins per
 * coding agent — what each is, when it was saved and used, rename, make
 * default, remove, add one — where they were used, and for workspace
 * admins the workspace's switch and who has sign-ins (that, never what).
 * Templates over the element's state; agent-signin.js owns the element.
 */
import { html, css, nothing } from '/vendor/lit-all.min.js';
import '/vendor/bx-icons.js';
import { whyNot, kindWords, dayWords, expiryWords, nextStep } from '/vendor/agent-signins.js';

/** manageInit(): the list's own state. */
export const manageInit = () => ({ busy: '', err: '', renaming: '', removing: '', adding: '', uses: null, people: null, peopleErr: '' });

// D184: signed out is a warning (its glyph, words and tint); the step to
// take is the primary button (the accent, its ink); the link and the code
// are in the code face; controls 28px with the focus ring. The list is a
// panel: rows that wrap at phone width, one action per button.
export const signinCss = css`
  :host { display: block; margin-bottom: 8px; font: var(--bx-font, 13px/18px "Instrument Sans", system-ui, sans-serif); color: var(--bx-text, #E9EAF0); }
  :host([view="button"]), :host([view="name"]) { display: inline; margin: 0; }
  :host([view="manage"]) { margin: 0; }
  .box { border: 1px solid var(--bx-warn, #F2994A); border-radius: var(--bx-radius, 2px); background: var(--bx-warn-bg, #382F2C); padding: 8px 12px;
    display: flex; flex-direction: column; gap: 8px; }
  .box[data-phase="done"] { border-color: var(--bx-ok, #A3CF5E); background: var(--bx-ok-bg, #2F352E); }
  .row { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
  .row form { display: contents; }
  form.paste { display: flex; flex-direction: column; gap: 8px; }
  .msg { flex: 1; min-width: 0; }
  .msg bx-icon { color: var(--bx-warn, #F2994A); margin-right: 6px; }
  :focus-visible { outline: var(--bx-focus-outline, 3px solid #3DD6F5); outline-offset: var(--bx-focus-offset, 2px);
    box-shadow: var(--bx-focus-halo, 0 0 0 2px #0B0C12); }
  .go, button, a.go { display: inline-flex; align-items: center; gap: 6px; box-sizing: border-box; min-height: var(--bx-control-h, 28px); padding: 0 11px;
    border: 1px solid var(--bx-border-strong, #666A7E); border-radius: var(--bx-radius, 2px); font: inherit; font-weight: 600; white-space: nowrap; cursor: pointer; }
  button { background: var(--bx-panel, #1F2028); color: var(--bx-text, #E9EAF0); }
  button:hover:not([disabled]) { background: var(--bx-hover, #2A2B34); }
  .go, button.go { background: var(--bx-accent, #8C9BFF); border-color: var(--bx-accent, #8C9BFF); color: var(--bx-accent-ink, #0B0C12); text-decoration: none; }
  .go:hover, button.go:hover:not([disabled]) { background: var(--bx-accent-hover, #A9B4FF); border-color: var(--bx-accent-hover, #A9B4FF); }
  button.danger { border-color: var(--bx-danger, #FF7A7A); color: var(--bx-danger, #FF7A7A); }
  button[disabled] { opacity: .5; cursor: default; }
  button.link { min-height: 0; border: 0; padding: 0; background: none; color: var(--bx-muted, #A3A6B6); font-weight: 400; text-decoration: underline; }
  button.link:hover:not([disabled]) { background: none; color: var(--bx-text, #E9EAF0); }
  input { flex: 1; min-width: 12em; box-sizing: border-box; min-height: var(--bx-control-h, 28px);
    background: var(--bx-panel, #1F2028); color: var(--bx-text, #E9EAF0); border: 1px solid var(--bx-border-strong, #666A7E);
    border-radius: var(--bx-radius, 2px); padding: 0 8px; font: var(--bx-font-code, 400 12px/18px "JetBrains Mono", ui-monospace, monospace); }
  input::placeholder { color: var(--bx-subtle, #8E91A2); opacity: 1; font: var(--bx-font, 13px/18px "Instrument Sans", system-ui, sans-serif); }
  input.url { color: var(--bx-muted, #A3A6B6); }
  input.name, input.rename { font: inherit; }
  .status { color: var(--bx-muted, #A3A6B6); white-space: pre-wrap; overflow-wrap: anywhere; }
  .status.ok { color: var(--bx-ok, #A3CF5E); }
  .status.bad { color: var(--bx-danger, #FF7A7A); }
  .status bx-icon { margin-right: 6px; }
  .hint, .muted { color: var(--bx-muted, #A3A6B6); overflow-wrap: anywhere; }
  .links { gap: 4px 16px; }
  code { font: var(--bx-font-code, 400 12px/18px "JetBrains Mono", ui-monospace, monospace); }

  .scrim { position: fixed; inset: 0; z-index: 4000; background: var(--bx-scrim, rgba(0, 0, 0, 0.55)); display: flex; align-items: flex-start; justify-content: center; padding: 6vh 8px 8px; box-sizing: border-box; }
  .dialog:focus { outline: none; }
  .dialog { width: min(680px, 100%); max-height: 100%; overflow: auto; box-sizing: border-box; background: var(--bx-panel, #1F2028);
    border: 1px solid var(--bx-border-strong, #666A7E); border-radius: var(--bx-radius, 2px); box-shadow: var(--bx-shadow-pop, 0 12px 32px rgba(0, 0, 0, 0.6)); }
  .list { display: flex; flex-direction: column; gap: 12px; padding: 16px; }
  .list h3 { margin: 0; flex: 1; font: var(--bx-font-title, 600 16px/22px "Instrument Sans", system-ui, sans-serif); }
  .list h4 { margin: 0; font: inherit; font-weight: 600; }
  .list .lead { color: var(--bx-muted, #A3A6B6); margin: 0; }
  .banner { border: 1px solid var(--bx-warn, #F2994A); background: var(--bx-warn-bg, #382F2C); border-radius: var(--bx-radius, 2px); padding: 8px 12px; display: flex; gap: 8px; align-items: center; flex-wrap: wrap; }
  .banner span { flex: 1; min-width: 12em; }
  .banner.bad { border-color: var(--bx-danger, #FF7A7A); background: var(--bx-danger-bg, #3A2B32); }
  section { display: flex; flex-direction: column; gap: 6px; border-top: 1px solid var(--bx-border, #33353F); padding-top: 12px; }
  .item { display: flex; align-items: center; gap: 4px 8px; flex-wrap: wrap; padding: 4px 0; }
  .item .what { flex: 1; min-width: 14em; display: flex; flex-direction: column; }
  .item .name { font-weight: 600; overflow-wrap: anywhere; }
  .item .meta { color: var(--bx-muted, #A3A6B6); font: var(--bx-font-meta, 400 12px/16px "Instrument Sans", system-ui, sans-serif); display: flex; flex-wrap: wrap; gap: 0 12px; }
  .item .meta .warn { color: var(--bx-warn, #F2994A); }
  .item .meta .bad { color: var(--bx-danger, #FF7A7A); }
  .badge { display: inline-block; margin-left: 6px; padding: 0 6px; border: 1px solid var(--bx-border-strong, #666A7E); border-radius: var(--bx-radius, 2px);
    font: var(--bx-font-meta, 400 12px/16px "Instrument Sans", system-ui, sans-serif); color: var(--bx-muted, #A3A6B6); }
  .acts { display: flex; gap: 6px; flex-wrap: wrap; }
  .confirm { border: 1px solid var(--bx-danger, #FF7A7A); background: var(--bx-danger-bg, #3A2B32); border-radius: var(--bx-radius, 2px); padding: 8px 12px; display: flex; gap: 8px; flex-wrap: wrap; align-items: center; }
  .confirm span { flex: 1; min-width: 14em; }
  table { border-collapse: collapse; width: 100%; }
  th, td { text-align: left; padding: 4px 8px 4px 0; vertical-align: top; overflow-wrap: anywhere; }
  th { font-weight: 400; color: var(--bx-muted, #A3A6B6); font: var(--bx-font-meta, 400 12px/16px "Instrument Sans", system-ui, sans-serif); }
  .scroll-x { overflow-x: auto; }
  table.people { min-width: 520px; }
  table.people th, table.people td { overflow-wrap: normal; white-space: nowrap; }
  label.switch { display: flex; gap: 8px; align-items: center; cursor: pointer; }
  label.switch input { flex: none; min-width: 0; min-height: 0; width: 16px; height: 16px; }
`;

const set = (el, patch) => { el._m = { ...el._m, ...patch }; };

// run one change of the list, then read it again
async function act(el, key, fn) {
  set(el, { busy: key, err: '' });
  try {
    await fn();
    set(el, { busy: '', renaming: '', removing: '' });
    await el.reload();
    el.dispatchEvent(new CustomEvent('bx-signins-changed', { bubbles: true, composed: true }));
  } catch (e) {
    set(el, { busy: '', err: [e.message || String(e), nextStep(e)].filter(Boolean).join(' ') });
  }
}

function meta(sg, now) {
  const exp = expiryWords(sg, now);
  return html`<span>${kindWords(sg.kind)}</span><span>saved ${dayWords(sg.createdAt, now)}</span><span>${sg.lastUsedAt ? `used ${dayWords(sg.lastUsedAt, now)}` : 'not used yet'}</span>${
    exp ? html`<span class=${exp.startsWith('expired') ? 'bad' : 'warn'}>${exp}</span>` : nothing}${
    sg.refusedAt ? html`<span class="bad" title=${sg.refused || ''}>refused ${dayWords(sg.refusedAt, now)}: sign in again</span>` : nothing}`;
}

function item(el, h, sg, now, canChange) {
  const m = el._m, busy = !!m.busy;
  if (m.renaming === sg.id) {
    const save = (e) => { e.preventDefault(); const v = e.currentTarget.querySelector('input').value.trim(); if (v) act(el, sg.id, () => el.api.rename(sg.id, v)); };
    return html`<form class="item" @submit=${save} @keydown=${(e) => { if (e.key === 'Escape') { e.stopPropagation(); set(el, { renaming: '' }); } }}>
      <input class="rename" maxlength="40" aria-label="the sign-in's name" .value=${sg.name} ?disabled=${busy}>
      <span class="acts"><button class="go" type="submit" ?disabled=${busy}>Save</button><button type="button" @click=${() => set(el, { renaming: '' })}>Cancel</button></span>
    </form>`;
  }
  if (m.removing === sg.id) {
    return html`<div class="confirm" data-id=${sg.id}>
      <span>Remove <b>${sg.name}</b>? Coding agents running on it are ended. The ${kindWords(sg.kind)} itself stays valid at ${h.name}'s provider until it expires or you revoke it there.</span>
      <span class="acts"><button class="danger remove-yes" ?disabled=${busy} @click=${() => act(el, sg.id, () => el.api.remove(sg.id))}>Remove</button><button @click=${() => set(el, { removing: '' })}>Cancel</button></span>
    </div>`;
  }
  return html`<div class="item" data-id=${sg.id}>
    <span class="what"><span class="name">${sg.name}${sg.default ? html`<span class="badge">default</span>` : nothing}</span><span class="meta">${meta(sg, now)}</span></span>
    <span class="acts">
      ${sg.default || !canChange ? nothing : html`<button class="make-default" ?disabled=${busy} @click=${() => act(el, sg.id, () => el.api.makeDefault(sg.id))} title="new sessions of ${h.name} use this one">Make default</button>`}
      <button class="rename-it" ?disabled=${busy} @click=${() => { set(el, { renaming: sg.id, removing: '' }); el.updateComplete.then(() => el.renderRoot.querySelector('input.rename')?.select()); }}>Rename</button>
      <button class="remove-it" ?disabled=${busy} @click=${() => set(el, { removing: sg.id, renaming: '' })}>Remove</button>
    </span>
  </div>`;
}

function harnessSection(el, h, now, canChange) {
  const m = el._m, n = h.signins.length;
  const none = h.guided
    ? `None saved. Sign in once and xbind uses it for your ${h.name} sessions.`
    : `None saved. ${h.name} takes a pasted key (${h.keys.map((k) => k.label).join(', or ')}); its own subscription login stays on the tile — ${h.login}.`;
  return html`<section data-harness=${h.id}>
    <div class="row"><h4>${h.name}</h4></div>
    ${n ? h.signins.map((sg) => item(el, h, sg, now, canChange)) : html`<p class="lead">${none}</p>`}
    ${m.adding === h.id
      ? html`<bx-agent-signin class="adding" harness=${h.id} provider=${h.name} data-add
          @bx-signin-done=${(e) => { e.stopPropagation(); set(el, { adding: '' }); el.reload(); el.dispatchEvent(new CustomEvent('bx-signins-changed', { bubbles: true, composed: true })); }}></bx-agent-signin>
          <div class="row"><button class="link" @click=${() => set(el, { adding: '' })}>Done adding</button></div>`
      : canChange && n < h.max ? html`<div class="row"><button class="add" @click=${() => set(el, { adding: h.id })}><bx-icon name="plus"></bx-icon>${h.guided ? 'Add a sign-in' : 'Add a key'}</button></div>` : nothing}
  </section>`;
}

async function loadUses(el) {
  if (el._m.uses) { set(el, { uses: null }); return; }
  try { const { uses } = await el.api.uses(); set(el, { uses: uses || [] }); } catch (e) { set(el, { err: e.message }); }
}

function usesSection(el, now) {
  const uses = el._m.uses;
  return html`<section class="uses">
    <div class="row"><button class="link show-uses" @click=${() => loadUses(el)}>${uses ? 'Hide where they were used' : 'Where they were used…'}</button></div>
    ${!uses ? nothing : !uses.length ? html`<p class="lead">No session has used a saved sign-in yet.</p>` : html`<div class="scroll-x"><table>
      <tr><th>When</th><th>Sign-in</th><th>Tile</th><th>Started by</th></tr>
      ${uses.slice(0, 50).map((u) => html`<tr><td>${dayWords(u.at, now)}</td><td>${u.name}</td><td><code>${u.tile}</code></td><td>${u.origin === 'agent' ? 'your workspace agent' : 'you'}</td></tr>`)}
    </table></div>`}
  </section>`;
}

async function loadPeople(el) {
  try { const p = await el.api.people(); set(el, { people: p, peopleErr: '' }); } catch (e) { set(el, { peopleErr: e.message }); }
}

function adminSection(el, d, now) {
  const m = el._m, pol = (m.people && m.people.policy) || d.policy || {};
  if (m.people === null && !m.peopleErr) loadPeople(el);
  const rows = (m.people?.people || []).flatMap((p) => p.signins.map((sg, i) => ({ p, sg, first: i === 0 })));
  const toggle = (e) => { const on = e.target.checked; act(el, 'policy', () => el.api.setPolicy(on)).then(() => loadPeople(el)); };
  return html`<section class="admin">
    <div class="row"><h4>For this workspace</h4><span class="badge">admins</span></div>
    <label class="switch"><input type="checkbox" class="policy" .checked=${!!pol.enabled} ?disabled=${!!pol.locked || m.busy === 'policy'} @change=${toggle}>
      <span>People may save coding-agent sign-ins${pol.locked ? html` <span class="muted">— ${pol.reason || 'set by your host'}</span>` : nothing}</span></label>
    <p class="lead">Off: none is used or saved; what people saved stays and works again when it is back on. You see that people have sign-ins — never their names for them, never a secret.</p>
    ${m.peopleErr ? html`<div class="status bad">${m.peopleErr}</div>` : nothing}
    ${!rows.length ? html`<p class="lead">${m.people ? 'Nobody has saved a sign-in.' : 'Loading…'}</p>` : html`<div class="scroll-x"><table class="people">
      <tr><th>Person</th><th>Coding agent</th><th>Kind</th><th>Saved</th><th>Last used</th><th></th></tr>
      ${rows.map(({ p, sg, first }) => html`<tr data-user=${p.user} data-id=${sg.id}>
        <td>${first ? html`<b>${p.user}</b> <span class="muted">(${p.total})</span>` : nothing}</td>
        <td>${(d.harnesses.find((h) => h.id === sg.harness) || {}).name || sg.harness}${sg.default ? html`<span class="badge">default</span>` : nothing}</td>
        <td>${kindWords(sg.kind)}</td><td>${dayWords(sg.createdAt, now)}</td><td>${dayWords(sg.lastUsedAt, now)}</td>
        <td><button class="revoke" ?disabled=${!!m.busy} title="remove it, end their sessions running on it, and tell them who did"
          @click=${() => act(el, sg.id, () => el.api.revoke(p.user, sg.id)).then(() => loadPeople(el))}>Revoke</button></td>
      </tr>`)}
    </table></div>`}
  </section>`;
}

/**
 * manageView(el, onClose): the list, over el's `_data` and `_m`. onClose
 * draws a Close button (the list in a dialog); null leaves it out.
 */
export function manageView(el, onClose) {
  const d = el._data, m = el._m, now = Date.now();
  const head = html`<div class="row"><h3>Coding-agent sign-ins</h3>${onClose ? html`<button class="close" @click=${onClose}>Close</button>` : nothing}</div>`;
  if (d === null) return html`<div class="list">${head}<p class="lead">Loading…</p></div>`;
  if (d === false) return html`<div class="list">${head}<div class="banner bad"><span>The saved sign-ins could not be read. Reload, or sign in to this workspace again.</span><button @click=${() => el.reload()}>Try again</button></div></div>`;
  const not = whyNot(d), canChange = !not;
  const hs = el.harness && el.view !== 'manage' ? d.harnesses.filter((h) => h.id === el.harness).concat(d.harnesses.filter((h) => h.id !== el.harness)) : d.harnesses;
  return html`<div class="list">
    ${head}
    <p class="lead">Sign in to a coding agent once. xbind keeps the sign-in sealed with the workspace vault — it is never shown again, to you or anyone — and uses it for the coding agents you start, on every tile only you can change. On a tile other people can change it is left out, and the session says so.</p>
    ${d.notice ? html`<div class="banner notice"><span>${d.notice.text}</span><button class="dismiss" @click=${() => act(el, 'notice', () => el.api.dismissNotice())}>OK</button></div>` : nothing}
    ${not ? html`<div class="banner why-not"><span>${not}</span></div>` : nothing}
    ${d.unisolated && !not ? html`<div class="banner"><span>This workspace runs without sandboxes (a development workspace): everything it runs could read a coding agent's sign-in.</span></div>` : nothing}
    ${m.err ? html`<div class="banner bad" role="alert"><span>${m.err}</span><button @click=${() => set(el, { err: '' })}>OK</button></div>` : nothing}
    ${d.available ? hs.map((h) => harnessSection(el, h, now, canChange)) : nothing}
    ${d.available ? usesSection(el, now) : nothing}
    ${d.admin ? adminSection(el, d, now) : nothing}
  </div>`;
}
