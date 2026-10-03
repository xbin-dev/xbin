// harness-start.mjs — starting a conversation with a coding agent in the web
// view (D147 §8 U2) over the fixtures (test/harness-fixtures.mjs,
// STUB's §4 routes): "Who answers" (#apick) lists the built-in agent and each
// coding agent with its monogram, the unavailable ones disabled with the
// reason; picking one hides the class and model pickers, narrows the sandbox
// picker to the sandboxes it fits and preselects one (remembered per
// harness); the ask carries {harness, class, sandbox} and no mode; the
// conversation's chip in the top bar and the rows' kind chips; the setup
// card (not signed in there; no sandbox fits → Create, prefilled); the
// new-chat dialog's #n-agent and #n-sandbox.
//
//   node test/harness-start.mjs        (needs playwright + a chromium build)
import { ORIGIN, STUB, serveTile, launch, checker } from './backend.mjs';
import { harnessSeed, API_DEV } from './harness-fixtures.mjs';

const { ok, done } = checker();
const browser = await launch();
const ctx = await browser.newContext({ viewport: { width: 1100, height: 760 } });
await serveTile(ctx);
await ctx.addInitScript(STUB, harnessSeed());
await ctx.addInitScript(() => { window.__alerts = []; window.alert = (m) => window.__alerts.push(String(m)); });
const page = await ctx.newPage();
const errors = [];
page.on('pageerror', (e) => errors.push(e.message));
page.on('console', (m) => { if (m.type() === 'error') errors.push(m.text()); });
const calls = (method, re) => page.evaluate(([m, r]) => window.__calls.filter((c) => c.method === m && new RegExp(r).test(c.url))
  .map((c) => ({ url: c.url, body: c.body ? JSON.parse(c.body) : null })), [method, re.source]);
const text = (sel) => page.textContent(sel).then((t) => (t || '').replace(/\s+/g, ' ').trim());
const shown = (sel) => page.$eval(sel, (e) => !e.hidden && getComputedStyle(e).display !== 'none').catch(() => false);

await page.goto(`${ORIGIN}/`);
await page.waitForSelector('#apick:not([hidden]) #abtn');
ok('who answers: the built-in agent until you pick', (await text('#abtn')).includes('Agent'));
ok('…the class picker beside it', await shown('#cpick'));
await page.click('#abtn');
await page.waitForSelector('#apick .clsmenu');
const rows = await page.$$eval('#apick .mi', (els) => els.map((e) => ({ id: e.dataset.agent, off: e.classList.contains('off'), text: e.textContent.replace(/\s+/g, ' ').trim() })));
ok('the rows: the built-in agent, then every coding agent', rows.map((r) => r.id).join() === 'agent,claude,codex,gemini,opencode', JSON.stringify(rows));
ok('…each with its monogram', ['CC', 'CX', 'GM', 'OC'].every((m, i) => rows[i + 1].text.startsWith(m)), rows.map((r) => r.text).join(' | '));
ok('…the available ones in the class they resolve to', rows[1].text.includes('in ▣ Coding') && !rows[1].off);
ok('…one that isn\'t available disabled, with why', rows[3].off && rows[3].text.includes('needs internet access') && rows[4].off && rows[4].text.includes("no bound sandbox manager's image has it"),
  JSON.stringify(rows.slice(3)));
await page.click('#apick .mi[data-agent="gemini"]', { force: true });
ok('picking a disabled one does nothing', !!(await page.$('#apick .clsmenu')) && !(await page.evaluate(() => window.__prefs.agent)));
await page.keyboard.press('Escape');
ok('Escape closes the menu', !(await page.$('#apick .clsmenu')));

