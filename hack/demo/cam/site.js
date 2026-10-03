// hack/demo/cam/site.js — what the website stills' shots share
// (shots/site-*.js, site-stills.sh): who a still signs in as, the screen it
// opens, the font a viewport gets, a tile's content to wait for, and the
// still itself (no hover, nothing loading, a caption for the manifest).
//
// The workspace is the demo film set (hack/demo/README.md): its people
// (company.json; the set's random password, password()), the screens each
// of them signs in to
// (data/layouts.json, put back at the start of every take so a retake
// starts the same) and the font size the seed gave them. On a phone (a
// viewport under the shell's 820 px breakpoint) a person gets the shell's
// own 13 px: the seeded 17 px is sized for a laptop's canvas.
'use strict';
const fs = require('fs');
const path = require('path');

const DEMO = path.resolve(__dirname, '..');
const company = JSON.parse(fs.readFileSync(path.join(DEMO, 'company.json'), 'utf8'));
const layouts = JSON.parse(fs.readFileSync(path.join(DEMO, 'data/layouts.json'), 'utf8'));
// the team's agent's tile (company.json agent)
const AGENT = company.agent.tile;

// password(cam): the set's people's password — random per set: $DEMO_PASSWORD,
// else the file beside the set's workspace (--set ws=…: <ws>.password,
// hack/demo/lib.sh demo_password)
function password(cam) {
  if (process.env.DEMO_PASSWORD) return process.env.DEMO_PASSWORD;
  const ws = cam.args.ws || process.env.DEMO_WS || '';
  try { return fs.readFileSync(`${ws}.password`, 'utf8').trim(); } catch {
    throw new Error(`no password for the set: DEMO_PASSWORD, or --set ws=<the set's workspace> with ${ws || '<ws>'}.password beside it (hack/demo/up.sh makes it)`);
  }
}

// assertSet(cam): the workspace --set ws= names is the film set (its branding
// title is the company's) — before a shot writes into its tiles' files
async function assertSet(cam) {
  const b = await api(cam, 'GET', '/api/xbin/branding');
  if (!b || b.title !== company.company.name) {
    throw new Error(`${cam.o.url} is not the ${company.company.name} film set (its branding title is ${JSON.stringify(b && b.title)}): no shot writes there`);
  }
  if (!cam.args.ws || !fs.existsSync(path.join(cam.args.ws, '.xbin', 'token'))) {
    throw new Error(`--set ws=${cam.args.ws || ''} is no workspace directory`);
  }
}

const isPhone = (cam) => cam.o.width < 820;
const fontFor = (cam, who) => (isPhone(cam) ? 13 : layouts.people[who]?.fontSize ?? 17);
const screenId = (name) => 's-' + name.toLowerCase();
const CARD = (tile) => `.card[data-path="${tile}"]`;

// layoutPref(who, screen): the person's layout pref as hack/demo/seed.sh
// writes it, open on `screen` (a name from layouts.json)
function layoutPref(who, screen) {
  const p = layouts.people[who];
  const screens = p.screens.map((s) => ({ id: screenId(s.name), name: s.name, tiles: s.tiles }));
  const active = screen ? screenId(screen) : screens[0].id;
  if (!screens.some((s) => s.id === active)) throw new Error(`${who} has no screen "${screen}" (hack/demo/data/layouts.json)`);
  return {
    screens, active, side: { width: p.sideWidth ?? 216, collapsed: !!p.collapsed, folders: [] },
    tabOrder: screens.map((s) => s.id), hiddenOrg: {}, recent: [], drafts: {},
  };
}

async function api(cam, method, p, body) {
  const r = await cam.page.context().request.fetch(`${cam.o.url}${p}`, { method, ...(body !== undefined ? { data: body } : {}) });
  if (!r.ok()) throw new Error(`${method} ${p}: HTTP ${r.status()} ${(await r.text()).slice(0, 200)}`);
  const t = await r.text();
  try { return t ? JSON.parse(t) : null; } catch { return t; }
}
const pref = (cam, name, body) => api(cam, 'PUT', `/api/xbin/prefs/${encodeURIComponent(name)}`, body);

