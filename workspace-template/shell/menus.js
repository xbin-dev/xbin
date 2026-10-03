// shell/menus.js — the shell's context-menu item lists (bx-menu items, see
// web/bx-menu.js) as pure functions over a plain view of the shell's state
// plus an actions object. bx-shell.js builds both (_menuState/_menuActions)
// and renders the result; keeping the builders free of element state is
// what lets `make check` unit-test them (hack/menus.test.mjs): which lines
// an org screen's draft state shows, what "Open tile" lists and disables,
// what the admin block offers per lifecycle state. An item's icon is a
// glyph name of /vendor/bx-icons.js (D184), which bx-menu draws.
//
// state:
//   orgScreen   the active org screen {id, canEdit} or null (personal screen)
//   draft       its draft {dirty} or null
//   owners      [{value, label}] the caller may create tiles for (shell._ownerOptions)
//   components  [{path, state, template, owner}] from /components
//   tiles       [{path, float}] open on this screen
//   recent      recently opened paths, newest first
//   showHidden  the sidebar's show-hidden toggle (D42)
//   canMutate   the screen accepts layout changes (personal, or an org draft)
//   prs         {path: open change proposals}
//   canAdminTile(path)  ws-admin, org admin of the owner org, or user-owner (D24)
// actions (all fire AFTER bx-menu has closed):
//   enterEdit(id) saveOrgDraft(id) discardDraft(id) copyOrgScreen(id)
//   newTileDialog(name, error, owner, {fixed}) addScreen() fitWindows(persist)
//   openTile(path) toggle(path) togglePin(path) frameOpen(path, layout)
//   openFullPage(path) lifecycle(path, state) openAdminWin(path, section)
//   confirm(message) → boolean
// optional in state:
//   deployState(path, c)  a tile's deployments state (below); absent, the
//                         lookup shell-kit.js installs answers

// Lifecycle predicates over a /components entry (shared with the sidebar).
export const offloaded = (c) => c?.state === 'offloaded' || c?.state === 'offloaded-full';
export const hidden = (c) => c?.state === 'hidden';

// ---- tile deployments (docs/tile-deployments.md) ----
// Optional: an xbind without them sends none of this, and every menu is
// today's. A tile with a deployment record carries the primary summary on
// its /components row, `deployments: {primary, pinned, protected}`, the same
// for everyone who sees the row. The rest (the checkpoint a pinned primary
// runs, what the viewer may do) is the tile's deployments state, GET
// /api/xbin/deployments?tile=: undefined while not loaded, null when this
// xbind couldn't answer, else the state in the viewer's view.
let deployLookup = () => undefined;
// useDeployLookup(fn): shell-kit.js's per-page store answers fn(path, c),
// loading a state the first time a tile with a summary asks.
export const useDeployLookup = (fn) => { deployLookup = fn; };

const primaryOf = (st) => (st?.deployments || []).find((d) => d.name === (st.primary || 'main')) || null;
// deploySummary(c, state) → the primary summary, from the state once it is
// loaded (it is fresher than the row), else the row's; null for a tile
// without a record.
export function deploySummary(c, st) {
  if (!st) return c?.deployments || null;
  if (!st.record) return null;
  return { primary: st.primary || 'main', pinned: primaryOf(st)?.liveReload === false, protected: !!st.protectedPrimary };
}
// deployCheckpoint(state) → the checkpoint the primary is pinned to ('' unknown)
export const deployCheckpoint = (st) => primaryOf(st)?.checkpoint?.id || '';
// deployFailed(state) → the primary's last deploy failed
export const deployFailed = (st) => primaryOf(st)?.lastDeploy?.result === 'failed';
// deployHint(summary, state) → the primary's state in a few words
export function deployHint(sum, st) {
  const P = sum.primary || 'main', cp = deployCheckpoint(st);
  if (!sum.pinned) return `${P} follows the work tree`;
  return cp ? `${P} pinned to ${cp}` : `${P} pinned`;
}
// A viewer the state gives only the primary's facts (read access) has
// nothing to operate; while the state isn't known the window decides.
const mayOperate = (st) => !st || (st.view !== 'reader' && st.caller?.level !== 'read');

