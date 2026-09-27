// hack/ui-harness/passes/sandboxes.js — the admin console's runtime →
// sandboxes tab and the sandbox column in runtime → components (D112).
//   A. live, as the harness runs (no --isolate): isolation tier 1, VMs
//      unavailable with the reason, the policy not editable here; a spawned
//      node backend listed as "host"; a shell listed as a terminal; a
//      terminal asked as a VM refused — and recorded as a failure.
//   B. a VM-capable host, faked by routing GET /sandboxes: emulated, over
//      the budget, a blue/green pair on one tile leaf (its memory counted
//      once), a VM terminal with its disk (and a tile sandbox's, named), a
//      tile-owned sandbox nested under its parent, a failure counted ×3;
//      tile sandboxes' VM sub-budget (D120) next to the budget; the policy
//      editor sends what the admin set (zero = the default) plus the edit —
//      never the effective values — the tiles switches and sub-budget
//      included, and warns that tiles on while VMs are emulated needs
//      tilesEmulated too.
//      Tile sandboxes (D120, WP-19): a manager's running sandbox nests
//      under its current backend; every definition is listed under its
//      manager tile — a stopped one with why, a removed tile's as
//      leftovers — with the admin's stop and delete (sent with ?tile=);
//      the health line (a low-disk hold, the removal backlog, the books);
//      the sandboxes policy editor sends what the admin set plus the edits
//      (never the overrides) and warns that off stops what runs.
//   C. components: an idle tile that asks for a VM shows it (routed
//      /runtime + /auth-overview: isolation on, the tile not running).
const { URL, login, closeCtx, settle, gotoTab, shot, checker, sleep } = require('../lib');

const TILE = 'apps/crawler';

const ago = (s) => new Date(Date.now() - s * 1000).toISOString();
const MiB = 1 << 20;
function fixture() {
  const web = { kind: 'backend', tile: 'apps/web', mode: 'vm', accel: 'emulate', memMiB: 1024, vcpus: 2, leaf: 'apps~web-1', owner: 'dev1',
    stats: { cpu: 1.5, mem: 100 * MiB, pids: 9, scope: 'tile' } };
  return {
    sandboxes: [
      { ...web, id: 'backend:apps~web-1:g3', gen: 3, pid: 4101, started: ago(600), uptimeSec: 600 },
      { ...web, id: 'backend:apps~web-1:g4', gen: 4, pid: 4202, started: ago(5), uptimeSec: 5 },
      { id: 'tile-child', kind: 'tile', tile: 'apps/web', parent: 'backend:apps~web-1:g4', mode: 'namespace', name: 'build box', pid: 4300,
        started: ago(4), uptimeSec: 4, stats: { cpu: 0, mem: 20 * MiB, pids: 2, scope: 'sandbox' } },
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
    ],
    failureCounts: { refused: 3, exit: 1 }, cgroup: true, intervalSec: 2,
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

  // ---- A: live, no --isolate ----
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

  // ---- B: a VM-capable host (routed) ----
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
  check(/tile sandboxes ✓ \(emulated ✗\)/.test(view) && /\(tiles 2 GiB\)/.test(view), `the policy line shows the tiles switches and budget (${view.trim()})`);
  check(await q.locator('tr[data-sbx-id="tile-child"][data-depth="1"]').count() === 1, "a tile's own sandbox nests under its parent");
  const head = await q.locator('tr.sbx-tile[data-sbx-tile="apps/web"]').textContent();
  check(/150\.0M in use/.test(head), `the tile leaf's memory is counted once for its two generations (100M, plus its sandboxes' 20M and 30M) (${head.replace(/\s+/g, ' ').trim()})`);
  check(/draining/.test(await q.locator('tr[data-sbx-id="backend:apps~web-1:g3"]').textContent()), 'the older generation is draining');
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
  await shot(q, 'admin-sandboxes-tile-policy');
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

  // ---- C: an idle tile that asks for a VM ----
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
  check(await irow.locator('[data-sbx-cell="vm"].idle').count() === 1, 'components: an idle tile asking for a VM shows ⧉ VM, muted');
  await shot(C.page, 'admin-components-vm');
  await closeCtx(C.ctx, C.page);
  done();
}

module.exports = { sandboxes };
