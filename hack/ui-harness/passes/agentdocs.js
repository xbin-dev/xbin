// hack/ui-harness/passes/agentdocs.js — the agent's document tools (D201),
// end to end, with no coding sandbox: a PDF attached in the chat; the
// scripted model (hack/fakeopenai "doc tour") reads it (doc_read), looks at
// its first page (doc_view — the picture reaches the model), writes a
// workbook (sheet_write: values, formulas, a style, a chart), a deck
// (slides_write) and a Word document (doc_write). The chat shows each
// written file as a line that opens its preview and downloads the file; the
// preview pane draws the workbook as a table; the download is the file
// itself (a zip whose first bytes say so). Screenshots in the harness theme.
const { login, sleep, shot, checker, noGocryptfs, THEME } = require('../lib');

const URL = process.env.URL || 'http://127.0.0.1:8697';
const until = (page, fn, arg, timeout = 20000) => page.waitForFunction(fn, arg ?? null, { timeout, polling: 50 });
const text = (page, sel) => page.$$eval(sel, (els) => els.map((e) => e.textContent.trim().replace(/\s+/g, ' ')));
const answered = (page, t, timeout) => until(page, ([t]) => [...document.querySelectorAll('#timeline .msg.assistant:not(.live)')]
  .some((e) => e.textContent.includes(t)), [t], timeout);

// tinyPDF: a minimal PDF, a page per text (Helvetica), offsets right.
function tinyPDF(pages) {
  let out = '%PDF-1.4\n';
  const offs = [];
  const obj = (body) => { offs.push(Buffer.byteLength(out)); out += `${offs.length} 0 obj\n${body}\nendobj\n`; };
  obj('<< /Type /Catalog /Pages 2 0 R >>');
  obj(`<< /Type /Pages /Kids [${pages.map((_, i) => `${4 + 2 * i} 0 R`).join(' ')}] /Count ${pages.length} >>`);
  obj('<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>');
  pages.forEach((t, i) => {
    obj(`<< /Type /Page /Parent 2 0 R /MediaBox [0 0 300 200] /Contents ${5 + 2 * i} 0 R /Resources << /Font << /F1 3 0 R >> >> >>`);
    const s = `BT /F1 18 Tf 30 100 Td (${t}) Tj ET`;
    obj(`<< /Length ${s.length} >>\nstream\n${s}\nendstream`);
  });
  const xref = Buffer.byteLength(out);
  out += `xref\n0 ${offs.length + 1}\n0000000000 65535 f \n${offs.map((o) => `${String(o).padStart(10, '0')} 00000 n \n`).join('')}`;
  out += `trailer\n<< /Size ${offs.length + 1} /Root 1 0 R >>\nstartxref\n${xref}\n%%EOF\n`;
  return Buffer.from(out, 'latin1');
}