// ---- the deployment a tile's window shows (docs/tile-deployments.md) ----
// deployNames(state) → what a window may show, the primary first: every
// deployment the viewer's state lists ([] while unknown; a reader's state
// names the primary only, so it offers nothing to switch to).
export function deployNames(st) {
  if (!st?.record) return [];
  const P = st.primary || 'main', ds = (st.deployments || []).map((d) => d.name).filter(Boolean);
  return [P, ...ds.filter((n) => n !== P)];
}
// shownDeployment(want, state) → the non-primary deployment a window shows,
// or '' (the primary): a name the state no longer lists, or the primary's own
// (its alias), shows the primary; while the state is unknown the layout wins.
export function shownDeployment(want, st) {
  if (!want || st === undefined) return want || '';
  const P = st?.primary || 'main';
  return want !== P && deployNames(st).includes(want) ? want : '';
}
// deployMenu(path, c, state, shown, a) → the window head's ⇈ menu: which
// deployment the window shows (a pick; the primary follows the role, so a
// reassignment moves it), dev's full page, and the Deployments panel.
// a: {show(name|''), openPanel(), openPage(ref)}.
export function deployMenu(path, c, st, shown, a) {
  const sum = deploySummary(c, st), P = sum?.primary || 'main', names = deployNames(st);
  const byName = new Map((st?.deployments || []).map((d) => [d.name, d]));
  const items = [{ kind: 'header', label: 'this window shows' }];
  for (const n of names.length ? names : [P]) {
    const d = byName.get(n), cp = d?.checkpoint?.id || '';
    const hint = n === P ? `primary${sum?.pinned ? ` · ${cp ? `pinned to ${cp}` : 'pinned'}` : ''}`
      : st?.liveReload === n ? 'live reload' : cp ? `pinned to ${cp}` : '';
    items.push({ label: n, mono: true, hint, checked: (shown || P) === n, action: () => a.show(n === P ? '' : n) });
  }
  if (shown) items.push({ icon: 'popout', label: `Open ${path}+${shown} full page`, action: () => a.openPage(`${path}+${shown}`) });
  if (sum && mayOperate(st)) items.push({ kind: 'sep' }, { icon: 'deploy', label: 'Deployments…', hint: deployHint(sum, st), action: a.openPanel });
  return items;
}

const tidy = (l) => l.replace(/^— | —$/g, '');
const base = (p) => p.slice(p.lastIndexOf('/') + 1);
const dir = (p) => (p.includes('/') ? p.slice(0, p.lastIndexOf('/')) : '');

// The canvas (background) menu: org-screen draft lines, open tile, create,
// new screen, bring windows on-screen.
export function canvasMenuItems(s, a) {
  const items = [];
  const os = s.orgScreen;
  if (os) {
    const d = s.draft;
    if (!d && os.canEdit) items.push({ icon: 'pencil', label: 'Edit this org screen', action: () => a.enterEdit(os.id) });
    if (d) {
      items.push({ icon: 'save', label: 'Save and update for everyone', disabled: !d.dirty, action: () => a.saveOrgDraft(os.id) });
      items.push({ icon: 'refresh', label: 'Discard draft', action: () => a.discardDraft(os.id) });
    }
    items.push({ icon: 'copy', label: 'Copy to my screens', action: () => a.copyOrgScreen(os.id) });
    items.push({ kind: 'sep' });
  }
  items.push({ icon: 'window', label: 'Open tile', items: openTileItems(s, a) });
  const owners = s.owners ?? [];
  if (owners.length >= 2) {
    items.push({ icon: 'plus', label: 'Create a new tile', items: owners.map((o) => ({
      label: tidy(o.label), action: () => a.newTileDialog('', '', o.value, { fixed: true }) })) });
  } else if (owners.length === 1) {
    items.push({ icon: 'plus', label: 'Create a new tile…', hint: tidy(owners[0].label),
      action: () => a.newTileDialog('', '', owners[0].value, { fixed: true }) });
  } else {
    items.push({ icon: 'plus', label: 'Create a new tile…', disabled: true, hint: s.ownerHint ?? 'org-only policy — ask an org admin' });
  }
  items.push({ icon: 'split', label: 'New screen', action: () => a.addScreen() });
  items.push({ kind: 'sep' });
  items.push({ icon: 'restore', label: 'Bring windows on-screen', hint: 'pop-ups, floats', action: () => a.fitWindows(true) });
  return items;
}

