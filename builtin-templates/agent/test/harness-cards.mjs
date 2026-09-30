// harness-cards.mjs — a coding harness's transcript on the web
// (harness-cards.js, D-harness §8 U3) over the fixtures
// (test/harness-fixtures.mjs, STUB's §4 routes): a card per ACP family and
// what each shows opened, an edit's per-file patches unfolding, a command's
// output (ANSI stripped, its tail with "show all", the exit code), a call's
// status while it runs, fails or waits, a Claude Task's steps folding inside
// it, the orphan mark, the 📋 plan pin and the usage badge following the
// `harness` stream event — and the patch highlighted by /vendor/bx-code.js
// when the page can import it (plain without).
//
//   node test/harness-cards.mjs        (needs playwright + a chromium build)
import { readFileSync, existsSync } from 'node:fs';
import { join, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';
import { ORIGIN, STUB, serveTile, launch, checker } from './backend.mjs';
import { harnessSeed } from './harness-fixtures.mjs';

const { ok, done } = checker();
const browser = await launch();
const errors = [];

async function open(seed, extra) {
  const ctx = await browser.newContext();
  await serveTile(ctx);
  if (extra) await extra(ctx);
  await ctx.addInitScript(STUB, seed);
  const page = await ctx.newPage();
  page.on('pageerror', (e) => errors.push(e.message));
  // a 404 is said by the response (the highlighter's, where it isn't served, is the fallback's cue)
  page.on('console', (m) => { if (m.type() === 'error' && !/^Failed to load resource/.test(m.text())) errors.push(m.text()); });
  page.on('response', (r) => { if (r.status() >= 400 && !r.url().endsWith('/vendor/bx-code.js')) errors.push(`${r.status()} ${r.url()}`); });
  return page;
}
const seed = harnessSeed();
const page = await open(seed);
const card = (id) => `#timeline [data-k="ch1:${id}"]`;
const text = (sel) => page.textContent(sel).then((t) => (t || '').trim());
const has = (sel) => page.$(sel).then(Boolean);
// click, then wait for the page to show it (the chat paints on the next frame)
const flip = async (target, sel) => {
  const was = await has(sel);
  await page.click(target);
  await page.waitForFunction(([q, w]) => !!document.querySelector(q) !== w, [sel, was]);
};
const toggle = (id) => flip(`${card(id)} > .tch`, `${card(id)}.on`);

await page.goto(`${ORIGIN}/#c=21`);
await page.waitForSelector('.hcard[data-kind="execute"]');

// --- every family --------------------------------------------------------------------------------
const kinds = await page.$$eval('#timeline > .hcard', (els) => els.map((e) => e.dataset.kind));
ok('a card per call, every ACP kind', ['read', 'search', 'think', 'edit', 'delete', 'move', 'fetch', 'execute', 'switch_mode', 'other']
  .every((k) => kinds.includes(k)) && kinds.length === 11, kinds.join());
ok('done calls carry no status chip, only what they came to', (await page.$$('#timeline > .hcard > .tch .st')).length === 0
  && (await text(`${card('toolu_10')} .oc`)) === 'exit 1');

// execute: the command, the output ANSI-stripped, the exit code
await toggle('toolu_10');
ok('execute: the command, copyable', (await text(`${card('toolu_10')} .hcmd pre`)) === '$ go test ./... -run TestClientRetry -count=20'
  && await has(`${card('toolu_10')} .hcmd button`));
const out = await text(`${card('toolu_10')} .hout pre`);
ok('execute: its output, ANSI stripped', out.startsWith('--- FAIL: TestClientRetry') && !out.includes('\x1b') && !out.includes('[31m'), JSON.stringify(out));
ok('execute: exit 1 as bad news', (await text(`${card('toolu_10')} .hexit.bad`)) === 'exit 1');
ok('execute: the call and the adapter\'s tool', (await text(`${card('toolu_10')} .tname`)).startsWith('acp:execute · Bash'));

// edit: a row per file, +/−, collapsed until asked
await toggle('toolu_06');
ok('edit: its file, +5 −1', (await text(`${card('toolu_06')} .hfile .hpath`)) === '/work/api/client.go'
  && (await text(`${card('toolu_06')} .hadd`)) === '+5' && (await text(`${card('toolu_06')} .hdel`)) === '−1');
ok('edit: the patch folded', !(await has(`${card('toolu_06')} .hdiff`)));
await flip(`${card('toolu_06')} .hfh`, `${card('toolu_06')} .hdiff`);
ok('edit: the patch unfolds, drawn plain without the highlighter', await has(`${card('toolu_06')} .hdiff.plain`)
  && (await page.$$(`${card('toolu_06')} .hdiff .d`)).length === 5 && (await page.$$(`${card('toolu_06')} .hdiff .a`)).length === 1
  && (await page.$$(`${card('toolu_06')} .hdiff .h`)).length === 1, await page.innerHTML(`${card('toolu_06')} .hdiff`));
await flip(`${card('toolu_06')} .hfh`, `${card('toolu_06')} .hdiff`);
ok('edit: …and folds again', !(await has(`${card('toolu_06')} .hdiff`)));

// delete, read, search, move, fetch, switch_mode, other
await toggle('toolu_07');
await flip(`${card('toolu_07')} .hfh`, `${card('toolu_07')} .hnopatch`);
ok('delete: the file, deleted, no patch to show', (await text(`${card('toolu_07')} .hst`)) === 'deleted'
  && (await text(`${card('toolu_07')} .hnopatch`)) === 'the file was deleted');
await toggle('toolu_01');
ok('read: where, and what it read', (await text(`${card('toolu_01')} .hplaces`)) === '/work/api/client_test.go:1'
  && (await text(`${card('toolu_01')} .res pre`)).includes('func TestClientRetry') && (await text(`${card('toolu_01')} .oc`)) === '5 lines');
await toggle('toolu_02');
ok('search: its matches', (await page.$$(`${card('toolu_02')} .hplaces span`)).length === 2 && (await text(`${card('toolu_02')} .oc`)) === '2 matches');
await toggle('toolu_08');
ok('move: both paths', (await text(`${card('toolu_08')} .hplaces`)).includes('/work/api/internal/util.go'));
await toggle('toolu_09');
ok('fetch: the address, a link that opens apart', (await page.getAttribute(`${card('toolu_09')} .hplaces a`, 'rel')) === 'noopener noreferrer'
  && (await page.getAttribute(`${card('toolu_09')} .hplaces a`, 'href')) === 'https://pkg.go.dev/net/http#Client.Do');
await toggle('toolu_13');
ok('other: its raw input behind a toggle', !(await has(`${card('toolu_13')} .hpre`)));

// the Task: its steps inside it, folding with it
ok('a Task says how many steps it took', (await text(`${card('toolu_03')} > .tch .oc.steps`)) === '2 steps');
ok('…folded, its steps are not drawn', !(await has('#timeline [data-k="ch1:toolu_04"]')));
await toggle('toolu_03');
await page.waitForSelector(`${card('toolu_03')} .hkids [data-k="ch1:toolu_04"]`);
ok('…open: its steps and its text inside it, as cards', await has(`${card('toolu_03')} .hkids .hcard[data-k="ch1:toolu_05"]`)
  && await has(`${card('toolu_03')} .hkids .msg.assistant`) && (await text(`${card('toolu_03')} .task`)).includes('List the callers of Client.Do'));
ok('…and its answer', (await text(`${card('toolu_03')} .answer pre`)) === 'Do is called from sync.go and fetch.go.');
await flip(`${card('toolu_03')} .hkids [data-k="ch1:toolu_05"] > .tch`, `${card('toolu_03')} [data-k="ch1:toolu_05"].on`);
ok('a nested step opens inside it', await has(`${card('toolu_03')} [data-k="ch1:toolu_05"] .res pre`));
await toggle('toolu_03');
ok('…and folds with it', !(await has('#timeline [data-k="ch1:toolu_04"]')) && !(await has('#timeline [data-k="ch1:toolu_05"]')));

// --- live: running, streaming, failing; a long output; an orphan -------------------------------------
const push = (ev) => page.evaluate((e) => window.__push(e), ev);
const now = Math.floor(Date.now() / 1000);
const asst = (id, callId, args, extra = {}) => ({ type: 'message', run: 21, root: 21, data: { id, runId: 21, seq: id, role: 'assistant', content: '', created: now,
  toolCalls: [{ id: callId, type: 'function', function: { name: 'acp:execute', arguments: JSON.stringify(args) } }], ...extra } });
const tool = (id, callId, content, acp) => ({ type: 'message', run: 21, root: 21, data: { id, runId: 21, seq: id, role: 'tool', toolCallId: callId, name: 'acp:execute', content, created: now, acp } });
await push(asst(40, 'h1:toolu_20', { command: 'make test', summary: 'Run make test' }));
await push(tool(41, 'h1:toolu_20', '(running…)', { kind: 'execute', title: 'make test', status: 'in_progress', output: 'ok  pkg/a\n' }));
await page.waitForSelector(card('toolu_20'));
ok('running: a spinner and its chip', await has(`${card('toolu_20')} .spin`) && (await text(`${card('toolu_20')} .st.run`)) === 'running');
await toggle('toolu_20');
ok('running: the output so far', (await text(`${card('toolu_20')} .hout pre`)) === 'ok  pkg/a');
await push(tool(41, 'h1:toolu_20', '(running…)', { kind: 'execute', title: 'make test', status: 'in_progress', output: 'ok  pkg/a\nok  pkg/b\n' }));
await page.waitForFunction(() => document.querySelector('[data-k="ch1:toolu_20"] .hout pre')?.textContent.includes('pkg/b'));
ok('running: the output streams into the open card', true);
await push(tool(41, 'h1:toolu_20', 'error: exit status 2', { kind: 'execute', title: 'make test', status: 'failed', exitCode: 2, output: 'ok  pkg/a\nFAIL pkg/c\n' }));
await page.waitForSelector(`${card('toolu_20')}.error`);
ok('failed: its chip, the exit code, no spinner', (await text(`${card('toolu_20')} .st.bad`)) === 'failed' && (await text(`${card('toolu_20')} .hexit.bad`)) === 'exit 2'
  && !(await has(`${card('toolu_20')} .spin`)));
await push(asst(42, 'h1:toolu_21', { command: 'cat big.log', summary: 'Show the log' }));
await push(tool(43, 'h1:toolu_21', 'error: exit status 1', { kind: 'execute', status: 'failed', exitCode: 1, output: 'x'.repeat(25000) + 'THE END', outputTruncated: 4096 }));
await page.waitForSelector(`${card('toolu_21')} .hout`);
ok('a failed call opens by itself', await has(`${card('toolu_21')}.on`));
const tail = await text(`${card('toolu_21')} .hout pre`);
ok('a long output: its last 20 000 characters, and what was not kept', tail.length === 20001 && tail.endsWith('THE END')
  && (await text(`${card('toolu_21')} .hout .muted`)).includes('4 096 bytes'), tail.length);
await page.click(`${card('toolu_21')} .hout .lnk`);
await page.waitForFunction(() => document.querySelector('[data-k="ch1:toolu_21"] .hout pre').textContent.length > 25000);
ok('…"show all" shows all of it', (await text(`${card('toolu_21')} .hout pre`)).length === 25007);
await push({ type: 'message', run: 21, root: 21, data: { id: 44, runId: 21, seq: 44, role: 'assistant', content: '', created: now, acp: { parent: 'h1:toolu_99' },
  toolCalls: [{ id: 'h1:toolu_22', type: 'function', function: { name: 'acp:read', arguments: '{"file_path":"/work/api/x.go"}' } }] } });
await push({ type: 'message', run: 21, root: 21, data: { id: 45, runId: 21, seq: 45, role: 'tool', toolCallId: 'h1:toolu_22', name: 'acp:read', content: 'package api', created: now,
  acp: { kind: 'read', status: 'completed', parent: 'h1:toolu_99' } } });
await page.waitForSelector(card('toolu_22'));
ok('an orphan (its subagent\'s call paged out) is flat, marked ↳', (await text(`${card('toolu_22')} .hl`)).startsWith('↳ Read /work/api/x.go'));

// --- the top bar: usage, changes, the plan pin ----------------------------------------------------------
ok('usage: ctx and the cost', (await text('#top .husage')) === 'ctx 26% · $0.41'
  && (await page.getAttribute('#top .husage', 'title')).includes('52 000 of 200 000 tokens'));
ok('what it changed', (await text('#top .hcounts')) === '13 tool calls · 4 files +5 −15');
ok('the plan pin, folded: its progress and where it is', (await text('#top .planpin .tasktoggle')).startsWith('📋 Plan · 3/3')
  && (await text('#top .planpin .taskline')) === 'all done');
await flip('#top .planpin .tasktoggle', '#top .planpin.open');
ok('…unfolded: its entries with their status', (await page.$$('#top .planpin .planlist li.completed')).length === 3);
const h21 = seed.runs.find((r) => r.id === 21).harness;
await push({ type: 'harness', run: 21, root: 21, data: { ...h21, state: 'working', usage: { used: 184000, size: 200000 },
  plan: { entries: [{ content: 'Find why the test fails', status: 'completed' }, { content: 'Add jitter', status: 'completed' },
    { content: 'Run the tests 50 times', status: 'in_progress', priority: 'high' }, { content: 'Write it up', status: 'pending' }] } } });
await page.waitForFunction(() => document.querySelector('#top .planpin .tasktoggle')?.textContent.includes('2/4'));
ok('the harness event: the plan follows', (await page.$$('#top .planpin .planlist li')).length === 4
  && (await text('#top .planpin li.in_progress .pt')) === 'Run the tests 50 times' && (await text('#top .planpin li.in_progress .pg')) === '◐'
  && await has('#top .planpin li.in_progress .pp'));
ok('…and the usage (a full context warns)', (await text('#top .husage')) === 'ctx 92%' && await has('#top .husage.bad'));
await flip('#top .planpin .tasktoggle', '#top .planpin.open');
ok('…folded: now: the entry in progress', (await text('#top .planpin .taskline')) === 'now: Run the tests 50 times');

// a park: its call waits for you
await page.evaluate(() => { location.hash = '#c=22'; });
await page.waitForSelector('[data-k="ch2:toolu_02"]');
ok('a parked call needs your approval', (await text('[data-k="ch2:toolu_02"] .st.warn')) === 'needs approval');
ok('no plan, no usage: nothing in the top bar', !(await has('#top .planpin')) && !(await has('#top .husage')));
// the built-in agent's conversation: none of it
await page.evaluate(() => { location.hash = '#c=25'; });
await page.waitForSelector('.acard');
ok('the built-in agent\'s conversation: no harness chips', !(await has('#top .hcounts')) && !(await has('.hcard')));

// --- the highlighter: /vendor/bx-code.js's diffHTML when the page can import it --------------------------
const web = join(dirname(fileURLToPath(import.meta.url)), '..', '..', '..', 'web');
// the page's import map names lit in a workspace (xbin.json importMap); here the module says it itself.
// xbind's /vendor/ modules the highlighter imports (the kit, lit and marked stay the STUB's).
const lit = (src) => src.replace(/from 'lit'/g, "from '/vendor/lit-all.min.js'");
const STUBBED = ['lit-all.min.js', 'marked.esm.js', 'bx-kit.js', 'scroll-window.js'];
const served = (ctx) => ctx.route('**/vendor/*.js', (r) => {
  const f = new URL(r.request().url()).pathname.replace(/^\/vendor\//, '');
  if (STUBBED.includes(f)) return r.fallback();
  const p = [join(web, f), join(web, 'vendor', f)].find((x) => existsSync(x));
  return p ? r.fulfill({ contentType: 'text/javascript', body: lit(readFileSync(p, 'utf8')) }) : r.fulfill({ status: 404, body: '' });
});
const page2 = await open(harnessSeed(), served);
await page2.goto(`${ORIGIN}/#c=21`);
await page2.click('#timeline [data-k="ch1:toolu_06"] > .tch');
await page2.click('#timeline [data-k="ch1:toolu_06"] .hfh');
await page2.waitForSelector('[data-k="ch1:toolu_06"] .hdiff.hl', { timeout: 5000 }).catch(() => {});
ok('with xbind\'s code viewer: the patch highlighted', await page2.$('[data-k="ch1:toolu_06"] .hdiff.hl .d')
  && (await page2.$$('[data-k="ch1:toolu_06"] .hdiff.hl [class^="hljs-"]')).length > 0);

ok('no page errors', errors.length === 0, errors.join(' | '));
await browser.close();
done('harness-cards');
