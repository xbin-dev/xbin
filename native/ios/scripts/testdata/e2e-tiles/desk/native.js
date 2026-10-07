// A support desk's native view for the UI tests (XbinNavigationTests, D189):
// a rev-2 split — the ticket list, a ticket pushed over it on a phone (two
// columns on a tablet) — opened from a deep link: the runtime document
// starts at `#t=<id>`, and a link that arrives while it runs comes through
// xbn.navigate as a `hashchange`. Static data: no backend.
import { html, render, repeat } from '/vendor/xb-native.js';

const tickets = [
  { id: 't-1', subject: 'Checkout fails on Safari', customer: 'Northwind Traders' },
  { id: 't-2', subject: 'Invoice shows the wrong VAT', customer: 'Bellhaven Books' },
  { id: 't-3', subject: 'SSO login loops', customer: 'Kestrel Labs' },
];
let open = null;

const linked = () => new URLSearchParams(location.hash.slice(1)).get('t');
const show = (id) => { open = tickets.some((t) => t.id === id) ? id : null; paint(); };
window.addEventListener('hashchange', () => show(linked()));

const detail = () => {
  const t = tickets.find((x) => x.id === open);
  return t ? html`
    <screen title=${t.subject} subtitle=${t.customer} style="list">
      <section title="Ticket">
        <row title="Number" detail=${t.id}/>
        <row title="Customer" detail=${t.customer}/>
      </section>
    </screen>` : html`<screen title="No ticket"><empty icon="chat" title="No ticket open"/></screen>`;
};

const paint = () => render(html`
  <split detail=${open !== null} @close=${() => { open = null; paint(); }}>
    <screen title="Tickets" style="list">
      <list>
        ${repeat(tickets, (t) => t.id, (t) => html`<row title=${t.subject} subtitle=${t.customer} nav
            ?selected=${t.id === open} @tap=${() => show(t.id)}/>`)}
      </list>
    </screen>
    ${detail()}
  </split>`);

show(linked());
