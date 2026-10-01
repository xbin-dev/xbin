// harness-catalog.js — the ⚙ Coding agents tab on the web (D147
// §4.3.10), for the tile's managers: each coding agent GET /harnesses
// lists — whether it could be started and why not, the sandbox managers and
// images that have it, the sandboxes it was found or signed in on, the
// classes that allow it (⚙ Classes changes which), its modes and sign-in
// command — and checking a running sandbox now (?probe=). What it says is
// model/harness-manage.js; the app draws the same from
// native/harness-catalog.js. agent.js opens it as a settings tab.
//
// Above it, Coding-agent sign-ins (D179): your own saved sign-ins per
// coding agent (GET /prefs/harness-signins — a person's own partition only;
// elsewhere it says why there are none): what each is, its state, the
// default; Make default, Rename, Forget, and a key or token pasted (the
// field emptied as it is sent; the backend keeps it, nothing draws it).
// What it says is model/harness-signins.js.
import { html, render, nothing } from '/vendor/lit-all.min.js';
import { catalogRows, probeTargets, modesWords } from './model/harness-manage.js';
import { signinGroups, keyFor } from './model/harness-signins.js';

/** tabHarnesses draws the tab into the settings body: the catalog read afresh. */
export async function tabHarnesses(bd, app) {
  const st = { ref: '', busy: false, msg: '', sbusy: '', serr: '', smsg: '', env: {} };
  await Promise.all([app.harness.load(), app.sbx.load(true), app.harness.loadSignins()]);
  bd.textContent = '';
  const host = document.createElement('div');
  bd.append(host);
  const draw = () => render(tabTpl(st, app, draw), host);
  const check = async () => {
    if (!st.ref) return;
    st.busy = true; st.msg = ''; draw();
    await app.harness.load(st.ref);
    const name = probeTargets(app.sbx.list).find((t) => t.ref === st.ref)?.name || st.ref;
    st.busy = false;
    st.msg = app.harness.error || `checked ${name} ✓`;
    draw();
  };
  st.check = check;
  draw();
}

function tabTpl(st, app, draw) {
  const rows = catalogRows(app.harness.catalog, app.sbx.list, app.classes);
  const targets = probeTargets(app.sbx.list);
  if (!st.ref && targets.length) st.ref = targets[0].ref;
  const facts = (label, v) => (v ? html`<div class="hfact"><span class="muted">${label}</span> ${v}</div>` : nothing);
  return html`${signinsTpl(st, app, draw)}<div class="sec hcat"><h4>Coding agents</h4>
    <div class="hint">Claude Code, Codex, Gemini CLI and opencode run in a coding sandbox and answer a conversation instead of this agent's own loop.
      One can be started when a bound sandbox manager's image has it, a class people may use allows it (⚙ Classes: the Coding agents toolset),
      and that class allows a sandbox with an egress other than none — it must reach its provider.</div>
    ${app.harness.error ? html`<div class="err">${app.harness.error}</div>` : nothing}
    ${targets.length ? html`<div class="hcheck"><label class="muted" for="hc-ref">Check a running sandbox now</label>
      <select id="hc-ref" @change=${(e) => { st.ref = e.target.value; }}>${targets.map((t) => html`<option value=${t.ref} ?selected=${t.ref === st.ref}>${t.name}</option>`)}</select>
      <button class="btn btnsm" id="hc-check" ?disabled=${st.busy} @click=${() => st.check()}>${st.busy ? 'Checking…' : 'Check'}</button>
      <span class="muted" id="hc-msg">${st.msg}</span></div>` : nothing}
    ${rows.map((r) => html`<div class="hrow" data-harness=${r.id}>
      <div class="hhd"><span class="kind">${r.mono}</span><b>${r.name}</b>
        <span class="badge ${r.available ? 'ok' : 'warn'}">${r.available ? 'available' : 'not available'}</span>
        ${r.why ? html`<span class="muted hwhy">${r.why}</span>` : nothing}</div>
      ${facts('Images', r.images.length ? r.images.map((i) => i.label).join(' · ') : 'none has it')}
      ${facts('Sandboxes', r.sandboxes.length ? html`${r.sandboxes.map((s, i) => html`${i ? ' · ' : ''}<span class="hsb ${s.tone}" data-ref=${s.ref}>${s.name}: ${s.label}</span>`)}` : 'not looked for in any yet')}
      ${facts('Classes', r.classes.length ? r.classes.join(', ') : 'none you may use allows it')}
      ${facts('Modes', modesWords(r))}
      ${facts('Sign-in', r.login ? html`<span class="mono">${r.login}</span>` : '')}
      ${facts('Options', r.options.join(', '))}
    </div>`)}
  </div>`;
}

