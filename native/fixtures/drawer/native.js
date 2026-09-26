// drawer — a chat tile whose conversation list is a drawer: a `sheet
// edge="leading"` laid over the chat (the agent template's pattern). The
// script opens it from the toolbar, so the drawer is open over the
// conversation: a search field, a new-chat row, the conversations grouped by
// day — a glyph as the badge, unread as the accent dot, the open one selected
// — each with its actions (pin, rename, delete after a confirmation), which
// fold behind the row's ⋯ as well as its swipe and context menu. Dismissing
// the drawer (the backdrop, a swipe) reports `dismiss`.
import { html, render, repeat, nothing } from '/vendor/xb-native.js';
import { selfApi } from '/vendor/bx-kit.js';

let convs = null;
let chat = null;
let drawer = false;
let q = '';
let draft = '';

async function load() {
  [convs, chat] = await Promise.all([selfApi('/conversations'), selfApi('/conversations/c-31')]);
  paint();
}

const close = () => { drawer = false; paint(); };
const GLYPH = { ask: ['waiting for you', 'accent'], error: ['failed', 'danger'] };

const convRow = (c) => {
  const g = GLYPH[c.state];
  return html`<row title=${c.title} subtitle=${c.shared ? `⇆ ${c.shared}` : nothing}
      badge=${g ? g[0] : nothing} tone=${g ? g[1] : c.unread ? 'accent' : nothing} ?selected=${c.id === chat.id}
      @tap=${() => { close(); }}>
    <actions>
      <button icon="pin" @tap=${() => { c.pinned = !c.pinned; paint(); }}>${c.pinned ? 'Unpin' : 'Pin'}</button>
      <button icon="pencil" @tap=${() => {}}>Rename</button>
      <button icon="trash" role="destructive" confirm=${{ title: `Delete "${c.title}" and its history?`, label: 'Delete', destructive: true }}
              @tap=${() => { convs.items = convs.items.filter((x) => x.id !== c.id); paint(); }}>Delete</button>
    </actions>
  </row>`;
};

const days = () => {
  const out = [];
  for (const c of convs.items.filter((x) => !q || x.title.toLowerCase().includes(q.toLowerCase()))) {
    const last = out[out.length - 1];
    if (last && last.day === c.day) last.rows.push(c); else out.push({ day: c.day, rows: [c] });
  }
  return out;
};

const drawerSheet = () => html`
  <sheet open=${drawer} edge="leading" title="Conversations" @dismiss=${close}>
    <screen title="Conversations" style="list" search=${q} @search=${(e) => { q = e.value; paint(); }}>
      <section>
        <row title="New chat" icon="plus" @tap=${close}/>
        <row title="Automations" icon="clock" badge="2" tone="accent" nav @tap=${close}/>
      </section>
      ${repeat(days(), (d) => d.day, (d) => html`<section title=${d.day}>${repeat(d.rows, (c) => c.id, convRow)}</section>`)}
    </screen>
  </sheet>`;

const paint = () => render(!convs ? nothing : html`
  <screen title=${chat.title} subtitle=${chat.agent} style="scroll">
    <toolbar>
      <button icon="list" @tap=${() => { drawer = true; paint(); }}>Conversations</button>
      <button icon="plus" @tap=${() => {}}>New chat</button>
    </toolbar>
    <transcript follow>
      ${repeat(chat.messages, (m) => m.id, (m) => html`<message role=${m.role} text=${m.text} ?markdown=${m.role === 'assistant'}/>`)}
    </transcript>
    <composer value=${draft} placeholder="Ask the agent" @input=${(e) => { draft = e.value; paint(); }} @send=${() => { draft = ''; paint(); }}/>
  </screen>
  ${drawerSheet()}`);

load();
