// native/home.js — no conversation open: the greeting and example asks an
// instance customises (HOME, model/home.js), what needs you (GET /needs),
// the brake while it is on, and the composer (a new ask starts a
// conversation of its own). The web draws the same from home.js.
import { html, repeat, nothing } from '/vendor/xb-native.js';
import { ui, ctx, guard, push } from './ui.js';
import { REASON } from '../model/home.js';
import { composerTpl } from './chat.js';

export function homeScreen() {
  const app = ctx.app;
  const H = app.HOME;
  const mcp = globalThis.xbin?.iface?.('mcp');
  const mcpBound = !!(mcp && (mcp.endpoints || []).length);
  return html`<screen title=${H.title} subtitle=${H.tagline} style="scroll">
    <toolbar>
      <button icon="list" @tap=${() => { ui.drawer = true; ctx.paint(); }}>Conversations</button>
      <menu icon="ellipsis" label="More">${mainMenu()}</menu>
    </toolbar>
    ${ui.err ? html`<notice tone="danger" text=${ui.err}/>` : nothing}
    ${app.halted ? html`<notice tone="warn" title="Halted" text="Every run of this agent is stopped. Resume it from ⋯."/>` : nothing}
    <text style="title2">${H.hi}</text>
    <text tone="muted">${H.sub}</text>
    ${mcpBound ? nothing : html`<notice tone="info" text="No MCP servers are bound yet — see ⋯ → Settings → MCP."/>`}
    <section title="Try">${repeat(H.examples, (e) => e, (e) => html`<row title=${e} icon="sparkles"
      @tap=${() => { ui.draft = e; ctx.paint(); }}/>`)}</section>
    ${app.needs && app.needs.length ? html`<section title="Needs you">${repeat(app.needs, (n) => `${n.reason}:${n.run.id}:${n.subRun || ''}`, (n) => html`
      <row title=${n.run.title || 'run ' + n.run.id} subtitle=${REASON[n.reason] || n.reason}
        icon=${n.reason === 'failed' ? 'warning' : n.reason === 'approval' ? 'shield' : 'question'}
        tone=${n.reason === 'failed' ? 'danger' : 'accent'} nav @tap=${() => app.select(n.subRun || n.run.id)}/>`)}</section>` : nothing}
    ${composerTpl(null, null)}
  </screen>`;
}

// mainMenu: what the web's side bar and top bar hold beyond conversations —
// the Automations page, settings (managers) and the brake (model/rules.js
// halt). before(): what to do first (the drawer closes itself).
export function mainMenu(before = () => {}) {
  const app = ctx.app;
  const go = (fn) => () => { before(); fn(); };
  const h = app.rules.halt(app.me, app.halted, app.convs.all());
  const n = app.autos.summary ? (app.autos.summary.unread || 0) + (app.autos.summary.attention || 0) : 0;
  return html`
    <button icon="clock" @tap=${go(() => app.openAutomations())}>${n ? `Automations (${n} new)` : 'Automations'}</button>
    ${app.me.manager ? html`<button icon="gear" @tap=${go(() => push({ kind: 'settings' }))}>Settings</button>` : nothing}
    ${h.shown ? html`<divider/>${app.halted
      ? html`<button icon="play" @tap=${go(guard(() => app.setHalt(false)))}>Resume the agent</button>`
      : html`<button icon="power" role="destructive" confirm=${{ title: 'Stop every running agent now?',
          message: 'Runs stop at once and nothing starts until you resume.', label: 'Halt', destructive: true }}
          @tap=${go(guard(() => app.setHalt(true)))}>Halt every run</button>`}` : nothing}`;
}