// signIn(cam, who, {screen, font, fit}): their own session, their font,
// their seeded screens open on `screen`, the shell loaded (on a phone, the
// first app as tall as the phone unless fit is false). No theme of their
// own: the take's theme (shot.js --theme) is the system's, which they follow.
async function signIn(cam, who, { screen, font, fit = true } = {}) {
  await cam.login(who, password(cam));
  await pref(cam, 'settings', { fontSize: font ?? fontFor(cam, who) });
  await api(cam, 'DELETE', '/api/xbin/prefs/theme');
  if (layouts.people[who]) await pref(cam, 'layout', layoutPref(who, screen));
  await cam.openShell();
  if (screen) await cam.waitFor((t, id) => t.activeScreen === id, screenId(screen), { timeout: 15000, label: `screen ${screen}` });
  if (fit) await fitPhone(cam);
}

// fitPhone(cam): on a phone the screen's apps stack full width, each as
// tall as its tile — sized for a laptop's canvas; the first one fills the
// phone instead: from its top to the screen's bottom, as far as the shell
// lets a stacked card grow (82 % of the screen's height). Only the screen on
// show.
async function fitPhone(cam) {
  if (!isPhone(cam)) return;
  const first = await cam.page.locator('bx-canvas .gtile').first().boundingBox().catch(() => null);
  const top = first ? first.y : 110;
  const h = Math.max(260, Math.floor(Math.min(cam.o.height * 0.82, cam.o.height - top - 10)));
  await cam.sh((t, h) => t.setGeom((ts) => ts.map((x, i) => (i === 0 ? { ...x, h } : x))), h);
  await cam.settle();
}

// tile(cam, tile, sel): the tile's document once it shows `sel`
async function tile(cam, p, sel, { timeout = 60000 } = {}) {
  await cam.waitSel(`${CARD(p)} bx-frame`, { state: 'attached', timeout });
  const f = await cam.in(p, { timeout });
  if (sel) await f.waitForSelector(sel, { timeout });
  return f;
}

// openConversation(doc, title): the agent's conversation of that title, by
// its link (#c=<id>), the way a person's bookmark opens it
async function openConversation(doc, title) {
  await doc.waitForSelector(`text=${title}`, { timeout: 30000 });
  const id = await doc.evaluate(async (title) => {
    const d = await (await xbin.fetch(`/api/${xbin.self}/conversations`)).json();
    return [...(d.pinned || []), ...(d.items || [])].find((r) => r.title === title)?.id;
  }, title);
  if (!id) throw new Error(`no conversation "${title}"`);
  await doc.evaluate((id) => { location.hash = `c=${id}`; }, id);
}

// top(cam, tile): on a phone, scroll the stacked canvas so the tile's card
// is at the top (a desk shows the whole screen already)
async function top(cam, p) {
  if (!isPhone(cam)) return;
  await cam.page.locator(CARD(p)).first().evaluate((el) => el.scrollIntoView({ block: 'start' }));
  await cam.settle();
}

// shoot(cam, name, caption): a still with nothing hovered and nothing
// moving — the mouse parked on the top bar's edge (over a scroller, it
// would tint that scroller's bar), two frames, a beat for late paints
// (fonts, a chart's first draw)
// persona: who the frame shows signed in, when not the shot's `who`
async function shoot(cam, name, caption, { settleMs = 600, persona } = {}) {
  await cam.page.mouse.move(Math.round(cam.o.width / 2), 2);
  await cam.settle();
  await cam.sleep(settleMs);
  return cam.still(name, { caption, ...(persona ? { persona } : {}) });
}

module.exports = { company, layouts, AGENT, password, assertSet, isPhone, fontFor, screenId, CARD, layoutPref, api, pref, signIn, fitPhone, tile, openConversation, top, shoot };
