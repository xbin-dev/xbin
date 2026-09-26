// icons — a label editor's icon picker: every icon name of the vocabulary,
// grouped, each drawn with its name under it (so a renderer's symbol mapping
// can be checked by eye), and a preview row of the label being edited.
import { html, render, repeat, nothing } from '/vendor/xb-native.js';

let catalog = null;
let label = null;

async function load() {
  const [c, l] = await Promise.all([
    xbin.fetch(`/api/${xbin.self}/icons`).then((r) => r.json()),
    xbin.fetch(`/api/${xbin.self}/labels/deploy`).then((r) => r.json()),
  ]);
  catalog = c; label = l;
  paint();
}

const cell = (name) => html`
  <stack gap="xs" align="center">
    <icon name=${name} tone=${name === label.icon ? 'accent' : nothing}/>
    <text style="caption2" tone=${name === label.icon ? 'accent' : 'muted'}>${name}</text>
  </stack>`;

const paint = () => render(!catalog ? nothing : html`
  <screen title="Label icon" style="list">
    <section title="Preview">
      <row title=${label.name} subtitle=${label.description} icon=${label.icon} badge=${String(label.count)} tone="accent"/>
    </section>
    ${repeat(catalog.groups, (g) => g.title, (g) => html`
      <section title=${g.title} badge=${String(g.icons.length)}>
        <stack axis="h" wrap gap="l">${g.icons.map(cell)}</stack>
      </section>`)}
  </screen>`);

load();
