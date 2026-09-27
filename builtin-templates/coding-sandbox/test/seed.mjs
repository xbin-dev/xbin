// seed.mjs — a manager as the tests see it: GET /ops/state with sandboxes of
// three consumers in every state, usage near its quotas, a built image, a
// failed one and one not offered, an orphan, a substrate with VMs and an
// unbound `open` class; GET /sbx/hello and the page's own sandboxes with a
// small tree of files. The web tests (web.mjs), the native tree tests
// (hack/coding-sandbox-ui.test.mjs) and the model tests use it.
export const NOW = Date.UTC(2026, 8, 27, 12);
const SELF = 'apps/coding-sandbox';
const size = (id, memMiB, vcpus, diskGiB) => ({ id, memMiB, vcpus, diskGiB });
const box = (id, extra = {}) => ({
  id, name: id.replace(/^sb-/, ''), state: 'running', stateDetail: '', image: { id: 'base', title: 'Ubuntu with git, Go, Node and Python' },
  size: size('small', 2048, 2, 20), isolation: 'vm', egress: 'none', egressDetail: '', owner: { user: 'alice', via: 'apps/agent', asserted: true },
  visibility: 'private', members: [], shares: [], shared: false, labels: {}, workdir: '/work', home: '/home/dev', user: 'dev', shell: '/bin/bash',
  caps: ['exec', 'files', 'tar', 'tty', 'snapshots', 'clone'], created: NOW - 86400e3, lastActive: NOW - 120e3, autoStopMin: 30, version: 3,
  consumer: 'apps/agent', runtime: 's' + id.slice(3).padEnd(12, '0'), mode: 'vm', diskBytes: 734003200, execsRunning: 0,
  base: { version: 'v7', outdated: false }, ...extra,
});

export const CONFIG = {
  mode: 'auto',
  images: [
    { id: 'base', title: 'Ubuntu with git, Go, Node and Python', default: true, tools: ['git', 'go', 'node', 'python3'] },
    { id: 'node', title: 'Node 22 + pnpm', tools: ['git', 'node', 'pnpm'], setup: 'apt-get update && apt-get install -y nodejs npm\nnpm i -g pnpm', buildEgress: 'internet' },
    { id: 'rust', title: 'Rust', tools: ['cargo'], setup: 'curl https://sh.rustup.rs | sh -s -- -y' },
  ],
  sizes: [
    { id: 'small', title: 'Small', memMiB: 2048, vcpus: 2, diskGiB: 20, default: true },
    { id: 'medium', title: 'Medium', memMiB: 4096, vcpus: 4, diskGiB: 40 },
    { id: 'large', title: 'Large', memMiB: 8192, vcpus: 8, diskGiB: 80 },
  ],
  quotas: { consumer: { sandboxes: 20, running: 8 }, person: { sandboxes: 4, running: 2 }, consumers: { 'apps/agent': { sandboxes: 6, running: 3 } } },
  layout: { workdir: '/work', home: '/home/dev', user: 'dev', uid: 1000, gid: 1000, shell: '/bin/bash' },
  autoStopMin: 30,
  mounts: [],
};

