// native/home.js — the new chat screen (pushed from the conversation list's
// New chat, D190): the greeting and example asks an instance customises
// (HOME, model/home.js), who answers and the pickers for the new chat (class,
// model, sandbox), the brake while it is on, and the composer (a new ask
// starts a conversation of its own, which takes this screen's place). What
// needs you heads the list (native/convs.js). The web draws the same from
// home.js. And the main menu — the list's ⋯.
import { html, repeat, nothing } from '/vendor/xb-native.js';
import { ui, ctx, guard, push } from './ui.js';
import { composerTpl, modelPickerTpl, folded } from './chat.js';
import { classPickerTpl } from './classes.js';
import { sandboxPickerTpl } from './sandboxes.js';
import { homeSetupTpl } from './harness-start.js';
import { openAutos, compact } from './nav.js';

export function newScreen() {
  const app = ctx.app;
  const H = app.HOME;
  const mcp = globalThis.xbin?.iface?.('mcp');
  const mcpBound = !!(mcp && (mcp.endpoints || []).length);
  const fold = compact(); // a phone's bar: the pickers go into ⋯ (native/pick.js)
  const options = html`<button icon="pencil" @tap=${() => { ui.newChat = { text: ui.draft || '', title: '', system: '', class: app.classId }; ctx.paint(); }}>New chat with options…</button>`;
  return html`<screen title="New chat" style="scroll">
    ${fold ? html`<toolbar>
      <menu icon="ellipsis" label="More">${folded([classPickerTpl(true), app.harness.picked() ? nothing : modelPickerTpl(null, true),
        sandboxPickerTpl(true), ctx.ext.toolbar(null, { fold: true })])}${options}</menu>
    </toolbar>` : html`<toolbar>
      ${classPickerTpl()}
      ${app.harness.picked() ? nothing : modelPickerTpl(null)}
      ${sandboxPickerTpl()}
      ${ctx.ext.toolbar(null) || nothing}
      <menu icon="ellipsis" label="More">${options}</menu>
    </toolbar>`}
    ${ui.err ? html`<notice tone="danger" text=${ui.err}/>` : nothing}
    ${app.halted ? html`<notice tone="warn" title="Halted" text="Every run of this agent is stopped. Resume it from the list's More."/>` : nothing}
    ${homeSetupTpl()}
    <text style="title2">${H.hi}</text>
    <text tone="muted">${H.sub}</text>
    ${mcpBound ? nothing : html`<notice tone="info" text="No MCP servers are bound yet — see More → Settings → MCP."/>`}
    <section title="Try">${repeat(H.examples, (e) => e, (e) => html`<row title=${e} icon="sparkles"
      @tap=${() => { ui.draft = e; ctx.paint(); }}/>`)}</section>
    ${composerTpl(null, null)}
  </screen>`;
}

// mainMenu: what the web's side bar and top bar hold beyond conversations —
// the Automations page, settings (managers), the seams' items (ext.main:
// Projects, Coding agents, …) and the brake (model/rules.js halt). In the
// list's bar.
export function mainMenu() {
  const app = ctx.app;
  const h = app.rules.halt(app.me, app.halted, app.convs.all());
  const n = app.autos.summary ? (app.autos.summary.unread || 0) + (app.autos.summary.attention || 0) : 0;
  return html`
    <button icon="clock" @tap=${() => openAutos()}>${n ? `Automations (${n} new)` : 'Automations'}</button>
    ${ctx.ext.main(() => {}) || nothing}
    ${app.me.manager ? html`<button icon="gear" @tap=${() => push({ kind: 'settings' })}>Settings</button>` : nothing}
    ${h.shown ? html`<divider/>${app.halted
      ? html`<button icon="play" @tap=${guard(() => app.setHalt(false))}>Resume the agent</button>`
      : html`<button icon="power" role="destructive" confirm=${{ title: 'Stop every running agent now?',
          message: 'Runs stop at once and nothing starts until you resume.', label: 'Halt', destructive: true }}
          @tap=${guard(() => app.setHalt(true))}>Halt every run</button>`}` : nothing}`;
}
