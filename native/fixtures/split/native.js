// split — a mail tile as list/detail (split prefer="auto": two columns when
// there is room, stacked when compact). The primary is a plain lazy list that
// loads more at its end; the detail renders the selected message. The script
// opens a message and scrolls the list to its end; then a bus event delivers a
// new mail, which lands on top. The unread count goes to the app's badge.
import { html, render, repeat, nothing } from '/vendor/xb-native.js';
import { selfApi } from '/vendor/bx-kit.js';

let page = null;
let mails = [];
let open = null;
let body = null;

async function load() { page = await selfApi('/mailboxes/inbox'); mails = page.mails; paint(); }
async function more() {
  if (!page.next) return;
  page = await selfApi(`/mailboxes/inbox?after=${page.next}`);
  mails = [...mails, ...page.mails];
  paint();
}
async function show(m) {
  open = m.id; body = null; m.unread = false; paint();
  body = await selfApi(`/mails/${m.id}`);
  paint();
}

const detail = () => (!body ? html`<screen title=""><empty icon="mail" title="No message selected" text="Pick a message from the list."/></screen>` : html`
  <screen title=${body.subject} style="scroll">
    <toolbar>
      <button icon="back" @tap=${() => {}}>Reply</button>
      <button icon="archive" @tap=${() => {}}>Archive</button>
      <button role="destructive" icon="trash" @tap=${() => {}}>Delete</button>
    </toolbar>
    <stack gap="m">
      <stack gap="xs">
        <text style="headline">${body.from}</text>
        <text style="caption" tone="muted">${`to ${body.to.join(', ')} · ${body.date}`}</text>
      </stack>
      <divider/>
      <markdown source=${body.text} @link=${(e) => xbin.native.open(e.href)}/>
    </stack>
  </screen>`);

xbin.bus.on(`res:${xbin.self}/bus/inbox/`, (topic, data) => {
  if (topic.endsWith('/new')) { mails = [data.mail, ...mails]; paint(); }
});

const paint = () => {
  if (page) xbin.native.meta({ badge: String(mails.filter((m) => m.unread).length) });
  render(!page ? nothing : html`
  <split prefer="auto">
    <screen title="Inbox" subtitle=${`${mails.filter((m) => m.unread).length} unread`}>
      <toolbar><button icon="pencil" @tap=${() => {}}>Compose</button></toolbar>
      <list style="plain" @more=${more}>
        ${repeat(mails, (m) => m.id, (m) => html`
          <row title=${m.from} subtitle=${m.subject} detail=${m.time} ?selected=${m.id === open}
               badge=${m.unread ? 'new' : nothing} tone=${m.unread ? 'accent' : nothing} @tap=${() => show(m)}/>`)}
      </list>
    </screen>
    ${detail()}
  </split>`);
};

load();
