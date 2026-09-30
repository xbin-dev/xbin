// hack/ui-harness/passes/agentmoves.js — a partitioned agent's shared
// conversation that stops being shared moves to its owner's own space, and
// the page follows it (work pack AF, the owner's ruling 90 §I10; the
// template's API.md "Partitioned instances" → "Shared conversations"),
// against the real apps/agent seeded partitioned (HARNESS_ISOLATE=1
// HARNESS_AGENT_PARTITION=1) and hack/fakeopenai:
//   - the admin starts a conversation shared with dev1 at the global
//     instance and has it open;
//   - dev1 leaves it — nobody else is in it now: the admin's open page
//     follows it to its new id in the admin's own partition (from 2^40), its
//     transcript there; the list shows it once, never the old one;
//   - the old address (#c=<old id>, a push link) opens the new one;
//   - dev1, at the global instance, learns nothing of the move and can't
//     drive it; nor can the admin's own page (only their partition's
//     backend, with the mailed key); no page errors.
// Otherwise (the default unpartitioned seed) it says SKIP.
const { login, log, shot, checker, noGocryptfs } = require('../lib');

const URL = process.env.URL || 'http://127.0.0.1:8697';
const B = 2 ** 40;
const until = (page, fn, arg, timeout = 20000) => page.waitForFunction(fn, arg ?? null, { timeout, polling: 50 });
const call = (page, p, { method = 'GET', body, global } = {}) => page.evaluate(async ([p, method, body, global]) => {
  const r = await xbin.fetch(`/api/${xbin.self}${p}`, { method, ...(body ? { headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) } : {}),
    ...(global ? { partition: 'global' } : {}) });
  return { status: r.status, body: await r.json().catch(() => null) };
}, [p, method, body ?? null, !!global]);
const rows = (page) => page.$$eval('#runs .run', (els) => els.map((e) => ({ id: +e.dataset.id, t: e.querySelector('.t')?.textContent || '' })));
const hashID = (page) => page.evaluate(() => { const m = /#c=(\d+)/.exec(location.hash); return m ? +m[1] : 0; });

async function open(browser, user, pass) {
  const { ctx, page } = await login(browser, user, pass, { viewport: { width: 1300, height: 900 } });
  const errors = [];
  page.on('pageerror', (e) => errors.push(e.message));
  page.on('response', (r) => { if (r.status() >= 500 && r.url().includes('/api/apps/agent')) log(`agentMoves(${user}): ${r.status()} ${r.request().method()} ${r.url()}`); });
  await page.goto(`${URL}/c/apps/agent/`);
  await page.waitForSelector('#msg', { timeout: 60000 });
  return { ctx, page, errors };
}

async function agentMoves(browser) {
  const { check, skip, done } = checker('agent-moves');
  if (noGocryptfs()) { skip(`apps/agent is held: ${noGocryptfs()}`); return done(); }
  if (!process.env.HARNESS_AGENT_PARTITION) { skip('apps/agent is seeded unpartitioned — run with HARNESS_ISOLATE=1 HARNESS_AGENT_PARTITION=1'); return done(); }
  const A = await open(browser, 'admin', 'admin');
  const D = await open(browser, 'dev1', 'devpass123');
  const part = await A.page.evaluate(() => xbin.partition || '');
  check(part.startsWith('user:'), `the admin's page is their own partition (xbin.partition ${JSON.stringify(part)})`);
  if (!part.startsWith('user:')) return done();

  // the admin's conversation shared with dev1, at the global instance, open
  const tok = `mplan${Date.now().toString(36)}`;
  const r = await call(A.page, '/ask', { method: 'POST', global: true,
    body: { text: `${tok} says hello`, title: `${tok} plan`, share: { members: [{ user: 'dev1', role: 'participant' }] } } });
  const old = r.body?.id || 0;
  check(r.status === 200 && old > 0 && old < B, `a conversation shared with dev1 at the global instance (${r.status}, id ${old})`);
  await A.page.evaluate((id) => { location.hash = 'c=' + id; }, old);
  await until(A.page, () => [...document.querySelectorAll('#timeline .msg.assistant:not(.live)')].some((e) => e.textContent.includes('Hello from the fake model.')), null, 60000);
  await shot(A.page, 'agent-moves-shared');

  // dev1 leaves: nobody else is in it — it moves to the admin's own space, and the open page follows
  const left = await call(D.page, `/runs/${old}/members/dev1`, { method: 'DELETE', global: true });
  check(left.status === 200, `dev1 leaves it (${left.status})`);
  const moved = await until(A.page, (b) => { const m = /#c=(\d+)/.exec(location.hash); return m && +m[1] >= b && +m[1]; }, B, 60000).then((h) => h.jsonValue(), () => 0);
  check(moved >= B, `the admin's open page follows it to their own space (#c=${moved || await hashID(A.page)})`);
  const shown = await until(A.page, () => [...document.querySelectorAll('#timeline .msg.assistant')].some((e) => e.textContent.includes('Hello from the fake model.')), null, 20000)
    .then(() => true, () => false);
  check(shown, 'its transcript, there');
  const once = await until(A.page, ([t, a, b]) => {
    const ids = [...document.querySelectorAll('#runs .run')].filter((e) => (e.querySelector('.t')?.textContent || '').includes(t)).map((e) => +e.dataset.id);
    return ids.length === 1 && ids[0] === b && !document.querySelector(`#runs .run[data-id="${a}"]`);
  }, [tok, old, moved], 30000).then(() => true, () => false);
  check(once, `the list shows it once, at its new id (${JSON.stringify((await rows(A.page)).filter((x) => x.t.includes(tok) || x.id === old))})`);
  await shot(A.page, 'agent-moves-followed');

  // the old address opens the new one
  await A.page.evaluate(() => { location.hash = ''; });
  await until(A.page, () => !/#c=/.test(location.hash), null, 10000).catch(() => {});
  await A.page.evaluate((id) => { location.hash = 'c=' + id; }, old);
  const via = await until(A.page, (b) => { const m = /#c=(\d+)/.exec(location.hash); return m && +m[1] >= b && +m[1]; }, B, 20000).then((h) => h.jsonValue(), () => 0);
  check(via === moved, `#c=<the old id> opens it where it went (${via})`);

  // dev1, at the global instance, learns nothing of the move and can't drive it (done answers him as for no move at all);
  // nor can the admin's own page — only their partition's backend has the key the move was mailed with
  const tries = [
    [D, 'GET', `/moves/${old}`, 404], [D, 'GET', `/moves/${old}/export`, 404], [D, 'POST', `/moves/${old}/abandon`, 404],
    [D, 'POST', `/moves/${old}/done`, 200, 'gone'], [D, 'POST', '/moves/99999/done', 200, 'gone'],
    [A, 'GET', `/moves/${old}/export`, 403], [A, 'POST', `/moves/${old}/done`, 403], [A, 'POST', `/moves/${old}/abandon`, 403],
  ];
  const got = [];
  for (const [who, method, p, want, state] of tries) {
    const x = await call(who.page, p, { method, global: true, ...(method === 'POST' ? { body: { to: moved + 1 } } : {}) });
    got.push(`${who === D ? 'dev1' : 'admin'} ${method} ${p}: ${x.status}${x.body?.state ? ' ' + x.body.state : ''}`);
    check(x.status === want && (!state || x.body?.state === state), `${got[got.length - 1]} (want ${want}${state ? ' ' + state : ''})`);
  }
  const still = await call(A.page, `/moves/${old}`, { global: true });
  check(still.status === 200 && still.body?.state === 'moved' && still.body?.to === moved, `the move's record unchanged by those (${still.status} ${JSON.stringify(still.body)})`);
  const errs = [...A.errors, ...D.errors];
  check(errs.length === 0, `no page errors${errs.length ? ': ' + errs.join(' | ') : ''}`);
  await A.ctx.close();
  await D.ctx.close();
  return done();
}

module.exports = { agentMoves };