// pick Claude Code: the class resolves, the sandbox picker narrows and preselects
await page.click('#abtn');
await page.click('#apick .mi[data-agent="claude"]');
await page.waitForFunction(() => document.getElementById('abtn')?.textContent.includes('Claude Code'));
ok('remembered as your pick (prefs/agent)', (await page.evaluate(() => window.__prefs.agent)) === 'claude');
ok('the class picker hides (resolved)', !(await shown('#cpick')));
ok('…and the built-in model\'s', !(await shown('#msel')));
await page.waitForFunction((ref) => document.getElementById('ssel')?.value === ref, API_DEV);
ok('the sandbox picker shows, api-dev preselected', await shown('#ssel'));
const opts = await page.$$eval('#ssel option', (els) => els.map((e) => ({ v: e.value, off: e.disabled, t: e.title })));
const opt = (name) => opts.find((o) => o.v.endsWith(name)) || {};
ok('…a sandbox with no egress disabled, with why', opt('sb-9c1d').off && opt('sb-9c1d').t.includes("egress is none"), JSON.stringify(opt('sb-9c1d')));
ok('…one whose image lacks it disabled, with why', opt('sb-2b8e').off && opt('sb-2b8e').t.includes("image doesn't have Claude Code"), JSON.stringify(opt('sb-2b8e')));
await page.waitForFunction(() => window.__prefs['harness-sandbox']);
ok('the sandbox is remembered for it (prefs/harness-sandbox)', (await page.evaluate(() => window.__prefs['harness-sandbox'].claude)) === API_DEV);
ok('the composer says who answers, and where', (await page.getAttribute('#msg', 'placeholder')) === 'ask Claude Code — it works in api-dev…');
ok('signed in there: no setup card', !(await page.$('#hsetup')));

// the ask
await page.fill('#msg', 'fix the flaky test');
await page.click('#send');
await page.waitForSelector('#hchip');
const ask = (await calls('POST', /\/ask$/)).pop().body;
ok('the ask: the harness, its class, its sandbox — no mode, no model', ask.harness && ask.harness.provider === 'claude' && !('mode' in ask.harness)
  && ask.class === 'coding' && ask.sandbox && ask.sandbox.ref === API_DEV && !('model' in ask) && ask.text === 'fix the flaky test', JSON.stringify(ask));
ok('the top bar: which agent, its state, its shared sandbox (the people glyph, D184)', /CC\s*Claude Code · starting$/.test(await text('#hchip'))
  && !!(await page.$('#hchip bx-icon[name="people"]')), await text('#hchip'));
ok('…whose title says who can read it', (await page.getAttribute('#hchip', 'title')).includes('api-dev is shared — the people who may use it can read what Claude Code does here'));
ok('no "Who answers" in a conversation', !(await shown('#apick')));

// the rows' kind chips
const kinds = await page.$$eval('#runs .run', (els) => Object.fromEntries(els.map((e) => [e.dataset.id, e.querySelector('.kind')?.textContent || ''])));
ok('the rows a coding agent answers carry its monogram', kinds[21] === 'CC' && kinds[24] === 'CX' && kinds[25] === '', JSON.stringify(kinds));

// Codex: not signed in on api-dev → the setup card says so
await page.click('#home');
await page.waitForSelector('#abtn');
await page.click('#abtn');
await page.click('#apick .mi[data-agent="codex"]');
await page.waitForSelector('#hsetup[data-kind="signin"]');
ok('not signed in there: the card says the first message asks', (await text('#hsetup')).includes("Codex isn't signed in on api-dev. Your first message asks you to sign in to Codex there")
  && (await text('#hsetup')).includes('everyone who may use it acts as you with Codex'), await text('#hsetup'));

// no sandbox fits: Create, prefilled — it becomes the next chat's
await page.click('#abtn');
await page.click('#apick .mi[data-agent="claude"]');
await page.evaluate(() => { window.__sbx.sandboxes = window.__sbx.sandboxes.filter((s) => s.name !== 'api-dev'); });
await page.evaluate(async () => { const { ctx } = await import('/web-ext.js'); await ctx.app.sbx.load(true); });
await page.waitForSelector('#hsetup[data-kind="create"]');
ok('no sandbox fits: the setup card', (await text('#hsetup')).startsWith('Claude Code needs a coding sandbox with internet access'));
ok('…offers to create one', (await text('#hsetup-create')) === 'Create claude-dev');
await page.click('#hsetup-create');
await page.waitForSelector('#sbx-form');
const form = await page.evaluate(() => ({ name: document.getElementById('sbxf-name').value, egress: document.getElementById('sbxf-egress').value,
  image: document.getElementById('sbxf-image')?.value }));
