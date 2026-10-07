// split-collapse — a support desk as list/detail on vocabulary rev 2: the
// split collapses on a phone (the ticket list, a ticket's conversation pushed
// over it while `detail` is true; Back reports `close`), shows both columns on
// a tablet (`columns` follows the sidebar button), and opens from a deep link:
// the runtime document starts at `#t=<id>` and the app's xbn.navigate fires
// `hashchange` for the next link. The conversation keeps the first unread
// message in place as replies arrive (`anchor`), reports leaving its end
// (`edge`) so a "↓ N new" button appears, and jumps to the end (`scrollTo`).
import { html, render, repeat, nothing } from '/vendor/xb-native.js';
import { selfApi } from '/vendor/bx-kit.js';

let tickets = null;
let open = null; // the ticket shown in the detail
let thread = null;
let columns = 'all';
let atEnd = true;
let unseen = 0;
let jumps = 0;

const linked = () => new URLSearchParams(location.hash.slice(1)).get('t');

async function load() {
  tickets = (await selfApi('/tickets')).tickets;
  const t = linked();
  if (t) await show(t); else paint();
}

async function show(id) {
  open = id;
  thread = null;
  unseen = 0;
  atEnd = true;
  const t = tickets.find((x) => x.id === id);
  if (t) t.unread = 0;
  paint();
  thread = await selfApi(`/tickets/${id}`);
  paint();
}

// a deep link while the view runs: the app calls xbn.navigate('#t=…')
window.addEventListener('hashchange', () => { const t = linked(); if (t && t !== open) show(t); });

xbin.bus.on(`res:${xbin.self}/bus/tickets/`, (topic, data) => {
  if (!topic.endsWith('/reply') || !thread || data.ticket !== open) return;
  thread.messages = [...thread.messages, data.message];
  if (!atEnd) unseen++;
  paint();
});

const detail = () => (!thread ? html`
  <screen title=""><empty icon="chat" title="No ticket open" text="Pick a ticket from the list."/></screen>` : html`
  <screen title=${thread.subject} subtitle=${`${thread.customer} · ${thread.priority}`}>
    <toolbar>
      <button icon="check" @tap=${() => {}}>Resolve</button>
    </toolbar>
    <transcript follow anchor=${thread.firstUnread ?? nothing} scrollTo=${`end#${jumps}`}
        @edge=${(e) => { if (e.edge === 'end') { atEnd = e.at; if (atEnd) unseen = 0; paint(); } }}>
      ${repeat(thread.messages, (m) => m.id, (m) => html`<message role=${m.from === 'agent' ? 'user' : 'assistant'}
          sender=${m.sender} text=${m.text} time=${m.time}/>`)}
    </transcript>
    ${unseen ? html`<button role="primary" icon="expand" @tap=${() => { jumps++; unseen = 0; paint(); }}>${`${unseen} new`}</button>` : nothing}
    <composer placeholder="Reply to the customer" @send=${() => {}}/>
  </screen>`);

const paint = () => render(!tickets ? nothing : html`
  <split detail=${open !== null} columns=${columns}
      @close=${() => { open = null; thread = null; paint(); }}
      @columns=${(e) => { columns = e.value; paint(); }}>
    <screen title="Tickets" subtitle=${`${tickets.filter((t) => t.unread).length} with new replies`} style="list">
      <list style="plain">
        ${repeat(tickets, (t) => t.id, (t) => html`
          <row title=${t.subject} subtitle=${t.customer} detail=${t.time} ?selected=${t.id === open}
               badge=${t.unread ? String(t.unread) : nothing} tone=${t.priority === 'urgent' ? 'danger' : t.unread ? 'accent' : nothing}
               @tap=${() => show(t.id)}/>`)}
      </list>
    </screen>
    ${detail()}
  </split>`);

load();
