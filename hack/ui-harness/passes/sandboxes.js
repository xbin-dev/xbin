// hack/ui-harness/passes/sandboxes.js — the admin console's runtime →
// sandboxes tab and the sandbox column in runtime → components (D112).
//   A. live, as the harness runs (no --isolate): isolation tier 1, VMs
//      unavailable with the reason, the policy not editable here; a spawned
//      node backend listed as "host"; a shell listed as a terminal; a
//      terminal asked as a VM refused — and recorded as a failure.
//   B. a VM-capable host, faked by routing GET /sandboxes: emulated, over
//      the budget, a blue/green pair on one tile leaf (its memory counted
//      once), a VM terminal with its disk, a tile-owned sandbox nested under
//      its parent, a failure counted ×3; the policy editor sends what the
//      admin set (zero = the default) plus the edit — never the effective
//      values.
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
    ],
    disks: [
      { key: 'apps~dev-2', path: '/ws/.xbin/term/apps~dev-2/vm/disk.img', tile: 'apps/dev', apparentBytes: 20 * 2 ** 30, allocatedBytes: 700 * MiB, inUse: true },
      { key: 'apps~gone-3', path: '/ws/.xbin/term/apps~gone-3/vm/disk.img', apparentBytes: 20 * 2 ** 30, allocatedBytes: MiB, inUse: false },
    ],
    failures: [
      { time: ago(30), kind: 'backend', tile: 'apps/web', mode: 'vm', stage: 'refused', count: 3,
        error: "the workspace's VM memory budget (4096 MiB) is spent — close a VM terminal or ask an admin to raise it" },
      { time: ago(300), kind: 'terminal', tile: 'apps/dev', user: 'alice', mode: 'vm', stage: 'exit', count: 1,
        error: 'the VM exited (125): vm sandbox: guest agent never answered' },
    ],
    failureCounts: { refused: 3, exit: 1 }, cgroup: true, intervalSec: 2,
    health: {
      isolation: { tier: 3, isolate: true, rootfs: '/opt/xbin/rootfs', scopeUids: false, cgroup: true, uidRange: true,
        protections: { seccomp: true, landlock: true, landlockAbi: 5 } },
      vm: {
        available: true, emulated: true, accel: 'emulate',
        note: 'no KVM (/dev/kvm is missing): VMs run under software emulation, several times slower',
        kvm: '/dev/kvm is missing (no hardware virtualization, or nested virtualization is off on this VM)',
        assets: { kernel: '/opt/xbin/bin/vmlinux', agent: '/opt/xbin/bin/xbin-vmagent', mkfsErofs: '/opt/xbin/bin/mkfs.erofs',
          bx: '/opt/xbin/bin/bx', qemu: '/opt/xbin/bin/qemu-system-x86_64', qemuBios: '/opt/xbin/bin/qemu-bios-microvm.bin',
          qemuPvh: '/opt/xbin/bin/qemu-pvh.bin', vhostVsock: '/opt/xbin/bin/vhost-device-vsock' },
        policy: { terminals: true, backends: true, memMiB: 2048, vcpus: 2, maxVMs: 8, budgetMiB: 4096, diskGiB: 20 },
        stored: { terminals: true, backends: true, budgetMiB: 4096 },
        used: { vms: 3, memMiB: 5120 },
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
  let put = null;
  await B.page.route('**/api/xbin/sandboxes*', (route) => route.fulfill({ json: fx }));
  await B.page.route('**/api/xbin/vm/policy', (route) => {
    put = JSON.parse(route.request().postData() || '{}');
    return route.fulfill({ json: { status: { available: true, emulated: true }, policy: fx.health.vm.policy } });
  });
  await gotoTab(B.page, 'sandboxes', 'apps/web');
  const q = B.page;
  check(await q.locator('[data-vm-avail="emulated"]').count() === 1, 'emulated VMs are said so');
  check(await q.locator('.sbx-budget[data-over]').count() === 1, 'over the budget (lowered while VMs run) shows');
  check(await q.locator('tr[data-sbx-id="tile-child"][data-depth="1"]').count() === 1, "a tile's own sandbox nests under its parent");
  const head = await q.locator('tr.sbx-tile[data-sbx-tile="apps/web"]').textContent();
  check(/120\.0M in use/.test(head), `the tile leaf's memory is counted once for its two generations (${head.replace(/\s+/g, ' ').trim()})`);
  check(/draining/.test(await q.locator('tr[data-sbx-id="backend:apps~web-1:g3"]').textContent()), 'the older generation is draining');
  check(/×3/.test(await q.locator('tr[data-sbx-failure][data-stage="refused"]').first().textContent()), 'a repeated failure shows its count');
  check(await q.locator('[data-sbx-disk="apps~gone-3"]').count() === 1, 'a disk no tile holds is listed');
  check(await q.locator('.sbx-piece.no[data-piece="firecracker"]').count() === 1, 'the missing firecracker is marked');
  await shot(q, 'admin-sandboxes-vm');
  await q.locator('[data-edit-policy]').click();
  await settle(q);
  check(/emulated here/.test(await q.locator('[data-vm-policy="edit"]').textContent()), 'the editor warns: backends on while emulated');
  await q.locator('[data-vm-policy="edit"] input[name="memMiB"]').fill('1024');
  await q.locator('[data-save-policy]').click();
  for (let i = 0; i < 40 && !put; i++) await sleep(100);
  check(JSON.stringify(put) === JSON.stringify({ terminals: true, backends: true, memMiB: 1024, vcpus: 0, maxVMs: 0, budgetMiB: 4096, diskGiB: 0 }),
    `the policy sent is what the admin set plus the edit (${JSON.stringify(put)})`);
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