async function agentDocs(browser) {
  const { check, skip, done } = checker(`agent-docs-${THEME}`);
  if (noGocryptfs()) { skip(`apps/agent is held: ${noGocryptfs()}`); return done(); }
  const { ctx, page } = await login(browser, 'admin', 'admin', { viewport: { width: 1300, height: 950 }, acceptDownloads: true });
  const errors = [];
  page.on('pageerror', (e) => errors.push(e.message));
  await page.goto(`${URL}/c/apps/agent/`);
  await page.waitForSelector('#msg', { timeout: 30000 });
  // new chats in the internal class (no sandbox: the point of D201), the fake model
  await page.waitForSelector('#tset', { timeout: 30000 });
  if (!(await page.$('.clsmenu'))) await page.click('#tset');
  await page.click('.clsmenu .mi[data-class="internal"]');
  await until(page, () => !document.querySelector('.clsmenu'));
  await page.evaluate(async () => {
    const base = `/api/${location.pathname.split('/').slice(2, -1).join('/')}`;
    const c = await (await xbin.fetch(`${base}/config`)).json();
    c.model = 'fake/fake-chat';
    await xbin.fetch(`${base}/config`, { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(c) });
  });

  // a conversation, then the PDF attached to the message that starts the tour
  await page.click('#home');
  await page.fill('#msg', 'hello docs');
  await page.press('#msg', 'Enter');
  await answered(page, 'Hello from the fake model.', 60000);
  await page.setInputFiles('#clipin', { name: 'paper.pdf', mimeType: 'application/pdf', buffer: tinyPDF(['Quarterly numbers', 'Second page']) });
  await until(page, () => document.querySelector('#composer, .composer, footer')?.textContent.includes('paper.pdf'), null, 15000).catch(() => {});
  await page.fill('#msg', 'doc tour');
  await page.press('#msg', 'Enter');
  const ok = await answered(page, 'Docs done:', 180000).then(() => true, () => false);
  check(ok, 'the agent read the PDF, looked at a page and wrote a workbook, a deck and a document');
  const finalText = (await text(page, '#timeline .msg.assistant:not(.live)')).filter((t) => t.includes('Docs done:')).pop() || '';
  check(/paper\.pdf — PDF, 2 page\(s\)/.test(finalText), `doc_read read the attached PDF (${finalText.slice(0, 120)})`);
  check(finalText.includes('saw the page'), 'doc_view\'s picture of the page reached the model');
  const chip = await page.$eval('.afile:has-text("paper.pdf")', (e) => e.className).catch(() => 'missing');
  check(!/\bgone\b/.test(chip), `the attached PDF's chip is a file, not drawn as deleted (${chip})`);
  const heads = await text(page, '.tcard .hl');
  for (const h of ['Read the PDF', 'Look at page 1', 'Write the sales sheet', 'Write the deck', 'Write the report']) {
    check(heads.includes(h), `a "${h}" tool card (${JSON.stringify(heads.slice(-6))})`);
  }

  // each written file is a line: its kind, name and version, its preview, its download
  const lines = await text(page, '.step.doc');
  for (const want of ['workbook sales.xlsx v1', 'slides deck.pptx v1', 'document report.docx v1']) {
    check(lines.some((l) => l.includes(want) && l.includes('download')), `the chat's line for ${want.split(' ')[1]} (${JSON.stringify(lines)})`);
  }
  // the preview pane follows the newest write: the report
  await until(page, () => !document.getElementById('preview').hidden, null, 15000).catch(() => {});
  check((await page.textContent('#prev-path')).includes('report.docx.html'), 'the newest preview (the report) is open in the pane');
  // the workbook's line opens its preview: the sheet as a table
  await page.click('.step.doc:has-text("sales.xlsx") .steplnk >> nth=0');
  await until(page, () => document.getElementById('prev-path')?.textContent.includes('sales.xlsx.html'), null, 10000);
  await sleep(500);
  const frame = page.frameLocator('#prevframe');
  const sheet = await frame.locator('body').textContent({ timeout: 10000 }).catch(() => '');
  check(sheet.includes('Region') && sheet.includes('250') && sheet.includes('198'), `the workbook's preview shows its cells and the computed totals (${sheet.slice(0, 80)})`);
  await shot(page, `agent-docs-sheet-${THEME}`, { fullPage: false });
  // the deck's preview
  await page.click('.step.doc:has-text("deck.pptx") .steplnk >> nth=0');
  await until(page, () => document.getElementById('prev-path')?.textContent.includes('deck.pptx.html'), null, 10000);
  await sleep(500);
  const deck = await frame.locator('body').textContent({ timeout: 10000 }).catch(() => '');
  check(deck.includes('Q3 review') && deck.includes('Highlights') && deck.includes('By region'), 'the deck\'s preview draws its slides');
  await shot(page, `agent-docs-deck-${THEME}`, { fullPage: false });

  // download: the file itself
  const [dl] = await Promise.all([
    page.waitForEvent('download', { timeout: 15000 }).catch(() => null),
    page.click('.step.doc:has-text("sales.xlsx") .steplnk:has-text("download")'),
  ]);
  if (dl) {
    const p = await dl.path();
    const head = p ? require('fs').readFileSync(p).subarray(0, 2).toString('latin1') : '';
    check(dl.suggestedFilename() === 'sales.xlsx' && head === 'PK', `the download is the workbook (${dl.suggestedFilename()}, starts ${JSON.stringify(head)})`);
  } else {
    check(false, 'download: no download event');
  }

  // the run's files: the three documents, their previews, the page picture
  const files = await page.evaluate(async () => {
    const base = `/api/${location.pathname.split('/').slice(2, -1).join('/')}`;
    const id = (location.hash.match(/c=(\d+)/) || [])[1];
    const r = await xbin.fetch(`${base}/runs/${id}/files`);
    return r.ok ? (await r.json()).map((f) => `${f.path}:${f.mime || ''}`) : [`HTTP ${r.status}`];
  });
  for (const want of ['sales.xlsx:application/vnd.openxmlformats-officedocument.spreadsheetml.sheet', 'deck.pptx:', 'report.docx:', 'paper.p1.png:image/png', 'sales.xlsx.html:']) {
    check(files.some((f) => f.startsWith(want)), `the run keeps ${want.split(':')[0]} (${JSON.stringify(files)})`);
  }
  check(errors.length === 0, `no page errors (${errors.join(' | ')})`);
  await ctx.close();
  done();
}

module.exports = { agentDocs };
