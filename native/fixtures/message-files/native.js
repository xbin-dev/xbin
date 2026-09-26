// message-files — a support chat about a damaged parcel where messages carry
// files: photos (tile-relative sources, which the app loads itself with the
// tile's frame token), a label photo small enough to travel as a data:
// image, and documents. An image with a src draws as a thumbnail — tap it
// for a Quick Look preview — and anything else (a PDF, a CSV, an image
// without a src) as a chip with its name. The composer holds a reply
// draft.
import { html, render, repeat, nothing } from '/vendor/xb-native.js';

const base = `/api/${xbin.self}/tickets/10432`;
let ticket = null;
let draft = '';

async function load() {
  ticket = await (await xbin.fetch(base)).json();
  paint();
}

const hhmm = (t) => new Intl.DateTimeFormat(undefined, { hour: '2-digit', minute: '2-digit', hourCycle: 'h23' }).format(new Date(t));

const msg = (m) => html`
  <message role=${m.from === ticket.me ? 'user' : 'assistant'} sender=${m.from} time=${hhmm(m.at)} text=${m.text}
           files=${m.files?.length ? m.files.map(({ name, mime, src }) => ({ name, mime, ...(src ? { src } : {}) })) : nothing}/>`;

const paint = () => render(!ticket ? nothing : html`
  <screen title=${`Order #${ticket.order}`} subtitle=${`Support · ${ticket.agent}`} style="scroll">
    <transcript follow>${repeat(ticket.messages, (m) => m.id, msg)}</transcript>
    <composer value=${draft} placeholder="Reply to support" @input=${(e) => { draft = e.value; paint(); }}
              @send=${() => { draft = ''; paint(); }}/>
  </screen>`);

load();
