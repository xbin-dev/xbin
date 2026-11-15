// workspace-agent-empty — a new session of a person's workspace agent, with
// no connection to the workspace at the moment (D233): the transcript's
// `empty` slot — what the agent does and doesn't do in this session, and
// starters built from the person's own tiles — under a `connection` line
// that is disabled while nothing can be reached, and a composer that keeps
// the draft. A starter fills the message box (`suggestion`); it never sends.
// The app's chrome says what the agent was opened about (xbn.agent): a chip.
import { html, render, nothing } from '/vendor/xb-native.js';
import { selfApi } from '/vendor/bx-kit.js';

let v = null;
let draft = '';

async function load() { v = await selfApi('/ws/view'); paint(); }

// What the agent was opened about: the app's chrome says (xbn.agent), where it
// shows this tile as the person's workspace agent. An offer: one tap drops it.
if (xbin.native.supports('agent')) {
  xbin.native.on('agent', (m) => {
    if (m.type !== 'xbin:agent-context' || !v) return;
    const t = v.tiles[m.tile];
    v.context = [...v.context.filter((c) => c.id !== m.tile), ...(t ? [{ id: m.tile, label: `About ${t.name}`, icon: 'box', tone: t.connected ? 'ok' : 'warn' }] : [])];
    paint();
  });
}

// "Add ___ to Invoices": the starter as written; the person fills the blank
function fill(e) {
  const s = v.empty.suggestions.find((x) => x.id === e.id);
  if (!s) return;
  draft = s.text;
  paint();
}

const paint = () => render(!v ? nothing : html`
  <screen title="Workspace agent" subtitle=${v.session} style="scroll">
    <connection connected=${v.connection.connected} model=${v.connection.model} ?private=${v.connection.private}
                label=${v.connection.label} ?disabled=${v.offline} @tap=${() => {}}/>
    ${v.offline ? html`<notice tone="warn" title=${v.offline.title} text=${v.offline.text}/>` : nothing}
    <transcript follow empty=${v.empty} @suggestion=${fill}/>
    <composer value=${draft} placeholder="Ask for an outcome…" hint=${v.offline ? v.offline.hint : v.hint} context=${v.context}
              @input=${(e) => { draft = e.value; paint(); }} @send=${() => {}}
              @remove-context=${(e) => { v.context = v.context.filter((c) => c.id !== e.id); paint(); }}/>
  </screen>`);

load();
