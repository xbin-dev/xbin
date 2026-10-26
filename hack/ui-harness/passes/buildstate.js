// hack/ui-harness/passes/buildstate.js — covers D196: a window says what
// its tile's backend is doing while it builds and starts. apps/bs-go is a
// Go tile whose backend waits 4 s before it listens (a slow start), and
// whose page calls it on load. As the admin:
//   1. open and live, a save rebuilds it: the window's live square breathes
//      and its title bar says "Compiling…", then "Starting…", then nothing
//      once it serves; the frame's bx-build events go compile → starting →
//      live, and the sidebar row carries the build glyph meanwhile
//      (shot: buildstate-starting-<theme>);
//   2. another save: a window opened in another browser while it is
//      starting says "Starting…" at once (the /components row's backend
//      state), and nothing once it serves.
// The tile is removed at the end.
const path = require('path');
const { URL, fs, login, closeCtx, openShell, usePersonalScreen, settle, sh, shotEl, checker, sleep, THEME } = require('../lib');

const WS = process.env.WS || '';
const T = 'apps/bs-go';

const MAIN = (n) => `package main

import (
	"fmt"
	"net/http"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// a slow start (build ${n}): the window says "Starting…" meanwhile
func main() {
	time.Sleep(4 * time.Second)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ping", func(w http.ResponseWriter, r *http.Request) { fmt.Fprintln(w, "pong ${n}") })
	xbin.Serve(mux)
}
`;
const PAGE = `<!doctype html><meta charset="utf-8"><title>bs-go</title>
<p id="out">calling…</p>
<script type="module">
  const r = await window.xbin.fetch('/api/${T}/ping');
  document.getElementById('out').textContent = r.ok ? await r.text() : 'error ' + r.status;
</script>`;

// the window's title bar and the sidebar row, as the person sees them
const look = (page) => page.evaluate((tile) => {
  const sh = document.querySelector('bx-shell')?.shadowRoot, cv = sh?.querySelector('bx-canvas')?.shadowRoot;
  const card = cv?.querySelector(`.card[data-path="${tile}"]`);
  return {
    card: !!card,
    chip: card?.querySelector('.bchip')?.textContent ?? '',
    square: card?.querySelector('.lsq')?.className.replace('lsq', '').trim() ?? '',
    side: sh?.querySelector('bx-side')?.shadowRoot?.querySelector(`.item[data-path="${tile}"] .bld`)?.dataset.build ?? '',
  };
}, T);

// watch(page) → a function that returns everything look() saw since, every 100 ms
function watch(page) {
  const seen = [];
  let on = true;
  (async () => { while (on) { seen.push(await look(page).catch(() => ({}))); await sleep(100); } })();
  return () => { on = false; return seen; };
}

async function until(fn, label, timeout = 60000) {
  const deadline = Date.now() + timeout;
  for (;;) {
    const v = await fn().catch(() => null);
    if (v) return v;
    if (Date.now() > deadline) throw new Error(`timed out: ${label}`);
    await sleep(150);
  }
}