export const OPS = {
  self: SELF,
  backend: { name: 'xbin', registered: ['xbin'] },
  runtime: {
    enabled: true, isolation: true, users: 'any',
    modes: [{ mode: 'namespace' }, { mode: 'vm', accel: 'kvm' }], unavailable: [],
    egress: [{ class: 'none', reach: 'none' }, { class: 'class:internet', slot: 'internet', ref: 'internet', reach: 'internet' },
      { class: 'class:open', slot: 'open', ref: '', reach: 'none' }],
    caps: ['exec', 'files', 'tar', 'tty', 'snapshots', 'clone'],
    limits: { sandboxes: 32, running: 8, waitMaxSec: 120, perSandbox: { maxMemMiB: 8192, maxVCPUs: 8, maxDiskGiB: 200 } },
    used: { sandboxes: 5, running: 3 },
  },
  offer: { caps: ['exec', 'files', 'tar', 'tty', 'snapshots', 'clone'], egress: ['none', 'internet'], images: ['base', 'node', 'rust'], sizes: ['small', 'medium', 'large'],
    notes: [] },
  config: CONFIG,
  images: [
    { id: 'node', runtime: 'img-node-a1b2c3', snapshot: 's-1', setupHash: 'x', mode: 'vm', state: 'ready', log: 'added 1 package\nsetup done', built: NOW - 3600e3 },
    { id: 'rust', runtime: 'img-rust-d4e5f6', setupHash: 'y', mode: 'vm', state: 'error', detail: 'the setup script exited 6: curl: (6) Could not resolve host: sh.rustup.rs', log: 'curl: (6) Could not resolve host: sh.rustup.rs',
      // the last good build, of the script before, kept until a build succeeds
      previous: { id: 'rust', runtime: 'img-rust-a0a0a0', snapshot: 's-3', setupHash: 'y0', mode: 'vm', state: 'ready', built: NOW - 3 * 86400e3 } },
  ],
  sandboxes: [
    box('sb-api', { name: 'api-dev', egress: 'internet', size: size('medium', 4096, 4, 40), lastActive: NOW - 60e3, execsRunning: 2 }),
    box('sb-web', { name: 'web', state: 'stopped', lastActive: NOW - 7200e3, owner: { user: 'bob', via: 'apps/agent', asserted: true } }),
    box('sb-term', { name: 'shell box', consumer: 'apps/sandbox-terminal', owner: { user: 'carol', via: 'apps/sandbox-terminal', asserted: false },
      visibility: 'team', shares: [{ consumer: 'apps/agent', users: '*' }], lastActive: NOW - 600e3 }),
    box('sb-node', { name: 'frontend', image: { id: 'node', title: 'Node 22 + pnpm' }, state: 'creating', stateDetail: 'building the image node (its first use)',
      lastActive: NOW - 30e3, diskBytes: 0 }),
    box('sb-rusty', { name: 'rusty', image: { id: 'rust', title: 'Rust' }, state: 'error', stateDetail: 'the image rust didn\'t build: the setup script exited 6', lastActive: NOW - 3000e3 }),
    box('sb-own', { name: 'mine', consumer: SELF, owner: { user: 'admin', via: SELF, asserted: false }, lastActive: NOW - 900e3 }),
  ],
  orphans: [{ name: 's7f7f7f7f7f7f', state: 'stopped', labels: {}, created: NOW - 99e6 }],
  usage: {
    consumers: { 'apps/agent': { sandboxes: 4, running: 1, memMiB: 4096, vcpus: 4, diskGiB: 100 }, 'apps/sandbox-terminal': { sandboxes: 1, running: 1, memMiB: 2048, vcpus: 2, diskGiB: 20 },
      [SELF]: { sandboxes: 1, running: 1, memMiB: 2048, vcpus: 2, diskGiB: 20 } },
    people: { alice: { sandboxes: 3, running: 1 }, bob: { sandboxes: 1 }, carol: { sandboxes: 1, running: 1 }, admin: { sandboxes: 1, running: 1 } },
  },
};

export const HELLO = {
  protocol: 1, protocols: [1], manager: { name: 'coding-sandbox', title: 'Coding sandboxes', version: '1.0.0' },
  caps: ['exec', 'files', 'tar', 'tty', 'snapshots', 'clone'], egress: ['none', 'internet'],
  images: [{ id: 'base', title: 'Ubuntu with git, Go, Node and Python', default: true, tools: ['git', 'go'] }, { id: 'node', title: 'Node 22 + pnpm', tools: ['node'] }],
  sizes: [{ id: 'small', title: 'Small', memMiB: 2048, vcpus: 2, diskGiB: 20, default: true }, { id: 'medium', title: 'Medium', memMiB: 4096, vcpus: 4, diskGiB: 40 }],
  limits: { sandboxes: 4, waitMaxSec: 120, fileMax: 67108864 },
};

export const MINE = [
  box('sb-own', { name: 'mine', consumer: undefined, owner: { user: 'admin', via: SELF, asserted: false }, lastActive: NOW - 900e3 }),
  box('sb-team', { name: 'team box', state: 'stopped', owner: { user: 'dora', via: SELF, asserted: false }, visibility: 'team', lastActive: NOW - 5000e3 }),
];

export const FILES = {
  '/work': { entries: [
    { name: 'README.md', type: 'file', size: 30, mtimeMs: NOW - 3600e3 },
    { name: 'src', type: 'dir', size: 0, mtimeMs: NOW - 7200e3 },
    { name: 'logo.bin', type: 'file', size: 6, mtimeMs: NOW - 3600e3 },
  ] },
  '/work/README.md': { content: '# api\n\nrun `make` to build.\n' },
  '/work/logo.bin': { content: 'PNG\u0000\u0001\u0002' },
  '/work/src': { entries: [{ name: 'main.go', type: 'file', size: 12, mtimeMs: NOW - 60e3 }] },
  '/work/src/main.go': { content: 'package main' },
};

export const SEED = { self: SELF, me: { user: 'admin', level: 'write', write: true, operator: true, self: SELF }, ops: OPS, hello: HELLO, mine: MINE, files: FILES };
// a person who may open the page and isn't an operator: read access to the
// tile, so they look and never change (the stub refuses their changes)
export const READER = { ...SEED, me: { user: 'dora', level: 'read', write: false, operator: false, self: SELF }, ops: null };
