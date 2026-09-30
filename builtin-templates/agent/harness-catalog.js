// harness-catalog.js — the ⚙ Coding agents tab on the web (D147
// §4.3.10), for the tile's managers: each coding agent GET /harnesses
// lists — whether it could be started and why not, the sandbox managers and
// images that have it, the sandboxes it was found or signed in on, the
// classes that allow it (⚙ Classes changes which), its modes and sign-in
// command — and checking a running sandbox now (?probe=). What it says is
// model/harness-manage.js; the app draws the same from
// native/harness-catalog.js. agent.js opens it as a settings tab.
import { html, render, nothing } from '/vendor/lit-all.min.js';
import { catalogRows, probeTargets, modesWords } from './model/harness-manage.js';

/** tabHarnesses draws the tab into the settings body: the catalog read afresh. */
export async function tabHarnesses(bd, app) {
  const st = { ref: '', busy: false, msg: '' };
  await Promise.all([app.harness.load(), app.sbx.load(true)]);
  bd.textContent = '';
  const host = document.createElement('div');
  bd.append(host);
  const draw = () => render(tabTpl(st, app), host);
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

function tabTpl(st, app) {
  const rows = catalogRows(app.harness.catalog, app.sbx.list, app.classes);
  const targets = probeTargets(app.sbx.list);
  if (!st.ref && targets.length) st.ref = targets[0].ref;
  const facts = (label, v) => (v ? html`<div class="hfact"><span class="muted">${label}</span> ${v}</div>` : nothing);
  return html`<div class="sec hcat"><h4>Coding agents</h4>
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

const CSS = `
  .hcat .hrow { padding: 8px 0; border-top: 1px solid var(--bx-border); font-size: 12px; }
  .hcat .hhd { display: flex; flex-wrap: wrap; gap: 6px; align-items: center; margin-bottom: 3px; }
  .hcat .hhd .badge.ok { color: var(--bx-green); } .hcat .hhd .badge.warn { color: var(--bx-yellow, #d9a441); }
  .hcat .hwhy { font-size: 11.5px; }
  .hcat .hfact { margin: 2px 0 0 4px; overflow-wrap: anywhere; }
  .hcat .hfact > .muted { display: inline-block; min-width: 84px; }
  .hcat .hsb.ok { color: var(--bx-green); } .hcat .hsb.warn { color: var(--bx-yellow, #d9a441); } .hcat .hsb.bad { color: var(--bx-red); }
  .hcat .hcheck { display: flex; flex-wrap: wrap; gap: 6px; align-items: center; margin: 10px 0; }
`;
if (!document.getElementById('harness-catalog-css')) {
  document.head.append(Object.assign(document.createElement('style'), { id: 'harness-catalog-css', textContent: CSS }));
}
