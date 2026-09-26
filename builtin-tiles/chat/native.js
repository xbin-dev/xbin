// native.js — the chat tile for the xbin app (docs/frontend-kit.md). The agent
// loop stays in JS: chat-core.js is the page's own engine (models from every
// bound "llm" endpoint, the MCP client and tool routing, SSE streaming, up to
// 8 tool rounds, <think> splitting), shared with index.html. This view renders
// its state with the chat family: messages (streaming markdown), thinking, a
// tool card per call, and the composer; the toolbar picks the model and starts
// a new chat.
import { html, render, repeat, nothing } from '/vendor/xb-native.js';
import { Chat } from './chat-core.js';

const chat = new Chat({ llm: xbin.iface('llm'), mcp: xbin.iface('mcp') });
let draft = '';
chat.addEventListener('change', () => paint());   // any turn/tool/stream update

const failTone = { stopped: 'muted', truncated: 'warn', error: 'danger' };
const turn = (m) => {
  if (m.role === 'user') return html`<message role="user" text=${m.text}/>`;
  if (m.role === 'tool') return html`
    <toolcard title=${m.label} icon="wrench" family="tool" state=${m.state}>
      <code text=${m.args}/>
      ${m.result != null ? html`<code text=${m.result}/>` : nothing}
    </toolcard>`;
  return html`
    ${m.think ? html`<thinking text=${m.think} ?live=${m.thinking} seconds=${m.thinkSecs}/>` : nothing}
    ${m.text || m.streaming ? html`<message role="assistant" markdown text=${m.text} ?streaming=${m.streaming}/>` : nothing}
    ${m.error ? html`<step glyph="!" tone=${failTone[m.errorKind] || 'danger'} text=${m.error}/>` : nothing}`;
};
const toolsBadge = () => {
  if (!chat.mcpEndpoints.length) return nothing;
  const n = chat.tools.length, label = `${n} tool${n === 1 ? '' : 's'}`;
  return chat.toolErrors.length ? html`<badge tone="warn">${`⚠ ${label}`}</badge>` : html`<badge tone="muted">${label}</badge>`;
};
const paint = () => render(html`
  <screen title="Chat" style="scroll">
    <toolbar>
      ${chat.models.length ? html`<picker style="menu" value=${chat.model} options=${chat.models} @change=${(e) => chat.setModel(e.value)}/>` : nothing}
      ${toolsBadge()}
      <button icon="plus" @tap=${() => chat.reset()}>New chat</button>
    </toolbar>
    <transcript follow>
      ${repeat(chat.history, (m) => m.id, turn)}
      ${chat.note ? html`<step glyph="!" tone=${chat.noteError ? 'danger' : 'muted'} text=${chat.note}/>` : nothing}
    </transcript>
    <composer value=${draft} placeholder="Message" ?busy=${chat.busy}
              @input=${(e) => { draft = e.value; paint(); }}
              @send=${(e) => { if (chat.send(e.value)) { draft = ''; paint(); } }}
              @stop=${() => chat.abort()}/>
  </screen>`);
paint();
chat.loadModels();
chat.loadTools();
