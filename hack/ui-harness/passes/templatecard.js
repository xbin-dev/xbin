// hack/ui-harness/passes/templatecard.js — covers PD-35 PD-19 PD-52 — the
// Tile Manager (workspace-template/tiles/manager/index.html) on its own page
// as admin, against a scripted catalog (page.route: no builtin names a mode
// yet, and the harness runs without --isolate):
//   1. a template that names a mode shows "Keep each person's data apart",
//      ticked; one this xbind can't honour shows it off and disabled with
//      "(needs --isolate)"; one that names none shows no box;
//   2. creating with the box ticked sends no "partition"; unticked sends
//      "partition": false; the disabled box sends nothing;
//   3. the Updates tab shows an update's notes (a partition kept as
//      installed) after the list reloads, and a "pr" answer without a PR
//      (upstream changed only a partition) reads "nothing to propose".
const { URL, login, closeCtx, waitSel, settle, shot, checker } = require('../lib');

const catalog = [
  { id: 'harness-apart', source: 'builtin', title: 'Apart', description: 'names a mode', defaultName: 'apart', partition: ['user', 'global'] },
  { id: 'harness-blocked', source: 'builtin', title: 'Blocked', description: 'names a mode, no isolation', defaultName: 'blocked', partition: ['user'], partitionSkipped: 'needs --isolate' },
  { id: 'harness-plain', source: 'builtin', title: 'Plain', description: 'names none', defaultName: 'plain' },
];
const note = 'tiles/x/xbin.json: partition kept as installed (["user"]; upstream asks none): edit it deliberately to request a switch';
const prNote = 'tiles/x/xbin.json: partition kept as installed (none; upstream asks ["user"]): edit it deliberately to request a switch';

async function templateCard(browser) {
  const { check, done } = checker('template-card');
  const { ctx, page } = await login(browser, 'admin', 'admin', { viewport: { width: 1100, height: 1000 } });
  const sent = [];
  await page.route('**/api/xbin/templates', (r) => r.fulfill({ json: catalog }));
  await page.route('**/api/xbin/templates/updates', (r) => r.fulfill({ json: { instances: [] } }));
  await page.route('**/api/xbin/templates/new', (r) => {
    const body = r.request().postDataJSON();
    sent.push(body);
    r.fulfill({ json: { path: body.path, files: [], pendingGrants: [], ...(body.partition === false ? { partitionSkipped: 'opted out' } : {}) } });
  });
  let updates = [{ id: 'scaffold:tiles/x', installPath: 'tiles/x', fromVersion: 1, toVersion: 1, clean: 1, files: [{ path: 'xbin.json', status: 'clean' }], hasUpdate: true }];
  await page.route('**/api/xbin/builtins/updates', (r) => r.fulfill({ json: updates }));
  await page.route('**/api/xbin/builtins/update', (r) => {
    const { mode } = r.request().postDataJSON();
    updates = [];
    r.fulfill({ json: mode === 'pr' ? { files: [], notes: [prNote] } : { files: ['tiles/x/xbin.json'], notes: [note] } });
  });
  try {
    await page.goto(`${URL}/c/tiles/manager/`);
    await page.locator('.tabs button[data-tab="template"]').click();
    await waitSel(page, '#templates .tile', { timeout: 15000 });
    const card = (title) => page.locator('#templates .tile', { has: page.locator('.title', { hasText: new RegExp(`^${title}$`) }) });
    const box = (title) => card(title).locator('input.apart');

    // 1. the boxes
    check(await box('Apart').count() === 1 && await box('Apart').isChecked() && await box('Apart').isEnabled(),
      'a template that names a mode: "Keep each person\'s data apart", ticked');
    check(/Keep each person's data apart/.test(await card('Apart').textContent()), 'the box is labelled');
    check(await box('Blocked').count() === 1 && !(await box('Blocked').isChecked()) && await box('Blocked').isDisabled(),
      'without isolation: the box is off and disabled');
    check((await card('Blocked').textContent()).includes('(needs --isolate)'), 'and says "(needs --isolate)"');
    check(await box('Plain').count() === 0, 'a template that names no mode: no box');
    await shot(page, 'template-card', { fullPage: false });

    // 2. what create sends
    const create = async (title, path) => {
      const n = sent.length + 1;
      await card(title).locator('input.path').fill(path);
      await card(title).locator('button.make').click();
      await page.waitForFunction((p) => [...document.querySelectorAll('#templates .imsg.ok')].some((m) => m.textContent.includes(p)), path, { timeout: 10000 });
      return sent.length === n ? sent[n - 1] : null;
    };
    let body = await create('Apart', 'apps/apart-1');
    check(body && !('partition' in body), `ticked: no "partition" sent (${JSON.stringify(body)})`);
    await box('Apart').uncheck();
    body = await create('Apart', 'apps/apart-2');
    check(body && body.partition === false, `unticked: "partition": false (${JSON.stringify(body)})`);
    body = await create('Blocked', 'apps/blocked-1');
    check(body && !('partition' in body), `disabled: no "partition" sent (${JSON.stringify(body)})`);

    // 3. the Updates tab keeps an update's notes past the reload
    await page.locator('.tabs button[data-tab="updates"]').click();
    await waitSel(page, '#updates .tile button[data-mode="merge"]', { timeout: 10000 });
    await page.locator('#updates .tile button[data-mode="merge"]').click();
    await page.waitForFunction(() => !document.getElementById('update-notes').hidden, null, { timeout: 10000 });
    await settle(page);
    const shown = await page.locator('#update-notes').textContent();
    check(shown.includes('scaffold:tiles/x') && shown.includes('partition kept as installed'), `Updates tab shows the notes ("${shown.slice(0, 80)}…")`);
    await shot(page, 'template-card-update-notes', { fullPage: false });
    updates = [{ id: 'scaffold:tiles/x', installPath: 'tiles/x', fromVersion: 1, toVersion: 1, conflicts: 1, files: [], hasUpdate: true }];
    await page.locator('.tabs button[data-tab="create"]').click();
    await page.locator('.tabs button[data-tab="updates"]').click();
    await waitSel(page, '#updates .tile button[data-mode="pr"]', { timeout: 10000 });
    await page.locator('#updates .tile button[data-mode="pr"]').first().click();
    await page.waitForFunction((n) => document.getElementById('update-notes').textContent.includes(n), prNote, { timeout: 10000 });
    check(!(await page.locator('#update-notes').isHidden()), 'a "pr" answer without a PR (only a partition changed) shows its notes');
  } finally {
    await closeCtx(ctx, page);
  }
  done();
}

module.exports = { templateCard };
