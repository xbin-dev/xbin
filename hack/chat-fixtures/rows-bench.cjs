// hack/chat-fixtures/rows-bench.cjs — templates vs elements for the chat
// kit's transcript rows: the measurement behind the kit drawing rows as lit
// template functions of (block, ctx). Not a test; run it against any xbind
// (it only needs /vendor/lit-all.min.js from it — the UI harness's will do):
//
//   PLAYWRIGHT_DIR=<dir with node_modules/playwright> URL=http://127.0.0.1:<port> node hack/chat-fixtures/rows-bench.cjs
//
// It draws the same rows three ways inside one scroller in a LitElement's
// shadow root, on a CPU throttled 4× (as the agentLongPerf pass):
//   tpl    a lit template function of (block, ctx) per row, keyed by repeat,
//          skipped by guard([v]) — what bx-agent does today
//   el     a custom element per row with its own shadow root (static styles
//          shared through adoptedStyleSheets), .block/.ctx properties
//   light  a custom element per row rendering into light DOM
// and times: first render of N rows; 200 updates of the last row (a
// streaming message); a window step (30 rows added at the top, then 30
// removed at the bottom); the heap after.
const path = require('path');
const URL = process.env.URL;
if (!URL || !process.env.PLAYWRIGHT_DIR) throw new Error('set URL (an xbind to load lit from) and PLAYWRIGHT_DIR');
const pw = require(path.join(process.env.PLAYWRIGHT_DIR, 'node_modules', 'playwright'));

async function main() {
  const browser = await pw.chromium.launch();
  const page = await browser.newPage({ viewport: { width: 1400, height: 1300 } });
  await page.goto(URL + '/login');
  const cdp = await page.context().newCDPSession(page);
  await cdp.send('Emulation.setCPUThrottlingRate', { rate: 4 });
  const out = {};
  for (const mode of ['tpl', 'el', 'light']) {
    for (const n of [30, 120, 2000]) {
      const runs = [];
      for (let rep = 0; rep < 5; rep++) {
        await page.reload();
        const r = await page.evaluate(bench, { mode, n });
        await cdp.send('HeapProfiler.collectGarbage');
        const heap = await cdp.send('Runtime.getHeapUsage');
        r.heapMiB = heap.usedSize / 1048576;
        const dom = await cdp.send('Memory.getDOMCounters');
        r.nodes = dom.nodes;
        runs.push(r);
      }
      const med = (k) => { const xs = runs.map((r) => r[k]).sort((a, b) => a - b); return Math.round(xs[2] * 10) / 10; };
      out[`${mode} n=${n}`] = { first: med('first'), stream200: med('stream'), windowStep: med('win'), heapMiB: med('heapMiB'), nodes: med('nodes') };
      console.log(`${mode} n=${n}`, JSON.stringify(out[`${mode} n=${n}`]));
    }
  }
  await browser.close();
}

