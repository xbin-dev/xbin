// A report beside its conversation for the UI tests (XbinNavigationTests,
// D208): a `split` rev 3 with `halves` — on an iPad the conversation and the
// report half each, on an iPhone the report pushed over the conversation —
// whose report is a `canvas` rev 2 that fills its screen, zooms with − / +
// (`zoom`) and hands a tapped link to the tile (`@link`), which says which
// and shares the file it names (files/q3.csv, a file of this tile). Static
// data; what the app lacks (an older app) falls back to index.html.
import { html, render, repeat, nothing, native } from '/vendor/xb-native.js';

const messages = [
  { id: 'm1', role: 'user', sender: 'You', text: 'Summarise Q3 revenue by region and attach the figures.' },
  { id: 'm2', role: 'assistant', sender: 'Agent', text: 'Done: EU grew 12 %, the US was flat. The report is open beside this conversation; the figures are linked from it.' },
];
const rows = Array.from({ length: 40 }, (_, i) => `<tr><td>Week ${i + 1}</td><td>${(200 + i * 3) % 260}</td><td>${(180 + i * 7) % 240}</td></tr>`).join('');
const REPORT = '<!doctype html><html><head><style>body{margin:16px;font:16px/1.5 system-ui;color:#111;background:#fff}' +
  'table{border-collapse:collapse;width:100%}td,th{border-bottom:1px solid #ddd;padding:6px;text-align:left}a{color:#1f3dff}</style></head>' +
  '<body><h1>Q3 revenue</h1><p>EU grew 12 % on Q2; the US was flat. <a href="files/q3.csv">The figures</a> are attached.</p>' +
  `<table><tr><th>Week</th><th>EU</th><th>US</th></tr>${rows}</table><p><a href="files/q3.csv">Download the figures</a></p></body></html>`;
const LINKS = { 'files/q3.csv': 'files/q3.csv' };

let open = true;
let zoom = 1;
let tapped = '';
const STEPS = [0.5, 0.67, 0.8, 0.9, 1, 1.1, 1.25, 1.5, 1.75, 2, 2.5, 3];
const step = (dir) => {
  const i = STEPS.findIndex((z) => z >= zoom - 1e-6);
  zoom = STEPS[Math.max(0, Math.min(STEPS.length - 1, (i < 0 ? STEPS.length - 1 : i) + dir))];
  paint();
};

async function link(e) {
  tapped = e.href;
  paint();
  if (LINKS[e.href]) await native.share({ file: LINKS[e.href] });
}

const doc = () => html`
  <screen title="q3-report.html" subtitle="v1" style="scroll">
    <toolbar place="leading"><button icon="xmark" @tap=${() => { open = false; paint(); }}>Close</button></toolbar>
    <toolbar place="bottom">
      <button icon="minus" @tap=${() => step(-1)}>Zoom out</button>
      <button @tap=${() => { zoom = 1; paint(); }}>${`${Math.round(zoom * 100)}%`}</button>
      <button icon="plus" @tap=${() => step(1)}>Zoom in</button>
    </toolbar>
    ${tapped ? html`<notice tone="info" text=${`Tapped ${tapped}`}/>` : nothing}
    <canvas html=${REPORT} fill zoom=${zoom} @link=${link}/>
  </screen>`;

const paint = () => render(html`
  <split halves detail=${open} @close=${() => { open = false; paint(); }}>
    <screen title="Q3 review" subtitle="report">
      <toolbar>${open ? nothing : html`<button icon="doc" @tap=${() => { open = true; paint(); }}>Open report</button>`}</toolbar>
      <transcript follow>
        ${repeat(messages, (m) => m.id, (m) => html`<message role=${m.role} sender=${m.sender} text=${m.text}/>`)}
      </transcript>
      <composer placeholder="Ask about the report"/>
    </screen>
    ${doc()}
  </split>`);

paint();
