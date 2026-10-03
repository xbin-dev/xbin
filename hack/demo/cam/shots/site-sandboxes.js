// shots/site-sandboxes.js — the coding sandboxes: Lukas Brandt (staff
// engineer) on the team's sandbox manager — three VM sandboxes the
// engineers made (two running, one stopped), who owns each, its size and
// network, and the usage against the team's quotas. The seed makes them
// (hack/demo/seed.sh); a take starts the two that should be running (an
// xbind restart, a set's reset, stops them) and stops the third. On a phone
// his own sandbox, opened, with a shell in it (its card alone left most of
// the screen empty).
'use strict';
const site = require('../site');

const TILE = 'apps/coding-sandbox';

module.exports = async (cam) => {
  const doc = await cam.in(TILE);
  await cam.mark('sandboxes', doc.locator('text=SANDBOXES').first(), { optional: true });
  await cam.mark('manager', site.CARD(TILE), { optional: site.isPhone(cam) });
  let shell = false;
  if (site.isPhone(cam)) shell = await phoneShell(cam, doc);
  await site.shoot(cam, 'sandboxes', site.isPhone(cam)
    ? (shell
      ? "Lukas Brandt's own coding sandbox on his phone: a VM on the team's image, running, his alone — and a shell in it"
      : "Lukas Brandt's own coding sandbox on his phone: a VM on the team's image, running, his alone until he shares it")
    : "Larkspan's coding sandboxes as Lukas Brandt (staff engineer) sees them: three VM sandboxes — who owns each, size, network, state — and the usage against the team's quotas", { settleMs: 1500 });
  if (shell) await doc.locator('#term-end').dispatchEvent('click').catch(() => {});
};

// phoneShell: on a phone, his sandbox opened and a shell in it — the card
// alone leaves the screen empty; a command shows what the VM is. False (the
// card alone) when no shell answers.
async function phoneShell(cam, doc) {
  try {
    await doc.locator('.card.sbx [data-act="open"]').first().dispatchEvent('click');
    await doc.locator('#sub-term').waitFor({ timeout: 15000 });
    await doc.locator('#sub-term').dispatchEvent('click');
    const term = doc.locator('#term bx-terminal');
    await term.waitFor({ timeout: 20000 });
    await term.evaluate(async (el) => {
      for (let i = 0; i < 300; i++) {
        const t = el.testApi?.();
        if (t) for (let r = 0; r < 60; r++) if (/[$#❯]\s*$/.test(t.screenLine(r) || '')) return;
        await new Promise((r) => setTimeout(r, 100));
      }
      throw new Error('no prompt in the sandbox shell');
    });
    await doc.locator('#term bx-terminal textarea').first().focus();
    await cam.type(cam.args.phoneCommand);
    await cam.press('Enter');
    await cam.sleep(2500);
    // from the top of his list (the page's header is sticky: a card scrolled
    // to the top hides under it): his card, the shell's output under it
    await doc.locator('.card.sbx.on').first().evaluate((el) => {
      for (let p = el.parentElement; p; p = p.parentElement) if (p.scrollHeight > p.clientHeight) p.scrollTop = 0;
      document.scrollingElement.scrollTop = 0;
    });
    await cam.mark('shell', doc.locator('#term'));
    return true;
  } catch (e) {
    cam.log('no sandbox shell for the phone still:', e.message.split('\n')[0]);
    await doc.locator('#detail-close').dispatchEvent('click').catch(() => {});
    return false;
  }
}

module.exports.description = "the coding sandboxes: the engineers' VM sandboxes and their quotas";
module.exports.defaults = { who: 'lukas', screen: 'Sandboxes', running: 'planner-oom-repro,routing-engine',
  // (short lines: a phone's shell is ~40 columns)
  phoneCommand: 'nproc; head -1 /proc/meminfo; go version' };

module.exports.setup = async (cam) => {
  await site.signIn(cam, cam.args.who, { screen: cam.args.screen });
  const doc = await site.tile(cam, TILE, 'text=Coding sandboxes');
  // the sandboxes as the set has them: these running, the rest stopped
  const want = new Set(String(cam.args.running).split(',').filter(Boolean));
  const moved = await doc.evaluate(async (want) => {
    const api = (p, o) => xbin.fetch(`/api/${xbin.self}${p}`, o);
    const st = await (await api('/ops/state')).json();
    const out = [];
    for (const s of st.sandboxes || []) {
      const run = want.includes(s.name);
      if (run && s.state !== 'running') { await api(`/ops/sandboxes/${s.id}/start?wait=90`, { method: 'POST' }); out.push(`started ${s.name}`); }
      if (!run && s.state === 'running') { await api(`/ops/sandboxes/${s.id}/stop?wait=60`, { method: 'POST' }); out.push(`stopped ${s.name}`); }
    }
    return out;
  }, [...want]);
  if (moved.length) cam.log(moved.join(', '));
  // the page polls; reload it so it shows the states it is filmed with
  await doc.evaluate(() => location.reload());
  const fresh = await site.tile(cam, TILE, 'text=Coding sandboxes');
  await fresh.waitForFunction((n) => (document.body.innerText.match(/\brunning\b/g) || []).length >= n, want.size, { timeout: 60000 });
  if (site.isPhone(cam)) {
    // a phone: his own sandboxes, as cards (the operators' table needs a desk)
    await fresh.locator('button:has-text("Yours")').first().dispatchEvent('click');
    await fresh.locator('text=New sandbox').first().waitFor({ timeout: 20000 });
  }
  await site.top(cam, TILE);
};