// "Open tile ▸": a find box, the five most recent tiles that aren't on this
// screen yet, and every other readable tile behind the filter.
export function openTileItems(s, a) {
  const readable = (s.components ?? []).filter((c) => c.path !== 'root' && !c.template && !offloaded(c)
    && !(hidden(c) && !s.showHidden));
  const byPath = new Map(readable.map((c) => [c.path, c]));
  const open = new Set((s.tiles ?? []).map((o) => o.path));
  const recent = (s.recent ?? []).filter((p) => byPath.has(p) && !open.has(p)).slice(0, 5);
  const row = (p, extra = {}) => ({ label: base(p), keywords: p, hint: dir(p), mono: true, action: () => a.openTile(p), ...extra });
  const rest = readable.filter((c) => !recent.includes(c.path)).sort((x, y) => base(x.path).localeCompare(base(y.path)));
  return [
    { kind: 'input', placeholder: 'find a tile…', empty: 'no tile matches', hint: 'type to find a tile — recently opened ones list here' },
    ...(recent.length ? [{ kind: 'header', label: 'recent' }, ...recent.map((p) => row(p))] : []),
    ...rest.map((c) => row(c.path, open.has(c.path) ? { quiet: true, disabled: true, hint: 'open' } : { quiet: true })),
  ];
}

// The tile menu (card head, sidebar row, or relayed from inside the tile):
// the four panels, screen actions, the admin lines.
export function tileMenuItems(path, s, a) {
  const c = (s.components ?? []).find((x) => x.path === path);
  const state = c?.state ?? 'enabled';
  const tile = (s.tiles ?? []).find((o) => o.path === path);
  const open = !!tile;
  const os = s.orgScreen;
  const draft = os ? s.draft : null;
  const items = [{ kind: 'grid', cells: [
    { icon: 'terminal', label: 'terminal', title: `terminal on ${path}`, action: () => a.frameOpen(path, 'term') },
    { icon: 'list', label: 'logs', title: 'backend logs', action: () => a.frameOpen(path, 'logs') },
    { icon: 'code', label: 'source', title: 'code browser + review', action: () => a.frameOpen(path, 'code') },
    { icon: 'diff', label: 'proposals', badge: s.prs?.[path] || null, title: 'change proposals from other tiles', action: () => a.frameOpen(path, 'prs') },
  ] }, { kind: 'sep' }];
  if (open) {
    items.push({ icon: 'xmark', label: 'Close on this screen', disabled: !s.canMutate, hint: s.canMutate ? '' : 'view mode',
      action: () => a.toggle(path) });
    items.push({ icon: tile?.float ? 'maximize' : 'restore', label: tile?.float ? 'Pin to the grid' : 'Unpin into a window',
      disabled: !s.canMutate, action: () => a.togglePin(path) });
  } else {
    items.push({ icon: 'plus', label: os && !os.canEdit ? 'Open on my screen' : os && !draft ? 'Open here (starts a draft)' : 'Open on this screen',
      action: () => a.openTile(path) });
  }
  items.push({ icon: 'popout', label: 'Open full page', action: () => a.openFullPage(path) });
  // One line, never a fifth square (the phone sheet's grid is four columns):
  // the terminal window's Deployments layout, for a tile with a record.
  const st = (s.deployState ?? deployLookup)(path, c);
  const dsum = deploySummary(c, st);
  if (dsum && mayOperate(st)) {
    items.push({ icon: 'deploy', label: 'Deployments…', hint: deployHint(dsum, st), action: () => a.frameOpen(path, 'deployments') });
  }
  if (s.canAdminTile?.(path)) {
    items.push({ kind: 'sep' }, { kind: 'header', label: 'admin' });
    if (state !== 'enabled') {
      items.push({ icon: 'play', label: state === 'hidden' ? 'Unhide' : 'Enable', action: () => a.lifecycle(path, 'enabled') });
    } else {
      items.push({ icon: 'pause', label: 'Disable', danger: true,
        action: () => a.confirm(`Disable ${path}? Its backend stops now.`) && a.lifecycle(path, 'disabled') });
    }
    if (state !== 'hidden' && !offloaded(c)) {
      items.push({ icon: 'eye-slash', label: 'Hide', danger: true,
        action: () => a.confirm(`Hide ${path}? It is disabled and drops out of sidebars until unhidden.`) && a.lifecycle(path, 'hidden') });
    }
    for (const [sec, label] of [['access', 'Access…'], ['runtime', 'Runtime…'], ['vault', 'Vault…'], ['grants', 'Roles & grants…'],
      ['interfaces', 'Interfaces…'], ['backup', 'Backup…'], ['cron', 'Cron…']]) {
      items.push({ icon: sec === 'access' ? 'settings' : '', label, action: () => a.openAdminWin(path, sec) });
    }
  }
  return items;
}
