// hack/ui-harness/passes/livepreview.js — the live port preview (D135), end
// to end: in a coding conversation bound to a fresh sandbox (apps/fakesbx —
// its sandboxes are host directories — or, with HARNESS_ISOLATE=1, the real
// coding-sandbox), fakeopenai's "sandbox serve" writes a page, serves it with
// python3 -m http.server as a background job and calls preview_port. It pins:
//   - the pane opens on the live step, labelled "live from the sandbox",
//     with Reload, the static frame hidden;
//   - the frame is sandbox="allow-scripts allow-forms" (never
//     allow-same-origin), on a /api/~<ticket>/ URL;
//   - the page's script ran (its h1), and from inside it saw: an opaque
//     origin, no cookie, no storage, no identity from /api/xbin/whoami or the
//     tile's API, top navigation blocked;
//   - its parent.postMessage changed nothing (the pane stays open);
//   - Reload loads it again (a fresh ticket);
//   - the conversation is deleted at the end (its server job is killed).
const { log, shot, checker, noGocryptfs } = require('../lib');
const { openAgent, classRows, pickClass, openDialog, closeDialog, createInDialog, turn } = require('./agentsandbox');

const until = (page, fn, arg, timeout = 20000) => page.waitForFunction(fn, arg ?? null, { timeout, polling: 50 });

async function livePreview(browser) {
  const { check, skip, done } = checker('live-preview');
  if (noGocryptfs()) { skip(`apps/agent is held: ${noGocryptfs()}`); return done(); }
  const A = await openAgent(browser, 'admin', 'admin');
  const a = A.page;
  const before = (await classRows(a)).find((r) => r.on)?.id || 'internal';
  let conv = 0;
  try {
    await pickClass(a, 'coding');
    await openDialog(a);
    await createInDialog(a, `harness-live-${Date.now().toString(36)}`);
    await closeDialog(a);
    const said = await turn(a, 'sandbox serve', 'Showing it live.', 60000);
    check(said.includes('Showing it live.'), `the agent served a page and called preview_port (${said.slice(0, 120)})`);
    conv = await a.evaluate(() => +(/c=(\d+)/.exec(location.hash)?.[1] || 0));
    await until(a, () => { const f = document.getElementById('livefr'); return f && !document.getElementById('preview').hidden; }, null, 20000);
    const pane = await a.evaluate(() => {
      const f = document.getElementById('livefr');
      return { sandbox: f.getAttribute('sandbox'), src: f.getAttribute('src'), label: document.getElementById('prev-path').textContent,
        reload: !document.getElementById('prev-reload').hidden, staticHidden: document.getElementById('prevframe').hidden };
    });
    check(pane.sandbox === 'allow-scripts allow-forms', `the live frame is sandboxed allow-scripts allow-forms, never same-origin (${pane.sandbox})`);
    check(/^\/api\/~p1\.[^/]+\/index\.html$/.test(pane.src || ''), `the frame loads below a path ticket (${(pane.src || '').replace(/~p1\.[^/]+/, '~p1.…')})`);
    check(/live from the sandbox/.test(pane.label) && pane.reload && pane.staticHidden, `the pane says it is live, offers Reload, hides the static frame (${pane.label})`);
    const frame = () => a.frames().find((f) => /\/api\/~p1\./.test(f.url()));
    await until(a, () => true, null, 1000);
    let seen = null;
    for (let i = 0; i < 100 && !seen; i++) {
      const f = frame();
      if (f) {
        seen = await f.evaluate(() => {
          const r = document.getElementById('r')?.textContent || '';
          return r.startsWith('{') ? { h1: document.getElementById('h').textContent, r: JSON.parse(r) } : null;
        }).catch(() => null);
      }
      if (!seen) await a.waitForTimeout(100);
    }
    check(!!seen && seen.h1 === 'scripts ran', `the page's script ran in the pane (${seen && seen.h1})`);
    const r = (seen && seen.r) || {};
    check(r.origin === 'null', `it is an opaque origin (${r.origin})`);
    check(r.cookie === '' || r.cookie === 'threw', `it sees no cookie (${r.cookie})`);
    check(r.storage === 'threw', `it has no storage (${r.storage})`);
    // credentials:'include' from an opaque origin: xbind's CORS carries no Allow-Credentials, so the
    // browser refuses the answer outright (threw); a bare 401 would do as well
    const noId = (x) => x === 401 || x === 'threw';
    check(noId(r.whoami) && noId(r.tileApi), `whoami and the tile's API give it no identity (${r.whoami}, ${r.tileApi})`);
    check(r.topNav === 'threw' && !(await a.evaluate(() => location.hash.includes('escaped'))), `top navigation is blocked (${r.topNav})`);
    check(await a.evaluate(() => !document.getElementById('preview').hidden && !!document.getElementById('livefr')), 'its parent.postMessage changed nothing: the pane stays open');
    await shot(a, 'live-preview', { fullPage: false });
    await a.evaluate(() => { document.getElementById('livefr').dataset.old = '1'; });
    await a.click('#prev-reload');
    await until(a, () => { const f = document.getElementById('livefr'); return f && !f.dataset.old; }, null, 15000);
    check(true, 'Reload loads the page again in a new frame (a fresh ticket)');
  } catch (e) {
    const why = await a.evaluate(async (id) => {
      const r = await xbin.fetch(`/api/apps/agent/runs/${id}/view`);
      const v = await r.json().catch(() => ({}));
      return { steps: (v.steps || []).filter((s) => s.kind === 'live').map((s) => `${s.kind}:${s.seq}:${s.created}:${s.detail}`).join(','),
        vis: document.visibilityState, stepLine: document.querySelector('#timeline .step.live')?.textContent || '', now: Date.now(),
        hidden: document.getElementById('preview').hidden,
        warn: document.getElementById('prev-warn').textContent, label: document.getElementById('prev-path').textContent };
    }, conv).catch((x) => ({ err: x.message }));
    check(false, `live preview: ${e.message} (${JSON.stringify(why)})`);
  } finally {
    if (conv) {
      await a.evaluate(async (id) => { await xbin.fetch(`/api/apps/agent/runs/${id}`, { method: 'DELETE' }); }, conv).catch((e) => log(`livePreview: deleting the conversation: ${e.message}`));
    }
    await pickClass(a, before).catch((e) => log(`livePreview: restoring admin's class: ${e.message}`));
  }
  check(A.errors.length === 0, `no page errors (${A.errors.join(' | ')})`);
  await A.ctx.close();
  done();
}

module.exports = { livePreview };
