// A rev-1 navigation stack for the UI tests (XbinNavigationTests): a `nav`
// whose root lists runbooks; a tap pushes one, Back reports `pop {depth}`
// and the tile drops it — the pattern the agent template's list-first view
// builds on (a list root, a chat pushed, Back returns). Static data.
import { html, render, repeat } from '/vendor/xb-native.js';

const books = [
  { id: 'rb-1', title: 'Rotate the database password', steps: 6 },
  { id: 'rb-2', title: 'Restore a backup to staging', steps: 9 },
  { id: 'rb-3', title: 'Drain a node for maintenance', steps: 4 },
];
let open = [];

const paint = () => render(html`
  <nav @pop=${(e) => { open = open.slice(0, e.depth - 1); paint(); }}>
    <screen title="Runbooks" style="list">
      <section>
        ${repeat(books, (b) => b.id, (b) => html`<row title=${b.title} nav @tap=${() => { open = [b.id]; paint(); }}/>`)}
      </section>
    </screen>
    ${repeat(open, (id) => id, (id) => {
      const b = books.find((x) => x.id === id);
      return html`<screen title=${b.title} style="list">
        <section title="Runbook"><row title="Id" detail=${b.id}/><row title="Steps" detail=${String(b.steps)}/></section>
      </screen>`;
    })}
  </nav>`);

paint();
