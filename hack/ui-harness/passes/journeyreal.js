// hack/ui-harness/passes/journeyreal.js — the owner's UX benchmark with
// nothing scripted (plans/wsagent/real-model-run-2026-11-17.md): the same
// journey as journeyagent.js — a fresh install, the host's LLM, the first
// login, the first-run sheet, ONE prompt — "Make a tool that rolls dice and
// give it to a new assistant I can chat with." — but the model behind the
// workspace agent is a real one (the host file's upstream is the endpoint
// JOURNEY_REAL_BASE names, its key read by xbind from JOURNEY_REAL_KEYFILE)
// and the tile's coding agent is a real one from the rootfs, on the
// workspace's LLM connector. The pass drives what a person would: Review…,
// the ticks, Apply, every permission request in the dock's Needs you, and
// at the end its own roll in the assistant.
//
// Nothing here asserts what the model says; it records what happened —
// every interaction through the same counter (journey.js), each stretch's
// wall time, where it stopped if it did — and writes out/journey-real.md,
// the conversation (out/journey-real-transcript.md) and the connector's
// usage. A place where the person had to do something the journey has no
// row for is a "dead-end" row.
//
//   JOURNEY_REAL_BASE     the upstream's base URL
//   JOURNEY_REAL_MODEL    the model's id at the upstream
//   JOURNEY_REAL_KEYFILE  a file holding the key (never the key)
//   JOURNEY_REAL_BUILD_MIN  how long the tile agent may build (default 25)
//
// Needs HARNESS_ISOLATE=1 and `make sealed`; SKIPs without the endpoint.
'use strict';
const path = require('path');
const { fs, OUT, checker, log, sleep } = require('../lib');
const J0 = require('./journey');

const DOCK = 'bx-agent-dock';
const DLG = 'bx-agent-changes';
const TA = 'bx-chat-composer textarea';
const WSAGENT = 'xbin/wsagent';
const SAY = 'Make a tool that rolls dice and give it to a new assistant I can chat with.';
const PERSON = { id: 'ada', name: 'Ada Admin', password: require('crypto').randomBytes(12).toString('base64url') }; // (made per run, never logged)
const redact = (s) => String(s).replace(/\b(xbl_|xbs_|xbt_|sk-)[A-Za-z0-9_-]{8,}/g, '[redacted]').replace(/Bearer [A-Za-z0-9_.-]{12,}/g, 'Bearer [redacted]').replace(/token=[0-9a-f]+/g, 'token=…');

