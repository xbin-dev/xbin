// hack/ui-harness/passes/llm.js — covers D226's UI: xbind's LLM connector
// on its own page (/xbin/llm) and in the admin console, end to end against
// the real API, with hack/fakeopenai as the one provider (120 tokens a
// call) and the deployment file run.sh starts xbind with
// (XBIN_LLM_CONFIG=$LLM_HOST_FILE: empty until this pass writes a host's
// settings into it). Run it in both themes (HARNESS_THEME=light).
//   1. the setup card: re-armed (the seed dismissed it; SKIPped, said so,
//      on a workspace whose connector was configured before) — the admin's
//      shell banner names /xbin/llm, a person who is not an admin gets no
//      alert, the admin console draws ONE card whose Set up… opens the LLM
//      tab and whose dismiss ends it for the workspace;
//   2. /xbin/llm served byte for byte, top-level only; unconfigured;
//   3. a provider: added through the form with a key — the key is nowhere
//      in the page afterwards (text, attributes, field values) nor in any
//      answer, the row reads its last four characters, and the editor
//      opens with an empty key field; Test answers inline;
//   4. the default model, the people switch, calls in flight (and a
//      write's notices, served), the default quota, a per-person and a
//      per-org override — each read back from the API; a changed address
//      asks for the key again;
//   5. usage after dev1's calls: their row (tokens today, cost from the
//      price table), the component and model tables, a sort by a column
//      header; an estimated request and an unknown model (served rows);
//   6. quota alerts: an org's warn limit and then the default's stop —
//      GET /alerts names dev1, the limit's source and /xbin/llm; the admin
//      console's bar links the LLM tab; the third call is 429;
//   7. dev1 (not an admin) on /xbin/llm: their own quota with a plain
//      sentence, their own usage, nothing of the settings — and the API
//      refuses the rest;
//   8. the host's file: a locked upstream, default model and quota parts
//      are read-only with "Set by your host", their controls disabled, and
//      a write is 409; its default quota is a ceiling over an org's entry
//      (said, shown in the fields, a higher limit refused, a lower one
//      saved); lockAll says so at the top and disables every control; the
//      file is emptied again;
//   9. the admin console: workspace → LLM is the same element; a person's
//      quota in users → LLM quota…, an org's on its page; a phone width.
// Everything it set is removed at the end (the usage it counted stays:
// counts are never deleted).
const path = require('path');
const { URL, fs, login, closeCtx, openShell, settle, shot, shotEl, checker, sleep, THEME, gotoTab } = require('../lib');

const WS = process.env.WS || '';
const REPO = process.env.REPO || path.join(__dirname, '..', '..', '..');
const HOST = process.env.LLM_HOST_FILE || '';
const FAKE = `http://${process.env.FAKEOPENAI_ADDR || '127.0.0.1:18977'}`;
const KEY = 'sk-harness-not-a-real-key-9Zq4'; // typed into the form; no provider ever sees a real one
const UP = 'fake', MODEL = 'fake-chat', CALL = 120; // fakeopenai: 100 tokens in, 20 out
const PAGE = `${URL}/xbin/llm`;

async function until(fn, label, timeout = 15000) {
  const deadline = Date.now() + timeout;
  for (;;) {
    const v = await fn().catch(() => null);
    if (v) return v;
    if (Date.now() > deadline) throw new Error(`timed out: ${label}`);
    await sleep(150);
  }
}

// everything a person could read or copy off the page: text, attribute
// values and field values, through every open shadow root
const everything = () => {
  const out = [];
  const walk = (root) => {
    for (const el of root.querySelectorAll('*')) {
      for (const a of el.attributes) out.push(a.value);
      if ('value' in el && typeof el.value === 'string') out.push(el.value);
      if (el.shadowRoot) walk(el.shadowRoot);
    }
    out.push(root.textContent || '');
  };
  walk(document);
  return out.join('\n');
};

