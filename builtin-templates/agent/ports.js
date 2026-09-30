// ports.js — the Ports section of the sandbox popover (#sbxpop, sandboxes.js
// popExtra): port-forward diagnostics for the people in the conversation
// (D135). It lists the conversation's live previews (its `live` steps),
// each probed now as its binder would reach it (GET /runs/{id}/ports), with
// Open to show it again, and probes any port of the active sandbox
// (GET /runs/{id}/ports/{sbx}/{port}). A probe answers the status, the
// type, a refusal and what to do (model/live.js probeWords) — never the page.
import { html, nothing } from '/vendor/lit-all.min.js';
import * as actions from './model/actions.js';
import { probeWords } from './model/live.js';

const sbxId = (ref) => String(ref || '').slice(String(ref || '').lastIndexOf('|') + 1);

export function makePorts(app, { openLive, repaint }) {
  const st = { run: 0, list: null, err: '', busy: false, port: '', path: '/', probe: null };
  const load = async (run) => {
    st.run = run; st.list = null; st.err = ''; st.busy = true; repaint();
    try { st.list = await actions.ports(run); } catch (e) { st.err = e.message; }
    if (st.run === run) { st.busy = false; repaint(); }
  };
  const probe = async (b) => {
    const port = Number(st.port);
    if (!(port >= 1 && port <= 65535)) { st.probe = { ok: false, error: 'a port is a number from 1 to 65535' }; repaint(); return; }
    st.probe = { busy: true }; repaint();
    try { st.probe = await actions.probePort(app.sel, sbxId(b.ref), port, st.path || '/'); } catch (e) { st.probe = { ok: false, error: e.message }; }
    st.probe.target = { sandbox: sbxId(b.ref), name: b.name, port, path: st.probe.path || st.path || '/' };
    repaint();
  };
  const line = (p) => {
    const w = probeWords(p);
    return html`<div class="pres ${w.tone}" title=${w.text}>${w.text}${w.hint ? html`<div class="hint">${w.hint}</div>` : nothing}</div>`;
  };
  // tpl(b): the section for the badge b (model/sandboxes.js sandboxBadge); close closes the popover.
  function tpl(b, close) {
    if (!b.talk || app.sel == null) return nothing; // participants only, as the routes (a coding agent's fixed sandbox too)
    if (st.run !== app.sel) { st.run = app.sel; st.list = null; st.probe = null; }
    const open = (det) => { close?.(); openLive(det); };
    return html`<div class="field sbxports" id="sbx-ports"><label>Ports — live previews, and what a port answers now</label>
      ${!st.list && !st.busy && !st.err ? html`<button class="btn ghost btnsm" id="ports-load" @click=${() => load(app.sel)}>Check the live previews</button>` : nothing}
      ${st.busy ? html`<div class="hint">probing…</div>` : nothing}
      ${st.err ? html`<div class="err">${st.err}</div>` : nothing}
      ${st.list && !st.list.length ? html`<div class="hint" id="ports-none">No live previews in this conversation yet (the agent's preview_port).</div>` : nothing}
      ${(st.list || []).map((p) => html`<div class="prow" data-port=${p.port}>
        <span class="mono">${p.name || p.sandbox}:${p.port}${p.path}</span>
        <button class="btn ghost btnsm" title="show it live in the preview pane" @click=${() => open(p)}>Open</button>
        ${line(p)}</div>`)}
      ${st.list ? html`<button class="lnk" id="ports-again" @click=${() => load(app.sel)}>probe again</button>` : nothing}
      <div class="sbxcwd pprobe"><input id="ports-port" class="mono" inputmode="numeric" placeholder="port" .value=${st.port}
          @input=${(e) => { st.port = e.target.value.trim(); }} @keydown=${(e) => { if (e.key === 'Enter') probe(b); }}>
        <input id="ports-path" class="mono" placeholder="/" .value=${st.path} @input=${(e) => { st.path = e.target.value; }}
          @keydown=${(e) => { if (e.key === 'Enter') probe(b); }}>
        <button class="btn btnsm" id="ports-probe" ?disabled=${!!(st.probe && st.probe.busy)} @click=${() => probe(b)}>Probe</button></div>
      ${st.probe && !st.probe.busy ? html`<div class="prow" id="ports-result">${line(st.probe)}
        ${st.probe.ok ? html`<button class="btn ghost btnsm" @click=${() => open(st.probe.target)}>Open</button>` : nothing}</div>` : nothing}
    </div>`;
  }
  return { tpl };
}