async function journeyReal(browser) {
  const { check, skip, done } = checker('journey-real');
  const real = { base: process.env.JOURNEY_REAL_BASE, model: process.env.JOURNEY_REAL_MODEL, keyFile: process.env.JOURNEY_REAL_KEYFILE };
  if (!real.base || !real.model || !real.keyFile) { skip('the journey against a real model: no JOURNEY_REAL_BASE/_MODEL/_KEYFILE (hack/wsagent-real-run.sh sets them)'); done(); return; }
  const why = J0.noJourney();
  if (why) { skip(`the journey against a real model: ${why}`); done(); return; }
  const buildMs = Number(process.env.JOURNEY_REAL_BUILD_MIN || 25) * 60000;
  const X = await J0.boot('real', { real });
  const note = []; // what happened, for the write-up
  const say = (s) => { note.push(s); log(`journeyReal: ${redact(s)}`); };
  let ctx, keep = async () => {};
  try {
    const o = await J0.openPage(browser, 'journeyReal');
    ctx = o.ctx;
    const page = o.page;
    const J = J0.journey('real', () => page);
    const call = async (method, p, data, headers) => { const r = await ctx.request.fetch(`${X.url}/api/xbin${p}`, { method, ...(data !== undefined ? { data } : {}), ...(headers ? { headers } : {}), failOnStatusCode: false }); return { status: r.status(), data: await r.json().catch(() => null) }; };
    const t0 = Date.now();

    await J0.firstLogin(J, page, X);
    await J0.firstRunSheet(J, page, PERSON);
    const frame = async () => { const el = await page.waitForSelector(`${DOCK} iframe`, { state: 'attached', timeout: 60000 }); const f = await el.contentFrame(); await f.waitForSelector('.wsp-main[aria-busy="false"]', { timeout: 180000 }); return f; };
    const f = await J.wait('the dock opens by itself; the agent\'s page is up', async () => { await page.waitForSelector(`${DOCK}[open]`, { timeout: 30000 }); return frame(); });
    const provs = await call('GET', '/agent/providers');
    say(`coding agents here (GET /agent/providers): ${[].concat(provs.data || []).map((h) => `${h.id}${h.llm ? ` [llm ${JSON.stringify(h.llm).slice(0, 120)}]` : ''}`).join(', ')}`);

    // the agent's own API, as its page reads it: the conversation, for the transcript
    const agentAPI = async (p) => {
      const tok = await call('GET', `/frame-token?component=${encodeURIComponent(WSAGENT)}`);
      const r = await ctx.request.fetch(`${X.url}/api/${WSAGENT}${p}`, { headers: { 'X-XBin-Frame-Token': (tok.data && tok.data.token) || '' }, failOnStatusCode: false });
      return r.json().catch(() => null);
    };
    const conversation = async () => {
      const ss = await agentAPI('/ws/sessions');
      const s = ((ss && ss.sessions) || [])[0];
      if (!s) return { status: '', messages: [] };
      const v = await agentAPI(`/runs/${s.id}/view`);
      return { status: s.status, waiting: s.waiting || '', messages: (v && v.messages) || [] };
    };
    const lastSaid = (c) => { const a = c.messages.filter((m) => m.role === 'assistant' && String(m.content || '').trim()); return a.length ? a[a.length - 1].content : ''; };
    const tools = (c) => c.messages.flatMap((m) => (m.toolCalls || m.tool_calls || []).map((t) => (t.function || {}).name)).filter(Boolean);

    // the conversation, as the write-up quotes it — written however the journey ends
    keep = async () => {
      const c = await conversation().catch(() => ({ messages: [] }));
      const out = ['# journey real — the workspace agent\'s conversation', ''];
      for (const m of c.messages) {
        if (m.role === 'system') continue;
        const calls = (m.toolCalls || m.tool_calls || []).map((x) => `  → ${(x.function || {}).name} ${(x.function || {}).arguments}`).join('\n');
        const text = String(m.content || '');
        out.push(`${m.role === 'tool' ? `  ← ${m.name}` : m.role.toUpperCase()}: ${m.role === 'tool' && text.length > 4000 ? `${text.slice(0, 4000)}… [${text.length - 4000} more]` : text}`, calls, '');
      }
      fs.writeFileSync(path.join(OUT, 'journey-real-transcript.md'), redact(out.join('\n')) + '\n');
      const usage = (await call('GET', '/llm/usage')).data || {};
      say(`usage: ${JSON.stringify(usage.totals || {})}; by component: ${JSON.stringify((usage.rows || []).map((r) => ({ component: r.component, requests: r.requests, in: r.inputTokens, out: r.outputTokens })))}`);
      // the tile agent's own transcript, as xbind keeps it (its last lines)
      for (const s of [].concat((await call('GET', '/term/sessions?origin=agent&compact=1')).data || [])) {
        const d = (await call('GET', `/term/sessions/${s.id}`)).data || {};
        say(`tile agent ${s.provider} in ${s.cwd}: status ${s.status}, turn ${s.turn || ''}, last: ${String(s.last || d.last || '').slice(0, 400)}`);
      }
    };

    J.section('the one prompt');
    await J.field(`the agent: the prompt ("${SAY}")`, () => f.fill(TA, SAY));
    await J.key('the agent: Enter', () => f.press(TA, 'Enter'));
    const tPrompt = Date.now();
    let planned = false, asked = 0;
    await J.wait('the agent looks and proposes one plan', async () => {
      for (let i = 0; i < 720 && !planned; i++) { // up to 6 minutes
        planned = (await f.locator('.wsc-req[data-kind="changeset"]').count()) > 0;
        if (planned) break;
        const c = i % 8 === 7 ? await conversation() : null;
        if (c && c.status === 'waiting_input' && asked < 2) { // it asked instead of proposing: the person says go on (counted)
          asked++;
          say(`the agent asked before proposing: ${String(c.waiting).slice(0, 300)}`);
          await J.field('the agent: an answer to its question ("Yes — go ahead.")', () => f.fill(TA, 'Yes — go ahead with what you think is best.'));
          await J.key('the agent: Enter', () => f.press(TA, 'Enter'));
        } else if (c && c.status === 'idle' && lastSaid(c) && i > 40 && (await f.locator('.wsc-req[data-kind="changeset"]').count()) === 0) {
          const single = ((await call('GET', '/delegate/requests')).data || {}).requests || [];
          say(`the agent ended its turn without a plan (${single.length} single request(s) open). It said: ${lastSaid(c).slice(0, 600)}`);
          break;
        }
        await sleep(500);
      }
    });
    const c1 = await conversation();
    say(`first turn: ${((Date.now() - tPrompt) / 1000).toFixed(0)} s; tools called: ${tools(c1).join(' ')}`);
    check(planned, `the agent proposed ONE plan from the one prompt (${asked} question(s) first)`);
    const singles = ((await call('GET', '/delegate/requests')).data || {}).requests || [];
    check(singles.length === 0, `no single request beside the plan (${singles.length})`);
    if (!planned) { J.deadEnd('no plan to review: the journey stops here'); throw new Error('no plan'); }

    J.section('the one review');
    await J.click('the plan\'s card: "Review…"', async () => { await f.locator('.wsc-req[data-kind="changeset"] button.primary').last().click(); await page.waitForSelector(`${DLG} [role="dialog"][data-view="review"]`, { timeout: 20000 }); });
    const lines = await page.$$eval(`${DLG} .item .line > span:first-child`, (els) => els.map((e) => e.textContent.trim()));
    say(`the plan, in xbind's words: ${JSON.stringify(lines)}`);
    const ticks = await page.$$eval(`${DLG} .item`, (els) => els.map((e) => ({ n: e.dataset.n, on: e.querySelector('input').checked, off: e.querySelector('input').disabled, admin: !!e.querySelector('[data-admin]') })));
    const start = (await page.locator(`${DLG} .item`).allInnerTexts()).map((t) => t.replace(/\s+/g, ' ')).find((t) => /Coding agent/.test(t)) || '';
    say(`the start line: ${start.slice(0, 500)}`);
    for (const tk of ticks) {
      if (!tk.on && !tk.off) await J.click(`the review: tick line ${tk.n}${tk.admin ? ' (an admin\'s line: it starts unticked)' : ' (a warned line: it starts unticked)'}`, () => page.check(`${DLG} .item[data-n="${tk.n}"] input`));
    }
    await page.screenshot({ path: path.join(J0.SHOTS, 'real-review.png') }).catch(() => {});
    await J.click(`the review: "Apply ${lines.length} changes…"`, async () => { await page.click(`${DLG} button[data-apply][aria-disabled="false"]`, { timeout: 20000 }); await page.waitForSelector(`${DLG} [role="dialog"]:is([data-view="results"], [data-view="stepup"])`, { timeout: 240000 }); });
    if ((await page.getAttribute(`${DLG} [role="dialog"]`, 'data-view')) === 'stepup') {
      await J.field('the review: the password (a sign-in older than ten minutes)', () => page.fill(`${DLG} #pw`, PERSON.password));
      await J.click('the review: apply', async () => { await page.click(`${DLG} button[data-apply][aria-disabled="false"]`); await page.waitForSelector(`${DLG} [role="dialog"][data-view="results"]`, { timeout: 240000 }); });
    }
    const results = await page.evaluate(() => { const r = document.querySelector('bx-agent-changes').shadowRoot; return [...r.querySelectorAll('.item')].map((e) => `${e.dataset.n}:${(e.querySelector('[data-result]') || { dataset: {} }).dataset.result || '?'}${e.querySelector('[data-error]') ? ` (${e.querySelector('[data-error]').textContent.trim().slice(0, 200)})` : ''}`).join(' '); });
    say(`Apply: ${results}`);
    const applied = !/failed|refused|skipped/.test(results);
    check(applied, `every chosen line applied or waits (${results})`);
    if (!applied) {
      await page.screenshot({ path: path.join(J0.SHOTS, 'real-apply-failed.png') }).catch(() => {});
      J.deadEnd(`a line of the plan was not applied: ${results}`);
      await page.click(`${DLG} button[data-close]`, { timeout: 3000 }).catch(() => {});
    } else {
      await page.waitForFunction(() => !document.querySelector('bx-agent-changes'), null, { timeout: 15000 }).catch(() => {});
    }
    const tApplied = Date.now();

    J.section('the tile agent builds (its permission requests are the person\'s)');
    const sessions = async () => [].concat((await call('GET', '/term/sessions?origin=agent&compact=1')).data || []).filter((x) => x && x.kind !== 'shell');
    const bound = async () => { const b = ((await call('GET', '/bindings')).data || {}).bindings || {}; return Object.entries(b).find(([, v]) => v && v.mcp) || null; };
    let answers = 0, trayOpen = false, extra = 0, builtAt = 0, lastState = '';
    const deadline = Date.now() + buildMs;
    let quiet = 0;
    while (Date.now() < deadline) {
      const ss = await sessions();
      const state = ss.map((s) => `${s.provider}:${s.status}${s.pending ? `(${s.pending} waiting)` : ''}`).join(' ');
      if (state !== lastState) { say(`tile agent(s) at ${((Date.now() - tApplied) / 1000).toFixed(0)} s: ${state || 'none'}`); lastState = state; }
      // a permission request: answered in the dock's Needs you, "Allow always" where it is offered
      if (ss.some((s) => s.pending > 0 || s.status === 'waiting_permission')) {
        if (!(await page.locator(`${DOCK} button[data-needs][aria-expanded="true"]`).count())) { // the tray is closed: one click opens it
          await J.click('the dock: "Needs you"', () => page.click(`${DOCK} button[data-needs]`));
          trayOpen = true;
        }
        const card = page.locator(`${DOCK} bx-agent-permits .bxc-perm`).first();
        if (!(await card.count())) { await sleep(500); continue; } // the request is on its way to the tray
        const always = card.locator('button[data-kind="allow_always"]:not([disabled])'), once = card.locator('button.bxc-primary:not([disabled])');
        const btn = (await always.count()) ? always.first() : (await once.count()) ? once.first() : null;
        if (btn) {
          const label = (await btn.innerText()).trim();
          const what = (await card.innerText().catch(() => '')).replace(/\s+/g, ' ').slice(0, 140);
          const pid = await card.getAttribute('data-pid');
          await page.waitForSelector(`${DOCK} bx-agent-permits[armed]`, { timeout: 8000 }).catch(() => {});
          await J.click(`Needs you: "${label}" (${what}…)`, async () => { await btn.click(); await page.locator(`${DOCK} bx-agent-permits .bxc-perm[data-pid="${pid}"]`).waitFor({ state: 'detached', timeout: 20000 }); });
          answers++;
          continue;
        }
      }
      // anything else the agent files on the way (a single request): the person allows it once — a row by hand
      const open = (((await call('GET', '/delegate/requests')).data || {}).requests || []).filter((r) => r.state === 'pending');
      for (const r of open) {
        extra++;
        say(`an extra request beside the plan: ${r.kind} ${r.method || ''} ${r.target || ''} — "${String(r.summary || '').slice(0, 160)}" (allowed once, by API)`);
        await call('POST', `/delegate/requests/${r.id}/approve`, { grant: 'once' });
        J.hand(2, `an extra request the agent filed after the plan (${r.kind} ${r.method || ''} ${r.target || ''}): open it, Allow once`);
      }
      const b = await bound();
      if (b && !builtAt) { builtAt = Date.now(); say(`xbind applied the waiting binding ${((builtAt - tApplied) / 1000).toFixed(0)} s after Apply: ${b[0]}.mcp → ${b[1].mcp}`); }
      const c = await conversation();
      const idle = c.status !== 'running' && c.status !== 'queued' && ss.every((s) => s.status === 'idle' || s.status === 'ended' || s.status === 'exited');
      quiet = idle && (b || ss.length === 0 || ss.every((s) => s.status !== 'running')) ? quiet + 1 : 0;
      if (b && quiet >= 10) break; // bound, and everything at rest for ~20 s: the agent has said what it had to
      if (!b && quiet >= 60) { say('everything is at rest and nothing is bound: the build did not get there'); break; }
      await sleep(2000);
    }
    const tDone = Date.now();
    const cEnd = await conversation();
    const b = await bound();
    say(`the agent's last words: ${lastSaid(cEnd).slice(0, 1500)}`);
    say(`the agent's tools over the whole journey: ${tools(cEnd).join(' ')}`);
    check(!!b, `the assistant's mcp slot was bound by xbind once the tile agent's work declared the provide (${b ? `${b[0]} → ${b[1].mcp}` : 'not bound'})`);
    if (!b) J.deadEnd('the tile was not built into an MCP server the plan\'s waiting line could bind');
    if (trayOpen && answers === 0) say('the tray was opened and nothing was answered');
    const toDone = J.totals();

    let rolled = '';
    if (b) {
      J.section('the chat (the person\'s own try)');
      const assistant = b[0];
      await J.click('top bar: the workspace agent chip (closes the dock: it covers the tile)', () => page.locator('button.chip.agent').click());
      await J0.openFromSidebar(J, page, assistant);
      const pop = page.locator(`.pqpop[data-prompts-for="${assistant}"]`);
      await pop.waitFor({ timeout: 5000 }).catch(() => {});
      if (await pop.count()) await J.click('the popover: fold it (it covers the chat)', () => pop.locator('.pqx').click());
      const tf = await J0.tileFrame(page, assistant);
      await J.wait('the assistant\'s page is up', () => tf.waitForSelector('textarea#msg', { timeout: 600000 }));
      await J.field('the assistant: the message ("Roll a d20 for me.")', () => tf.locator('textarea#msg').fill('Roll a d20 for me.'));
      await J.click('the assistant: "Send"', () => tf.locator('button#send').click());
      rolled = await J.wait('the answer shows a roll made with the tool', async () => {
        for (let i = 0; i < 360; i++) {
          const body = await tf.locator('body').innerText();
          const after = body.split('Roll a d20 for me.').pop();
          if (/roll_dice/.test(after) && /\b([1-9]|1\d|20)\b/.test(after.replace(/d20/g, '')) && !/thinking|working/i.test(after.slice(-40))) { await sleep(3000); return (await tf.locator('body').innerText()).split('Roll a d20 for me.').pop().replace(/\s+/g, ' ').slice(0, 400); }
          await sleep(500);
        }
        return '';
      }).catch(() => '');
      await page.screenshot({ path: path.join(J0.SHOTS, 'real-roll.png') }).catch(() => {});
      check(!!rolled, `the assistant called the tool its coding agent wrote and answered with a roll (${rolled.slice(0, 200)})`);
      if (!rolled) J.deadEnd('the assistant did not answer with a roll from the tool');
    }

    const usage = (await call('GET', '/llm/usage')).data || {};
    const t = J.write();
    const tot = usage.totals || {};
    const summary = { interactions: t.interactions, toDone: toDone.interactions, byHand: t.hand, deadEnds: t.deadEnds, questions: asked, permissionAnswers: answers, extraRequests: extra,
      secPromptToPlan: Math.round((tApplied - tPrompt) / 1000), secApplyToBound: builtAt ? Math.round((builtAt - tApplied) / 1000) : null, secApplyToRest: Math.round((tDone - tApplied) / 1000), secTotal: Math.round((Date.now() - t0) / 1000),
      requests: tot.requests, inputTokens: tot.inputTokens, outputTokens: tot.outputTokens, cachedTokens: tot.cachedTokens || 0, rolled: !!rolled, bound: !!b };
    say(`summary: ${JSON.stringify(summary)}`);
    say(`usage by component: ${JSON.stringify((usage.rows || []).map((r) => ({ component: r.component, model: r.model, requests: r.requests, in: r.inputTokens, out: r.outputTokens })))}`);
    await keep();
    fs.writeFileSync(path.join(OUT, 'journey-real-notes.md'), redact(note.map((n) => `- ${n}`).join('\n')) + '\n');
    fs.writeFileSync(path.join(OUT, 'journey-real-summary.json'), JSON.stringify(summary, null, 1) + '\n');
  } catch (e) {
    say(`stopped: ${String(e.message || e).split('\n').slice(0, 3).join(' ')}`);
    await keep().catch(() => {});
    fs.writeFileSync(path.join(OUT, 'journey-real-notes.md'), redact(note.map((n) => `- ${n}`).join('\n')) + '\n');
    check(false, `the journey ran to its end (${String(e.message || e).split('\n')[0].slice(0, 200)})`);
  } finally {
    if (ctx) await ctx.close().catch(() => {});
    await X.stop();
  }
  done();
}

module.exports = { journeyReal };
