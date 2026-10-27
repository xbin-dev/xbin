// hack/ui-harness/passes/tileide.js — the tile window as an IDE (D202), the
// way a person drives it, on a tile the pass makes (apps/idewin: a staged
// new file, its history, two change proposals):
//   - the ✕ is the title bar's top-right corner at 520, 700 and 1100 px —
//     with no session (the launcher, no pickers), and after a coding agent
//     ended (the fake one: before D202 the degraded bar dropped the ✕ after
//     the tabs);
//   - the tools: Files, Changes and PRs open their lists in the side panel
//     (again shuts it), Deploy opens the Deployments tab, Logs the dock;
//   - split by a pane's button; the divider by the mouse, the keyboard and a
//     double-click; a file from Files opens in the focused pane; Changes
//     opens a w: tab and a history commit a c: tab; PRs lists the proposals,
//     opens a pr: tab, and Reject asks through the window's dialog; a tab
//     dragged by the mouse onto a pane's right edge splits it;
//   - the status bar speaks for the session in view;
//   - a shell is the same element, connected, its scrollback whole, through
//     a move, a split, a divider drag and the window resized;
//   - a reload restores the layout; an older frame's pref ({open, layout:
//     'split', paneW}) opens with Files;
//   - the tile menu's terminal / logs / source / proposals squares and its
//     Deployments (open(layout));
//   - the phone sheet at 390 px: one pane (a split layout shows its focused
//     pane, every tab in its strip), no split buttons.
// Needs $WS (the harness workspace). Screenshots tile-ide-*-<theme>.png:
// run it in both themes (HARNESS_THEME=light).
const path = require('path');
const { execFileSync } = require('child_process');
const { URL, fs, sleep, login, closeCtx, settle, sh, fr, waitFor, waitSel, openShell, usePersonalScreen, openTile, shotEl, checker, THEME } = require('../lib');
const { MBOX } = require('./codepanels');

const T = 'apps/idewin';
const sel = `bx-frame[src="${T}"]`;
const WS = process.env.WS || '';
const PREF = `${URL}/api/xbin/prefs/${encodeURIComponent(`term:${T.replaceAll('/', ':')}`)}`;
const BOX = { x: 260, y: 90, w: 1100, h: 700 }; // the window, on screen (viewport 1600 × 1000)

const ide = (page) => fr(page, T, (f) => ({ panes: f.ide.panes, focus: f.ide.focus, side: f.ide.side, dock: f.ide.dock, tree: f.ide.tree, layout: f.layout }));
const paneOf = (st, ref) => st.panes.find((p) => p.tabs.includes(ref)) ?? null;
const box = (page, q) => page.locator(q).first().evaluate((el) => { const r = el.getBoundingClientRect(); return { x: r.x, y: r.y, w: r.width, h: r.height }; });
const shown = (page, q) => page.locator(`${sel} ${q}`).first().isVisible().catch(() => false);
const toolBtn = (page, label) => page.locator(`${sel} .tools button.tool[aria-label^="${label}"]`);
const row = (page, list, name) => page.locator(`${sel} ${list} button.row:not(.commit)`).filter({ has: page.locator('.nm', { hasText: name }) }).first();
const hasScope = (st) => st.pickers.some((c) => /\bscope\b/.test(c));
async function until(fn, label, timeout = 15000) {
  const end = Date.now() + timeout;
  for (;;) {
    const v = await fn().catch(() => null);
    if (v) return v;
    if (Date.now() > end) return null;
    await sleep(150);
  }
}

// the ✕: the title bar's top-right corner, nothing over it, clear of the name
const corner = (page) => page.locator(sel).evaluate((host) => {
  const root = host.shadowRoot, pop = root.querySelector('.pop')?.getBoundingClientRect(), xb = root.querySelector('.titlebar .winx');
  if (!pop || !xb) return null;
  const x = xb.getBoundingClientRect(), bar = root.querySelector('.titlebar').getBoundingClientRect(), name = root.querySelector('.titlebar .tname').getBoundingClientRect();
  const at = root.elementFromPoint(x.left + x.width / 2, x.top + x.height / 2);
  return { w: Math.round(pop.width), right: Math.round(pop.right - x.right), top: Math.round(x.top - bar.top), clear: at?.closest('.winx') === xb, apart: name.right <= x.left + 1 };
});
async function xAt(page, check, what) {
  for (const w of [520, 700, 1100]) {
    await fr(page, T, (f, t, b) => f.setPop(b), { ...BOX, w });
    await settle(page);
    const c = await corner(page);
    check(!!c && Math.abs(c.w - w) <= 2 && c.right <= 2 && c.top <= 1 && c.clear && c.apart, `${what}: at ${w} px the ✕ is the title bar's top-right corner, clear of the name (${JSON.stringify(c)})`);
  }
  await fr(page, T, (f, t, b) => f.setPop(b), BOX);
  await settle(page);
}

