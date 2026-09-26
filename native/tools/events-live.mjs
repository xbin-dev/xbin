#!/usr/bin/env node
// events-live.mjs — the app's /ws/events socket against a running xbind, with
// the app's own credential: a bearer on the upgrade (the app's device
// session; URLSessionWebSocketTask sends the same header). Run by app-live
// (native/tools/app-check), which hands it the device session and feeds the
// frames printed here through the app's WorkspaceEvents; by hand:
//
//   node native/tools/events-live.mjs http://127.0.0.1:9461 <bearer> [<workspace-dir> <tile>]
//
// It checks that the upgrade is refused without a bearer and with a bad one,
// opens it with the bearer, then causes events: a branding change (PUT
// /api/xbin/branding, admin — the title is put back) and, given the
// workspace directory, a file written into <tile> and removed (a `reload`).
// stdout: one JSON object per line — {"check": name, "ok": bool} and
// {"frame": "<text as received>"}. Exit 0 once the expected frames arrived,
// 1 on a failed check or after 10 s.
import fs from 'node:fs';
import path from 'node:path';

const [origin, bearer, workspace, tile] = process.argv.slice(2);
if (!origin || !bearer) {
  console.error('usage: events-live.mjs <origin> <bearer> [<workspace-dir> <tile>]');
  process.exit(2);
}
const wsURL = origin.replace(/^http/, 'ws') + '/ws/events';
const out = (o) => process.stdout.write(JSON.stringify(o) + '\n');
let failed = false;
const check = (name, ok) => { out({ check: name, ok }); if (!ok) failed = true; };

function attempt(headers) {
  return new Promise((resolve) => {
    const ws = new WebSocket(wsURL, { headers });
    ws.onopen = () => { ws.close(); resolve(true); };
    ws.onerror = () => resolve(false);
  });
}

check('refused without a bearer', !(await attempt({})));
check('refused with a bad bearer', !(await attempt({ Authorization: 'Bearer not-a-session' })));

const auth = { Authorization: `Bearer ${bearer}` };
const ws = new WebSocket(wsURL, { headers: { ...auth, 'X-XBin-Client': 'app/events-live' } });
const want = new Set(['branding']);
if (workspace && tile) want.add(`reload:${tile}`);
const seen = new Set();
let restoreTitle = null;
let written = null;

const finish = async (code) => {
  if (written) fs.rmSync(written, { force: true });
  if (restoreTitle !== null) {
    await fetch(`${origin}/api/xbin/branding`, {
      method: 'PUT', headers: { ...auth, 'content-type': 'application/json' }, body: JSON.stringify({ title: restoreTitle }),
    }).catch(() => {});
  }
  try { ws.close(); } catch { /* closing */ }
  process.exit(code);
};
const timer = setTimeout(() => {
  check(`frames seen: ${[...want].filter((w) => !seen.has(w)).join(', ')} missing`, false);
  finish(1);
}, 10000);

ws.onerror = () => { check('opened with the bearer', false); clearTimeout(timer); finish(1); };
ws.onmessage = (m) => {
  const text = typeof m.data === 'string' ? m.data : String(m.data);
  out({ frame: text });
  let e = null;
  try { e = JSON.parse(text); } catch { return; }
  if (e.type === 'branding') seen.add('branding');
  if (e.type === 'reload') seen.add(`reload:${e.component}`);
  if ([...want].every((w) => seen.has(w))) {
    clearTimeout(timer);
    finish(failed ? 1 : 0);
  }
};
ws.onopen = async () => {
  check('opened with the bearer', true);
  const b = await (await fetch(`${origin}/api/xbin/branding`, { headers: auth })).json();
  restoreTitle = b.title ?? '';
  const put = await fetch(`${origin}/api/xbin/branding`, {
    method: 'PUT', headers: { ...auth, 'content-type': 'application/json' }, body: JSON.stringify({ title: `events-live ${Date.now()}` }),
  });
  check(`PUT /api/xbin/branding (${put.status})`, put.ok);
  if (workspace && tile) {
    written = path.join(workspace, tile, `events-live-${process.pid}.txt`);
    fs.writeFileSync(written, String(Date.now()));
  }
};
