// hack/ui-harness/passes/sandboxes.js — the admin console's runtime →
// sandboxes tab and the sandbox column in runtime → components (D112).
//   A. live, as the harness runs (no --isolate): isolation tier 1, VMs
//      unavailable with the reason, the policy not editable here; a spawned
//      node backend listed as "host", its row still backend:<key>:g<gen>
//      with no deployment field; ?deployment= narrows and is echoed, a bad
//      name is a 400; a shell listed as a terminal; a terminal asked as a VM
//      refused — and recorded as a failure.
//   B. a VM-capable host, faked by routing GET /sandboxes: emulated, over
//      the budget, a blue/green pair on one tile leaf (its memory counted
//      once), a tile running another deployment beside main (generations
//      count per deployment: main's current one isn't draining; the dev
//      generations are listed under the tile, below main's, with the
//      deployment named; each leaf's memory is counted once, main's old
//      generation draining in the flat leaf included), a VM terminal with its
//      disk (and a tile sandbox's, named), a tile-owned sandbox nested under
//      its parent, a failure counted ×3 and one naming its deployment; tile
//      sandboxes' VM sub-budget (D120) next to the budget; the policy editor
//      sends what the admin set (zero = the default) plus the edit — never
//      the effective values — the tiles switches and sub-budget included,
//      and warns that tiles on while VMs are emulated needs tilesEmulated
//      too.
//      Tile sandboxes (D120, WP-19): a manager's running sandbox nests
//      under its current backend; every definition is listed under its
//      manager tile — a stopped one with why, a removed tile's as
//      leftovers — with the admin's stop and delete (sent with ?tile=);
//      the health line (a low-disk hold, the removal backlog, the books);
//      the sandboxes policy editor sends what the admin set plus the edits
//      (never the overrides) and warns that off stops what runs.
//   C. components: an idle tile that asks for a VM shows it (routed
//      /runtime + /auth-overview: isolation on, the tile not running).
//   L. under ISOLATE=1 (run.sh; instead of A): a live tile sandbox —
//      examples/sandbox-go imported, its cap approved, a sandbox started
//      through it; the admin sees it running under its manager, stops it
//      (why says so) and deletes it.
const path = require('path');
const { URL, fs, login, closeCtx, settle, gotoTab, shot, checker, sleep } = require('../lib');

const TILE = 'apps/crawler';

