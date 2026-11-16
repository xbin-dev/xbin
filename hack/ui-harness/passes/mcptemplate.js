// hack/ui-harness/passes/mcptemplate.js — the MCP server example's page and
// the one-line fold of a tile's optional slots (docs/mcp.md; the journey's
// G9a and G10). As the admin:
//   1. (HARNESS_ISOLATE=1: the backend runs) examples/mcp-server copied in
//      as apps/mcpt-dice: its page lists roll_dice, read-only, with a form;
//      calling it with sides 20 shows "rolled 1d20: N"; at phone width the
//      page has no sideways scroll (shots: mcptemplate-page-<theme>,
//      mcptemplate-page-phone-<theme>);
//   2. apps/mcpt-asst asks for an llm slot marked required, an mcp slot, a
//      net slot and cap:open-links: unbound, its popover is over the
//      window and says llm is required;
//   3. llm bound (to apps/mcpt-llm, a tile providing openai): no popover —
//      one line in the card's flow above the tile, "1 grant request · 2
//      optional interfaces", and nothing bound or dismissed by it (shot:
//      mcptemplate-line-<theme>);
//   4. its "show" opens the popover with every row (the slots unfolded),
//      ⌃ folds it to the badge, the badge opens the popover again;
//   5. <bx-bindings> on its own (an older shell's popover) folds the two
//      optional rows into one line until it is clicked.
// The tiles, the binding and the dismissals are removed at the end.
'use strict';
const path = require('path');
const { URL, fs, login, closeCtx, openShell, usePersonalScreen, settle, sh, shotEl, checker, sleep, THEME } = require('../lib');

const WS = process.env.WS || '';
const DICE = 'apps/mcpt-dice', ASST = 'apps/mcpt-asst', LLM = 'apps/mcpt-llm';

async function until(fn, label, timeout = 15000) {
  const deadline = Date.now() + timeout;
  for (;;) {
    const v = await fn().catch(() => null);
    if (v) return v;
    if (Date.now() > deadline) throw new Error(`timed out: ${label}`);
    await sleep(200);
  }
}

function writeTiles() {
  const man = {
    [LLM]: { title: 'A model', provides: { openai: { kind: 'http', service: 'openai', role: 'writer' } } },
    [ASST]: {
      title: 'Assistant', uses: [{ target: 'cap:open-links', role: 'writer' }],
      interfaces: { llm: { kind: 'http', service: 'openai', multi: true, required: true }, mcp: { kind: 'http', service: 'mcp', multi: true }, net: { kind: 'net' } },
    },
  };
  for (const t of [LLM, ASST]) {
    const dir = path.join(WS, t);
    fs.mkdirSync(dir, { recursive: true });
    fs.writeFileSync(path.join(dir, 'index.html'), `<!doctype html><title>${t}</title><body style="margin:0;padding:16px;font:13px sans-serif"><h1>${t}</h1><p>The page under the window's prompts.</p>\n`);
    fs.writeFileSync(path.join(dir, 'xbin.json'), JSON.stringify(man[t]) + '\n');
  }
}

// What the shell shows of a tile: its badge, its popover, its one line.
const look = (page, tile) => page.evaluate((tile) => {
  const cv = document.querySelector('bx-shell')?.shadowRoot?.querySelector('bx-canvas')?.shadowRoot;
  const card = cv?.querySelector(`.card[data-path="${tile}"]`);
  const pop = cv?.querySelector(`.pqpop[data-prompts-for="${tile}"]`);
  const line = cv?.querySelector(`.pqline[data-prompts-for="${tile}"]`);
  const body = card?.querySelector('.cbody');
  const b = pop?.querySelector('bx-bindings')?.shadowRoot;
  const r = (el) => { const x = el?.getBoundingClientRect(); return x ? { y: Math.round(x.y), h: Math.round(x.height) } : null; };
  return {
    card: !!card, badge: Number(card?.querySelector('.pqb')?.dataset.prompts ?? 0),
    pop: !!pop, line: line?.textContent.replace(/\s+/g, ' ').trim() ?? '', lineRect: r(line), bodyRect: r(body), lineBeforeBody: line?.nextElementSibling === body,
    slots: [...(b?.querySelectorAll('.row:not(.opt)') ?? [])].map((x) => `${x.querySelector('.slot')?.textContent.trim()}${x.querySelector('.req') ? ' (required)' : ''}`),
    folded: b?.querySelector('.row.opt')?.textContent.replace(/\s+/g, ' ').trim() ?? '',
  };
}, tile);

