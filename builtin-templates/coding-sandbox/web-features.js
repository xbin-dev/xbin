// web-features.js — what the web view (index.html, web*.js) implements, by
// feature key (model/features.js), and where. The native view's is
// native-features.js; hack/coding-sandbox-ui.test.mjs holds both against the
// registry. Side-effect free: import it anywhere.
export const IMPLEMENTS = {
  'ops.list': 'web-ops.js — opsTab (model/ops.js sandboxRows)',
  'ops.facts': 'web-ops.js — rowTpl',
  'ops.lifecycle': 'web-ops.js — rowTpl actions start, stop (app.opAct)',
  'ops.delete': 'web-ops.js — rowTpl delete, confirmed',
  'ops.snapshots': 'web-ops.js — snapshotsTpl (take, delete confirmed)',
  'ops.snapshots.restore': 'web-ops.js — snapshotsTpl Restore, confirmed',
  'ops.ports': 'web-ops.js — the Ports button → web-ports.js portsTpl (GET /ports/{id}, /ports/{id}/{port})',
  'mine.ports': 'web-mine.js — the Ports tab → web-ports.js portsTpl',
  'ops.shares': 'web-ops.js — rowTpl, the who line (model/format.js whoText); no control',
  'ops.usage': 'web-ops.js — usageTpl (model/ops.js usageRows)',
  'ops.orphans': 'web-ops.js — orphansTpl, confirmed',
  'ops.backend': 'web-ops.js — backendTpl (model/ops.js backendInfo)',

  'images.list': 'web-ops.js — imagesTab (model/ops.js imageRows)',
  'images.build': 'web-ops.js — imagesTab, the build pill and the kept build, Build now / Rebuild (app.build)',
  'images.log': 'web-ops.js — imagesTab, the last build\'s output',
  'images.edit': 'web-ops.js — imageFormTpl (model/ops.js imageForm, applyImage)',
  'images.remove': 'web-ops.js — imagesTab Remove, confirmed (model/ops.js removeImage)',

  'settings.mode': 'web-settings.js — modeTpl (model/ops.js modeInfo)',
  'settings.egress': 'web-settings.js — egressTpl (model/ops.js backendInfo classes)',
  'settings.sizes': 'web-settings.js — sizesTpl (model/ops.js applySizes)',
  'settings.quotas': 'web-settings.js — quotasTpl (model/ops.js quotaRows, setQuota)',
  'settings.advanced': 'web-settings.js — advancedTpl (model/ops.js parseMount)',

  'mine.list': 'web-mine.js — mineTab, rowTpl (model/mine.js myRows)',
  'mine.create': 'web-mine.js — createTpl (model/mine.js createForm)',
  'mine.lifecycle': 'web-mine.js — rowTpl actions, delete confirmed (app.act)',
  'mine.shares': 'web-mine.js — shareTpl → web-ops.js sharesTpl',
  'mine.visibility': 'web-mine.js — shareTpl (app.setVisibility)',

  'files.browse': 'web-mine.js — filesTpl (crumbs, Up, a path typed; model/mine.js fileRows)',
  'files.view': 'web-mine.js — fileTpl (app.readFile)',
  'files.download': 'web-mine.js — filesTpl download (xbin.download)',
  'files.upload': 'web-mine.js — filesTpl Upload (app.upload)',
  'files.mkdir': 'web-mine.js — filesTpl New folder',
  'files.remove': 'web-mine.js — filesTpl Remove, confirmed',

  'term.open': 'web-mine.js — termTpl (<bx-terminal src>: model/mine.js terminalSrc)',
  'term.end': 'web-mine.js — termTpl End, endTerm (app.endShell)',

  'state.errors': 'web.js — ui.err (ui.run); web-ops.js, web-mine.js — the load errors',
  'state.reader': 'web.js — tabs (Your sandboxes only); web-mine.js — the reader note',
  'state.readonly': 'web-mine.js — the read-only note, New sandbox disabled, no row actions, Terminal and Sharing disabled, files without Upload, New folder and Remove (model/mine.js myRows filesWhy, termWhy, shareWhy)',
  'state.refresh': 'web.js — ↻ and the 10 s reload while visible',
};
