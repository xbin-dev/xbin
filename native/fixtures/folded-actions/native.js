// folded-actions — a code review tile as list/detail: the review's threads
// (rows) beside the open thread (a transcript). Every row and every message
// carries several actions — a destructive one behind a confirmation, a
// native copy — which fold out of the way: a row's behind a trailing ⋯ (and
// its swipe actions and context menu), a message's behind a ⋯ under its
// bubble, instead of a line of buttons that overflows the bubble. The script
// opens the first open thread and resolves the second.
import { html, render, repeat, nothing } from '/vendor/xb-native.js';
import { selfApi } from '/vendor/bx-kit.js';

let review = null;
let open = null;
let draft = '';

async function load() { review = await selfApi('/reviews/412'); paint(); }
async function show(t) { open = await selfApi(`/reviews/412/threads/${t.id}`); t.unread = false; paint(); }
const resolve = (t) => { t.resolved = !t.resolved; paint(); };
const link = (t) => `https://git.acme.dev/checkout/pull/412#${t.id}`;

const threadRow = (t) => html`
  <row title=${t.where} subtitle=${t.first} detail=${t.replies ? String(t.replies) : nothing} icon=${t.resolved ? 'check' : 'chat'}
       mono="title" tone=${t.resolved ? 'ok' : t.unread ? 'accent' : nothing} ?selected=${open?.id === t.id} @tap=${() => show(t)}>
    <actions>
      <button icon=${t.resolved ? 'refresh' : 'check'} @tap=${() => resolve(t)}>${t.resolved ? 'Reopen' : 'Resolve'}</button>
      <button icon="bell" @tap=${() => {}}>Mute</button>
      <button icon="link" copy=${link(t)}>Copy link</button>
      <button icon="trash" role="destructive" confirm=${{ title: `Delete the thread on ${t.where}?`, label: 'Delete', destructive: true }}
              @tap=${() => { review.threads = review.threads.filter((x) => x !== t); paint(); }}>Delete</button>
    </actions>
  </row>`;

const comment = (c) => html`
  <message role=${c.author === review.me ? 'user' : 'assistant'} sender=${c.author} time=${c.time} markdown text=${c.text}>
    <actions>
      <button icon="chat" @tap=${() => { draft = `> ${c.text.split('\n')[0]}\n\n`; paint(); }}>Quote reply</button>
      <button icon="copy" copy=${c.text}>Copy</button>
      ${c.author === review.me ? html`
        <button icon="pencil" @tap=${() => {}}>Edit</button>
        <button icon="trash" role="destructive" confirm=${{ title: 'Delete this comment?', label: 'Delete', destructive: true }} @tap=${() => {}}>Delete</button>`
        : html`<button icon="star" @tap=${() => {}}>React</button>`}
    </actions>
  </message>`;

const paint = () => render(!review ? nothing : html`
  <split prefer="auto">
    <screen title=${`#${review.id}`} subtitle=${review.title} style="list">
      <section title="Open" badge=${String(review.threads.filter((t) => !t.resolved).length)}>
        ${repeat(review.threads.filter((t) => !t.resolved), (t) => t.id, threadRow)}
      </section>
      <section title="Resolved">
        ${repeat(review.threads.filter((t) => t.resolved), (t) => t.id, threadRow)}
      </section>
    </screen>
    ${!open ? html`<screen title=""><empty icon="chat" title="No thread open"/></screen>` : html`
      <screen title=${open.where} style="scroll">
        <transcript follow>${repeat(open.comments, (c) => c.id, comment)}</transcript>
        <composer value=${draft} placeholder="Reply" @input=${(e) => { draft = e.value; paint(); }} @send=${() => { draft = ''; paint(); }}/>
      </screen>`}
  </split>`);

load();
