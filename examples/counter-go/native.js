// examples/counter-go/native.js — the counter as a native screen for the xbin
// app (docs/frontend-kit.md). Same endpoints as index.html: GET /count to show
// it, POST /count then a reload to increment. The web page stays the tile's UI
// everywhere else; this file is only loaded by the app's native runtime.
import { html, render, nothing } from '/vendor/xb-native.js';
import { selfApi } from '/vendor/bx-kit.js';

let count = null, busy = false, err = '';

async function load() {
  try { count = (await selfApi('/count')).count; err = ''; } catch (e) { err = String(e.message ?? e); }
  paint();
}
async function inc() {
  busy = true; paint();
  try { await selfApi('/count', { method: 'POST' }); await load(); } catch (e) { err = String(e.message ?? e); }
  finally { busy = false; paint(); }
}
const paint = () => render(html`
  <screen title="Counter" style="form">
    <section>
      <row title="Count" detail=${count ?? '…'} mono="detail"/>
      <button role="primary" icon="plus" ?busy=${busy} @tap=${inc}>+1</button>
      ${err ? html`<notice tone="danger" text=${err}/>` : nothing}
    </section>
  </screen>`);
paint();   // at once: the app wants a tree before the backend answers (a cold start can take seconds)
load();