async function buildState(browser) {
  const { check, skip, done } = checker(`build-state-${THEME}`);
  if (!WS) { skip('no $WS'); done(); return; }
  const A = await login(browser, 'admin', 'admin', { viewport: { width: 1400, height: 900 } });
  const original = await (await A.ctx.request.get(`${URL}/api/xbin/prefs/layout`)).json().catch(() => null);
  let B = null;
  try {
    const r = await A.ctx.request.post(`${URL}/api/xbin/create`, { data: { path: T, runtime: 'go', title: 'build state' } });
    check(r.ok() || r.status() === 409, `${T} exists (${r.status()})`);
    const dir = path.join(WS, T);
    await until(async () => fs.existsSync(path.join(dir, 'xbin.json')), 'the scaffold on disk', 15000);
    fs.mkdirSync(path.join(dir, 'backend'), { recursive: true });
    fs.writeFileSync(path.join(dir, 'backend', 'main.go'), MAIN(1));
    fs.writeFileSync(path.join(dir, 'index.html'), PAGE);
    await sleep(1500); // the watcher settles on the new files

    // 1. open it, live; then a save rebuilds it: compile, then start, then live
    await openShell(A.page);
    await usePersonalScreen(A.page);
    await sh(A.page, (t, p) => t.openTile(p), T);
    await until(async () => { const v = await look(A.page); return v.card && !v.chip && v.square === 'live' ? v : null; }, 'live after the first build');
    const out = await until(async () => {
      const t = await A.page.frameLocator(`bx-canvas .card[data-path="${T}"] bx-frame iframe`).locator('#out').textContent();
      return /pong/.test(t) ? t : null;
    }, 'the page answered', 20000).catch(() => '');
    check(/pong \d/.test(out), `the page got its backend's answer once it started (${JSON.stringify(out)})`);
    await sh(A.page, () => {
      window.__phases = [];
      document.addEventListener('bx-build', (e) => window.__phases.push(e.detail?.phase || e.detail?.state));
    });
    const stop = watch(A.page);
    fs.writeFileSync(path.join(dir, 'backend', 'main.go'), MAIN(2));
    await until(async () => (await look(A.page)).chip === 'Starting…', 'Starting…').catch(() => null);
    await shotEl(A.page, `bx-canvas .card[data-path="${T}"]`, `buildstate-starting-${THEME}`).catch(() => {});
    await until(async () => { const v = await look(A.page); return v.card && !v.chip && v.square === 'live' ? v : null; }, 'live').catch(async () => { check(false, `never live: ${JSON.stringify(await look(A.page))}`); });
    const seen = stop();
    const chips = [...new Set(seen.map((v) => v.chip).filter(Boolean))];
    check(chips.includes('Compiling…') && chips.includes('Starting…') && chips.indexOf('Compiling…') < chips.indexOf('Starting…'),
      `the title bar says Compiling…, then Starting… (${JSON.stringify(chips)})`);
    check(seen.some((v) => v.square === 'building'), 'the live square breathes meanwhile');
    check(seen.some((v) => v.side), `the sidebar row carries the build glyph meanwhile (${JSON.stringify([...new Set(seen.map((v) => v.side))])})`);
    const phases = await sh(A.page, () => window.__phases);
    const ci = phases.indexOf('compile'), si = phases.indexOf('starting');
    check(ci >= 0 && si > ci && phases.slice(si).includes('live'), `bx-build: compile → starting → live (${JSON.stringify(phases)})`);

    // 2. another save; a window opened while it starts knows at once
    fs.writeFileSync(path.join(dir, 'backend', 'main.go'), MAIN(3));
    await until(async () => (await look(A.page)).chip === 'Starting…', 'Starting… after the save');
    B = await login(browser, 'admin', 'admin', { viewport: { width: 1400, height: 900 } });
    await openShell(B.page);
    const first = await until(async () => { const v = await look(B.page); return v.card ? v : null; }, 'B\'s window', 10000).catch(async () => look(B.page));
    check(first.chip === 'Starting…', `a window opened while it starts says "Starting…" at once (${JSON.stringify(first)})`);
    const live = await until(async () => { const v = await look(B.page); return v.card && !v.chip ? v : null; }, 'B live').catch(async () => look(B.page));
    check(!live.chip, 'and nothing once it serves');
  } finally {
    if (original) await A.ctx.request.put(`${URL}/api/xbin/prefs/layout`, { data: original, headers: { 'X-Prefs-Writer': 'harness-buildstate' } }).catch(() => {});
    const dir = path.join(WS, T);
    if (WS && path.isAbsolute(WS) && dir === path.join(WS, 'apps', 'bs-go')) fs.rmSync(dir, { recursive: true, force: true });
    if (B) await closeCtx(B.ctx, B.page);
    await settle(A.page).catch(() => {});
    await closeCtx(A.ctx, A.page);
  }
  done();
}

module.exports = { buildState };
