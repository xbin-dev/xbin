// hack/ui-harness/passes/richcopy.js — copying from a tile with its
// formatting (D199, #34) and the agent template's code colouring (#26),
// through a real xbind, in the shell:
//   - a right-click on a selection in a tile leads with Copy, and Copy now
//     puts the selection's HTML on the clipboard beside its text — rebuilt
//     from an allowlist (a list and a table survive; a script, an image, an
//     onclick and a javascript: link don't);
//   - xbin.copy({text, html}) from a tile's click: the shell's frame writes
//     both flavours, sanitized the same way, and answers true;
//   - the agent template: an answer's fenced code is highlighted by xbind's
//     /vendor/highlight.min.js, and its Copy puts the answer's markdown and
//     its rendering (a numbered list, a table) on the clipboard.
// Screenshots: the coloured answer, in the run's theme.
const { URL, sleep, login, closeCtx, settle, sh, waitFor, waitSel, openShell, usePersonalScreen, openTile, foldPrompts, closeTile, tileFrame, shot, checker, THEME, noGocryptfs } = require('../lib');

// the clipboard as the shell page reads it: {text, html}
const clipboard = (page) => page.evaluate(async () => {
  const out = { text: '', html: '' };
  for (const it of await navigator.clipboard.read()) {
    if (it.types.includes('text/plain')) out.text = await (await it.getType('text/plain')).text();
    if (it.types.includes('text/html')) out.html = await (await it.getType('text/html')).text();
  }
  return out;
});
const clearClip = (page) => page.evaluate(() => navigator.clipboard.writeText(''));