const ago = (s) => new Date(Date.now() - s * 1000).toISOString();
const MiB = 1 << 20;
function fixture() {
  const web = { kind: 'backend', tile: 'apps/web', mode: 'vm', accel: 'emulate', memMiB: 1024, vcpus: 2, leaf: 'apps~web-1', owner: 'dev1',
    stats: { cpu: 1.5, mem: 100 * MiB, pids: 9, scope: 'tile' } };
  const api = { kind: 'backend', tile: 'apps/api', mode: 'namespace', owner: 'dev1' };
  const dev = { deployment: 'dev', leaf: 'tile-apps~api-2/d-dev/backend', stats: { cpu: 2, mem: 50 * MiB, pids: 6, scope: 'deployment' } };
  return {
    sandboxes: [
      { ...web, id: 'backend:apps~web-1:g3', gen: 3, pid: 4101, started: ago(600), uptimeSec: 600 },
      { ...web, id: 'backend:apps~web-1:g4', gen: 4, pid: 4202, started: ago(5), uptimeSec: 5 },
      { id: 'tile-child', kind: 'tile', tile: 'apps/web', parent: 'backend:apps~web-1:g4', mode: 'namespace', name: 'build box', pid: 4300,
        started: ago(4), uptimeSec: 4, stats: { cpu: 0, mem: 20 * MiB, pids: 2, scope: 'sandbox' } },
      // main at g2 beside a blue/green pair of the tile's dev deployment; main's
      // g1 still drains in the flat leaf it ran in before dev started
      { ...api, id: 'backend:apps~api-2:g1', gen: 1, pid: 4400, started: ago(1200), uptimeSec: 1200, leaf: 'apps~api-2',
        stats: { cpu: 0, mem: 10 * MiB, pids: 1, scope: 'tile' } },
      { ...api, id: 'backend:apps~api-2:g2', gen: 2, pid: 4401, started: ago(900), uptimeSec: 900, leaf: 'tile-apps~api-2/d-main/backend',
        stats: { cpu: 0.5, mem: 30 * MiB, pids: 4, scope: 'tile' } },
      { ...api, ...dev, id: 'backend+dev:apps~api-2:g7', gen: 7, pid: 4402, started: ago(20), uptimeSec: 20 },
      { ...api, ...dev, id: 'backend+dev:apps~api-2:g8', gen: 8, pid: 4403, started: ago(3), uptimeSec: 3 },
      { id: 't1', kind: 'terminal', tile: 'apps/dev', user: 'alice', mode: 'vm', accel: 'emulate', memMiB: 2048, vcpus: 2, pid: 5000,
        started: ago(120), uptimeSec: 120, leaf: 'term-t1', disk: '/ws/.xbin/term/apps~dev-2/vm/disk.img',
        stats: { cpu: 3.2, mem: 700 * MiB, pids: 14, scope: 'sandbox' } },
      { id: 'a1', kind: 'agent', tile: 'apps/dev', user: 'bob', label: 'claude', name: 'fixer', status: 'running', mode: 'namespace',
        pid: 5100, started: ago(60), uptimeSec: 60 },
      // a manager's own sandbox: no parent — it nests under the current generation
      { id: 'tile:apps~web-1:box-a', kind: 'tile', tile: 'apps/web', name: 'box-a', for: 'apps/agent', forUser: 'alice', mode: 'namespace',
        memMiB: 2048, vcpus: 2, pid: 4400, started: ago(60), uptimeSec: 60, leaf: 'sbx-apps~web-1-box-a',
        stats: { cpu: 0.5, mem: 30 * MiB, pids: 3, scope: 'sandbox' } },
    ],
    tileSandboxes: [
      { tile: 'apps/web', name: 'box-a', uid: 'aaaaaaaaaaaa', state: 'running', mode: 'namespace', memMiB: 2048, vcpus: 2, diskGiB: 20,
        diskBytes: 3 * 2 ** 30, for: 'apps/agent', forUser: 'alice', lastActive: Date.now() - 60000, tileExists: true },
      { tile: 'apps/web', name: 'box-b', uid: 'bbbbbbbbbbbb', state: 'stopped', stateDetail: 'idle for 30 minutes: stopped, state kept (idleStopMin)',
        mode: 'namespace', memMiB: 2048, vcpus: 2, diskGiB: 20, diskBytes: 2 ** 30, lastActive: Date.now() - 3600000, tileExists: true },
      { tile: 'apps/gone', name: 'old', uid: 'cccccccccccc', state: 'stopped', mode: 'namespace', memMiB: 2048, vcpus: 2, diskGiB: 20,
        stateDetail: 'its tile was removed: stopped, state kept (a workspace admin can delete it)', diskBytes: 5 * 2 ** 30, tileExists: false },
    ],
    disks: [
      { key: 'apps~dev-2', path: '/ws/.xbin/term/apps~dev-2/vm/disk.img', tile: 'apps/dev', apparentBytes: 20 * 2 ** 30, allocatedBytes: 700 * MiB, inUse: true },
      { key: 'apps~gone-3', path: '/ws/.xbin/term/apps~gone-3/vm/disk.img', apparentBytes: 20 * 2 ** 30, allocatedBytes: MiB, inUse: false },
      { kind: 'tile', key: 'apps~web-1', sandbox: 'box-1', sandboxUid: '0123456789ab', path: '/ws/.xbin/sbx/apps~web-1/box-1.0123456789ab/cur/vm/disk.img', tile: 'apps/web',
        apparentBytes: 10 * 2 ** 30, allocatedBytes: 300 * MiB, inUse: false },
    ],
    failures: [
      { time: ago(30), kind: 'backend', tile: 'apps/web', mode: 'vm', stage: 'refused', count: 3,
        error: "the workspace's VM memory budget (4096 MiB) is spent — close a VM terminal or ask an admin to raise it" },
      { time: ago(300), kind: 'terminal', tile: 'apps/dev', user: 'alice', mode: 'vm', stage: 'exit', count: 1,
        error: 'the VM exited (125): vm sandbox: guest agent never answered' },
      { time: ago(400), kind: 'backend', tile: 'apps/api', deployment: 'dev', mode: 'namespace', stage: 'refused', count: 1,
        error: 'the workspace runs 12 non-primary backends, the most allowed at once' },
    ],
    failureCounts: { refused: 4, exit: 1 }, cgroup: true, intervalSec: 2,
    health: {
      tileSandboxes: { cgroup: '', flows: { used: 12, cap: 16384 }, policyError: '', lowDisk: true,
        total: { memMiB: { used: 2176, cap: 24576 }, pids: { used: 31, cap: 32768 } }, trash: { entries: 2, bytes: 7 * 2 ** 30 } },
      isolation: { tier: 3, isolate: true, rootfs: '/opt/xbin/rootfs', scopeUids: false, cgroup: true, uidRange: true,
        protections: { seccomp: true, landlock: true, landlockAbi: 5 } },
      vm: {
        available: true, emulated: true, accel: 'emulate',
        note: 'no KVM (/dev/kvm is missing): VMs run under software emulation, several times slower',
        kvm: '/dev/kvm is missing (no hardware virtualization, or nested virtualization is off on this VM)',
        assets: { kernel: '/opt/xbin/bin/vmlinux', agent: '/opt/xbin/bin/xbin-vmagent', mkfsErofs: '/opt/xbin/bin/mkfs.erofs',
          bx: '/opt/xbin/bin/bx', qemu: '/opt/xbin/bin/qemu-system-x86_64', qemuBios: '/opt/xbin/bin/qemu-bios-microvm.bin',
          qemuPvh: '/opt/xbin/bin/qemu-pvh.bin', vhostVsock: '/opt/xbin/bin/vhost-device-vsock' },
        policy: { terminals: true, backends: true, memMiB: 2048, vcpus: 2, maxVMs: 8, budgetMiB: 4096, diskGiB: 20,
          tiles: true, tilesBudgetMiB: 2048, tilesEmulated: false },
        stored: { terminals: true, backends: true, budgetMiB: 4096, tiles: true, tilesBudgetMiB: 0, tilesEmulated: false },
        used: { vms: 3, memMiB: 5120 },
        usedTiles: { vms: 1, memMiB: 1024 },
        usedBy: { 'apps/web': { vms: 2, memMiB: 2048 }, 'apps/dev': { vms: 1, memMiB: 3072 } },
      },
    },
  };
}