// a tab dragged by the mouse onto a pane's middle ('c') or right edge ('r');
// returns the drop zone lit while over it
async function dragTab(page, ref, pane, zone) {
  await page.locator(`${sel} .ptab[data-ref="${ref}"]`).evaluate((el) => el.scrollIntoView({ block: 'nearest', inline: 'nearest' })); // a long strip scrolls
  const tab = await box(page, `${sel} .ptab[data-ref="${ref}"]`), p = await box(page, `${sel} .pane[data-pane="${pane}"]`);
  const from = { x: tab.x + tab.w / 2, y: tab.y + tab.h / 2 };
  const to = { x: p.x + p.w * (zone === 'r' ? 0.9 : 0.35), y: p.y + 29 + (p.h - 29) * 0.5 };
  await page.mouse.move(from.x, from.y);
  await page.mouse.down();
  for (let i = 1; i <= 10; i++) { await page.mouse.move(from.x + (to.x - from.x) * i / 10, from.y + (to.y - from.y) * i / 10); await page.waitForTimeout(30); }
  const hot = await page.locator(`${sel} .dzhot`).first().getAttribute('class', { timeout: 1000 }).catch(() => '');
  await page.mouse.up();
  await settle(page);
  return hot;
}

async function tileIde(browser) {
  const { check, skip, done } = checker(`tile-ide-${THEME}`);
  const A = await login(browser, 'admin', 'admin', { viewport: { width: 1600, height: 1000 } });
  const api = (method, p, data) => A.ctx.request.fetch(`${URL}/api/xbin/${p}`, { method, data });
  const endSessions = async () => { for (const s of await (await api('GET', `term/sessions?cwd=${encodeURIComponent(T)}`)).json()) await api('DELETE', `term/sessions/${encodeURIComponent(s.id)}`); };
  const openPrs = async () => ((await (await api('GET', `code/prs?target=${encodeURIComponent(T)}&state=open`)).json()).prs || []).filter((p) => !p.state || p.state === 'open');
  try {
    const mk = await api('POST', 'create', { path: T, title: 'idewin' });
    check(mk.ok() || mk.status() === 409, `the tile exists (${mk.status()})`);
    if (!WS) { skip('no $WS: the pass changes the tile’s files on disk'); return; }
    const dir = path.join(WS, T);
    for (let i = 0; i < 100 && !fs.existsSync(path.join(dir, 'index.html')); i++) await sleep(100);
    fs.mkdirSync(path.join(dir, 'src'), { recursive: true });
    fs.writeFileSync(path.join(dir, 'src', 'app.js'), `// written by the tileIde pass\nexport const at = ${Date.now()};\n`);
    execFileSync('git', ['-C', dir, 'add', 'src/app.js']); // staged: the uncommitted change holds it
    for (const p of await openPrs()) await api('POST', 'code/pr/state', { target: T, n: p.number, state: 'rejected', comment: 'harness tidy' });
    const pr1 = await (await api('POST', 'code/prs', { target: T, title: 'Add a footer', series: MBOX('Add a footer', '<footer>made here</footer>') })).json();
    const pr2 = await (await api('POST', 'code/prs', { target: T, title: 'Add a header', series: MBOX('Add a header', '<header>hi</header>') })).json();
    check(pr1.number > 0 && pr2.number > pr1.number, `two proposals (#${pr1.number}, #${pr2.number})`);
    await endSessions();
    await A.ctx.request.delete(PREF);
    await desk(A, check, skip, { api, pr1, pr2 });
    await phone(browser, check);
  } finally {
    await endSessions().catch(() => { });
    for (const p of await openPrs().catch(() => [])) await api('POST', 'code/pr/state', { target: T, n: p.number, state: 'rejected', comment: 'harness tidy' }).catch(() => { });
    await A.ctx.request.delete(PREF).catch(() => { });
    await closeCtx(A.ctx, A.page).catch(() => { });
  }
  done();
}

