// hack/demo/measure/browser.js — the browser half of the measurements
// (browser_test.go starts it against a daemon it set up; see
// hack/demo/measurements.md): real Chromium through Playwright, driving the
// shell as a person does. Two runs, each its own process (the harness lib
// reads URL once):
//
//   node browser.js static    URL = xbind itself. The handbook tile open in
//     the shell; each sample writes a new index.html (an editor's save) and
//     waits for the tile's window to show it. The page reports its own first
//     contentful paint (PerformanceObserver 'paint'), as wall-clock time:
//     save → the new page painted.
//   node browser.js predict   URL = the latency proxy in front of xbind. The
//     handbook's terminal; each sample presses one key at the shell prompt
//     and times keydown (the event's own timestamp) → the typed glyph in the
//     terminal's DOM, either the prediction overlay (.pov, web/bx-terminal.js)
//     or xterm's rows holding the shell's echo — a MutationObserver over
//     .xterm-screen. Modes auto, on and off (the 🔧 menu's choice).
//
// Env: URL, MEASURE_OUT, MEASURE_WS (the workspace), MEASURE_USER /
// MEASURE_PASSWORD, MEASURE_VERSIONS (static: a JSON list of {file,
// headline}), MEASURE_KEYS (predict: samples per mode), PLAYWRIGHT_DIR.
const path = require('path');
const fs = require('fs');
const { performance } = require('perf_hooks');
const { pw, URL, login, closeCtx, settle, fr, waitSel, openShell, openTile, tileFrame, sleep, log } = require('../../ui-harness/lib');

const TILE = 'apps/handbook';
const OUT = process.env.MEASURE_OUT;
const now = () => performance.timeOrigin + performance.now(); // wall clock, sub-ms

function writer(name) {
  const file = path.join(OUT, name + '.jsonl');
  fs.writeFileSync(file, '');
  return (o) => fs.appendFileSync(file, JSON.stringify({ at: Date.now(), ...o }) + '\n');
}

async function staticVisible(browser) {
  const out = writer('browser-static');
  const versions = JSON.parse(fs.readFileSync(process.env.MEASURE_VERSIONS, 'utf8'));
  const index = path.join(process.env.MEASURE_WS, TILE, 'index.html');
  const { ctx, page } = await login(browser, process.env.MEASURE_USER, process.env.MEASURE_PASSWORD);
  await openShell(page);
  await openTile(page, TILE);
  // the home screen's default tiles push the card below the fold, and an
  // offscreen frame is never painted: bring it into view, as a person would
  await page.locator(`.card[data-path="${TILE}"]`).evaluate((el) => el.scrollIntoView({ block: 'start' }));
  await settle(page);
  // what the tile's window shows now: its headline and its own paint time
  const probe = async () => {
    const f = page.frames().find((x) => x.url().includes(`/c/${TILE}/`));
    if (!f) return null;
    try {
      return await f.evaluate(() => {
        const nav = performance.getEntriesByType('navigation')[0];
        return {
          headline: document.querySelector('#headline')?.textContent ?? null,
          fcp: document.documentElement.dataset.fcp ? Number(document.documentElement.dataset.fcp) : null,
          origin: performance.timeOrigin,
          responseEnd: nav ? performance.timeOrigin + nav.responseEnd : null,
          dcl: nav ? performance.timeOrigin + nav.domContentLoadedEventEnd : null,
        };
      });
    } catch { return null; } // navigating: its context went away
  };
  for (let t0 = Date.now(); ; await sleep(50)) {
    const p = await probe();
    if (p && p.fcp) break;
    if (Date.now() - t0 > 30000) throw new Error('the handbook never painted in the shell');
  }
  for (let i = 0; i < versions.length; i++) {
    const v = versions[i];
    await sleep(1200); // the last save's batch is over (the watcher debounces 300 ms)
    const html = fs.readFileSync(v.file);
    const t0 = now();
    fs.writeFileSync(index, html);
    let p = null;
    for (const deadline = Date.now() + 15000; ; await sleep(10)) {
      p = await probe();
      if (p && p.headline === v.headline && p.fcp) break;
      if (Date.now() > deadline) {
        const f = page.frames().find((x) => x.url().includes(`/c/${TILE}/`));
        const dbg = f ? await f.evaluate(() => ({ vis: document.visibilityState, paints: performance.getEntriesByType('paint').map((e) => e.name), url: location.href })).catch((e) => String(e)) : 'no frame';
        await page.screenshot({ path: path.join(OUT, `static-stuck-${i}.png`) }).catch(() => {});
        throw new Error(`save ${i}: the window never showed "${v.headline}" (last ${JSON.stringify(p)}; ${JSON.stringify(dbg)})`);
      }
    }
    const row = {
      i, headline: v.headline,
      visibleMs: p.fcp - t0, // save → the new page's first contentful paint
      navStartMs: p.origin - t0, // save → the window began loading it (the reload event reached the shell)
      responseEndMs: p.responseEnd - t0,
      dclMs: p.dcl - t0,
    };
    out(row);
    log(`static ${i}: visible after ${row.visibleMs.toFixed(1)} ms (navigation began at ${row.navStartMs.toFixed(1)})`);
  }
  await closeCtx(ctx, page);
}