// runs in the page
async function bench({ mode, n }) {
  const lit = await import('/vendor/lit-all.min.js');
  const { LitElement, html, css, nothing, repeat, guard, unsafeHTML } = lit;
  const rowCss = css`
    .row { margin: 0 0 12px; }
    .who { font: 600 11px/14px system-ui; text-transform: uppercase; color: #a3a6b6; }
    .bubble { font: 14px/20px system-ui; }
    .tool { border: 1px solid #33353f; border-radius: 2px; margin: 0 0 8px; background: #1f2028; }
    .tool > summary { list-style: none; cursor: pointer; padding: 4px 8px; display: flex; gap: 8px; font: 12px/18px monospace; }
    .title { flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    .chip { display: inline-flex; gap: 4px; height: 20px; padding: 0 6px; border: 1px solid #33353f; font: 600 11px/14px system-ui; text-transform: uppercase; }
    .chip.completed { color: #a3cf5e; }
    .body { padding: 8px; border-top: 1px solid #33353f; }
    pre { margin: 0; font: 12px/18px monospace; white-space: pre-wrap; }
    .thought > summary { color: #a3a6b6; cursor: pointer; }
  `;
  // a block of the three common kinds, the long pass's mix: a markdown message, an edit call, a thought every tenth
  const mk = (i) => {
    const kind = i % 10 === 0 ? 'thought' : i % 2 ? 'assistant' : 'tool';
    return { k: String(i), v: 0, kind, text: `### Unit ${i}\n\nChanging file${i}.go: rename the helper, keep behaviour.`,
      html: `<h3>Unit ${i}</h3><p>Changing <code>file${i}.go</code>:</p><ul><li>rename the helper</li><li>keep <strong>behaviour</strong></li></ul><pre><code>func helper${i}() int { return ${i} }</code></pre>`,
      rec: { id: 'long' + i, kind: 'edit', title: `Edit file${i}.go`, status: 'completed' } };
  };
  const ctx = { opened: new Set(), isOpen(b, slot) { return this.opened.has(b.k + ':' + slot); }, setOpen() {} };
  const rowTpl = (b, c) => {
    switch (b.kind) {
      case 'assistant': return html`<div class="row agent"><div class="who">agent</div><div class="bubble">${unsafeHTML(b.html)}</div></div>`;
      case 'thought': return html`<div class="row"><details class="thought"><summary>Thought for 2s</summary>${c.isOpen(b, 'body') ? html`<div class="md">${b.text}</div>` : nothing}</details></div>`;
      default: return html`<details class="tool" @toggle=${() => c.setOpen(b, 'body', true)}>
        <summary><span class="ic"></span><span class="title" title=${b.rec.title}>${b.rec.title}</span><span class="chip ${b.rec.status}">${b.rec.status} +4/-4</span></summary>
        ${c.isOpen(b, 'body') ? html`<div class="body"><pre>${b.text}</pre></div>` : nothing}</details>`;
    }
  };
  class RowEl extends LitElement {
    static properties = { block: { attribute: false }, ctx: { attribute: false }, v: { type: Number } };
    static styles = rowCss;
    render() { return rowTpl(this.block, this.ctx); }
  }
  class RowLight extends RowEl {
    static styles = [];
    createRenderRoot() { return this; }
  }
  customElements.define('bxc-row', RowEl);
  customElements.define('bxc-row-light', RowLight);
  class Host extends LitElement {
    static properties = { rows: { attribute: false } };
    static styles = [rowCss, css`:host { display: block; height: 900px; } .scroll { height: 100%; overflow: auto; } bxc-row, bxc-row-light { display: block; }`];
    render() {
      const row = mode === 'tpl' ? (b) => html`<div class="blk" data-k=${b.k}>${guard([b.v], () => rowTpl(b, ctx))}</div>`
        : mode === 'el' ? (b) => html`<bxc-row class="blk" data-k=${b.k} .block=${b} .ctx=${ctx} .v=${b.v}></bxc-row>`
          : (b) => html`<bxc-row-light class="blk" data-k=${b.k} .block=${b} .ctx=${ctx} .v=${b.v}></bxc-row-light>`;
      return html`<div class="scroll">${repeat(this.rows, (b) => b.k, row)}</div>`;
    }
  }
  customElements.define('bxc-host', Host);
  const frame = () => new Promise((r) => requestAnimationFrame(() => setTimeout(r, 0)));
  // painted(): the host and every row element have rendered, and a frame went by
  const painted = async (host) => {
    await host.updateComplete;
    for (const el of host.renderRoot.querySelectorAll('bxc-row, bxc-row-light')) await el.updateComplete;
    await frame();
  };
  const host = document.createElement('bxc-host');
  let rows = Array.from({ length: n }, (_, i) => mk(i + 100));
  host.rows = rows;
  let t = performance.now();
  document.body.replaceChildren(host);
  await painted(host);
  const first = performance.now() - t;
  // a message streaming at the bottom: the last row changes 200 times
  const last = { ...mk(1), k: 'live', kind: 'assistant' };
  rows = [...rows, last];
  host.rows = rows;
  await painted(host);
  t = performance.now();
  for (let i = 0; i < 200; i++) {
    last.html += `<p>Paragraph ${i} of the answer.</p>`;
    last.v++;
    host.rows = [...rows];
    await host.updateComplete;
    const el = host.renderRoot.querySelector('[data-k="live"]');
    if (el.updateComplete) await el.updateComplete;
  }
  await frame();
  const stream = performance.now() - t;
  // a window step: 30 rows in at the top, 30 out at the bottom
  t = performance.now();
  rows = [...Array.from({ length: 30 }, (_, i) => mk(i + 50)), ...rows];
  host.rows = rows;
  await painted(host);
  rows = rows.slice(0, rows.length - 30);
  host.rows = rows;
  await painted(host);
  const win = performance.now() - t;
  return { first, stream, win };
}

main().catch((e) => { console.error(e); process.exit(1); });
