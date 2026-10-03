// hack/deploy-fixtures.mjs — the deployments states the terminal window's view
// tests draw from (hack/deploy-state.test.mjs, hack/deploy-panel.test.mjs):
// the zero state, paused, attached, a reader's primary-only view, and the
// callers and deployment rows they are built of. Not a test file itself.

const T = 'apps/crm';
const NOW = Date.parse('2026-09-27T12:00:00Z');
const at = (min) => new Date(NOW - min * 60000).toISOString();
const opts = { now: NOW };
const yes = { ok: true };
const no = (why, kind = 'authority') => ({ ok: false, why, kind });

const PROTECTED = `the primary of ${T} (main) is protected: only tile managers change its code, and not from a terminal or agent session`;
const ISOLATE = 'pinning a backend to a checkpoint needs isolation (--isolate)';
const NEEDS_TERMINAL = (op) => `${op} needs terminal-level access on ${T}`;
const TODAY = [{ value: 'on', label: 'tile API', icon: 'plug' }, { value: 'off', label: 'no API', icon: 'error' }];

const terminalCaller = (over = {}) => ({
  level: 'terminal', manager: false, humanSession: true, readOnly: false,
  can: { pause: yes, resume: yes, reloadNow: yes, add: yes, edges: no('tile managers only'), protect: no('tile managers only') },
  ...over,
});
const depCan = (over = {}) => ({ open: yes, deploy: yes, restart: yes, promoteTo: yes, rollback: yes, attach: yes, remove: no('main can\'t be removed', 'state'), ...over });

// main following the work tree (the zero state's synthesized row)
const mainLive = (over = {}) => ({
  name: 'main', primary: true, liveReload: true, checkpoint: null,
  status: { state: 'static', gen: 1, serving: 'work-tree' }, url: `/c/${T}/`, can: depCan(), ...over,
});
// main pinned to c:3f2a1c9 by a pause 12 minutes ago
const mainPinned = (over = {}) => ({
  name: 'main', primary: true, liveReload: false,
  checkpoint: { id: 'c:3f2a1c9', hash: '3f2a1c9e', feed: 'work-tree', at: at(12), by: 'user:ana' },
  status: { state: 'static', gen: 2, serving: 'c:3f2a1c9' }, url: `/c/${T}/`,
  lastDeploy: { id: 2, how: 'pause', at: at(12), by: 'user:ana', result: 'ok' }, can: depCan(), ...over,
});
const devLive = (over = {}) => ({
  name: 'dev', primary: false, liveReload: true, checkpoint: null,
  status: { state: 'static', gen: 1, serving: 'work-tree' }, url: `/c/${T}+dev/`, can: depCan({ remove: yes }), ...over,
});

const zero = (over = {}) => ({
  tile: T, record: false, schema: 0, seq: 0, features: ['live-reload/1'], owner: 'org:devs', view: 'full',
  primary: 'main', liveReload: 'main', lastLiveReload: 'main', protectedPrimary: false,
  allowed: { pause: yes, deployments: yes }, deployments: [mainLive()], edges: [], caller: terminalCaller(), ...over,
});
const paused = (over = {}) => ({
  tile: T, record: true, schema: 1, seq: 3, features: ['live-reload/1'], owner: 'org:devs', view: 'full',
  primary: 'main', liveReload: '', lastLiveReload: 'main', liveReloadSince: { at: at(12), by: 'user:ana' },
  workTree: { changed: 3, since: 'c:3f2a1c9' }, protectedPrimary: false,
  allowed: { pause: yes, deployments: yes }, deployments: [mainPinned()], edges: [], caller: terminalCaller(), ...over,
});
// attached to the primary, with a record (another setting keeps one)
const attachedMain = (over = {}) => paused({ liveReload: 'main', workTree: undefined, deployments: [mainLive()], ...over });
// live reload on dev, main pinned
const onDev = (over = {}) => paused({
  liveReload: 'dev', lastLiveReload: 'dev', workTree: undefined,
  deployments: [mainPinned({ lastDeploy: { id: 2, how: 'attach', at: at(12), by: 'user:ana', result: 'ok' } }), devLive()], ...over,
});
// the reader view (11-contract §1.3): the primary's facts only
const reader = (over = {}) => ({
  tile: T, record: true, schema: 1, features: ['live-reload/1'], owner: 'org:devs', view: 'reader',
  primary: 'main', liveReload: '', protectedPrimary: false,
  deployments: [{
    name: 'main', primary: true, liveReload: false,
    checkpoint: { id: 'c:3f2a1c9', hash: '3f2a1c9e', at: at(12), by: 'user:ana' },
    status: { state: 'static', gen: 2, serving: 'c:3f2a1c9' }, url: `/c/${T}/`,
    lastDeploy: { at: at(12), by: 'user:ana', result: 'ok' },
  }],
  caller: { level: 'read', manager: false, humanSession: true, readOnly: false,
    can: { pause: no('needs write access'), resume: no('needs write access'), reloadNow: no('needs write access'), add: no('needs write access'), edges: no('needs write access'), protect: no('needs write access') } },
  ...over,
});

const labels = (items) => items.map((it) => (it.kind ? `<${it.kind}>` : it.label));
const byLabel = (items, l) => items.find((it) => it.label === l);
const summary = (over = {}) => ({ primary: 'main', pinned: true, protected: false, ...over });

const CODE = { deployment: 'main', from: 'c:3f2a1c9', to: 'c:7b19e02', files: 3, added: 40, removed: 12, workTreeAt: at(0) };

export {
  T, NOW, at, opts, yes, no, PROTECTED, ISOLATE, NEEDS_TERMINAL, TODAY, terminalCaller, depCan, mainLive, mainPinned, devLive, zero, paused, attachedMain, onDev, reader, labels, byLabel, summary, CODE,
};
