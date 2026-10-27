// hack/ui-harness/passes/codepanels.js — the tile window's code and proposal
// panels (D202: web/code-tree.js, code-view.js, pr-panels.js) mounted on
// their own on the shell page against a tile the pass creates:
//   - <bx-code-tree> lists its files with their uncommitted status (M, and A
//     for a staged new file: an untracked one is in no diff)
//     and a click asks for the file's tab (bx-open-tab f:<path>); `active`
//     marks the open one;
//   - <bx-code-changes> lists the uncommitted files with their counts
//     (w:<path>), the history (c:<rev>) and, folded, the activity;
//   - <bx-code-view> highlights the file and follows it live;
//   - <bx-code-diff> shows the uncommitted change of one file, and a commit
//     under its subject;
//   - <bx-pr-list> lists a proposal and counts it (bx-pr-count); a click asks
//     for its tab (pr:<n>);
//   - <bx-pr-view> rejects through `.ask` (the window's dialog) with the
//     note in the thread; without `.ask`, Merged asks in a line of its own,
//     never the browser's prompt();
//   - <bx-prs>, now the two side by side, still opens the newest open one.
// Needs $WS (the harness workspace) to change the tile's files. Run it in
// both themes (HARNESS_THEME=light).
const path = require('path');
const { execFileSync } = require('child_process');
const { URL, fs, sleep, login, closeCtx, openShell, checker, THEME, shot } = require('../lib');

const T = 'apps/codepanels';
const WS = process.env.WS || '';

const MBOX = (title, line) => [
  'From 0000000000000000000000000000000000000000 Mon Sep 17 00:00:00 2001',
  'From: Harness <harness@example.org>',
  'Date: Mon, 26 Oct 2026 10:00:00 +0000',
  `Subject: [PATCH] ${title}`,
  '',
  '---',
  ' index.html | 1 +',
  ' 1 file changed, 1 insertion(+)',
  '',
  'diff --git a/index.html b/index.html',
  'index 1111111..2222222 100644',
  '--- a/index.html',
  '+++ b/index.html',
  '@@ -1,1 +1,2 @@',
  ' <!doctype html>',
  `+${line}`,
  '-- ',
  '2.40.0',
  '',
].join('\n');

// in the page: an element's shadow root query (sel inside the element at host)
const inEl = (page, host, fn, arg) => page.evaluate(([host, fn, arg]) => {
  const el = document.querySelector(host);
  // eslint-disable-next-line no-new-func
  return new Function('el', 'root', 'arg', `return (${fn})(el, root, arg);`)(el, el?.shadowRoot, arg);
}, [host, fn.toString(), arg ?? null]);

async function until(fn, label, timeout = 15000) {
  const end = Date.now() + timeout;
  for (;;) {
    const v = await fn();
    if (v) return v;
    if (Date.now() > end) throw new Error(`timed out: ${label}`);
    await sleep(150);
  }
}

