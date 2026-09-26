// tabs — an issue tracker screen with segmented tabs (icons and badges).
// Only the selected tab is materialized: the script switches from "Open" to
// "Mine", so "Open" is empty in the tree and "Mine" holds its rows.
import { html, render, repeat, nothing } from '/vendor/xb-native.js';

let data = null;
let tab = 'open';

async function load() {
  data = await (await xbin.fetch(`/api/${xbin.self}/issues`)).json();
  paint();
}

const issue = (i) => html`
  <row title=${i.title} subtitle=${`#${i.id} · ${i.author}`} badge=${i.label} tone=${i.label === 'bug' ? 'danger' : i.label === 'feature' ? 'accent' : 'muted'}
       icon=${i.state === 'closed' ? 'check' : 'info'} nav @tap=${() => {}}/>`;
const group = (list, empty) => (list.length ? repeat(list, (i) => i.id, issue) : html`<empty icon="check" title=${empty}/>`);

const paint = () => render(!data ? nothing : html`
  <screen title="Issues" subtitle=${data.repo} style="list" search="" @search=${() => {}}>
    <tabs style="segmented" selected=${tab} @change=${(e) => { tab = e.key; paint(); }}>
      <tab key="open" title="Open" icon="info" badge=${String(data.open.length)}>
        <section>${group(data.open, 'Nothing open')}</section>
      </tab>
      <tab key="mine" title="Mine" icon="person" badge=${String(data.mine.length)}>
        <section title="Assigned to you">${group(data.mine, 'Nothing assigned')}</section>
        <section title="Waiting for review">${group(data.review, 'Nothing to review')}</section>
      </tab>
      <tab key="closed" title="Closed" icon="archive">
        <section>${group(data.closed, 'Nothing closed yet')}</section>
      </tab>
    </tabs>
  </screen>`);

load();
