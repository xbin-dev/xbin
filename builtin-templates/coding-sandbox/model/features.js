// model/features.js — every feature of the coding-sandbox page, by key: the
// contract that keeps its two views level (D96's mechanism, as the agent
// template's). Each view declares the keys it implements (web-features.js,
// native-features.js), and hack/coding-sandbox-ui.test.mjs fails when a view
// misses a key that is not listed below as an intended difference, with the
// reason. A UX change lands here, in the model and in BOTH views in the same
// change.
//
// Keys are <area>.<feature>[.<detail>].
export const AREAS = {
  ops: 'Sandboxes — every consumer\'s, for the operators (metadata, never contents)',
  images: 'Images — the base and setup scripts, and their builds',
  settings: 'Settings — the operators\' mode, networks, sizes, quotas and the rest',
  mine: 'Your sandboxes — the page as a consumer of its own',
  files: 'Files — a sandbox\'s file browser',
  term: 'Terminal — a shell in one of your sandboxes',
  state: 'States of the whole page',
};

export const FEATURES = {
  // Operators: every sandbox
  'ops.list': 'every sandbox of every consumer, most recently active first',
  'ops.facts': 'each one\'s state (and why), consumer, owner, image, size, network, isolation, disk and last activity',
  'ops.lifecycle': 'start and stop any sandbox',
  'ops.delete': 'delete any sandbox, confirmed',
  'ops.snapshots': 'a sandbox\'s snapshots: list, take one, delete one (confirmed)',
  'ops.snapshots.restore': 'restore a snapshot, confirmed',
  'ops.shares': 'who may use each sandbox (its visibility, members and the consumers it is shared with), shown and never changed: only its home consumer or its owner changes that',
  'ops.usage': 'usage by consumer and by person against the quota that binds each',
  'ops.orphans': 'the substrate\'s sandboxes this manager doesn\'t know, deleted (confirmed)',
  'ops.backend': 'the backend and the substrate: its errors, modes, capabilities and what hello leaves out (notes)',
  'ops.ports': 'a sandbox\'s Ports row: whether the manager offers ports (live previews) and why not — the runtime lacks them, the backend doesn\'t forward, its agent predates them (restart it) — and a probe of one port: status, type, refusal, latency, never the page',

  // Images
  'images.list': 'the images: title, tools, whether it has a setup script, whether consumers are offered it',
  'images.build': 'build status (building, built, failed and why; the previous good build a failed or running rebuild keeps); build or rebuild now',
  'images.log': 'the last build\'s output',
  'images.edit': 'add or change an image: id, title, tools, setup script, the build\'s network, the default',
  'images.remove': 'remove an image, confirmed',

  // Settings
  'settings.mode': 'the isolation mode (automatic, VMs, namespaces), and what new sandboxes get with it now or why none can be made',
  'settings.egress': 'the sandbox networks (internet, open): what each class is bound to and reaches, whether it is offered, how to bind it',
  'settings.sizes': 'the sizes consumers pick from: add, change, remove, the default',
  'settings.quotas': 'quotas: the defaults for every consumer and every person, and overrides for one',
  'settings.advanced': 'the layout (workdir, home, user, shell), the idle stop and the mounts',

  // Your sandboxes
  'mine.list': 'the sandboxes you may use here: state, image, size, network, isolation, owner, last activity',
  'mine.create': 'create one: name, image, size, network, who may use it',
  'mine.lifecycle': 'start, stop; delete (confirmed) your own',
  'mine.shares': 'share one of yours with another consumer, or stop sharing',
  'mine.visibility': 'who may use one of yours: you (and members) or the team',
  'mine.ports': 'the Ports row of one of yours (as ops.ports); a reader is told it takes write access',

  // Files
  'files.browse': 'a directory\'s entries, directories first; into one, up, or to a path typed',
  'files.view': 'a text file\'s content (its first 256 KiB); a binary one says so',
  'files.download': 'download a file',
  'files.upload': 'upload files into the directory',
  'files.mkdir': 'make a directory',
  'files.remove': 'remove a file or a directory, confirmed',

  // Terminal
  'term.open': 'a shell in the sandbox as you, at its working directory',
  'term.end': 'close the terminal, ending its shell',

  // States
  'state.errors': 'a refused call or an unreachable backend says why',
  'state.reader': 'someone who isn\'t an operator sees only their own sandboxes',
  'state.readonly': 'someone with read access to the tile looks and never changes: every change (create, lifecycle, sharing, a terminal, file changes) is hidden or disabled, saying it needs write access',
  'state.refresh': 'the page reads everything again (by hand, and while it is on screen)',
};

// Intended differences, per view, each with the reason.
export const DIFFERENCES = {
  web: {},
  native: {
    'ops.ports': 'a port-forward diagnostic for the web page first (the owner asked for it there); the probe route is the same for the app when it is wanted',
    'mine.ports': 'as ops.ports',
    'files.upload': 'the app uploads only from its composer (a chat\'s attachments): there is no file-picker primitive for a browser of files, so uploads stay on the web page',
  },
};

// gaps checks a view's declaration against the registry:
//   missing  keys it neither implements nor lists as a difference
//   unknown  keys it implements that the registry does not have
//   stale    differences listed for it that it implements, or that no longer exist
export function gaps(view, implemented) {
  const has = new Set(Object.keys(implemented || {}));
  const diff = DIFFERENCES[view] || {};
  return {
    missing: Object.keys(FEATURES).filter((k) => !has.has(k) && !diff[k]),
    unknown: [...has].filter((k) => !(k in FEATURES)),
    stale: Object.keys(diff).filter((k) => has.has(k) || !(k in FEATURES)),
  };
}