async function richCopy(browser) {
  const { check, skip, done } = checker(`rich-copy-${THEME}`);
  const { ctx, page } = await login(browser, 'admin', 'admin', { viewport: { width: 1400, height: 900 } });
  await ctx.grantPermissions(['clipboard-read', 'clipboard-write'], { origin: URL });
  await openShell(page);
  await usePersonalScreen(page);
  await sh(page, (t) => t.addScreen()); // a screen of its own: tiles land at the top left, whatever an earlier run left
  await settle(page);

  // (a) a right-click Copy carries the selection's HTML, sanitized
  await openTile(page, 'apps/focusy');
  const frame = await tileFrame(page, 'apps/focusy');
  await frame.waitForSelector('p', { timeout: 10000 });
  await frame.evaluate(() => {
    const box = document.createElement('div');
    box.id = 'rich';
    box.innerHTML = '<ol start="3"><li><b>three</b></li><li onclick="alert(1)">four</li></ol>' +
      '<table><tr><th>a</th><th>b</th></tr><tr><td colspan="2">1</td></tr></table>' +
      '<p class="x" style="color:red">see <a href="javascript:alert(1)">this</a> and <a href="https://example.com/">that</a>' +
      '<img src="https://example.com/x.png"><script>window.__ran = 1</script></p>';
    document.body.appendChild(box);
    const r = document.createRange(); r.selectNodeContents(box);
    const s = getSelection(); s.removeAllRanges(); s.addRange(r);
  });
  await page.bringToFront();
  // the seed's apps/focusy has an interface to bind: its decisions popover
  // (D195) sits over the top of the window — fold it to the title bar's badge
  await foldPrompts(page, 'apps/focusy');
  await clearClip(page);
  const rb = await frame.locator('#rich li').first().boundingBox();
  await page.mouse.click(rb.x + 8, rb.y + rb.height / 2, { button: 'right' });
  await waitSel(page, 'bx-menu .it');
  await page.locator('bx-menu .it', { hasText: 'Copy' }).first().click();
  await waitFor(page, (t) => !t.menuOpen, null, { label: 'the menu closed on Copy' });
  await sleep(200);
  let c = await clipboard(page);
  check(c.text.includes('three') && c.text.includes('four'), `the text is the selection's (${JSON.stringify(c.text.slice(0, 80))})`);
  check(/<ol start="3"><li><strong>three<\/strong><\/li><li>four<\/li><\/ol>/.test(c.html), `the list survives, its start kept, b as strong, no onclick (${c.html.slice(0, 160)})`);
  check(/<table>.*<td colspan="2">1<\/td>/.test(c.html), 'the table survives');
  check(/<a href="https:\/\/example.com\/">that<\/a>/.test(c.html) && !/javascript:/.test(c.html) && c.html.includes('this'), 'an https link stays; a javascript: link is its words only');
  check(!/<img|<script|class=|style=|onclick/.test(c.html), `no image, script, class, style or handler (${c.html})`);

  // (b) xbin.copy from the tile's own click
  await frame.evaluate(() => {
    const b = document.createElement('button');
    b.id = 'cp'; b.textContent = 'copy it';
    b.onclick = async () => { window.__r = await xbin.copy({ text: 'plain words', html: '<ul><li>one</li></ul><img src=x onerror="window.__ran=1"><a href="javascript:1">j</a>' }); };
    document.body.appendChild(b);
  });
  await clearClip(page);
  await frame.locator('#cp').click();
  await frame.waitForFunction(() => window.__r !== undefined, null, { timeout: 5000 }).catch(() => {});
  check(await frame.evaluate(() => window.__r === true), 'xbin.copy resolves true');
  c = await clipboard(page);
  check(c.text === 'plain words', `its text (${JSON.stringify(c.text)})`);
  check(c.html.includes('<ul><li>one</li></ul>') && !/<img|onerror|javascript:/.test(c.html), `its HTML, sanitized (${c.html})`);
  check(!(await frame.evaluate(() => window.__ran)), 'nothing the tile sent ran');
  await closeTile(page, 'apps/focusy');

  // (c) the agent template: a coloured fence, an answer's Copy — on the fake
  // model (fakeopenai) in the internal class, as the agentTemplate pass sets up
  if (noGocryptfs()) { skip(`apps/agent is held: ${noGocryptfs()}`); await closeCtx(ctx, page); return done(); }
  await openTile(page, 'apps/agent');
  await foldPrompts(page, 'apps/agent'); // its interface to bind (D195) over the class menu
  const ag = await tileFrame(page, 'apps/agent', { timeout: 30000 });
  await ag.waitForSelector('#msg', { timeout: 30000 });
  await ag.evaluate(async () => {
    const base = `/api/${location.pathname.split('/').slice(2, -1).join('/')}`;
    const c = await (await xbin.fetch(`${base}/config`)).json();
    c.model = 'fake/fake-chat';
    await xbin.fetch(`${base}/config`, { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(c) });
  });
  await ag.waitForSelector('#tset', { timeout: 30000 });
  if (!(await ag.$('.clsmenu'))) await ag.click('#tset');
  await ag.click('.clsmenu .mi[data-class="internal"]');
  await ag.waitForFunction(() => !document.querySelector('.clsmenu'));
  await ag.fill('#msg', 'show markdown please');
  await ag.press('#msg', 'Enter');
  await ag.waitForFunction(() => [...document.querySelectorAll('#timeline .msg.assistant:not(.live)')].some((e) => e.textContent.includes('second step')), null, { timeout: 30000 });
  await ag.waitForSelector('#timeline .msg.assistant pre code.hljs .hljs-keyword', { timeout: 10000 }).catch(() => {});
  check(!!(await ag.$('#timeline .msg.assistant pre code.hljs .hljs-keyword')), 'the answer\'s fenced code is highlighted');
  await settle(page);
  await shot(page, `richcopy-agent-${THEME}`, { fullPage: false });
  await clearClip(page);
  const msg = ag.locator('#timeline .msg.assistant:not(.live)', { hasText: 'second step' }).last();
  await msg.hover();
  await msg.locator('.acts .act').click();
  await ag.waitForFunction(() => document.querySelector('#timeline .msg.assistant .acts .act.done'), null, { timeout: 5000 }).catch(() => {});
  c = await clipboard(page);
  check(c.text.startsWith('The plan:') && c.text.includes('1. **first** step'), `Copy: the markdown as text (${JSON.stringify(c.text.slice(0, 60))})`);
  check(/<ol>/.test(c.html) && /<table>/.test(c.html) && /<pre><code>func main/.test(c.html), `…and its rendering as HTML (${c.html.slice(0, 200)})`);
  await closeTile(page, 'apps/agent');
  await closeCtx(ctx, page);
  done();
}

module.exports = { richCopy };
