// hack/ui-harness/phone.js — a phone for the app-tunnel passes (D204): it
// runs bin/tunnelpair (devclient over the sidecar binary's client mode —
// the real xbin-iroh, or hack/fakeiroh; run.sh's HARNESS_IROH, which the
// harness xbind runs too) on a pairing link, so a pass sees xbind's real
// pairing: the same code on the phone and on the computer, Codes match or
// They don't, the phone hearing which. CommonJS, like the passes.
//
//   const ph = phone(link, "Ann's iPhone", { endpoint, connect });
//   const code = await ph.code();         // "012 345", once xbind awaits the computer
//   const end = await ph.end();           // {t: 'enrolled', deviceId} | {t: 'rejected', reason}
//   ph.leave();                           // the phone goes away (SIGTERM): before the confirmation, abandoned
//
// endpoint: the server's EndpointId, which a fakeiroh ticket doesn't carry
// (tunnelEndpoint). connect: after pairing, sign in over tunnel/1 and hold a
// connection (the admin console counts the phone connected) until leave().
const path = require('path');
const fs = require('fs');
const { spawn } = require('child_process');
const { URL } = require('./lib');

const REPO = process.env.REPO || path.join(__dirname, '..', '..');
const TUNNELPAIR = path.join(REPO, 'bin', 'tunnelpair');
const IROH = process.env.HARNESS_IROH || '';

// why the real phone can't run here ('' when it can)
function phoneMissing() {
  if (!IROH) return 'HARNESS_IROH unset (run.sh sets it: bin/xbin-iroh, else bin/fakeiroh)';
  if (!fs.existsSync(IROH)) return `no sidecar at ${IROH}`;
  if (!fs.existsSync(TUNNELPAIR)) return `no ${TUNNELPAIR} (run.sh builds it: go build -o bin/tunnelpair ./hack/tunnelpair)`;
  return '';
}

// tunnelEndpoint(request): the server's EndpointId, for a fakeiroh ticket
// (an admin's request context).
async function tunnelEndpoint(request) {
  const v = await (await request.get(`${URL}/api/xbin/app-tunnel`)).json();
  return v.endpointId || '';
}

function phone(link, name, { endpoint = '', connect = false } = {}) {
  const args = ['-iroh', IROH, '-link', link, '-name', name];
  if (endpoint && /[?&]t=fakeiroh/.test(link)) args.push('-endpoint', endpoint);
  if (connect) args.push('-connect');
  const p = spawn(TUNNELPAIR, args, { stdio: ['pipe', 'pipe', 'pipe'] });
  const lines = [];
  let waiters = [];
  let exited = null;
  let buf = '';
  const stderr = [];
  const wake = () => { for (const w of waiters) w(); };
  p.stdout.on('data', (b) => {
    buf += b.toString();
    let i;
    while ((i = buf.indexOf('\n')) >= 0) {
      const l = buf.slice(0, i);
      buf = buf.slice(i + 1);
      try { lines.push(JSON.parse(l)); } catch { /* not ours */ }
    }
    wake();
  });
  p.stderr.on('data', (b) => { stderr.push(b.toString()); if (stderr.length > 200) stderr.shift(); });
  p.on('exit', (code) => { exited = code; wake(); });
  // until(pred): the first line pred accepts, or why none will come
  const until = (pred, label, timeout = 30000) => new Promise((resolve, reject) => {
    const t0 = Date.now();
    const check = () => {
      const hit = lines.find(pred);
      if (hit) { done(); resolve(hit); return; }
      const err = lines.find((l) => l.t === 'error');
      if (err || exited !== null || Date.now() - t0 > timeout) {
        done();
        reject(new Error(`${name}: no ${label} (${err ? err.error : exited !== null ? `exited ${exited}` : 'timed out'}; ` +
          `lines ${JSON.stringify(lines)}; stderr ${stderr.join('').slice(-600)})`));
      }
    };
    const timer = setInterval(check, 200);
    const done = () => { clearInterval(timer); waiters = waiters.filter((w) => w !== check); };
    waiters.push(check);
    check();
  });
  return {
    lines,
    code: async () => (await until((l) => l.t === 'code', 'code')).code,
    end: () => until((l) => l.t === 'enrolled' || l.t === 'rejected', 'end of pairing'),
    signedIn: () => until((l) => l.t === 'signed-in', 'sign-in over the tunnel'),
    leave: () => { if (exited === null) p.kill('SIGTERM'); },
    gone: () => new Promise((r) => (exited !== null ? r(exited) : p.once('exit', r))),
  };
}

module.exports = { phone, phoneMissing, tunnelEndpoint };