// --- Coding-agent sign-ins (D179) -----------------------------------------------------

/** openSignins shows Coding-agent sign-ins in a dialog of its own — for
 * everyone (the ⚙ panel is the managers'): #hctl's Account section and the
 * sign-in card open it. */
export async function openSignins(app) {
  let dlg = document.getElementById('hsdlg');
  if (!dlg) {
    dlg = Object.assign(document.createElement('dialog'), { id: 'hsdlg', className: 'dlg hsdlg' });
    document.body.append(dlg);
  }
  const st = { sbusy: '', serr: '', smsg: '', env: {} };
  const draw = () => render(html`<div class="dlg-bd">${signinsTpl(st, app, draw)}</div>
    <div class="dlg-ft"><button class="btn" id="hsdlg-close" @click=${() => dlg.close()}>Close</button></div>`, dlg);
  draw();
  if (!dlg.open) dlg.showModal();
  await app.harness.loadSignins();
  draw();
}

function signinsTpl(st, app, draw) {
  const s = app.harness.signins;
  const act = async (what, fn) => {
    st.sbusy = what; st.serr = ''; st.smsg = '';
    draw();
    try { st.smsg = (await fn()) || ''; } catch (e) { st.serr = e.message; }
    st.sbusy = '';
    draw();
  };
  const head = html`<h4>Coding-agent sign-ins</h4>`;
  if (!s.loaded) return html`<div class="sec hsignins" id="hsignins">${head}<div class="muted">loading…</div></div>`;
  if (!s.available) return html`<div class="sec hsignins" id="hsignins">${head}<div class="hint" id="hs-none">No saved sign-ins here: ${s.why || 'they live only in your own space'}.</div></div>`;
  const groups = signinGroups(s);
  // the secret is read from its field as it is sent and the field emptied at once
  const add = (g) => (e) => {
    e.preventDefault();
    const f = e.target;
    const secret = f.querySelector('input[type=password]').value;
    f.querySelector('input[type=password]').value = '';
    const name = f.querySelector('input[name=name]').value.trim();
    const env = st.env[g.harness] || '';
    if (!secret) return;
    act(`add:${g.harness}`, async () => {
      const r = await app.harness.saveSignin({ harness: g.harness, name, secret, env });
      f.querySelector('input[name=name]').value = '';
      return `Saved ${r.signin.name} for ${g.name}.`;
    });
  };
  const rename = (g, r) => act(`rename:${r.id}`, async () => {
    const name = prompt(`Rename ${r.name} (${g.name})`, r.name);
    if (name == null || !name.trim() || name.trim() === r.name) return '';
    await app.harness.updateSignin(r.id, { name: name.trim() });
    return `Renamed to ${name.trim()}.`;
  });
  const forget = (g, r) => act(`forget:${r.id}`, async () => {
    if (!confirm(`Forget ${r.name} (${g.name})? It is deleted from your vault, and the coding agents using it stop now; the next message starts them without it.`)) return '';
    const res = await app.harness.forgetSignin(r.id);
    return `Forgot ${r.name}${res && res.stopped ? ` — ${res.stopped} coding agent${res.stopped === 1 ? '' : 's'} stopped` : ''}.`;
  });
  return html`<div class="sec hsignins" id="hsignins">${head}
    <div class="hint">Sign-ins of your own, kept in your vault and handed to a coding agent only in your own conversations, in a sandbox of yours no one else uses —
      where they win over the sandbox's own sign-in. Never copied into a sandbox. A conversation can pick one (its ▾ menu); new ones use the default.
      "Remember for my other sandboxes" on a sign-in card saves one too.</div>
    ${st.serr ? html`<div class="err" id="hs-err">${st.serr}</div>` : nothing}
    ${st.smsg ? html`<div class="muted" id="hs-msg">${st.smsg}</div>` : nothing}
    ${groups.map((g) => html`<div class="hsgroup" data-harness=${g.harness}><h5>${g.name}</h5>
      ${g.rows.length ? g.rows.map((r) => html`<div class="hsrow" data-id=${r.id}>
        <span class="nm">${r.name}</span>${r.isDefault ? html`<span class="badge ok">default</span>` : nothing}
        <span class="muted">${r.what}</span><span class="st ${r.status.tone}">${r.status.text}</span>
        <span class="sp"></span>
        ${r.isDefault ? nothing : html`<button class="btn btnsm ghost" data-act="default" ?disabled=${!!st.sbusy}
          @click=${() => act(`default:${r.id}`, async () => { await app.harness.updateSignin(r.id, { default: true }); return `${r.name} is ${g.name}'s default now.`; })}>Make default</button>`}
        <button class="btn btnsm ghost" data-act="rename" ?disabled=${!!st.sbusy} @click=${() => rename(g, r)}>Rename</button>
        <button class="btn btnsm ghost danger" data-act="forget" ?disabled=${!!st.sbusy} @click=${() => forget(g, r)}>Forget</button>
      </div>`) : html`<div class="muted">none saved${g.mint ? ' — sign in with "Remember for my other sandboxes", or paste one below' : ''}</div>`}
      <form class="hsadd" @submit=${add(g)}>
        <input name="name" maxlength="40" placeholder=${g.rows.length ? 'name, e.g. Work' : 'Personal'} aria-label="its name">
        <input type="password" autocomplete="off" spellcheck="false" placeholder=${g.keys.map((k) => k.label).join(' or ')} aria-label=${`a key or token for ${g.name}`}
          @input=${(e) => { const k = keyFor(s, g.harness, e.target.value); e.target.title = k ? `goes in as ${k.env}` : 'say which key it is'; }}>
        ${g.keys.length > 1 ? html`<select aria-label="which key" @change=${(e) => { st.env[g.harness] = e.target.value; }}>
          <option value="">by its prefix</option>${g.keys.map((k) => html`<option value=${k.env} ?selected=${st.env[g.harness] === k.env}>${k.label}</option>`)}</select>` : nothing}
        <button class="btn btnsm" ?disabled=${!!st.sbusy}>${st.sbusy === `add:${g.harness}` ? 'Saving…' : 'Save'}</button>
      </form>
    </div>`)}
  </div>`;
}

