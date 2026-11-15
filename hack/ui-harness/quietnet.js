// hack/ui-harness/quietnet.js — the harness's Chromium, where the host's
// network changes cannot reach it.
//
// Chromium on Linux listens on a netlink socket for link and address changes
// (NetworkChangeNotifierLinux) and, on each one, fails every request that is
// waiting for a socket with net::ERR_NETWORK_CHANGED — whichever interface
// changed, requests to loopback included. A page here loads a hundred ES
// modules six at a time, so most of a load is spent in that queue; and on a
// machine where sandboxes come and go (each a veth or a tap on the host) a
// pass eventually loses a module, or a navigation, to somebody else's
// sandbox. Chromium has no switch for it (the knob, HttpNetworkSession's
// ignore_ip_address_changes, is only wired up for Cronet).
//
// So the browser gets its own network namespace (`unshare -Urn`): netlink
// reports a namespace's own interfaces only, and nothing ever changes in
// this one. The ports the pages need are relayed across — inside, a listener
// on 127.0.0.1:<port>; a unix socket (the filesystem is shared); outside,
// the real 127.0.0.1:<port> — so every URL, origin and cookie is what it
// was. quietnet.sh is the executable Playwright starts; it enters the
// namespace, starts the inner relay (this file, `inner`) and execs the
// browser Playwright would have started.
//
// Where a namespace can't be had (no unprivileged user namespaces, no `ip`,
// not Linux) or HARNESS_HOST_NET=1 asks, the browser starts on the host as
// it always did, and the log says so.
const { execFileSync } = require('child_process');
const fs = require('fs');
const net = require('net');
const os = require('os');
const path = require('path');

const WRAPPER = path.join(__dirname, 'quietnet.sh');

// relay(server side → dial): bytes both ways, either end's close or error ends both.
function relay(listenAt, dial) {
  const srv = net.createServer((a) => {
    const b = dial();
    const end = () => { a.destroy(); b.destroy(); };
    a.on('error', end); b.on('error', end); a.on('close', end); b.on('close', end);
    a.pipe(b); b.pipe(a);
  });
  return new Promise((resolve, reject) => { srv.once('error', reject); srv.listen(listenAt, () => resolve(srv)); });
}

// why(): '' when this machine can give the browser a namespace, else the reason.
function why() {
  if (process.env.HARNESS_HOST_NET === '1') return 'HARNESS_HOST_NET=1';
  if (process.platform !== 'linux') return `not Linux (${process.platform})`;
  try {
    execFileSync('unshare', ['-Urn', '--', 'ip', 'link', 'set', 'lo', 'up'], { stdio: 'ignore', timeout: 10000 });
  } catch (e) { return `unshare -Urn ip link set lo up: ${e.code || e.status || e.message}`; }
  return '';
}

// browserPath(pw, opts): the executable Playwright starts for these options.
// It has no API for the headless shell's path, so one browser is started and
// asked: it is this process's newest child.
async function browserPath(pw, opts) {
  const kids = () => new Set(fs.readdirSync(`/proc/${process.pid}/task`).flatMap((t) => {
    try { return fs.readFileSync(`/proc/${process.pid}/task/${t}/children`, 'utf8').trim().split(/\s+/).filter(Boolean); } catch { return []; }
  }));
  const before = kids();
  const probe = await pw.chromium.launch(opts);
  try {
    for (const pid of kids()) {
      if (before.has(pid)) continue;
      // (not cmdline: Chromium rewrites its own into one string)
      const exe = fs.readlinkSync(`/proc/${pid}/exe`);
      if (/chrom|headless/i.test(path.basename(exe))) return exe;
    }
  } finally { await probe.close(); }
  throw new Error('quietnet: could not tell which browser Playwright starts');
}

// namespace(ports): the host's half — a unix socket per port, relayed to the
// host's 127.0.0.1:<port> — and the environment quietnet.sh needs besides
// QUIETNET_EXE (what to exec in there). close() ends the relays.
async function namespace(ports) {
  // a unix socket's path is at most 107 bytes: /tmp when TMPDIR is deep
  let dir = fs.mkdtempSync(path.join(os.tmpdir(), 'xbin-quietnet-'));
  if (dir.length > 80) { fs.rmdirSync(dir); dir = fs.mkdtempSync('/tmp/xbin-quietnet-'); }
  const outer = [];
  const close = () => { for (const s of outer.splice(0)) s.close(); fs.rmSync(dir, { recursive: true, force: true }); };
  try {
    for (const port of ports) outer.push(await relay(path.join(dir, `${port}.sock`), () => net.connect(port, '127.0.0.1')));
  } catch (e) { close(); throw e; }
  return { env: { QUIETNET_DIR: dir, QUIETNET_PORTS: ports.join(','), QUIETNET_NODE: process.execPath, QUIETNET_JS: __filename }, close };
}

// launch(pw, opts, ports, log): chromium.launch(opts) in its own network
// namespace, with these loopback ports relayed to the host's.
async function launch(pw, opts = {}, ports = [], log = () => {}) {
  const no = why();
  if (no) {
    log(`browser on the host network (${no}): a network change on this machine can fail a request with ERR_NETWORK_CHANGED`);
    return pw.chromium.launch(opts);
  }
  const exe = await browserPath(pw, opts);
  const ns = await namespace(ports);
  try {
    const browser = await pw.chromium.launch({ ...opts, executablePath: WRAPPER, env: { ...process.env, ...opts.env, ...ns.env, QUIETNET_EXE: exe } });
    browser.on('disconnected', ns.close);
    return browser;
  } catch (e) { ns.close(); throw e; }
}

// inner(): inside the namespace — 127.0.0.1:<port> there to the unix socket
// the harness relays to the host's port. `ready` tells quietnet.sh the
// listeners are up; the relay goes when the browser (its parent) does, or
// never started.
async function inner() {
  const dir = process.env.QUIETNET_DIR;
  for (const port of (process.env.QUIETNET_PORTS || '').split(',').filter(Boolean)) {
    await relay({ host: '127.0.0.1', port: +port }, () => net.connect(path.join(dir, `${port}.sock`)));
  }
  fs.writeFileSync(path.join(dir, 'ready'), '');
  // the shell that started this becomes the browser (exec): same pid
  const parent = +process.env.QUIETNET_PARENT;
  setInterval(() => { if (process.ppid !== parent) process.exit(0); }, 250);
}

if (require.main === module && process.argv[2] === 'inner') inner().catch((e) => { console.error('quietnet:', e); process.exit(1); });

module.exports = { launch, why, namespace, WRAPPER };
