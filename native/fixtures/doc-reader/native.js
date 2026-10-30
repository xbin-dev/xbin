// doc-reader — a report beside the conversation that made it (D208: `split`
// rev 3, `canvas` rev 2). On a tablet or an open Duo the split's columns
// share the width (`halves`): the conversation, live, in one, the report in
// the other; on a phone the report is pushed over the conversation
// (`detail`; Back reports `close`). The report fills its screen (`canvas
// fill`): its own page scrolls and pinch-zooms, the bottom toolbar's − and +
// set the page `zoom` (the percentage resets it), and a tapped link — which
// never navigates — reaches the tile (`@link`), which shares the file it
// names.
import { html, render, repeat, nothing, native } from '/vendor/xb-native.js';
import { selfApi } from '/vendor/bx-kit.js';

let conv = null;
let report = null;
let open = true;
let zoom = 1;
let shared = '';

const STEPS = [0.5, 0.67, 0.8, 0.9, 1, 1.1, 1.25, 1.5, 1.75, 2, 2.5, 3];
const step = (dir) => {
  const i = STEPS.findIndex((z) => z >= zoom - 1e-6);
  zoom = STEPS[Math.max(0, Math.min(STEPS.length - 1, (i < 0 ? STEPS.length - 1 : i) + dir))];
  paint();
};
const pct = new Intl.NumberFormat('en-US', { style: 'percent' });

async function load() {
  [conv, report] = await Promise.all([selfApi('/conversation'), selfApi('/report')]);
  paint();
}

// a link in the report: the file it names, through the share sheet
async function link(e) {
  const f = report.files.find((x) => x.href === e.href);
  if (!f) return;
  shared = f.name;
  paint();
  await native.share({ file: `files/${encodeURIComponent(f.path)}` });
}

const doc = () => html`
  <screen title=${report.name} subtitle=${`v${report.version} · ${report.files.length} linked files`} style="scroll">
    <toolbar><button icon="xmark" @tap=${() => { open = false; paint(); }}>Close</button></toolbar>
    <toolbar place="bottom">
      <button icon="minus" ?disabled=${zoom <= STEPS[0]} @tap=${() => step(-1)}>Smaller</button>
      <button @tap=${() => { zoom = 1; paint(); }}>${pct.format(zoom)}</button>
      <button icon="plus" ?disabled=${zoom >= STEPS[STEPS.length - 1]} @tap=${() => step(1)}>Larger</button>
    </toolbar>
    ${shared ? html`<notice tone="info" text=${`Sharing ${shared}…`}/>` : nothing}
    <canvas html=${report.html} fill zoom=${zoom} @link=${link}/>
  </screen>`;

const paint = () => render(!conv ? nothing : html`
  <split halves detail=${open} @close=${() => { open = false; paint(); }}>
    <screen title=${conv.title} subtitle=${conv.model}>
      <transcript follow>
        ${repeat(conv.messages, (m) => m.id, (m) => html`<message role=${m.role} sender=${m.sender} text=${m.text} time=${m.time}/>`)}
      </transcript>
      <composer placeholder="Ask about the report"/>
    </screen>
    ${doc()}
  </split>`);

load();
