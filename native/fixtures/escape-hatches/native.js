// escape-hatches — a CI tile: the live build log is a terminal on the tile's
// own pty WebSocket; the coverage report is one of the tile's pages in a
// canvas (a WebView island); the build badge is static no-script HTML in a
// canvas. Everything around them is native.
import { html, render, nothing } from '/vendor/xb-native.js';

let b = null;

async function load() {
  b = await (await xbin.fetch(`/api/${xbin.self}/builds/latest`)).json();
  paint();
}

const badge = (st) => `<!doctype html><meta charset="utf-8"><style>
body{margin:0;font:600 13px system-ui;display:flex;gap:0}
span{padding:4px 8px}.k{background:#555;color:#fff;border-radius:4px 0 0 4px}
.v{background:${st === 'passing' ? '#2e7d32' : '#c62828'};color:#fff;border-radius:0 4px 4px 0}
</style><span class="k">build</span><span class="v">${st}</span>`;

const paint = () => render(!b ? nothing : html`
  <screen title=${`Build #${b.number}`} subtitle=${`${b.branch} · ${b.commit}`} style="scroll">
    <toolbar>
      <badge tone=${b.state === 'running' ? 'accent' : b.state === 'passed' ? 'ok' : 'danger'} ?pulse=${b.state === 'running'}>${b.state}</badge>
      <button role="destructive" icon="stop" @tap=${() => {}}>Cancel</button>
    </toolbar>
    <stack gap="m">
      <canvas html=${badge(b.lastResult)} height="xs"/>
      <progress value=${b.step / b.steps} label=${`step ${b.step} of ${b.steps}: ${b.stepName}`}/>
      <terminal src=${`term/${b.number}`} title=${`make ${b.target}`}/>
      <text style="headline">Coverage</text>
      <canvas src=${`reports/${b.number}/coverage.html`} height="l"/>
    </stack>
  </screen>`);

load();