async function desk(A, check, skip, { api, pr1, pr2 }) {
  const { page } = A;
  await openShell(page);
  await usePersonalScreen(page);
  await openTile(page, T);
  await page.locator(`.card[data-path="${T}"]`).evaluate((el) => el.scrollIntoView({ block: 'start' }));
  await fr(page, T, (f, t, b) => { f.open('term'); f.setPop(b); }, BOX);
  await waitSel(page, `${sel} .pane .launcher .lcard`);

  // ---- no session: the launcher, no pickers, the ✕ in its corner ----
  const st0 = await fr(page, T, (f) => f.ide.status);
  check(st0.pickers.length === 0 && await page.locator(`${sel} .status .who.none`).count() === 1, `an empty window: the launcher in its pane, no pickers on the status bar (${JSON.stringify(st0)})`);
  await xAt(page, check, 'no session');
  await shotEl(page, `${sel} .pop`, `tile-ide-empty-${THEME}`);

  // ---- a shell, its scrollback longer than the screen ----
  await fr(page, T, (f) => f.startKind('shell'));
  await waitFor(page, (t, s) => !!t.frameFor(s)?.testApi().tabs[0]?.id, T, { timeout: 20000, label: 'a shell' });
  const sid = await fr(page, T, (f) => f.tabs[0].id), sref = await fr(page, T, (f) => f.ide.sessionRef(0));
  const term = page.locator(`${sel} bx-terminal[session="${sid}"]`);
  const text = () => term.evaluate((el) => el.testApi().text()).catch(() => '');
  await until(async () => /[$#] ?$/m.test(await text()), 'the prompt', 20000);
  await term.click();
  await page.keyboard.type('for i in $(seq 1 150); do echo xbin-sb-$i; done; echo xbin-ide-$((6*7))');
  await page.keyboard.press('Enter');
  check(!!(await until(async () => /^xbin-ide-42\s*$/m.test(await text()), 'the shell answers')), 'the shell answers');
  await term.evaluate((el) => { el.xbinIde = 'mark'; });
  const survived = async (what) => {
    const k = await term.evaluate((el) => { const t = el.testApi().text(); return { same: el.xbinIde === 'mark', open: !!el.testApi().open, first: /^xbin-sb-1\s*$/m.test(t), last: /^xbin-sb-150\s*$/m.test(t), end: /^xbin-ide-42\s*$/m.test(t) }; });
    check(k.same && k.open && k.first && k.last && k.end, `${what}: the shell is the same element, connected, its scrollback whole (${JSON.stringify(k)})`);
  };
  check(hasScope(await fr(page, T, (f) => f.ide.status)), 'with a session the status bar has its pickers');

  // ---- the tools ----
  for (const [label, side, el] of [['Files', 'files', 'bx-code-tree'], ['Changes', 'changes', 'bx-code-changes'], ['PRs', 'prs', 'bx-pr-list']]) {
    await toolBtn(page, label).click();
    await settle(page);
    check((await ide(page)).side === side && await shown(page, `.side .sbody ${el}`), `${label} opens its list in the side panel (${el})`);
  }
  await toolBtn(page, 'PRs').click();
  await settle(page);
  check((await ide(page)).side === null && !(await shown(page, '.side')), 'the open tool again shuts the side panel');
  if (!(await toolBtn(page, 'Deploy').count())) skip('no Deploy tool: this xbind has no tile deployments state for the tile');
  else {
    await toolBtn(page, 'Deploy').click();
    await waitSel(page, `${sel} bx-deployments`);
    const sd = await ide(page);
    check(paneOf(sd, 'deploy')?.active === 'deploy' && sd.layout === 'deployments', `Deploy opens the Deployments tab (${JSON.stringify(sd.panes.map((p) => p.tabs))})`);
    await page.locator(`${sel} .ptab[data-ref="deploy"] .tabx`).click();
    await settle(page);
    check(!paneOf(await ide(page), 'deploy'), "the tab's ✕ closes it");
  }
  await toolBtn(page, 'Logs').click();
  await settle(page);
  check((await ide(page)).dock.open && await shown(page, '.dock bx-logs'), "Logs opens the dock with the backend's logs");
  await toolBtn(page, 'Logs').click();
  await settle(page);
  check(!(await ide(page)).dock.open && !(await shown(page, '.dock')), 'Logs again shuts it');
  await survived('the tools');

  // ---- split by the button: a new empty pane beside, focused ----
  const p0 = (await ide(page)).focus;
  await page.locator(`${sel} .pane[data-pane="${p0}"] .ptabs button[title="split right"]`).click();
  await settle(page);
  let st = await ide(page);
  const p1 = st.focus;
  check(st.panes.length === 2 && p1 !== p0 && paneOf(st, sref)?.id === p0 && paneOf(st, sref).active === sref && !st.panes.find((p) => p.id === p1).tabs.length && await shown(page, `.pane[data-pane="${p1}"] .launcher`),
    `split right: a new pane beside, focused, its launcher (${JSON.stringify(st.panes.map((p) => [p.id, p.tabs]))})`);
  await survived('a split');

  // ---- the divider: the mouse, the keyboard, a double-click ----
  const vd = page.locator(`${sel} .panes > .vd`).first();
  const vb = await vd.boundingBox(), area = await box(page, `${sel} .panes`);
  const x0 = vb.x + vb.width / 2, y0 = vb.y + vb.height / 2;
  await page.mouse.move(x0, y0);
  await page.mouse.down();
  for (let k = 1; k <= 6; k++) await page.mouse.move(x0 - 25 * k, y0);
  await page.mouse.up();
  await settle(page);
  const r1 = (await ide(page)).tree.r, want = (x0 - 150 - area.x) / area.w;
  check(Math.abs(r1 - want) <= 0.015, `the divider follows the mouse 150 px left (${r1}, want ≈${want.toFixed(3)})`);
  await survived('a divider drag');
  await vd.focus();
  await page.keyboard.press('ArrowRight');
  await settle(page);
  const r2 = (await ide(page)).tree.r;
  check(Math.abs(r2 - (r1 + 0.02)) <= 0.002 && await vd.getAttribute('aria-valuenow') === String(Math.round(r2 * 100)), `→ moves the focused divider 2 % (${r1} → ${r2})`);
  await vd.dblclick();
  await settle(page);
  check((await ide(page)).tree.r === 0.5, 'a double-click evens it');

  // ---- a file from Files opens in the focused pane ----
  await toolBtn(page, 'Files').click();
  await row(page, 'bx-code-tree', /^index\.html$/).click();
  await settle(page);
  st = await ide(page);
  check(paneOf(st, 'f:index.html')?.id === p1 && paneOf(st, 'f:index.html').active === 'f:index.html', `a file opens in the focused pane (${JSON.stringify(st.panes.map((p) => p.tabs))})`);
  check(!!(await until(() => page.locator(`${sel} .pane[data-pane="${p1}"] bx-code-view`).evaluate((el) => !!el.shadowRoot.querySelector('pre.code'))), 'the file'), 'the file shows, highlighted');
  await term.click(); // the shell's pane is the focused one now
  await row(page, 'bx-code-tree', /^xbin\.json$/).click();
  await settle(page);
  st = await ide(page);
  check(paneOf(st, 'f:xbin.json')?.id === p0, `a click in the other pane, then a file: it opens there (${JSON.stringify(st.panes.map((p) => p.tabs))})`);
  await page.locator(`${sel} .ptab[data-ref="${sref}"]`).click();

  // ---- Changes: the uncommitted file (w:) and a commit (c:) ----
  await page.locator(`${sel} .pane[data-pane="${p1}"] .ptabs`).click({ position: { x: 4, y: 4 } }); // focus the right pane
  await toolBtn(page, 'Changes').click();
  await until(() => row(page, 'bx-code-changes', /app\.js/).isVisible(), 'the changes list');
  await row(page, 'bx-code-changes', /app\.js/).click();
  await page.locator(`${sel} bx-code-changes button.commit`).first().click();
  await settle(page);
  st = await ide(page);
  const crev = paneOf(st, 'w:src/app.js') && st.panes.find((p) => p.id === p1).tabs.find((r) => /^c:[0-9a-f]{7,}$/.test(r));
  check(paneOf(st, 'w:src/app.js')?.id === p1 && !!crev && paneOf(st, crev).active === crev, `Changes opens the file's change (w:) and a commit (c:) in the focused pane (${JSON.stringify(st.panes.map((p) => p.tabs))})`);
  const head = await until(() => page.locator(`${sel} .pane[data-pane="${p1}"] bx-code-diff`).evaluate((el, r) => { const h = el.shadowRoot.querySelector('.dhead'); return h && h.textContent.includes(r) ? h.textContent.replace(/\s+/g, ' ').trim() : null; }, (crev || '').slice(2, 9)), 'the commit');
  check(!!head, `the commit's tab shows its diff under its subject and id (${head})`);

  // ---- PRs: the list, a pr: tab, Reject through the window's dialog ----
  await toolBtn(page, 'PRs').click();
  const prRow = page.locator(`${sel} bx-pr-list button.pr`).filter({ hasText: 'Add a footer' }).first();
  await until(() => prRow.isVisible(), 'the proposals');
  const counted = await until(async () => /PRs \(2\)/.test(await toolBtn(page, 'PRs').getAttribute('aria-label')), 'the count', 5000);
  check(await page.locator(`${sel} bx-pr-list button.pr`).count() >= 2 && !!counted, `PRs lists the two proposals and counts them (${await toolBtn(page, 'PRs').getAttribute('aria-label')})`);
  await prRow.click();
  await settle(page);
  st = await ide(page);
  check(paneOf(st, `pr:${pr1.number}`)?.id === p1, `a proposal opens as a pr: tab in the focused pane (${JSON.stringify(st.panes.map((p) => p.tabs))})`);
  const reject = page.locator(`${sel} bx-pr-view .actions button`).filter({ hasText: 'Reject' }).first();
  await until(() => reject.isVisible(), 'the proposal');
  await reject.click();
  await waitFor(page, (t, s) => !!t.frameFor(s)?.testApi().dialog, T, { label: 'the Reject dialog' });
  const dlg = await fr(page, T, (f) => f.dialog);
  check(/Reject/.test(dlg?.title || '') && dlg.fields?.[0]?.name === 'note', `Reject asks for a note in the window's dialog (${dlg?.title})`);
  await fr(page, T, (f) => f.answerDialog('ok', { note: 'not this one' }));
  check(!!(await until(() => page.locator(`${sel} bx-pr-view`).evaluate((el) => !!el.shadowRoot.querySelector('.st.rejected') && el.shadowRoot.querySelector('.thread')?.textContent.includes('not this one')), 'the rejection')), 'the proposal is rejected, the note in its thread');
  await shotEl(page, `${sel} .pop`, `tile-ide-split-${THEME}`);

  // ---- the session in view: the status bar's ----
  await api('PATCH', `term/sessions/${encodeURIComponent(sid)}`, { name: 'left' });
  await page.locator(`${sel} .pane[data-pane="${p1}"] .ptabs`).click({ position: { x: 4, y: 4 } });
  await fr(page, T, (f) => f.newTerm());
  await waitFor(page, (t, s) => (t.frameFor(s)?.testApi().tabs || []).filter((x) => x.id).length === 2, T, { timeout: 20000, label: 'a second shell' });
  const sid2 = await fr(page, T, (f, t, id) => f.tabs.find((x) => x.id && x.id !== id)?.id, sid);
  await api('PATCH', `term/sessions/${encodeURIComponent(sid2)}`, { name: 'right' });
  await waitFor(page, (t, s) => (t.frameFor(s)?.testApi().tabs || []).map((x) => x.name).sort().join() === 'left,right', T, { label: 'the names' });
  const who = async () => (await page.locator(`${sel} .status .who .lbl`).textContent()).trim();
  await term.click();
  const left = await who();
  await page.locator(`${sel} bx-terminal[session="${sid2}"]`).click();
  const right = await who();
  check(left === 'left' && right === 'right' && hasScope(await fr(page, T, (f) => f.ide.status)), `the status bar follows the session in view (${left} → ${right})`);

  // ---- a move and a real drag onto a pane's right edge ----
  await fr(page, T, (f, t, a) => f.ide.move(a.ref, a.to, 'c'), { ref: sref, to: p1 });
  await settle(page);
  check(paneOf(await ide(page), sref)?.id === p1, 'the shell moved to the other pane');
  await survived('a move');
  const hot = await dragTab(page, 'f:index.html', p0, 'r');
  st = await ide(page);
  const p2 = paneOf(st, 'f:index.html')?.id, r = (id) => st.panes.find((p) => p.id === id)?.rect;
  check(/\br\b/.test(hot || '') && st.panes.length === 3 && ![p0, p1].includes(p2) && r(p2).x > r(p0).x && r(p2).x < r(p1).x,
    `a tab dragged onto a pane's right edge lights it and splits it there (${hot}; ${JSON.stringify(st.panes.map((p) => [p.id, p.tabs]))})`);
  await fr(page, T, (f, t, a) => f.ide.move(a.ref, a.to, 'c'), { ref: sref, to: p0 });
  await settle(page);
  await survived('moved back');
  await shotEl(page, `${sel} .pop`, `tile-ide-three-${THEME}`);
  for (const [w, h] of [[760, 460], [1180, 760]]) {
    await fr(page, T, (f, t, b) => f.setPop(b), { ...BOX, w, h });
    await settle(page);
    await sleep(300);
    await survived(`the window at ${w}×${h}`);
  }
  await fr(page, T, (f, t, b) => f.setPop(b), BOX);

  // ---- a coding agent that ended: the ✕ stays in its corner ----
  await fr(page, T, (f) => f.startKind('agent', 'fake'));
  await waitFor(page, (t, s) => (t.frameFor(s)?.testApi().tabs || []).some((x) => x.kind === 'agent' && x.id), T, { timeout: 20000, label: 'the agent session' });
  const aid = await fr(page, T, (f) => f.tabs.find((x) => x.kind === 'agent').id);
  await api('DELETE', `term/sessions/${encodeURIComponent(aid)}`);
  await waitFor(page, (t, s) => (t.frameFor(s)?.testApi().tabs || []).some((x) => x.kind === 'agent' && x.ended), T, { timeout: 15000, label: 'the agent ended' });
  await xAt(page, check, 'an agent ended');
  await fr(page, T, (f) => f.closeTab(f.tabs.findIndex((x) => x.kind === 'agent')));
  await settle(page);

  // ---- a reload restores the layout ----
  const shape = (s) => JSON.stringify({ tree: s.tree, panes: s.panes.map((p) => [p.id, p.tabs.map((x) => (x.startsWith('s:') ? 's' : x)), p.active?.startsWith('s:') ? 's' : p.active]), side: s.side, dock: s.dock.open });
  const before = shape(await ide(page));
  check(!!(await until(async () => { const p = await (await A.ctx.request.get(PREF)).json(); return p?.ide && JSON.stringify(p.ide.tree) === JSON.stringify(JSON.parse(before).tree); }, 'the saved layout', 8000)), 'the layout is saved in the window pref');
  await page.reload();
  await page.waitForSelector('bx-shell', { timeout: 15000 });
  await waitFor(page, (t, s) => !!t?.frameFor(s)?.testApi().terminalOpen && t.frameFor(s).testApi().ide.panes.length === 3, T, { timeout: 20000, label: 'the window back' });
  const after = shape(await ide(page));
  check(after === before, `a reload restores the panes, tabs and side panel (${after === before ? after : `${before} → ${after}`})`);

  // ---- an older frame's pref: {layout: 'split', paneW} opens with Files ----
  const pop = (await (await A.ctx.request.get(PREF)).json())?.pop;
  await A.ctx.request.put(PREF, { data: { open: true, active: 0, activeId: sid, pop, layout: 'split', paneW: 40 } });
  await page.reload();
  await page.waitForSelector('bx-shell', { timeout: 15000 });
  await waitFor(page, (t, s) => !!t?.frameFor(s)?.testApi().terminalOpen, T, { timeout: 20000, label: 'the window from the older pref' });
  await settle(page);
  const old = await ide(page);
  check(old.side === 'files' && old.panes.length === 1 && old.panes[0].tabs.filter((x) => x.startsWith('s:')).length === 2 && await shown(page, '.side bx-code-tree'),
    `the older pref {layout: 'split', paneW: 40} opens one pane with both sessions and Files beside (${JSON.stringify(old)})`);

  // ---- the tile menu (open(layout)) ----
  const cell = (label) => sh(page, (t, a) => t.tileMenuItems(a.p).find((x) => x.kind === 'grid').cells.find((c) => c.label === a.label).action(), { p: T, label });
  await fr(page, T, (f) => f.ide.openTool('files')); // shut
  await cell('source');
  await settle(page);
  check((await ide(page)).side === 'files', "the menu's source square opens Files");
  await cell('logs');
  await settle(page);
  check((await ide(page)).dock.open && await shown(page, '.dock bx-logs'), "its logs square opens the dock");
  await cell('proposals');
  await waitFor(page, (t, a) => !!t.frameFor(a.s)?.testApi().ide.panes.some((p) => p.tabs.includes(`pr:${a.n}`)), { s: T, n: pr2.number }, { label: 'the newest proposal' }).catch(() => { });
  st = await ide(page);
  check(st.side === 'prs' && paneOf(st, `pr:${pr2.number}`)?.active === `pr:${pr2.number}`, `its proposals square opens PRs and the newest open one (${JSON.stringify(st.panes.map((p) => p.tabs))})`);
  const depRow = await sh(page, (t, p) => t.tileMenuItems(p).some((x) => x.label === 'Deployments…'), T);
  if (depRow) await sh(page, (t, p) => t.tileMenuItems(p).find((x) => x.label === 'Deployments…').action(), T);
  else await page.evaluate((tile) => window.dispatchEvent(new CustomEvent('bx-open-deployments', { detail: { tile } })), T); // the admin tile's way, for a tile with no record
  await settle(page);
  if (await fr(page, T, (f) => f.layouts.includes('deployments'))) check(paneOf(await ide(page), 'deploy')?.active === 'deploy', `Deployments (${depRow ? 'the menu row' : 'bx-open-deployments'}) opens its tab`);
  await cell('terminal');
  await settle(page);
  st = await ide(page);
  check(st.panes.find((p) => p.id === st.focus)?.active?.startsWith('s:'), "its terminal square shows the session in view");
  await fr(page, T, (f) => { f.ide.close('deploy'); f.ide.setDock(false); if (f.ide.side) f.ide.openTool(f.ide.side); });

  // a split layout for the phone to fold
  await fr(page, T, (f) => { f.ide.openFile('index.html'); f.ide.split('row'); });
  await settle(page);
  await sleep(800); // saved
}

// the phone sheet: one pane — the focused one of a split layout, every tab
// in its strip — and no split buttons
async function phone(browser, check) {
  const M = await login(browser, 'admin', 'admin', { viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true });
  try {
    const { page } = M;
    await openShell(page);
    await usePersonalScreen(page);
    await openTile(page, T);
    await fr(page, T, (f) => f.open('term'));
    await waitFor(page, (t, s) => !!t.frameFor(s)?.testApi().terminalOpen, T, { label: 'the sheet' });
    await settle(page);
    const st = await ide(page);
    const all = st.panes.flatMap((p) => p.tabs);
    const drawn = await page.locator(sel).evaluate((host) => {
      const root = host.shadowRoot;
      return { panes: root.querySelectorAll('.pane[data-pane]').length, tabs: root.querySelectorAll('.pane .ptab').length, on: root.querySelectorAll('.pane .ptab.on').length,
        splits: root.querySelectorAll('.ptabs button[title^="split"]').length, dividers: root.querySelectorAll('.panes > .vd').length };
    });
    check(st.panes.length >= 2 && drawn.panes === 1 && drawn.tabs === all.length && drawn.on === 1 && drawn.splits === 0 && drawn.dividers === 0,
      `the phone sheet shows one pane, every tab in its strip, no splits (${JSON.stringify({ drawn, model: st.panes.length })})`);
    const other = st.panes.find((p) => p.id !== st.focus && p.active)?.active;
    if (other) {
      await page.locator(`${sel} .ptab[data-ref="${other}"]`).tap();
      await settle(page);
      const st2 = await ide(page);
      check(paneOf(st2, other)?.id === st2.focus && await page.locator(`${sel} .ptab[data-ref="${other}"]`).getAttribute('class').then((c) => /\bon\b/.test(c)), "a tap on another pane's tab shows it");
    }
    await shotEl(page, `${sel} .pop`, `tile-ide-phone-${THEME}`);
  } finally {
    await closeCtx(M.ctx, M.page).catch(() => { });
  }
}

module.exports = { tileIde };