async function sandboxes(browser) {
  const { check, done } = checker('sandboxes');

  if (process.env.HARNESS_ISOLATE) await liveTileSandbox(browser, check);
  else await liveUnisolated(browser, check);
  await routedVMHost(browser, check);
  await idleVMTile(browser, check);
  done();
}

// L: a live tile sandbox, under ISOLATE=1.
async function liveTileSandbox(browser, check) {
  const MGR = 'apps/sbxgo';
  const L = await login(browser, 'admin', 'admin', { viewport: { width: 1400, height: 1000 } });
  const call = async (method, p, data) => {
    const r = await L.ctx.request.fetch(`${URL}${p}`, { method, data, failOnStatusCode: false });
    let body = null;
    try { body = await r.json(); } catch { /* 204, or not JSON */ }
    return { status: r.status(), body };
  };
  const until = async (what, ms, fn) => {
    for (const end = Date.now() + ms; Date.now() < end; await sleep(500)) if (await fn()) return true;
    check(false, `${what} (not within ${ms / 1000} s)`);
    return false;
  };
  // import the example (its files, as an import leaves them), approve its
  // grant, bind its sandboxes' network
  fs.cpSync(path.join(process.env.REPO, 'examples', 'sandbox-go'), path.join(process.env.WS, MGR), { recursive: true });
  await until(`${MGR} registered`, 30000, async () => (await call('GET', `/api/xbin/components/${MGR}`)).status === 200);
  check((await call('POST', '/api/xbin/grants', { from: MGR, target: 'cap:sandboxes', role: 'writer' })).status === 200, 'cap:sandboxes approved');
  check((await call('POST', '/api/xbin/bindings', { component: MGR, slot: 'internet', provider: 'internet' })).status === 200, 'its internet class bound');
  let last = null;
  if (!await until("the manager's backend answers", 300000, async () => (last = await call('GET', `/api/${MGR}/runtime`)).status === 200)) {
    check(false, `its last answer: ${last.status} ${JSON.stringify(last.body)?.slice(0, 300)}`);
    return closeCtx(L.ctx, L.page);
  }
  const made = await call('POST', `/api/${MGR}/sandboxes`, { name: 'live-1', egress: 'internet', start: true });
  check(made.status === 201 && made.body?.state === 'running', `a sandbox started through the manager (${made.status} ${JSON.stringify(made.body)?.slice(0, 200)})`);
  const ran = await call('POST', `/api/${MGR}/sandboxes/live-1/run`, { cmd: 'echo live-$((20+22))' });
  check(ran.body?.stdout?.head === 'live-42\n', `a command ran in it (${JSON.stringify(ran.body)?.slice(0, 200)})`);

  await gotoTab(L.page, 'sandboxes', 'sandboxes');
  const p = L.page, T = p.locator('bx-admin-tile-sandboxes');
  const row = T.locator(`tr[data-tsbx-row="${MGR}:live-1"]`);
  await row.waitFor({ timeout: 15000 }).catch(() => {});
  check(await row.count() === 1, 'the tile sandboxes list the live one');
  check(await row.locator('[data-tsbx-stop]').count() === 1, 'it runs: the admin may stop it');
  await until('the registry lists the running sandbox under its manager', 15000,
    async () => await p.locator(`tr[data-sbx-kind="tile"][data-sbx-id^="tile:apps~sbxgo"][data-depth="1"]`).count() === 1);
  await shot(p, 'admin-sandboxes-live');

  await row.locator('[data-tsbx-stop]').click();
  await until('the admin stop took', 20000, async () => (await call('GET', `/api/${MGR}/sandboxes/live-1`)).body?.state === 'stopped');
  const got = await call('GET', `/api/${MGR}/sandboxes/live-1`);
  check(/stopped by a workspace admin/.test(got.body?.stateDetail || ''), `the manager sees why (${got.body?.stateDetail})`);
  await until('the list shows it stopped, with why', 15000,
    async () => /workspace admin/.test(await row.locator('[data-tsbx-detail]').textContent().catch(() => '')) && await row.locator('[data-tsbx-stop]').count() === 0);
  await shot(p, 'admin-sandboxes-live-stopped');
  p.once('dialog', (dl) => dl.accept());
  await row.locator('[data-tsbx-delete]').click();
  await until('the admin delete took', 20000, async () => (await call('GET', `/api/${MGR}/sandboxes/live-1`)).status === 404);
  await until('the list drops it', 15000, async () => await row.count() === 0);
  await closeCtx(L.ctx, L.page);
}