ok('…the create form, filled in for it', form.name === 'claude-dev' && form.egress === 'internet' && form.image === 'base', JSON.stringify(form));
await page.click('#sbxf-create');
await page.waitForSelector('#sbx-msg');
await page.click('#sbx-close');
await page.waitForFunction(() => document.getElementById('ssel')?.value === 'apps/coding-sandbox|sb-claude-dev');
ok('…and it is the next chat\'s sandbox', !(await page.$('#hsetup[data-kind="create"]')));
await page.waitForSelector('#hsetup[data-kind="signin"]');
ok('a running sandbox it wasn\'t looked for in is probed', (await calls('GET', /harnesses\?probe=/)).some((c) => c.url.includes(encodeURIComponent('apps/coding-sandbox|sb-claude-dev'))));
ok('…and what the probe learned is said: not signed in there yet', (await text('#hsetup')).startsWith("Claude Code isn't signed in on claude-dev."), await text('#hsetup'));

// the new-chat dialog
await page.click('#newopts');
await page.waitForSelector('#n-agent');
ok('new chat: who answers, your pick first', (await page.$eval('#n-agent', (e) => e.value)) === 'claude');
ok('…the class and instructions are the built-in agent\'s: hidden', !(await page.$eval('#n-class', (e) => e.closest('.field').offsetParent))
  && !(await page.$eval('#n-system', (e) => e.closest('.field').offsetParent)));
ok('…its sandbox', (await page.$eval('#n-sandbox', (e) => e.value)) === 'apps/coding-sandbox|sb-claude-dev');
await page.selectOption('#n-agent', 'agent');
ok('the built-in agent: the class again, no sandbox field', !!(await page.$eval('#n-class', (e) => e.closest('.field').offsetParent)) && !(await page.$('#n-sandbox')));
await page.fill('#n-system', 'be brief');
await page.selectOption('#n-agent', 'codex');
await page.fill('#n-goal', 'port the CLI');
await page.click('#n-create');
await page.waitForFunction(() => window.__calls.filter((c) => c.method === 'POST' && c.url.endsWith('/ask')).length === 2);
const ask2 = (await calls('POST', /\/ask$/)).pop().body;
ok('…its ask: codex, its class and sandbox, no instructions', ask2.harness.provider === 'codex' && ask2.class === 'coding' && !('system' in ask2)
  && ask2.sandbox.ref === 'apps/coding-sandbox|sb-claude-dev' && ask2.text === 'port the CLI', JSON.stringify(ask2));

// the built-in agent again: the composer as before
await page.click('#home');
await page.click('#abtn');
await page.click('#apick .mi[data-agent="agent"]');
await page.waitForFunction(() => document.getElementById('abtn')?.textContent.includes('Agent'));
ok('the built-in agent: the class picker is back', await shown('#cpick'));
ok('…the placeholder too', (await page.getAttribute('#msg', 'placeholder')) === 'ask anything…');

// a narrow tile: the composer stays inside it
await page.setViewportSize({ width: 480, height: 700 });
await page.click('#abtn');
await page.click('#apick .mi[data-agent="claude"]');
const over = await page.evaluate(() => ({ doc: document.documentElement.scrollWidth - innerWidth, send: document.getElementById('send').getBoundingClientRect().right - innerWidth }));
ok('at 480 px: no sideways scroll, Send on screen', over.doc <= 0 && over.send <= 0, JSON.stringify(over));

ok('no page errors', errors.length === 0, errors.join(' | '));
ok('nothing alerted', !(await page.evaluate(() => window.__alerts.length)), await page.evaluate(() => window.__alerts.join(' | ')));
await browser.close();
done('harness-start');