async function codePanels(browser) {
  const { check, skip, done } = checker(`code-panels-${THEME}`);
  const { ctx, page } = await login(browser, 'admin', 'admin');
  const api = (method, p, data) => ctx.request.fetch(`${URL}/api/xbin/${p}`, { method, data });
  try {
    const mk = await api('POST', 'create', { path: T, title: 'codepanels' });
    check(mk.ok() || mk.status() === 409, `the tile exists (${mk.status()})`);
    if (!WS) { skip('no $WS: the pass changes the tile’s files on disk'); return; }
    const dir = path.join(WS, T), index = path.join(dir, 'index.html');
    for (let i = 0; i < 100 && !fs.existsSync(index); i++) await sleep(100);
    // the uncommitted change: index.html changed, notes.md new
    fs.writeFileSync(index, fs.readFileSync(index, 'utf8') + '\n<!-- changed by the codePanels pass -->\n');
    fs.writeFileSync(path.join(dir, 'notes.md'), '# Notes\n\nwritten by the pass\n');
    // staged, so the uncommitted change (git diff against HEAD) holds it: an
    // untracked file is in no diff (the panels show what the diff shows)
    execFileSync('git', ['-C', dir, 'add', 'notes.md']);

    await openShell(page);
    await page.evaluate(async () => {
      await import('/vendor/code-tree.js');
      await import('/vendor/code-view.js');
      await import('/vendor/pr-panels.js');
      await import('/vendor/bx-prs.js');
      const box = document.createElement('div');
      box.id = 'cp-box';
      box.style.cssText = 'position:fixed;inset:40px 20px 20px 20px;z-index:99999;display:grid;grid-template-columns:240px 1fr 1fr;grid-template-rows:1fr 1fr;gap:6px;background:var(--bx-bg)';
      window.__tabs = [];
      window.__counts = [];
      window.__prompted = 0;
      window.prompt = () => { window.__prompted++; return null; };
      box.addEventListener('bx-open-tab', (e) => window.__tabs.push(e.detail.ref));
      box.addEventListener('bx-pr-count', (e) => window.__counts.push(e.detail));
      document.body.appendChild(box);
    });
    const mount = (tag, attrs, props) => page.evaluate(([tag, attrs, props]) => {
      const el = document.createElement(tag);
      for (const [k, v] of Object.entries(attrs)) el.setAttribute(k, v);
      Object.assign(el, props || {});
      el.id = attrs.id || tag;
      el.style.minHeight = '0';
      el.style.border = '1px solid var(--bx-border)';
      document.getElementById('cp-box').appendChild(el);
    }, [tag, attrs, props]);
    const unmount = (id) => page.evaluate((id) => document.getElementById(id)?.remove(), id);

    // ---- the Files panel ----
    await mount('bx-code-tree', { component: T });
    const rows = await until(() => inEl(page, 'bx-code-tree', (el, root) => {
      const r = [...(root?.querySelectorAll('button.row') || [])].map((b) => ({ name: b.querySelector('.nm')?.textContent, st: b.querySelector('.gst')?.textContent || '' }));
      return r.some((x) => x.name === 'notes.md') ? r : null;
    }), 'the tree lists the files');
    check(rows.some((r) => r.name === 'index.html' && r.st === 'M') && rows.some((r) => r.name === 'notes.md' && r.st === 'A'),
      `the tree marks the changed (M) and new (A) files (${JSON.stringify(rows)})`);
    await inEl(page, 'bx-code-tree', (el, root) => [...root.querySelectorAll('button.row')].find((b) => b.querySelector('.nm')?.textContent === 'index.html').click());
    check((await page.evaluate(() => window.__tabs)).includes('f:index.html'), 'a file’s click asks for its tab (f:index.html)');
    await page.evaluate(() => { document.querySelector('bx-code-tree').active = 'index.html'; });
    await sleep(100);
    check(await inEl(page, 'bx-code-tree', (el, root) => !!root.querySelector('button.row.on')?.textContent.includes('index.html')), '`active` marks the open file');
    await page.focus('bx-code-tree');
    await inEl(page, 'bx-code-tree', (el, root) => root.querySelector('button.row').focus());
    await page.keyboard.press('ArrowDown');
    check(await inEl(page, 'bx-code-tree', (el, root) => root.activeElement !== root.querySelectorAll('button.row')[0] && root.activeElement?.classList.contains('row')), '↓ moves to the next row');

    // ---- the Changes panel ----
    await mount('bx-code-changes', { component: T });
    const ch = await until(() => inEl(page, 'bx-code-changes', (el, root) => {
      const files = [...(root?.querySelectorAll('button.row:not(.commit)') || [])].map((b) => b.querySelector('.nm')?.textContent + ' ' + b.querySelector('.cnt')?.textContent.trim());
      const commits = root?.querySelectorAll('button.commit').length || 0;
      return files.length >= 2 ? { files, commits } : null;
    }), 'the changes list');
    check(ch.files.some((f) => /^index\.html \+[1-9]\d* −0$/.test(f)) && ch.files.some((f) => /^notes\.md \+3 −0$/.test(f)), `the uncommitted files with their counts (${JSON.stringify(ch)})`);
    check(ch.commits >= 1, `the history lists the tile’s commits (${ch.commits})`);
    await inEl(page, 'bx-code-changes', (el, root) => { [...root.querySelectorAll('button.row:not(.commit)')][0].click(); root.querySelector('button.commit')?.click(); });
    const tabs = await page.evaluate(() => window.__tabs);
    check(tabs.some((t) => t.startsWith('w:')) && tabs.some((t) => /^c:[0-9a-f]{7,}$/.test(t)), `a file and a commit ask for their tabs (${JSON.stringify(tabs.slice(-2))})`);
    const rev = tabs.filter((t) => t.startsWith('c:')).pop()?.slice(2);
    await inEl(page, 'bx-code-changes', (el, root) => [...root.querySelectorAll('button.sec')].find((b) => b.textContent.includes('Activity')).click());
    check(!!(await until(() => inEl(page, 'bx-code-changes', (el, root) => root.querySelectorAll('.cd').length >= 4), 'the activity cards', 8000).catch(() => false)), 'Activity opens to its cards');
    await shot(page, `codepanels-lists-${THEME}`, { fullPage: false });

    // ---- the file and the diffs ----
    await unmount('bx-code-tree'); await unmount('bx-code-changes');
    await mount('bx-code-view', { component: T, path: 'index.html' });
    check(!!(await until(() => inEl(page, 'bx-code-view', (el, root) => root?.querySelector('pre.code .hljs-tag, pre.code [class^="hljs-"]') ? true : null), 'the highlighted file').catch(() => false)),
      'the file is highlighted');
    check(await inEl(page, 'bx-code-view', (el, root) => root.querySelector('.head').textContent.includes('index.html') && /\d/.test(root.querySelector('.gutter')?.textContent || '')), 'its breadcrumb and line numbers');
    fs.appendFileSync(index, '<!-- a live line -->\n');
    check(!!(await until(() => inEl(page, 'bx-code-view', (el, root) => root.querySelector('pre.code')?.textContent.includes('a live line') || null), 'the live refresh', 15000).catch(() => false)),
      'the view follows the file when it changes');
    await mount('bx-code-diff', { id: 'wdiff', component: T, rev: '', path: 'index.html' });
    check(!!(await until(() => inEl(page, '#wdiff', (el, root) => (root?.querySelector('.diff')?.textContent.includes('changed by the codePanels pass') && root.querySelector('.diff .d') && root.querySelector('.dhead').textContent.includes('not committed')) || null), 'the working-tree diff').catch(() => false)),
      'the uncommitted change of index.html, said to be uncommitted');
    check(await inEl(page, '#wdiff', (el, root) => !root.textContent.includes('notes.md')), 'narrowed to its one file');
    if (rev) {
      await mount('bx-code-diff', { id: 'cdiff', component: T, rev });
      const head = await until(() => inEl(page, '#cdiff', (el, root) => root?.querySelector('.dhead .id')?.textContent ? root.querySelector('.dhead').textContent.replace(/\s+/g, ' ') : null), 'the commit diff');
      check(head.includes(rev.slice(0, 7)), `a commit’s diff under its subject and id (${head})`);
    }
    await shot(page, `codepanels-views-${THEME}`, { fullPage: false });
    await unmount('bx-code-view'); await unmount('wdiff'); await unmount('cdiff');

    // ---- proposals ----
    const pr1 = await (await api('POST', 'code/prs', { target: T, title: 'Add a footer', series: MBOX('Add a footer', '<footer>made here</footer>') })).json();
    const pr2 = await (await api('POST', 'code/prs', { target: T, title: 'Add a header', series: MBOX('Add a header', '<header>hi</header>') })).json();
    check(pr1.number > 0 && pr2.number > pr1.number, `two proposals opened (#${pr1.number}, #${pr2.number})`);
    await mount('bx-pr-list', { target: T });
    const listed = await until(() => inEl(page, 'bx-pr-list', (el, root) => {
      const t = [...(root?.querySelectorAll('button.pr .t') || [])].map((x) => x.textContent);
      return t.length >= 2 ? t : null;
    }), 'the proposal list');
    check(listed.some((t) => t.includes('Add a footer')), `the list shows them (${JSON.stringify(listed)})`);
    const counts = await page.evaluate(() => window.__counts);
    check(counts.some((c) => c.n >= 2 && c.newest === pr2.number), `bx-pr-count says how many are open and the newest (${JSON.stringify(counts.slice(-1))})`);
    await inEl(page, 'bx-pr-list', (el, root) => [...root.querySelectorAll('button.pr')].find((b) => b.textContent.includes('Add a footer')).click());
    check((await page.evaluate(() => window.__tabs)).includes('pr:' + pr1.number), `a click asks for the proposal’s tab (pr:${pr1.number})`);

    await mount('bx-pr-view', { id: 'pv1', target: T, n: String(pr1.number) });
    await page.evaluate(() => {
      window.__asked = null;
      document.getElementById('pv1').ask = (spec) => { window.__asked = spec; return Promise.resolve({ button: 'ok', values: { note: 'not this one' } }); };
    });
    await until(() => inEl(page, '#pv1', (el, root) => root?.querySelector('pre.diff .d') ? true : null), 'the proposal’s diff');
    await inEl(page, '#pv1', (el, root) => [...root.querySelectorAll('.actions button')].find((b) => b.textContent.includes('Reject')).click());
    const rejected = await until(() => inEl(page, '#pv1', (el, root) => (root.querySelector('.st.rejected') && root.querySelector('.thread')?.textContent.includes('not this one')) || null), 'the rejection').catch(() => false);
    const asked = await page.evaluate(() => window.__asked);
    check(!!asked && /Reject/.test(asked.title) && asked.fields?.[0]?.name === 'note', `Reject asks through .ask (${asked?.title})`);
    check(!!rejected, 'the proposal is rejected, the note in its thread');

    await mount('bx-pr-view', { id: 'pv2', target: T, n: String(pr2.number) });
    await until(() => inEl(page, '#pv2', (el, root) => root?.querySelector('.actions') ? true : null), 'the second proposal');
    await inEl(page, '#pv2', (el, root) => [...root.querySelectorAll('.actions button')].find((b) => b.textContent.includes('Merged')).click());
    await sleep(200);
    const inline = await inEl(page, '#pv2', (el, root) => root.querySelector('.actions .what')?.textContent || '');
    check(/Mark merged/.test(inline) && (await page.evaluate(() => window.__prompted)) === 0, `without .ask, Merged asks in a line of its own, never prompt() (${inline})`);
    await shot(page, `codepanels-prs-${THEME}`, { fullPage: false });
    await inEl(page, '#pv2', (el, root) => [...root.querySelectorAll('.actions button')].find((b) => b.textContent === 'Cancel').click());
    await unmount('pv1'); await unmount('pv2'); await unmount('bx-pr-list');

    // ---- <bx-prs>: the two side by side, the newest open one first ----
    await mount('bx-prs', { component: T });
    const legacy = await until(() => inEl(page, 'bx-prs', (el, root) => root?.querySelector('bx-pr-view')?.shadowRoot?.querySelector('.ttl')?.textContent || null), '<bx-prs> opens one').catch(() => '');
    check(legacy.includes('Add a header'), `<bx-prs> still opens the newest open proposal (${legacy})`);
    await api('POST', 'code/pr/state', { target: T, n: pr2.number, state: 'rejected', comment: 'harness tidy' });
  } finally {
    await closeCtx(ctx, page).catch(() => {});
  }
  done();
}

module.exports = { codePanels, MBOX };