const CSS = `
  .hcat .hrow { padding: 8px 0; border-top: 1px solid var(--bx-border); font-size: 12px; }
  .hcat .hhd { display: flex; flex-wrap: wrap; gap: 6px; align-items: center; margin-bottom: 3px; }
  .hcat .hhd .badge.ok { color: var(--bx-green); } .hcat .hhd .badge.warn { color: var(--bx-yellow, #d9a441); }
  .hcat .hwhy { font-size: 11.5px; }
  .hcat .hfact { margin: 2px 0 0 4px; overflow-wrap: anywhere; }
  .hcat .hfact > .muted { display: inline-block; min-width: 84px; }
  .hcat .hsb.ok { color: var(--bx-green); } .hcat .hsb.warn { color: var(--bx-yellow, #d9a441); } .hcat .hsb.bad { color: var(--bx-red); }
  .hcat .hcheck { display: flex; flex-wrap: wrap; gap: 6px; align-items: center; margin: 10px 0; }
  .hsdlg .dlg-bd { min-width: min(560px, 88vw); }
  .hsignins h5 { margin: 10px 0 2px; font-size: 12.5px; }
  .hsignins .hsrow { display: flex; flex-wrap: wrap; gap: 6px; align-items: center; padding: 5px 0; border-top: 1px solid var(--bx-border); font-size: 12px; }
  .hsignins .hsrow .nm { font-weight: 600; }
  .hsignins .hsrow .sp { flex: 1 1 auto; }
  .hsignins .hsrow .badge.ok { color: var(--bx-green); }
  .hsignins .hsrow .st.warn { color: var(--bx-yellow, #d9a441); } .hsignins .hsrow .st.bad { color: var(--bx-red); }
  .hsignins .hsadd { display: flex; flex-wrap: wrap; gap: 6px; align-items: center; margin: 6px 0 4px; }
  .hsignins .hsadd input[name=name] { width: 10em; }
  .hsignins .hsadd input[type=password] { flex: 1 1 14em; min-width: 0; max-width: 340px; }
`;
if (!document.getElementById('harness-catalog-css')) {
  document.head.append(Object.assign(document.createElement('style'), { id: 'harness-catalog-css', textContent: CSS }));
}