async function predict(browser) {
  const out = writer('browser-predict');
  const perMode = Number(process.env.MEASURE_KEYS || 40);
  const { ctx, page } = await login(browser, process.env.MEASURE_USER, process.env.MEASURE_PASSWORD);
  await openShell(page);
  await openTile(page, TILE);
  await page.locator(`.card[data-path="${TILE}"]`).evaluate((el) => el.scrollIntoView({ block: 'start' }));
  await fr(page, TILE, (f) => f.open('term'));
  await fr(page, TILE, (f) => { if (!f.tabs.length) f.newTerm(); });
  const termSel = `bx-frame[src="${TILE}"] bx-terminal`;
  await waitSel(page, `${termSel} textarea`, { timeout: 60000 });
  const term = page.locator(termSel).first();
  const api = (fn, arg) => term.evaluate((el, a) => new Function('t', 'arg', `return (${a.fsrc})(t, arg)`)(el.testApi(), a.arg), { fsrc: fn.toString(), arg: arg ?? null });
  const until = async (fn, label, timeout = 30000) => {
    const t0 = Date.now();
    for (;;) {
      if (await api(fn)) return;
      if (Date.now() - t0 > timeout) throw new Error(`timed out waiting for ${label}`);
      await sleep(40);
    }
  };
  await until((t) => t.echoAck, 'the session frame with echoAck');
  await until((t) => t.cursor && t.cursor.col > 0, 'a shell prompt', 60000);
  await fr(page, TILE, (f) => f.focusTerminal());
  // the link as the terminal sees it: its smoothed ping round trip
  await sleep(6000); // a ping goes every 5 s
  await until((t) => t.rtt != null, 'a measured round trip');
  const rttAtStart = await api((t) => t.rtt);
  log(`terminal open; its round trip ${rttAtStart} ms`);

  // the probe: armed per key, it times keydown → the glyph at the cursor's
  // cell, from either layer, in the element's own document
  await term.evaluate((el) => {
    const screen = el.shadowRoot.querySelector('.xterm-screen');
    const rows = screen.querySelector('.xterm-rows');
    const st = { armed: null, result: null };
    const cellOf = (row, col) => {
      const r = rows.children[row];
      return r ? (r.textContent || '')[col] : undefined;
    };
    const check = () => {
      const a = st.armed;
      if (!a || st.result || a.key == null) return;
      const ov = el.testApi().overlay;
      const pov = screen.querySelector('.pov');
      const predicted = !!pov && !pov.hidden && ov.some((c) => c.row === a.row && a.col >= c.col && a.col < c.col + c.text.length && c.text[a.col - c.col] === a.ch);
      const echoed = cellOf(a.row, a.col) === a.ch;
      if (!predicted && !echoed) return;
      // dom: the glyph is in the DOM (the earliest it can be on screen);
      // frame: the next animation frame after that (the latest it is
      // painted — for xterm's echo, rendered inside a frame, possibly a
      // frame late)
      const r = st.result = { key: a.key, dom: performance.now(), frame: null, via: predicted ? 'prediction' : 'echo' };
      requestAnimationFrame(() => { r.frame = performance.now(); });
    };
    new MutationObserver(check).observe(screen, { childList: true, subtree: true, characterData: true, attributes: true, attributeFilter: ['hidden'] });
    window.addEventListener('keydown', (e) => { if (st.armed && st.armed.key == null) { st.armed.key = e.timeStamp; check(); } }, true);
    el.measureProbe = {
      arm(ch) { const c = el.testApi().cursor; st.armed = { ch, row: c.row, col: c.col, key: null }; st.result = null; },
      get result() { return st.result; },
      get armed() { return st.armed; },
    };
  });

  // the page's frame rate (rAF over 60 frames): a frame is how much later
  // than its DOM change a glyph can be painted
  const fps = await page.evaluate(() => new Promise((res) => {
    const ts = [];
    const f = (t) => { ts.push(t); if (ts.length < 61) requestAnimationFrame(f); else res(60000 / (ts[60] - ts[0])); };
    requestAnimationFrame(f);
  }));
  log(`frames: ${fps.toFixed(1)} per second`);

  // what a person types at a prompt, one key at a time
  const lines = ['git status', 'make test', 'ls -la src', 'cat README.md', 'grep -rn TODO .', 'docker ps', 'kubectl get pods', 'tail -f app.log'];
  for (const mode of ['auto', 'on', 'off']) {
    await api((t, m) => t.setPredict(m), mode);
    await sleep(300);
    let n = 0;
    for (let li = 0; n < perMode; li++) {
      const line = lines[li % lines.length];
      for (const ch of line) {
        if (n >= perMode) break;
        if (ch === ' ') { await page.keyboard.press('Space'); await sleep(400); continue; } // spaces typed, not timed
        await term.evaluate((el, c) => el.measureProbe.arm(c), ch);
        await page.keyboard.press(ch);
        let r = null;
        for (const deadline = Date.now() + 5000; ; await sleep(5)) {
          r = await term.evaluate((el) => el.measureProbe.result);
          if (r && r.frame != null) break;
          if (Date.now() > deadline) break;
        }
        const st = await api((t) => ({ rtt: t.rtt, predicting: t.predicting, pending: t.pending }));
        if (!r || r.frame == null) {
          out({ mode, ch, err: 'the glyph never appeared within 5 s', ...st });
        } else {
          out({ mode, ch, glyphDomMs: r.dom - r.key, glyphFrameMs: r.frame - r.key, via: r.via, rtt: st.rtt, predicting: st.predicting });
          n++;
        }
        await until((t) => t.pending === 0, 'the echo to confirm', 10000).catch(() => {});
        await sleep(250); // a typing pause: each key on its own
      }
      await page.keyboard.press('Control+u');
      await sleep(500);
    }
    log(`predict ${mode}: ${n} keys`);
  }
  out({ summary: true, rttAtStart, rttAtEnd: await api((t) => t.rtt), fps });
  await api((t) => t.setPredict('auto'));
  await closeCtx(ctx, page);
}

(async () => {
  const which = process.argv[2];
  const browser = await pw.chromium.launch();
  try {
    if (which === 'static') await staticVisible(browser);
    else if (which === 'predict') await predict(browser);
    else throw new Error('usage: node browser.js static|predict');
  } finally {
    await browser.close();
  }
  log(`${which}: done (${URL})`);
})().catch((e) => { console.error(e); process.exit(1); });