async function mcpTemplate(browser) {
  const { check, skip, done } = checker(`mcp-template-${THEME}`);
  check(!!WS, 'WS is set (run.sh exports it)');
  const A = await login(browser, 'admin', 'admin', { viewport: { width: 1400, height: 900 } });
  const api = (method, p, data) => A.ctx.request.fetch(`${URL}/api/xbin${p}`, {
    method, headers: { 'Content-Type': 'application/json' }, ...(data === undefined ? {} : { data: JSON.stringify(data) }), failOnStatusCode: false,
  });
  const original = await (await api('GET', '/prefs/layout')).json().catch(() => null);
  await api('DELETE', '/prefs/dismissed');
  try {
    await openShell(A.page);
    await usePersonalScreen(A.page);
    const id = await sh(A.page, (t) => t.addScreen('canvas'));
    await settle(A.page);

    // 1. the template's page
    if (!process.env.HARNESS_ISOLATE) skip('the MCP server example\'s page: its backend runs under HARNESS_ISOLATE=1 only');
    else {
      const stamped = await api('POST', '/templates/new', { source: 'mcp-server', path: 'apps/mcpt-stamped' });
      check(stamped.status() === 404, `there is no mcp-server template to instantiate (${stamped.status()})`);
      fs.cpSync(path.join(__dirname, '..', '..', '..', 'examples', 'mcp-server'), path.join(WS, DICE), { recursive: true }); // as a tile's coding agent would write it (docs/mcp.md)
      await until(async () => (await (await api('GET', '/components')).json()).some((c) => c.path === DICE), 'apps/mcpt-dice registered');
      await sh(A.page, (t, p) => t.openTile(p), DICE);
      const frame = await until(async () => A.page.frames().find((f) => f.url().includes(`/c/${DICE}/`)), 'the page\'s frame', 30000);
      const listed = await until(async () => (await frame.locator('section.tool').count()) > 0 || null, 'the tools listed (the backend builds in its sandbox)', 300000).catch(() => false);
      const text = (await frame.locator('body').innerText().catch(() => '')).replace(/\s+/g, ' ');
      check(!!listed && /roll_dice/.test(text) && /read-only/.test(text) && /1 tool · MCP 2025-06-18/.test(text), `the page lists roll_dice, read-only (${text.slice(0, 200)})`);
      if (listed) {
        await frame.locator('section.tool input[name="sides"]').fill('20');
        await frame.locator('section.tool button[type="submit"]').click();
        const out = await until(async () => { const t = await frame.locator('section.tool pre').innerText(); return /rolled 1d20: \d+/.test(t) ? t : null; }, 'the call\'s result', 30000).catch(() => '');
        check(/rolled 1d20: \d+ \(total \d+\)/.test(out) && /structuredContent/.test(out), `Call rolls a d20 through tools/call (${out.replace(/\s+/g, ' ').slice(0, 120)})`);
      }
      await shotEl(A.page, `bx-canvas .card[data-path="${DICE}"]`, `mcptemplate-page-${THEME}`);
      await A.page.setViewportSize({ width: 390, height: 844 });
      await settle(A.page);
      const fit = await frame.evaluate(() => [document.documentElement.scrollWidth, document.documentElement.clientWidth]).catch(() => [0, 1]);
      check(fit[0] <= fit[1] + 1, `at phone width the page doesn't scroll sideways (${fit.join(' ≤ ')})`);
      await shotEl(A.page, `bx-canvas .card[data-path="${DICE}"]`, `mcptemplate-page-phone-${THEME}`);
      await A.page.setViewportSize({ width: 1400, height: 900 });
      await sh(A.page, (t, p) => t.closeTile(p), DICE);
      await settle(A.page);
    }

    // 2. a tile with a required slot, unbound: the popover, which says so
    writeTiles();
    await until(async () => (await (await api('GET', '/components')).json()).filter((c) => [LLM, ASST].includes(c.path)).length === 2, 'the tiles registered');
    await sh(A.page, (t, p) => t.openTile(p), ASST);
    let a = await until(async () => { const v = await look(A.page, ASST); return v.pop && v.slots.length ? v : null; }, 'the assistant\'s popover').catch(async () => look(A.page, ASST));
    check(a.pop && !a.line && a.slots.includes('llm (required)') && a.slots.includes('mcp') && !a.folded, `unbound: the popover over the window, llm marked required (${JSON.stringify(a.slots)})`);

    // 3. llm bound: one line above the tile instead
    const bound = await api('POST', '/bindings', { component: ASST, slot: 'llm', providers: [LLM] });
    check(bound.ok(), `llm bound (${bound.status()})`);
    a = await until(async () => { const v = await look(A.page, ASST); return v.line ? v : null; }, 'the one line').catch(async () => look(A.page, ASST));
    check(!a.pop && a.line.startsWith('1 grant request · 2 optional interfaces') && /show$/.test(a.line), `its required slot bound: one line, no popover (${a.line})`);
    check(a.lineBeforeBody && a.lineRect && a.bodyRect && a.lineRect.y + a.lineRect.h <= a.bodyRect.y + 1 && a.lineRect.h <= 40, `the line is in the card's flow above the tile, one line high (${JSON.stringify([a.lineRect, a.bodyRect])})`);
    check(a.badge === 3, `the badge still counts what waits (${a.badge})`);
    await shotEl(A.page, `bx-canvas .card[data-path="${ASST}"]`, `mcptemplate-line-${THEME}`);
    const after = await (await api('GET', '/bindings')).json();
    const left = (after.pending || []).filter((p) => p.component === ASST);
    check(left.length === 2 && left.every((p) => p.optional === true) && !((after.bindings || {})[ASST] || {}).mcp, `nothing else was bound, and both rows are optional (${JSON.stringify(left.map((p) => [p.slot, p.optional]))})`);
    const dis = await (await api('GET', '/prefs/dismissed')).json().catch(() => ({}));
    check(!JSON.stringify(dis).includes(ASST), 'nothing was dismissed');

    // 4. show → the popover with every row; ⌃ → the badge; the badge → the popover
    await A.page.locator(`bx-canvas .pqline[data-prompts-for="${ASST}"] .pqshow`).click();
    a = await until(async () => { const v = await look(A.page, ASST); return v.pop && v.slots.length ? v : null; }, 'the popover from the line').catch(async () => look(A.page, ASST));
    check(a.pop && !a.line && a.slots.includes('mcp') && a.slots.includes('net') && !a.folded, `"show" opens the popover, every slot shown (${JSON.stringify(a.slots)})`);
    await shotEl(A.page, `bx-canvas .card[data-path="${ASST}"]`, `mcptemplate-line-open-${THEME}`);
    await A.page.locator(`bx-canvas .pqpop[data-prompts-for="${ASST}"] .pqx`).click();
    a = await look(A.page, ASST);
    check(!a.pop && !a.line && a.badge === 3, `⌃ folds it to the badge (${JSON.stringify({ pop: a.pop, line: a.line, badge: a.badge })})`);
    await A.page.locator(`bx-canvas .card[data-path="${ASST}"] .pqb`).click();
    a = await look(A.page, ASST);
    check(a.pop && !a.line, 'the badge opens the popover');

    // 5. <bx-bindings> alone — what an older shell's popover holds — folds them itself
    const own = await A.page.evaluate(async (tile) => {
      const el = document.createElement('bx-bindings');
      el.setAttribute('tile', tile);
      document.body.append(el);
      for (let i = 0; i < 50 && !el.shadowRoot?.querySelector('.row'); i++) await new Promise((r) => setTimeout(r, 100));
      const line = el.shadowRoot?.querySelector('.row.opt');
      const before = { line: line?.textContent.replace(/\s+/g, ' ').trim() ?? '', rows: el.shadowRoot?.querySelectorAll('.row:not(.opt)').length ?? -1 };
      line?.querySelector('a')?.click();
      await new Promise((r) => setTimeout(r, 300));
      const opened = el.shadowRoot?.querySelectorAll('.row:not(.opt)').length ?? -1;
      el.remove();
      return { ...before, opened };
    }, ASST);
    check(own.line === '2 optional interfaces — show' && own.rows === 0 && own.opened === 2, `<bx-bindings> on its own folds the optional rows into one line until clicked (${JSON.stringify(own)})`);
  } finally {
    await api('DELETE', '/bindings', { component: ASST, slot: 'llm' }).catch(() => {});
    await api('DELETE', '/prefs/dismissed').catch(() => {});
    if (original) await api('PUT', '/prefs/layout', original).catch(() => {});
    for (const t of [DICE, LLM, ASST]) {
      const dir = path.join(WS, t);
      if (WS && path.isAbsolute(WS) && dir.startsWith(path.join(WS, 'apps', 'mcpt-'))) fs.rmSync(dir, { recursive: true, force: true });
    }
    await settle(A.page).catch(() => {});
    await closeCtx(A.ctx, A.page);
  }
  done();
}

module.exports = { mcpTemplate };
