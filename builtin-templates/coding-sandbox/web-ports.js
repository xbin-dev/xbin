// web-ports.js — a sandbox's Ports row, on both pages (web-ops.js for the
// operators, web-mine.js for your own; API.md §Ports): whether this manager
// offers ports (live previews, D135) and why not, and a probe of one port —
// the status, the type, a refusal and how long it took. A diagnostic, not a
// browser: the page's body never comes back.
import { html, nothing } from '/vendor/lit-all.min.js';

// probeText says what a probe found, and what to do about a refusal.
export function probeText(p) {
  const type = String(p.contentType || '').split(';')[0];
  const ms = p.ms != null ? ` · ${p.ms} ms` : '';
  if (p.ok) return { tone: 'ok', text: `HTTP ${p.status}${type ? ' · ' + type : ''}${ms}` };
  const head = p.status ? `HTTP ${p.status}` : p.refusal || 'no answer';
  const hint = {
    'not-listening': 'nothing listens on that port in the sandbox: start its server',
    state: 'the sandbox isn\'t running: start it, then its server',
    unsupported: /predates|restart/.test(p.error || '') ? 'restart the sandbox (stop, then start): its agent predates ports' : '',
  }[p.refusal] || '';
  return { tone: 'bad', text: `${head}${ms}${p.error ? ' — ' + p.error : ''}${hint ? ' · ' + hint : ''}` };
}

export function portsTpl(app, ui, r) {
  const p = app.ports && app.ports.id === r.id ? app.ports : null;
  if (!p) {
    if (ui.portsFor !== r.id) { ui.portsFor = r.id; queueMicrotask(() => app.portsOf(r.id)); } // not while painting
    return html`<div class="muted">reading…</div>`;
  }
  const f = ui.forms['ports:' + r.id] || { port: '', path: '/' };
  const set = (k) => (e) => { ui.forms['ports:' + r.id] = { ...f, ...ui.forms['ports:' + r.id], [k]: e.target.value }; };
  const go = () => {
    const now = ui.forms['ports:' + r.id] || f; // as typed (typing doesn't repaint)
    const port = Number(String(now.port).trim());
    if (!(port >= 1 && port <= 65535)) { ui.err = 'a port is a number from 1 to 65535'; ui.paint(); return; }
    app.probePort(r.id, port, now.path || '/');
  };
  const info = p.info;
  const res = p.probe && !p.probe.busy ? probeText(p.probe) : null;
  return html`<div class="ports" id="ports" data-id=${r.id}>
    <b>Ports of ${r.name}</b>
    ${p.err ? html`<div class="err">${p.err}</div>` : !info ? html`<div class="muted">reading…</div>`
      : html`<div class="small" id="ports-offered">${info.offered && !info.restartNeeded
        ? html`<span class="pill ok">offered</span> <span class="muted">live previews reach a server on the sandbox's loopback</span>`
        : html`<span class="pill ${info.offered ? 'warn' : 'muted'}">${info.offered ? 'restart needed' : 'not offered'}</span> <span>${info.why}</span>`}</div>`}
    <div class="row"><label>port <input id="ports-port" class="mono" inputmode="numeric" size="6" .value=${f.port} @input=${set('port')}
        @keydown=${(e) => { if (e.key === 'Enter') go(); }}></label>
      <label>path <input id="ports-path" class="mono" .value=${f.path} @input=${set('path')} @keydown=${(e) => { if (e.key === 'Enter') go(); }}></label>
      <button id="ports-probe" ?disabled=${!!(p.probe && p.probe.busy)} @click=${go}>${p.probe && p.probe.busy ? 'Probing…' : 'Probe'}</button></div>
    ${res ? html`<div class="small probe ${res.tone}" id="ports-result">${p.probe.path ? html`<span class="mono">${p.probe.path}</span> — ` : nothing}${res.text}</div>` : nothing}
  </div>`;
}