async function llm(browser) {
  const { check, skip, done } = checker(`llm-${THEME}`);
  const kit = await import(path.join(REPO, 'web', 'llm-kit.js'));
  if (!HOST || !fs.existsSync(HOST)) {
    skip(`no deployment file (LLM_HOST_FILE=${HOST}): start the harness xbind with this checkout's run.sh`);
    done();
    return;
  }
  const A = await login(browser, 'admin', 'admin');
  const D = await login(browser, 'dev1', 'devpass123');
  const errors = [];
  for (const [who, x] of [['admin', A], ['dev1', D]]) x.page.on('pageerror', (e) => errors.push(`${who}: ${e.message}`));
  const call = (ctx) => async (method, p, data) => {
    const r = await ctx.request.fetch(`${URL}/api/xbin${p}`, { method, ...(data === undefined ? {} : { data }) });
    return { status: r.status(), body: await r.json().catch(() => ({})) };
  };
  const api = call(A.ctx), dapi = call(D.ctx);
  const cfg = async () => (await api('GET', '/llm/config')).body;
  const alerts = async (a = api) => ((await a('GET', '/alerts')).body.alerts ?? []);
  const host = (o) => fs.writeFileSync(HOST, `${JSON.stringify(o, null, 2)}\n`);
  const used = async (user) => ((await api('GET', `/llm/usage?user=${user}`)).body.rows ?? [])
    .filter((r) => r.day === kit.today()).reduce((n, r) => n + r.inputTokens + r.outputTokens, 0);
  const chat = (ctx) => ctx.request.post(`${URL}/api/xbin/llm/v1/chat/completions`, { data: { model: `${UP}/${MODEL}`, messages: [{ role: 'user', content: 'quick' }] } });
  const reset = async () => {
    host({});
    const c = await until(async () => { const v = await cfg(); return v.host && !Object.keys(v.locks ?? {}).length && v; }, 'the empty deployment file in force');
    const q = (await api('GET', '/llm/quotas')).body.quotas ?? {};
    const none = (m) => Object.fromEntries(Object.keys(m ?? {}).map((k) => [k, null]));
    await api('PUT', '/llm/quotas', { workspace: null, default: null, users: none(q.users), orgs: none(q.orgs) });
    for (const u of c.upstreams ?? []) await api('DELETE', `/llm/upstreams/${u.name}`);
    await api('PUT', '/llm/config', { enabled: true, people: 'admins', concurrency: 0, defaults: { model: '', roles: Object.fromEntries(Object.keys(c.defaults?.roles ?? {}).map((r) => [r, ''])) },
      aliases: Object.fromEntries(Object.keys(c.aliases ?? {}).map((a) => [a, ''])) });
  };
  const admin = A.page.locator('bx-llm-admin');
  const open = async (page = A.page) => { await page.goto(PAGE); await page.waitForSelector('bx-llm-page main[data-mode]'); };
  const loaded = async () => { await A.page.waitForSelector('bx-llm-admin [data-status]', { timeout: 15000 }); await settle(A.page); };

  try {
    // ---- 1. the setup card ----
    const marker = path.join(WS, 'data', 'llm', 'setup.json');
    const fresh = (await cfg()).set === undefined && !(await cfg()).configured; // no admin has touched the connector
    if (!fresh) await reset();
    if (!fresh || !WS || !fs.existsSync(marker)) {
      skip('the setup card: this workspace\'s connector was configured before, so the card is gone for good (a fresh seed shows it)');
    } else {
      fs.rmSync(marker);
      const a = (await alerts()).filter((x) => x.kind === 'llm-setup');
      check(a.length === 1 && a[0].message.includes('/xbin/llm') && a[0].dismiss === '/llm/setup/dismiss' && a[0].level === 'warn',
        `nothing configured: admins get one llm-setup alert naming /xbin/llm (${JSON.stringify(a)})`);
      check(!(await alerts(dapi)).some((x) => String(x.kind).startsWith('llm-')), 'a person who is not an admin gets no alert about it');
      await openShell(A.page);
      const banner = A.page.locator('bx-shell .alerts .alert[data-kind="llm-setup"]');
      await banner.waitFor({ timeout: 15000 });
      check((await banner.innerText()).includes('/xbin/llm') && await banner.locator('.dismiss').count() === 1 && await banner.locator('a[data-llm-page][href="/xbin/llm"]').count() === 1,
        'the admin\'s shell shows it as a banner with Dismiss, /xbin/llm in its words and as a link');
      await openShell(D.page);
      await sleep(500);
      check(await D.page.locator('bx-shell .alerts .alert[data-kind="llm-setup"]').count() === 0, '…and dev1\'s shell shows nothing');
      await gotoTab(A.page, 'components', 'Set up an LLM');
      const card = A.page.locator('bx-admin [data-llm-setup]');
      check(await card.count() === 1 && await A.page.locator('bx-admin .alertbar .al', { hasText: 'No LLM is set up' }).count() === 0,
        'the admin console draws ONE card for it, not the alert row as well');
      await shotEl(A.page, 'bx-admin .alertbar', `llm-setupcard-${THEME}`);
      await card.locator('[data-llm-open]').click();
      await A.page.waitForFunction(() => document.querySelector('bx-admin')?.testApi().tab === 'llm', null, { timeout: 10000 });
      await A.page.waitForSelector('bx-admin-llm bx-llm-admin [data-status=unconfigured]', { timeout: 15000 });
      check(true, 'its Set up… opens workspace → LLM, which says the connector is not set up');
      await A.page.locator('bx-admin [data-llm-dismiss]').click();
      await until(async () => !(await alerts()).some((x) => x.kind === 'llm-setup'), 'the dismissal reaches /alerts');
      await until(async () => await A.page.locator('bx-admin [data-llm-setup]').count() === 0, 'the card goes');
      check(fs.existsSync(marker), 'dismiss ends it for the workspace (POST /llm/setup/dismiss)');
    }

    // ---- 2. the page: served as shipped, top-level only, unconfigured ----
    const raw = await A.ctx.request.get(PAGE, { headers: { Accept: 'text/html' } });
    const h = raw.headers();
    check(raw.status() === 200 && (await raw.text()) === fs.readFileSync(path.join(REPO, 'web', 'llm.html'), 'utf8'), 'GET /xbin/llm answers web/llm.html byte for byte (no HTML transform)');
    check(h['x-frame-options'] === 'DENY' && /frame-ancestors 'none'/.test(h['content-security-policy'] || '') && /script-src 'self'/.test(h['content-security-policy'] || ''),
      `it refuses to be framed and runs no inline script (${h['x-frame-options']}; ${h['content-security-policy']})`);
    check((await A.ctx.request.get(`${URL}/vendor/llm.html`)).status() === 404, '/vendor/ has no copy of the page');
    await open();
    await loaded();
    check(await admin.locator('[data-status=unconfigured]').count() === 1 && await admin.locator('bx-llm-upstreams [data-empty]').count() === 1,
      'an admin\'s page, nothing configured: "Not set up", no provider');
    check(await admin.locator('[data-locked-all], [data-locked-some], [data-locked]').count() === 0, 'an empty deployment file locks nothing: no "Set by your host" anywhere');
    await shot(A.page, `llm-page-empty-${THEME}`);

    // ---- 3. a provider, its key, Test ----
    const ups = admin.locator('bx-llm-upstreams');
    await ups.locator('[data-add]').click();
    const ed = ups.locator('[data-editor=new]');
    await ed.locator('[data-f=name]').fill(UP);
    await ed.locator('[data-f=baseURL]').fill(`${FAKE}/v1`);
    check(await ed.locator('[data-f=key]').getAttribute('type') === 'password', 'the key field is masked');
    await ed.locator('[data-f=key]').fill(KEY);
    await ed.locator('[data-f=models]').fill(`${MODEL}, gpt-5-fake`);
    await ed.locator('[data-add-price]').click();
    await ed.locator('[data-p=model]').fill(MODEL);
    await ed.locator('[data-p=in]').fill('1');
    await ed.locator('[data-p=out]').fill('2');
    await shotEl(A.page, 'bx-llm-upstreams [data-editor=new]', `llm-provider-form-${THEME}`);
    const put = A.page.waitForResponse((r) => r.url().endsWith(`/api/xbin/llm/upstreams/${UP}`) && r.request().method() === 'PUT');
    await ed.locator('[data-save]').click();
    const answer = await (await put).text();
    const row = ups.locator(`tr[data-upstream=${UP}]`);
    await row.waitFor({ timeout: 10000 });
    await settle(A.page);
    const c1 = await cfg();
    const u1 = (c1.upstreams ?? []).find((u) => u.name === UP) ?? {};
    check(c1.configured && u1.baseURL === `${FAKE}/v1` && u1.key?.set && u1.key.source === 'vault' && u1.models.join() === `${MODEL},gpt-5-fake` && u1.prices?.[MODEL]?.out === 2,
      `the form's provider is in force (${JSON.stringify(u1)})`);
    check(!answer.includes(KEY) && !JSON.stringify(c1).includes(KEY), 'no answer carries the key');
    check(!(await A.page.evaluate(everything)).includes(KEY), 'after the save the key is nowhere on the page: no text, no attribute, no field value');
    check((await row.locator('[data-key]').innerText()).trim() === `ends in ${KEY.slice(-4)}`, 'the row reads its last four characters');
    check(await admin.locator('[data-status=ok]').count() === 1, 'the status line: on, one provider');
    await row.locator('[data-edit]').click();
    const ed2 = ups.locator(`[data-editor=${UP}]`);
    check(await ed2.locator('[data-f=key]').inputValue() === '' && await ed2.locator('[data-f=key]').getAttribute('placeholder') === 'unchanged'
      && await ed2.locator('[data-f=name]').isDisabled(), 'edit… opens with an empty key field ("unchanged"): the key is never shown again');
    await ed2.locator('button.quiet', { hasText: 'Cancel' }).click();
    await row.locator('[data-test]').click();
    await ups.locator('[data-test-result=ok]').waitFor({ timeout: 20000 });
    check(/fake-chat answered in \d+ ms \(\d+ tokens?\)/.test(await ups.locator('[data-test-result]').innerText()), `Test answers inline (${await ups.locator('[data-test-result]').innerText()})`);

    // ---- 4. the default model, the people switch, quotas ----
    await admin.locator('[data-model=default]').fill(`${UP}/${MODEL}`);
    await admin.locator('[data-save-models]').click();
    await admin.locator('[data-saved=models]').waitFor({ timeout: 10000 });
    check((await cfg()).defaults.model === `${UP}/${MODEL}`, 'the default model is saved');
    check(await admin.locator(`datalist#llm-models option[value="${UP}/${MODEL}"]`).count() === 1, 'the model fields offer the provider\'s models');
    await admin.locator('[data-people] input[value=everyone]').check();
    await until(async () => (await cfg()).people === 'everyone', 'the people switch is saved');
    check(true, 'who may call directly: everyone');
    // a changed address: the stored key doesn't follow it
    await row.locator('[data-edit]').click();
    await ed2.locator('[data-f=baseURL]').fill(`${FAKE.replace('127.0.0.1', 'localhost')}/v1`);
    await ed2.locator('[data-key-hint=moved]').waitFor({ timeout: 5000 });
    await ed2.locator('[data-save]').click();
    await until(async () => /the address changed/.test(await ed2.innerText()), 'the form refuses the move without a key');
    check(/Enter the key for the new address/.test(await ed2.locator('[data-key-hint]').innerText()) && (await cfg()).upstreams[0].baseURL === `${FAKE}/v1`,
      'a new host in the base URL: the form says the stored key is not kept and asks for it again (nothing is sent without it)');
    await ed2.locator('button.quiet', { hasText: 'Cancel' }).click();
    // calls in flight per caller; what a write did beyond what was asked is shown
    const notice = async (route) => {
      if (route.request().method() !== 'PUT') return route.continue();
      const res = await route.fetch();
      return route.fulfill({ response: res, json: { ...(await res.json()), notices: ['the key of upstream fake was removed: its address changed'] } });
    };
    await A.page.route('**/api/xbin/llm/config', notice);
    await admin.locator('form[data-concurrency] input').fill('8');
    await admin.locator('[data-save-concurrency]').click();
    await admin.locator('[data-saved=concurrency]').waitFor({ timeout: 10000 });
    await A.page.unroute('**/api/xbin/llm/config', notice);
    check((await cfg()).concurrency === 8 && /the key of upstream fake was removed/.test(await admin.locator('[data-notices]').innerText()),
      'calls at once: saved; a write\'s notices are shown at the top');
    // a provider xbind found to have no Responses API (its calls are translated to chat completions): the row says
    // so, with when it was found, and a reset that has the provider asked again. This upstream has the API, so
    // the detection is put into one answer here — what xbind's own answer carries is internal/llm's test
    const found = async (route) => {
      if (route.request().method() !== 'PUT') return route.continue();
      const res = await route.fetch();
      const j = await res.json();
      j.upstreams = (j.upstreams ?? []).map((u) => (u.name === UP ? { ...u, translating: true, translatingSince: '2026-11-17T13:02:00Z' } : u));
      return route.fulfill({ response: res, json: j });
    };
    await A.page.route('**/api/xbin/llm/config', found);
    await admin.locator('form[data-concurrency] input').fill('9');
    await admin.locator('[data-save-concurrency]').click();
    const badge = row.locator('[data-translating=detected]');
    await badge.waitFor({ timeout: 10000 });
    await A.page.unroute('**/api/xbin/llm/config', found);
    check(/^Responses: translated — detected \d{1,2}[:.]\d{2}/i.test((await badge.innerText()).trim()), `a detected chat-only provider's row says "Responses: translated — detected <time>" (${(await badge.innerText()).trim()})`);
    await shotEl(A.page, `bx-llm-upstreams tr[data-upstream=${UP}]`, `llm-provider-translated-${THEME}`);
    const reset = A.page.waitForRequest((r) => r.url().endsWith(`/api/xbin/llm/upstreams/${UP}`) && r.method() === 'PUT');
    await row.locator('[data-redetect]').click();
    const sent = (await reset).postDataJSON();
    check(sent.responsesVia === 'auto' && Object.keys(sent).length === 1, `reset sends responsesVia: auto and nothing else (${JSON.stringify(sent)})`);
    await until(async () => await row.locator('[data-translating]').count() === 0, 'after the reset the row no longer says translated');
    await admin.locator('form[data-concurrency] input').fill('8');
    await admin.locator('[data-save-concurrency]').click();
    await until(async () => (await cfg()).concurrency === 8, 'calls at once is 8 again');
    const base = await used('dev1'); // counts from an earlier run today stay: limits are set relative to them
    const quotas = admin.locator('bx-llm-quotas');
    const setQuota = async (subject, fields, action) => {
      const q = quotas.locator(`bx-llm-quota[subject="${subject}"]`);
      for (const [k, v] of Object.entries(fields)) await q.locator(`input[data-limit=${k}]`).fill(String(v));
      if (action) await q.locator('select[data-action]').selectOption(action);
      await q.locator('[data-save]').click();
      await q.locator('[data-saved]').waitFor({ timeout: 10000 });
    };
    await setQuota('default', { tokensPerDay: base + 200 });
    await quotas.locator('form[data-add=users] input').fill('sales1');
    await quotas.locator('form[data-add=users] button').click();
    await setQuota('user:sales1', { tokensPerMonth: '1M', costPerMonth: '5' });
    await quotas.locator('form[data-add=orgs] input').fill('devs');
    await quotas.locator('form[data-add=orgs] button').click();
    await setQuota('org:devs', { tokensPerDay: base + 100 }, 'warn');
    const q1 = (await api('GET', '/llm/quotas')).body.quotas;
    check(q1.default?.tokensPerDay === base + 200 && q1.users?.sales1?.tokensPerMonth === 1e6 && q1.users.sales1.costPerMonth === 5
      && q1.orgs?.devs?.tokensPerDay === base + 100 && q1.orgs.devs.action === 'warn', `the default, a person's and an org's quota are saved (${JSON.stringify(q1)})`);
    check(/1M tokens\/month · \$5\.00\/month · stops/.test(await quotas.locator('details[data-override="user:sales1"] summary').innerText()), 'an override\'s row says it in words');
    await shotEl(A.page, 'bx-llm-quotas', `llm-quotas-${THEME}`);

    // ---- 5 + 6. usage after a call; the quota alerts ----
    check((await chat(D.ctx)).status() === 200, 'dev1 calls the connector directly (people: everyone)');
    const warn = await until(async () => (await alerts()).find((x) => x.kind === 'llm-quota' && x.message.includes('dev1')), 'an llm-quota alert for dev1');
    check(warn.level === 'warn' && warn.message.includes('the quota of org devs') && warn.message.includes('still go through') && warn.message.includes('/xbin/llm'),
      `past the org's warn limit: admins are told, the org and the page named (${warn.message})`);
    check(!(await alerts(dapi)).some((x) => x.kind === 'llm-quota'), '…and only admins are');
    await open(D.page);
    await D.page.waitForSelector('bx-llm-me [data-quota-message=warn]', { timeout: 15000 });
    const said = await D.page.locator('bx-llm-me [data-quota-message]').innerText();
    check(/^You have passed today’s LLM tokens/.test(said.trim()) && /Calls still go through/.test(said), `dev1 reads it in plain words (${said.trim()})`);
    await api('PUT', '/llm/quotas', { orgs: { devs: null } });
    check((await chat(D.ctx)).status() === 200, 'without the org\'s override the default holds dev1: under it, the call runs');
    const third = await chat(D.ctx);
    const refusal = await third.json().catch(() => ({}));
    check(third.status() === 429 && refusal.error?.code === 'xbin_llm_quota_exceeded', `at the default's limit the next call is refused (${third.status()} ${refusal.error?.code})`);
    const stop = await until(async () => (await alerts()).find((x) => x.kind === 'llm-quota' && /dev1 reached .*their calls are refused/.test(x.message)), 'the stop alert');
    check(stop.message.includes('the default quota') && stop.message.includes('bx llm quota set user:dev1'), `…and the alert says so (${stop.message})`);
    await open();
    await loaded();
    const usage = admin.locator('bx-llm-usage');
    const mine = usage.locator('table[data-usage=user] tr[data-row=dev1]');
    await mine.waitFor({ timeout: 10000 });
    const cells = (await mine.locator('td').allInnerTexts()).map((s) => s.trim());
    check(cells[1] === kit.fmtTokens(base + 2 * CALL), `usage: dev1's row counts their tokens today (${cells.join(' | ')}; want ${kit.fmtTokens(base + 2 * CALL)})`);
    check(/^\$0\.\d+/.test(cells[2]), `…and their cost, from the price table (${cells[2]})`);
    check(await usage.locator('table[data-usage=user] tr[data-row=admin]').count() === 1, 'the Test call is counted to the admin who ran it');
    check(/direct calls/.test(await usage.locator('table[data-usage=component]').innerText()), 'per component: calls no tile made are "direct calls"');
    check(await usage.locator(`table[data-usage=model] tr[data-row="${UP}/${MODEL}"]`).count() === 1, 'per model: the provider\'s model');
    await usage.locator('table[data-usage=user] [data-sort=label]').click();
    await settle(A.page);
    const names = (await usage.locator('table[data-usage=user] tbody tr td:first-child').allInnerTexts()).map((s) => s.trim());
    check(await usage.locator('table[data-usage=user] th[aria-sort=ascending]').count() === 1 && names.join() === [...names].sort((x, y) => x.localeCompare(y)).join(),
      `a click on a column header sorts by it (${names.join(', ')})`);
    await shotEl(A.page, 'bx-llm-usage', `llm-usage-${THEME}`);
    await shot(A.page, `llm-page-${THEME}`);
    // an estimated request and a name nothing lists (served: fakeopenai always reports usage, and its models are listed)
    const more = async (route) => {
      const res = await route.fetch();
      const j = await res.json();
      return route.fulfill({ response: res, json: { ...j, rows: [...(j.rows ?? []),
        { day: kit.today(), user: 'sales1', component: 'apps/agent', model: `${UP}/(unknown)`, requests: 3, failed: 2, estimated: 1, inputTokens: 40, outputTokens: 9, cost: 0, unpriced: 1 }] } });
    };
    await A.page.route('**/api/xbin/llm/usage*', more);
    await usage.locator('[data-refresh]').click();
    const unk = usage.locator(`table[data-usage=model] tr[data-row="${UP}/(unknown)"]`);
    await unk.waitFor({ timeout: 10000 });
    const ucells = (await unk.locator('td').allInnerTexts()).map((s) => s.trim());
    check(ucells[0] === `${UP}: unknown models` && ucells[1] === '~49' && /\+ 1 unpriced/.test(ucells[2]) && /3\s*\(2 failed · 1 estimated\)/.test(ucells[5]),
      `an estimated request is marked and unknown model names are one row (${ucells.join(' | ')})`);
    await A.page.unroute('**/api/xbin/llm/usage*', more);

    // ---- 7. a person who is not an admin ----
    await open(D.page);
    await D.page.waitForSelector('bx-llm-me [data-quota-message=stop]', { timeout: 15000 });
    await D.page.waitForSelector('bx-llm-me bx-llm-usage table[data-usage=model]', { timeout: 15000 });
    const dmode = await D.page.locator('bx-llm-page main').getAttribute('data-mode');
    const dsaid = (await D.page.locator('bx-llm-me [data-quota-message]').innerText()).trim();
    check(dmode === 'me' && /^You have used all of today’s LLM tokens/.test(dsaid) && /refused until it resets/.test(dsaid), `dev1's page is their own; at the limit it says so plainly (${dsaid})`);
    check(await D.page.locator('bx-llm-me [data-limit=tokensPerDay][data-state=stop]').count() === 1, 'their limit is a bar, full');
    const dtext = await D.page.evaluate(everything);
    check(await D.page.locator('bx-llm-admin [data-status], bx-llm-upstreams, bx-llm-quotas').count() === 0 && !dtext.includes(FAKE) && !dtext.includes('sales1'),
      'nothing of the settings: no provider, no address, nobody else\'s quota');
    const drows = (await D.page.locator('bx-llm-me table[data-usage=model] tbody tr td:first-child').allInnerTexts()).map((s) => s.trim());
    check(drows.join() === `${UP}/${MODEL}` && !dtext.includes('owner token'), `their own usage only (${drows.join(', ')})`);
    const refused = await Promise.all([dapi('GET', '/llm/config'), dapi('GET', '/llm/quotas'), dapi('GET', '/llm/usage?user=admin'), dapi('PUT', '/llm/config', { people: 'everyone' }),
      dapi('POST', '/llm/setup/dismiss')]);
    check(refused.every((r) => r.status === 403), `xbind refuses dev1 the settings, the quotas and another person's usage (${refused.map((r) => r.status).join(', ')})`);
    await shot(D.page, `llm-me-${THEME}`);

    // ---- 8. the host's file ----
    host({ upstreams: { house: { baseURL: `${FAKE}/v1`, models: [MODEL], private: true } }, defaults: { model: `house/${MODEL}` },
      quotas: { workspace: { tokensPerMonth: 50000000 }, default: { tokensPerDay: 5000000 }, users: {} } });
    await until(async () => (await cfg()).locks?.['defaults.model']?.locked, 'xbind reads the changed deployment file');
    await open();
    await loaded();
    check(await admin.locator('[data-locked-some]').count() === 1 && await admin.locator('[data-locked-all]').count() === 0, 'a note says the host sets part of it');
    const house = ups.locator('tr[data-upstream=house]');
    check((await house.locator('[data-locked]').innerText()).trim() === 'Set by your host' && await house.locator('[data-edit]').isDisabled() && await house.locator('[data-remove]').isDisabled()
      && await house.locator('[data-test]').isEnabled(), 'the host\'s provider: "Set by your host", edit and remove disabled, Test still there');
    check(await row.locator('[data-edit]').isEnabled(), '…while the admin\'s own provider stays editable');
    const dm = admin.locator('[data-model=default]');
    check(await dm.isDisabled() && await dm.inputValue() === `house/${MODEL}` && await admin.locator('form[data-models] .rows [data-locked]').count() === 1,
      'the default model is the host\'s: its field disabled, "Set by your host" beside it');
    check(await admin.locator('[data-model="role:agent"]').isEnabled(), '…a role the file doesn\'t name is still the admin\'s');
    const wsq = quotas.locator('bx-llm-quota[subject=workspace]');
    await wsq.locator('form[data-host]').waitFor({ timeout: 10000 });
    check(await wsq.locator('input:enabled, select:enabled').count() === 0 && await wsq.locator('[data-save]').count() === 0 && await wsq.locator('[data-locked]').count() === 1
      && await wsq.locator('input[data-limit=tokensPerMonth]').inputValue() === '50M', 'the workspace quota is the host\'s: read-only, no Save');
    check(await quotas.locator('form[data-add=users]').count() === 0 && await quotas.locator('details[data-override^="user:"]').count() === 0
      && await quotas.locator('form[data-add=orgs]').count() === 1, 'per-person overrides are locked with none (no Add); per-org ones stay the admin\'s');
    const dq = quotas.locator('bx-llm-quota[subject=default]');
    check(await dq.locator('input[data-limit=tokensPerDay]').isDisabled() && await dq.locator('input[data-limit=tokensPerDay]').inputValue() === '5M', 'the default quota is the host\'s too');
    // …and a ceiling over an org's entry, which the admin may still write
    await quotas.locator('form[data-add=orgs] input').fill('devs');
    await quotas.locator('form[data-add=orgs] button').click();
    const oq0 = quotas.locator('bx-llm-quota[subject="org:devs"]');
    await oq0.locator('[data-ceiling]').waitFor({ timeout: 10000 });
    check(/Your host sets the default \(tokens per day 5M tokens\), and it is a ceiling/.test((await oq0.locator('[data-ceiling]').innerText()).replace(/\s+/g, ' '))
      && await oq0.locator('input[data-limit=tokensPerDay]').getAttribute('placeholder') === 'up to 5M', 'an org\'s entry under the host\'s default: the form says it is a ceiling, and shows it in the field');
    await oq0.locator('input[data-limit=tokensPerDay]').fill('6M');
    await oq0.locator('[data-save]').click();
    await until(async () => /at most 5M tokens/.test(await oq0.locator('.err').innerText()), 'a limit above the ceiling is refused in the form');
    check(!(await api('GET', '/llm/quotas')).body.quotas.orgs?.devs, 'a limit above the ceiling is not sent: it would give nothing');
    await oq0.locator('input[data-limit=tokensPerDay]').fill('1M');
    await oq0.locator('[data-save]').click();
    await oq0.locator('[data-saved]').waitFor({ timeout: 10000 });
    check((await api('GET', '/llm/quotas')).body.quotas.orgs?.devs?.tokensPerDay === 1e6, 'a lower one narrows it, and is saved');
    await api('PUT', '/llm/quotas', { orgs: { devs: null } });
    const w = await api('PUT', '/llm/config', { defaults: { model: `${UP}/${MODEL}` } });
    check(w.status === 409 && (w.body.locked ?? []).includes('defaults.model'), `xbind refuses a write to a locked setting (${w.status} ${JSON.stringify(w.body.locked)})`);
    await shot(A.page, `llm-locked-${THEME}`);
    host({ lockAll: true, upstreams: { house: { baseURL: `${FAKE}/v1`, models: [MODEL] } }, defaults: { model: `house/${MODEL}` } });
    await until(async () => (await cfg()).host?.lockAll, 'lockAll in force');
    await open();
    await loaded();
    const top = admin.locator('[data-locked-all]');
    check(await top.count() === 1 && /Set by your host/.test(await top.innerText()), 'a fully locked connector says so at the top');
    const live = await admin.evaluate((el) => {
      const out = [];
      const walk = (root) => { for (const x of root.querySelectorAll('*')) { if (x.matches('input, select') && !x.disabled) out.push(x.outerHTML.slice(0, 80)); if (x.shadowRoot) walk(x.shadowRoot); } };
      walk(el.shadowRoot);
      return out;
    });
    check(live.length === 0, `every field is disabled (${live.join(' ')})`);
    check(await ups.locator('[data-add]').isDisabled() && await admin.locator('[data-add-role]').isDisabled() && await admin.locator('[data-save-models]').isDisabled()
      && await ups.locator(`tr[data-upstream=${UP}]`).count() === 0, 'no provider can be added, and the admin\'s own is not in force');
    check(await admin.locator('bx-llm-usage table[data-usage=user]').count() === 1, 'usage is still there to read');
    await shot(A.page, `llm-lockall-${THEME}`);
    host({});
    await until(async () => { const v = await cfg(); return !v.host?.lockAll && !Object.keys(v.locks ?? {}).length; }, 'the deployment file emptied again');

    // ---- 9. the admin console ----
    await gotoTab(A.page, 'llm', 'Providers');
    const tab = A.page.locator('bx-admin-llm bx-llm-admin');
    await tab.locator(`tr[data-upstream=${UP}]`).waitFor({ timeout: 15000 });
    check(await tab.locator('[data-status=ok]').count() === 1 && await tab.locator('bx-llm-quotas').count() === 1 && await tab.locator('bx-llm-usage table[data-usage=user]').count() === 1,
      'workspace → LLM is the page\'s element, through the console\'s own identity');
    const bar = A.page.locator('bx-admin .alertbar .al[data-kind="llm-quota"]');
    // the console reads its alerts after the tab draws: the bar is waited for
    await bar.first().waitFor({ timeout: 15000 }).catch(() => {});
    check(await bar.count() === 1 && await bar.locator('[data-llm-quotas]').count() === 1, 'the console\'s alert bar shows the quota alert with a link to the tab');
    await shot(A.page, `llm-admin-tab-${THEME}`);
    await gotoTab(A.page, 'users', 'dev1');
    await A.page.locator('bx-admin-users button[data-llm-quota=dev1]').click();
    const uq = A.page.locator('bx-admin-users bx-llm-quota[subject="user:dev1"]');
    await uq.locator('input[data-limit=tokensPerDay]').waitFor({ timeout: 10000 });
    await uq.locator('input[data-limit=tokensPerDay]').fill('5M');
    await uq.locator('[data-save]').click();
    await uq.locator('[data-saved]').waitFor({ timeout: 10000 });
    await uq.locator('[data-limits] [data-limit=tokensPerDay]').waitFor({ timeout: 10000 });
    check((await api('GET', '/llm/quotas')).body.quotas.users?.dev1?.tokensPerDay === 5e6 && /of 5M tokens · from their own quota/.test(await uq.locator('[data-limits]').innerText()),
      'users → LLM quota…: a person\'s own quota, saved, with their limit in force and what is used');
    check(!(await alerts()).some((x) => x.kind === 'llm-quota'), 'raising the quota ends the alert');
    await shotEl(A.page, 'bx-admin-users [data-llm-quota-row=dev1]', `llm-user-quota-${THEME}`);
    await gotoTab(A.page, 'orgs/devs', 'LLM quota');
    const oq = A.page.locator('bx-admin-orgs bx-llm-quota[subject="org:devs"]');
    await oq.locator('input[data-limit=costPerMonth]').waitFor({ timeout: 10000 });
    await oq.locator('input[data-limit=costPerMonth]').fill('25');
    await oq.locator('[data-save]').click();
    await oq.locator('[data-saved]').waitFor({ timeout: 10000 });
    check((await api('GET', '/llm/quotas')).body.quotas.orgs?.devs?.costPerMonth === 25, 'organisations → an org: its LLM quota, saved');

    // a phone
    await A.page.setViewportSize({ width: 390, height: 844 });
    await open();
    await loaded();
    const over = await A.page.evaluate(() => document.scrollingElement.scrollWidth - innerWidth);
    check(over <= 1, `a phone's width: the page doesn't scroll sideways (${over}px over)`);
    await shot(A.page, `llm-page-phone-${THEME}`);
    await A.page.setViewportSize({ width: 1400, height: 900 });
    check(errors.length === 0, `no page error (${errors.join(' | ')})`);
  } finally {
    await reset().catch((e) => check(false, `clean-up: ${e.message}`));
    await api('POST', '/llm/setup/dismiss').catch(() => {});
    await closeCtx(D.ctx, D.page).catch(() => {});
    await closeCtx(A.ctx, A.page).catch(() => {});
  }
  const end = await (async () => { const B = await login(browser, 'admin', 'admin'); const v = await call(B.ctx)('GET', '/llm/config'); await B.ctx.close(); return v.body; })();
  check(!end.configured && end.people === 'admins' && !Object.keys(end.quotas?.users ?? {}).length && !Object.keys(end.locks ?? {}).length,
    `the connector is left unconfigured, with no quota and nothing locked (${JSON.stringify({ configured: end.configured, people: end.people, quotas: end.quotas })})`);
  done();
}

module.exports = { llm };
