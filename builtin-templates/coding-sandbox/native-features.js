// native-features.js — what the native view (native.js, native/) implements,
// by feature key (model/features.js), and where. The web view's is
// web-features.js; hack/coding-sandbox-ui.test.mjs holds both against the
// registry. Side-effect free: import it anywhere.
export const IMPLEMENTS = {
  'ops.list': 'native/ops.js — opsSections, the Sandboxes section (model/ops.js sandboxRows)',
  'ops.facts': 'native/ops.js — the row (state, consumer, owner) and opScreen (every fact)',
  'ops.lifecycle': 'native/ops.js — row actions and opScreen buttons (app.opAct)',
  'ops.delete': 'native/ops.js — row actions and opScreen, confirmed',
  'ops.snapshots': 'native/ops.js — opScreen Snapshots (take; delete confirmed)',
  'ops.snapshots.restore': 'native/ops.js — opScreen Snapshots Restore, confirmed',
  'ops.shares': 'native/ops.js — sharesSection',
  'ops.usage': 'native/ops.js — opsSections Usage (model/ops.js usageRows)',
  'ops.orphans': 'native/ops.js — opsSections Orphans, confirmed',
  'ops.backend': 'native/ops.js — opsSections Substrate (model/ops.js backendInfo)',

  'images.list': 'native/images.js — imagesSections (model/ops.js imageRows)',
  'images.build': 'native/images.js — imageScreen Build now / Rebuild',
  'images.log': 'native/images.js — imageScreen, the last build\'s output',
  'images.edit': 'native/images.js — imageFormScreen (model/ops.js imageForm, applyImage)',
  'images.remove': 'native/images.js — imageScreen Remove, confirmed',

  'settings.mode': 'native/settings.js — settingsSections Isolation (model/ops.js modeInfo)',
  'settings.egress': 'native/settings.js — settingsSections Networks',
  'settings.sizes': 'native/settings.js — sizeScreen (model/ops.js applySizes)',
  'settings.quotas': 'native/settings.js — quotaScreen (model/ops.js setQuota)',
  'settings.advanced': 'native/settings.js — advancedScreen (model/ops.js parseMount)',

  'mine.list': 'native/mine.js — mineSections (model/mine.js myRows)',
  'mine.create': 'native/mine.js — createSheet (model/mine.js createForm)',
  'mine.lifecycle': 'native/mine.js — row actions and myScreen, delete confirmed',
  'mine.shares': 'native/mine.js — myScreen → native/ops.js sharesSection',
  'mine.visibility': 'native/mine.js — myScreen Who may use it',

  'files.browse': 'native/mine.js — filesScreen (a pushed screen per directory; model/mine.js fileRows)',
  'files.view': 'native/mine.js — fileScreen',
  'files.download': 'native/mine.js — download (xbin.native.share with the file)',
  'files.mkdir': 'native/mine.js — mkdirSheet',
  'files.remove': 'native/mine.js — filesScreen row actions, confirmed',

  'term.open': 'native/mine.js — openTerminal, termScreen (the app\'s terminal, attached to a tty exec: model/mine.js attachSrc)',
  'term.end': 'native/mine.js — termScreen End; native.js pop → leaveTerminal',

  'state.errors': 'native/ui.js — fail(), act(): a notice on the screen on top',
  'state.reader': 'native.js — rootScreen (no tabs: Yours only)',
  'state.refresh': 'native.js — pull to refresh and the 15 s reload while visible',
};
