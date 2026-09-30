// hack/ui-harness/passes/livepreview.js — the live port preview (D135), end
// to end: in a coding conversation bound to a fresh sandbox, fakeopenai's
// "sandbox serve" writes a page, serves it with python3 -m http.server as a
// background job and calls preview_port. The seed binds the agent to
// apps/fakesbx (its sandboxes are directories — under HARNESS_ISOLATE=1 inside
// its own isolated backend). For the real coding-sandbox on the tilesbx
// runtime, on a HARNESS_ISOLATE=1 --keep instance: approve its pending
// cap:sandboxes grant (POST /api/xbin/grants), PUT its /ops/config
// {"mode":"auto"} (namespace sandboxes over the rootfs), bind the agent's
// `sandboxes` slot to apps/coding-sandbox alone, then --shots livePreview.
// It pins:
//   - the pane opens on the live step, labelled "live from the sandbox",
//     with Reload, the static frame hidden;
//   - the frame is sandbox="allow-scripts allow-forms" (never
//     allow-same-origin), on a /api/~<ticket>/ URL;
//   - the page's script ran (its h1), and from inside it saw: an opaque
//     origin, no cookie, no storage, no identity from /api/xbin/whoami or the
//     tile's API, top navigation blocked;
//   - its parent.postMessage changed nothing (the pane stays open);
//   - Reload loads it again (a fresh ticket);
//   - closed, the 📡 line opens it again, and the pane's status strip
//     checked its URL (HTTP 200, text/html) — Check asks again;
//   - "sandbox report": a report over 64 KB written in the sandbox and
//     render_html'd shows in the pane WHILE the turn still runs (the render
//     step streams), and the 🖼 line brings it back once closed;
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

    // closed, the 📡 line shows it again; its status strip says what the URL answers, and Check asks again
    const strip = () => a.evaluate(() => ({ tone: document.getElementById('live-strip')?.dataset.tone || '',
      text: document.querySelector('#live-strip .lsout')?.textContent || '', hidden: !!document.getElementById('livefr')?.hidden }));
    await a.click('#prev-close');
    await until(a, () => document.getElementById('preview').hidden && !document.getElementById('live-strip'));
    await a.click('#timeline .step.live .steplnk');
    await until(a, () => !!document.getElementById('livefr') && !document.getElementById('preview').hidden, null, 15000);
    await until(a, () => document.getElementById('live-strip')?.dataset.tone, null, 25000);
    let st = await strip();
    check(st.tone === 'ok' && /^HTTP 200 · text\/html/.test(st.text) && !st.hidden, `the 📡 line reopens the live pane, and its strip checked the URL (${st.text})`);
    await a.click('#live-check');
    await until(a, () => !document.getElementById('live-check').disabled && /^HTTP/.test(document.querySelector('#live-strip .lsout').textContent), null, 25000);
    st = await strip();
    check(st.tone === 'ok' && /^HTTP 200/.test(st.text), `Check asks the URL again (${st.text})`);
    await shot(a, 'live-strip', { fullPage: false });
    await a.click('#prev-close');
    await until(a, () => document.getElementById('preview').hidden);

    // a report over 64 KB from the sandbox: the pane opens while the turn still runs, and 🖼 brings it back
    const reportShown = () => [...document.querySelectorAll('#timeline .msg.assistant:not(.live)')].some((e) => e.textContent.includes('Report shown.'));
    await a.fill('#msg', 'sandbox report');
    await a.press('#msg', 'Enter');
    const paneHasReport = () => !document.getElementById('preview').hidden && /index\.html/.test(document.getElementById('prev-path').textContent)
      && (document.getElementById('prevframe').srcdoc || '').includes('row 4000 of the big report');
    await until(a, paneHasReport, null, 45000);
    const during = await a.evaluate(`(${reportShown.toString()})()`);
    check(!during && !(await a.evaluate(() => document.getElementById('stop').hidden)), 'a report over 64 KB, render_html\'d from the sandbox, shows in the pane while the turn still runs');
    await until(a, reportShown, null, 30000);
    await a.click('#prev-close');
    await until(a, () => document.getElementById('preview').hidden);
    const lines = await a.$$('#timeline .step.render .steplnk');
    await lines[lines.length - 1].click();
    await until(a, paneHasReport, null, 15000);
    check(true, 'the 🖼 line brings the report back');
    await a.click('#prev-close');
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