// A: live, no --isolate.
async function liveUnisolated(browser, check) {
  const A = await login(browser, 'admin', 'admin', { viewport: { width: 1400, height: 950 } });
  await A.ctx.request.get(`${URL}/api/${TILE}/`); // spawns the node backend
  const vmAsk = await A.ctx.request.get(`${URL}/ws/term?cwd=${encodeURIComponent(TILE)}&vm=1`);
  check(vmAsk.status() === 400, `a VM terminal is refused without isolation (${vmAsk.status()})`);
  // a shell on the tile, opened over the terminal socket from the shell's
  // page (the session cookie; a tile document has none) and left running
  const sid = await A.page.evaluate(async (tile) => {
    const ws = new WebSocket(`${location.origin.replace(/^http/, 'ws')}/ws/term?cwd=${encodeURIComponent(tile)}&net=none`);
    await new Promise((res) => { ws.onmessage = res; ws.onerror = res; ws.onclose = res; setTimeout(res, 5000); });
    ws.close();
    const list = await (await fetch(`/api/xbin/term/sessions?cwd=${encodeURIComponent(tile)}`)).json();
    return list.map((s) => s.id).pop() || '';
  }, TILE);
  check(!!sid, `a shell was opened on ${TILE} (${sid || 'none'})`);
  await gotoTab(A.page, 'sandboxes', 'sandboxes');
  await A.page.waitForSelector('[data-sbx-health]', { timeout: 15000 });
  await sleep(2500); // one poll after the shell and the backend are up
  const p = A.page;
  check(await p.locator('[data-vm-avail="no"]').count() === 1, 'VMs are unavailable here, and it says so');
  check(/isolation/.test(await p.locator('[data-vm-reason]').textContent().catch(() => '')), 'the reason names isolation');
  check(await p.locator('[data-vm-policy="off"]').count() === 1, "the VM policy can't be edited without isolation");
  const live = await (await A.ctx.request.get(`${URL}/api/xbin/sandboxes`)).json();
  const th = live.health?.tileSandboxes || {};
  check(Array.isArray(live.tileSandboxes) && 'lowDisk' in th && 'trash' in th && 'pids' in (th.total || {}) && 'policyError' in th,
    `GET /sandboxes has the tile sandboxes and their health (${JSON.stringify(th)})`);
  check(await p.locator('bx-admin-tile-sandboxes').count() === 0, 'no tile sandboxes section without isolation or definitions');
  check(await p.locator('tr[data-sbx-kind="backend"][data-sbx-mode="host"][data-sbx-id^="backend:apps~crawler"]').count() >= 1,
    `the running ${TILE} backend is listed, on the host`);
  // main's registry rows are what they were before tile deployments
  const listed = await (await A.ctx.request.get(`${URL}/api/xbin/sandboxes?tile=${encodeURIComponent(TILE)}`)).json();
  const backs = (listed.sandboxes || []).filter((e) => e.kind === 'backend');
  check(backs.length >= 1 && backs.every((e) => /^backend:apps~crawler-[0-9a-f]{8}:g\d+$/.test(e.id) && e.tile === TILE &&
    !('deployment' in e) && (!e.stats || e.stats.scope === 'tile')),
  `the ${TILE} backend rows keep their id and carry no deployment (${JSON.stringify(backs.map((e) => [e.id, e.deployment, e.stats?.scope]))})`);
  check(!('deployment' in listed), 'an answer asked for no deployment names none');
  // ?deployment= narrows (main's rows are the ones without a deployment) and is echoed
  const narrowed = async (dep) => (await A.ctx.request.get(`${URL}/api/xbin/sandboxes?tile=${encodeURIComponent(TILE)}&deployment=${dep}`));
  const onMain = await (await narrowed('main')).json();
  const onDev = await (await narrowed('dev')).json();
  check(onMain.deployment === 'main' && onMain.sandboxes.filter((e) => e.kind === 'backend').length === backs.length,
    `?deployment=main keeps main's rows and echoes it (${onMain.deployment}, ${onMain.sandboxes.length})`);
  check(onDev.deployment === 'dev' && onDev.sandboxes.length === 0, `?deployment=dev lists nothing here and echoes it (${onDev.deployment}, ${onDev.sandboxes.length})`);
  const bad = await narrowed('Dev');
  check(bad.status() === 400 && /deployment names are lowercase/.test((await bad.json()).error || ''), `a bad deployment name is a 400 (${bad.status()})`);
  if (sid) check(await p.locator(`tr[data-sbx-id="${sid}"][data-sbx-kind="terminal"][data-sbx-mode="host"]`).count() === 1, 'the shell is listed as a terminal');
  const refused = p.locator('tr[data-sbx-failure][data-stage="refused"]', { hasText: TILE });
  check(await refused.count() >= 1, 'the refused VM terminal is a recorded failure');
  await shot(p, 'admin-sandboxes-off');
  // the components tab says how the tile runs
  await gotoTab(p, 'components', TILE);
  const crow = p.locator('tr', { has: p.locator('a', { hasText: TILE }) }).first();
  check(await crow.locator('[data-sbx-cell="host"]').count() === 1, 'components: the tile runs on the host here');
  if (sid) await A.ctx.request.delete(`${URL}/ws/term?session=${encodeURIComponent(sid)}`);
  await closeCtx(A.ctx, A.page);
}

