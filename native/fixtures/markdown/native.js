// markdown — a runbook viewer: the tile fetches a markdown file and renders
// it with the markdown primitive (the runtime lexes it into tokens). The
// document uses every token shape: headings 1–6, emphasis, strikethrough,
// code spans, links, line breaks, bullet/ordered/loose/task lists, code
// blocks with and without a language, a blockquote, an aligned table and a
// rule. Tapped links go to xbin.native.open.
import { html, render, nothing } from '/vendor/xb-native.js';

let doc = null;
let updated = '';

async function load() {
  const r = await xbin.fetch(`/api/${xbin.self}/runbooks/rotate-db-password.md`);
  doc = await r.text();
  updated = r.headers.get('last-modified') ?? '';
  paint();
}

const paint = () => render(doc == null ? nothing : html`
  <screen title="Rotate the database password" subtitle=${updated ? `updated ${updated}` : ''} style="scroll">
    <markdown source=${doc} @link=${(e) => xbin.native.open(e.href)}/>
  </screen>`);

load();