// B: a VM-capable host (routed).
async function routedVMHost(browser, check) {
  const B = await login(browser, 'admin', 'admin', { viewport: { width: 1400, height: 1100 } });
  const fx = fixture();
  let put = null, sbxPut = null;
  const sbxCalls = [];
  const sbxPolicy = { policy: { enabled: true, perTile: { max: 16, running: 4, memMiB: 8192, vcpus: 8, diskGiB: 100 },
    perSandbox: { memMiB: 2048, vcpus: 2, diskGiB: 20, maxMemMiB: 8192, maxVCPUs: 8, maxDiskGiB: 200, pids: 4096 },
    total: { memMiB: 0, pids: 32768 }, idleStopMin: 30, outputRingMiB: 1, outputBudgetMiB: 64,
    overrides: { 'apps/web': { perTile: { max: 32 } } } },
  stored: { perTile: { max: 16 }, total: {}, overrides: { 'apps/web': { perTile: { max: 32 } } } } };
  await B.page.route('**/api/xbin/sandboxes*', (route) => route.fulfill({ json: fx }));
  await B.page.route('**/api/xbin/sandboxes/**', (route) => {
    const req = route.request(), u = new globalThis.URL(req.url()); // lib's URL is the base URL
    if (u.pathname.endsWith('/sandboxes/policy')) {
      if (req.method() === 'PUT') sbxPut = JSON.parse(req.postData() || '{}');
      return route.fulfill({ json: sbxPolicy });
    }
    sbxCalls.push(`${req.method()} ${u.pathname}${u.search}`);
    return route.fulfill(req.method() === 'DELETE' ? { status: 204, body: '' } : { json: { name: 'box-a', state: 'stopped' } });
  });
  await B.page.route('**/api/xbin/vm/policy', (route) => {
    put = JSON.parse(route.request().postData() || '{}');
    return route.fulfill({ json: { status: { available: true, emulated: true }, policy: fx.health.vm.policy } });
  });
  await gotoTab(B.page, 'sandboxes', 'apps/web');
  const q = B.page;
  check(await q.locator('[data-vm-avail="emulated"]').count() === 1, 'emulated VMs are said so');
  check(await q.locator('.sbx-budget[data-over]').count() === 1, 'over the budget (lowered while VMs run) shows');
  const tilesUsed = await q.locator('[data-vm-tiles-used]').textContent().catch(() => '');
  check(/tile sandboxes\s+1 GiB of 2 GiB · 1 VM\b/.test(tilesUsed.replace(/\s+/g, ' ')), `the tile sandboxes' sub-budget shows (${tilesUsed.replace(/\s+/g, ' ').trim()})`);
  const view = (await q.locator('[data-vm-policy="view"]').textContent()).replace(/\s+/g, ' ');
  check(/tile sandboxes on \(emulated off\)/.test(view) && /\(tiles 2 GiB\)/.test(view), `the policy line shows the tiles switches and budget (${view.trim()})`);
  check(await q.locator('tr[data-sbx-id="tile-child"][data-depth="1"]').count() === 1, "a tile's own sandbox nests under its parent");
  const head = await q.locator('tr.sbx-tile[data-sbx-tile="apps/web"]').textContent();
  check(/150\.0M in use/.test(head), `the tile leaf's memory is counted once for its two generations (100M, plus its sandboxes' 20M and 30M) (${head.replace(/\s+/g, ' ').trim()})`);
  check(/draining/.test(await q.locator('tr[data-sbx-id="backend:apps~web-1:g3"]').textContent()), 'the older generation is draining');
  const gen = async (id) => (await q.locator(`tr[data-sbx-id="${id}"]`).textContent()).replace(/\s+/g, ' ').trim();
  const [m1, m2, d7, d8] = [await gen('backend:apps~api-2:g1'), await gen('backend:apps~api-2:g2'),
    await gen('backend+dev:apps~api-2:g7'), await gen('backend+dev:apps~api-2:g8')];
  check(/draining/.test(m1) && !/draining/.test(m2) && /draining/.test(d7) && !/draining/.test(d8),
    `each deployment has its own current generation: main's g2 serves beside dev's g8 (${m1} | ${m2} | ${d7} | ${d8})`);
  check(/50\.0M/.test(d8) && !/50\.0M/.test(d7), `a deployment's leaf stats show on its current generation only (${d7} | ${d8})`);
  check(/10\.0M/.test(m1) && /30\.0M/.test(m2), `main's generations in two leaves show each leaf's stats (${m1} | ${m2})`);
  const apiHead = await q.locator('tr.sbx-tile[data-sbx-tile="apps/api"]').textContent();
  check(/90\.0M in use/.test(apiHead), `each leaf of the tile is counted once: main's flat and nested ones, dev's (${apiHead.replace(/\s+/g, ' ').trim()})`);
  // the dev generations are listed under the tile, after main's, the deployment named
  const trs = await q.locator('table.sbx tr').evaluateAll((els) => els.map((tr) => (tr.classList.contains('sbx-tile') ? `tile:${tr.dataset.sbxTile}`
    : tr.classList.contains('sbx-dep') ? `dep:${tr.dataset.sbxDeployment}` : tr.dataset.sbxId || '')));
  const from = trs.indexOf('tile:apps/api'), to = trs.findIndex((k, i) => i > from && k.startsWith('tile:'));
  const apiRows = trs.slice(from + 1, to < 0 ? trs.length : to).join(' ');
  check(apiRows === 'backend:apps~api-2:g1 backend:apps~api-2:g2 dep:dev backend+dev:apps~api-2:g7 backend+dev:apps~api-2:g8',
    `apps/api lists main's generations, then dev's under its heading (${apiRows})`);
  check(await q.locator('tr[data-sbx-deployment="dev"][data-sbx-id^="backend+dev:"]').count() === 2 && /\bdev · g8\b/.test(d8) && /\bmain · g2\b/.test(m2),
    `the rows name their deployment (${m2} | ${d8})`);
  check(await q.locator('tr[data-sbx-id^="backend:apps~api-2"][data-sbx-deployment]').count() === 0, "main's rows carry no deployment attribute");
  check(!/main ·/.test(await gen('backend:apps~web-1:g4')), 'a tile that runs only main names no deployment');
  check(await q.locator('tr[data-sbx-failure] [data-sbx-failure-deployment="dev"]').count() === 1, "a deployment's failure names it");
  check(/×3/.test(await q.locator('tr[data-sbx-failure][data-stage="refused"]').first().textContent()), 'a repeated failure shows its count');
  check(await q.locator('[data-sbx-disk="apps~gone-3"]').count() === 1, 'a disk no tile holds is listed');
  check(await q.locator('[data-sbx-disk="apps~web-1"] [data-sbx-disk-sandbox="box-1"]').count() === 1, "a tile sandbox's disk names its sandbox");
  check(/uid 0123456789ab/.test(await q.locator('[data-sbx-disk-sandbox="box-1"]').getAttribute('title')), "a tile sandbox disk's uid shows on hover");
  check(await q.locator('.sbx-piece.no[data-piece="firecracker"]').count() === 1, 'the missing firecracker is marked');
  // tile sandboxes (D120): nested under the manager, every definition listed, the admin's actions
  check(await q.locator('tr[data-sbx-id="tile:apps~web-1:box-a"][data-sbx-kind="tile"][data-depth="1"]').count() === 1,
    "a manager's running sandbox nests under its current backend generation");
  check(/for apps\/agent · alice/.test(await q.locator('tr[data-sbx-id="tile:apps~web-1:box-a"]').textContent()), "the tile sandbox row shows the manager's claims");
  const T = q.locator('bx-admin-tile-sandboxes');
  await T.locator('[data-tsbx-table]').waitFor({ timeout: 10000 });
  check(await T.locator('tr[data-tsbx-row]').count() === 3, 'every tile sandbox definition is listed, stopped ones too');
  check(await T.locator('tr[data-tsbx-tile="apps/gone"] [data-tsbx-orphan]').count() === 1, "a removed tile's sandboxes are marked leftovers");
  check(await T.locator('tr[data-tsbx-tile="apps/web"] [data-tsbx-orphan]').count() === 0, "a live manager's aren't");
  check(/idle for 30 minutes/.test(await T.locator('tr[data-tsbx-row="apps/web:box-b"] [data-tsbx-detail]').textContent()), 'a stopped sandbox says why');
  check(await T.locator('tr[data-tsbx-row="apps/web:box-a"] [data-tsbx-stop]').count() === 1 &&
    await T.locator('tr[data-tsbx-row="apps/web:box-b"] [data-tsbx-stop]').count() === 0, 'only a running sandbox has stop');
  check(await T.locator('[data-tsbx-low-disk]').count() === 1, 'the low-disk hold shows');
  const health = (await T.locator('[data-tsbx-health]').textContent()).replace(/\s+/g, ' ');
  check(/memory 2\.1 GiB of 24 GiB/.test(health) && /processes 31 of 32768/.test(health) && /relay flows 12 of 16384/.test(health) &&
    /removal backlog 2 \(7\.0G\)/.test(health), `the health line: the books, the flows, the removal backlog (${health.trim()})`);
  check(/1 tile override/.test(await T.locator('[data-tsbx-policy="view"]').textContent()), 'the policy line counts the overrides');
  await shot(q, 'admin-sandboxes-vm');
  await T.locator('tr[data-tsbx-row="apps/web:box-a"] [data-tsbx-stop]').click();
  q.once('dialog', (dl) => dl.accept());
  await T.locator('tr[data-tsbx-row="apps/gone:old"] [data-tsbx-delete]').click();
  for (let i = 0; i < 40 && sbxCalls.length < 2; i++) await sleep(100);
  check(sbxCalls.join(' | ') === 'POST /api/xbin/sandboxes/box-a/stop?tile=apps%2Fweb | DELETE /api/xbin/sandboxes/old?tile=apps%2Fgone',
    `stop and delete are the admin's, with ?tile= (${sbxCalls.join(' | ')})`);
  await T.locator('[data-tsbx-edit-policy]').click();
  await settle(q);
  await T.locator('[data-tsbx-policy="edit"] input[name="enabled"]').uncheck();
  await T.locator('[data-tsbx-policy="edit"] input[name="total.memMiB"]').fill('16384');
  await settle(q);
  check(/stops every running one now/.test(await T.locator('[data-tsbx-policy="edit"]').textContent()), 'the editor warns: off stops what runs');
  // A full-page shot leaves Chromium at another scroll offset than its scroll
  // anchoring remembers: the next layout (the click moving focus) snaps the
  // page back, the save button jumps from under the pointer and the click
  // lands on the form. Put the scroll back where it was before shooting.
  const y0 = await q.evaluate(() => scrollY);
  await shot(q, 'admin-sandboxes-tile-policy');
  await q.evaluate((y) => window.scrollTo(0, y), y0);
  await T.locator('[data-tsbx-save-policy]').click();
  for (let i = 0; i < 40 && !sbxPut; i++) await sleep(100);
  check(sbxPut && sbxPut.enabled === false && sbxPut.total?.memMiB === 16384 && sbxPut.total?.pids === 0 &&
    sbxPut.perTile?.max === 16 && sbxPut.perTile?.running === 0 && sbxPut.idleStopMin === 0 && !('overrides' in sbxPut),
  `the sandboxes policy sent is what the admin set plus the edits, overrides left alone (${JSON.stringify(sbxPut)})`);
  await q.locator('[data-edit-policy]').click();
  await settle(q);
  const editor = () => q.locator('[data-vm-policy="edit"]').textContent();
  check(/a backend in a VM is several times slower/.test(await editor()), 'the editor warns: backends on while emulated');
  check(/tile sandboxes can't use VM mode until emulation is allowed/.test(await editor()), 'the editor warns: tiles on while emulated needs tilesEmulated');
  await q.locator('[data-vm-policy="edit"] input[name="memMiB"]').fill('1024');
  await q.locator('[data-vm-policy="edit"] input[name="tilesEmulated"]').check();
  await settle(q);
  check(!/until emulation is allowed/.test(await editor()) && /Emulated tile VMs are several times slower/.test(await editor()),
    'allowing emulation for tiles swaps the warning');
  await shot(q, 'admin-sandboxes-vm-policy');
  await q.locator('[data-save-policy]').click();
  for (let i = 0; i < 40 && !put; i++) await sleep(100);
  check(JSON.stringify(put) === JSON.stringify({ terminals: true, backends: true, tiles: true, tilesEmulated: true,
    memMiB: 1024, vcpus: 0, maxVMs: 0, budgetMiB: 4096, diskGiB: 0, tilesBudgetMiB: 0 }),
  `the policy sent is what the admin set plus the edits (${JSON.stringify(put)})`);
  await closeCtx(B.ctx, B.page);
}

// C: an idle tile that asks for a VM.
async function idleVMTile(browser, check) {
  const C = await login(browser, 'admin', 'admin', { viewport: { width: 1400, height: 950 } });
  await C.page.route('**/api/xbin/runtime', async (route) => {
    const r = await route.fetch();
    const j = await r.json();
    j.host.isolate = true;
    j.backends = (j.backends || []).filter((b) => b.path !== TILE);
    return route.fulfill({ response: r, json: j });
  });
  await C.page.route('**/api/xbin/auth-overview', async (route) => {
    const r = await route.fetch();
    const j = await r.json();
    for (const c of j.components || []) if (c.path === TILE) c.vm = {};
    return route.fulfill({ response: r, json: j });
  });
  await gotoTab(C.page, 'components', TILE);
  await sleep(2500);
  const irow = C.page.locator('tr', { has: C.page.locator('a', { hasText: TILE }) }).first();
  check(await irow.locator('[data-sbx-cell="vm"].idle').count() === 1, 'components: an idle tile asking for a VM shows its VM badge, muted');
  await shot(C.page, 'admin-components-vm');
  await closeCtx(C.ctx, C.page);
}

module.exports = { sandboxes };
