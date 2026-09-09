// shell/menus.js — the shell's context-menu item lists (bx-menu items, see
// web/bx-menu.js) as pure functions over a plain view of the shell's state
// plus an actions object. bx-shell.js builds both (_menuState/_menuActions)
// and renders the result; keeping the builders free of element state is
// what lets `make check` unit-test them (hack/menus.test.mjs): which lines
// an org screen's draft state shows, what "Open tile" lists and disables,
// what the admin block offers per lifecycle state.
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
//   newTileDialog(name, message, owner, {fixed}) addScreen() fitWindows(persist)
//   openTile(path) toggle(path) togglePin(path) frameOpen(path, layout)
//   openFullPage(path) lifecycle(path, state) openAdminWin(path, section)
//   confirm(message) → boolean

// Lifecycle predicates over a /components entry (shared with the sidebar).
export const offloaded = (c) => c?.state === 'offloaded' || c?.state === 'offloaded-full';
export const hidden = (c) => c?.state === 'hidden';

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
    if (!d && os.canEdit) items.push({ icon: '✎', label: 'Edit this org screen', action: () => a.enterEdit(os.id) });
    if (d) {
      items.push({ icon: '💾', label: 'Save and update for everyone', disabled: !d.dirty, action: () => a.saveOrgDraft(os.id) });
      items.push({ icon: '↺', label: 'Discard draft', action: () => a.discardDraft(os.id) });
    }
    items.push({ icon: '⧉', label: 'Copy to my screens', action: () => a.copyOrgScreen(os.id) });
    items.push({ kind: 'sep' });
  }
  items.push({ icon: '▸', label: 'Open tile', items: openTileItems(s, a) });
  const owners = s.owners ?? [];
  if (owners.length >= 2) {
    items.push({ icon: '✦', label: 'Create a new tile', items: owners.map((o) => ({
      label: tidy(o.label), action: () => a.newTileDialog('', '', o.value, { fixed: true }) })) });
  } else if (owners.length === 1) {
    items.push({ icon: '✦', label: 'Create a new tile…', hint: tidy(owners[0].label),
      action: () => a.newTileDialog('', '', owners[0].value, { fixed: true }) });
  } else {
    items.push({ icon: '✦', label: 'Create a new tile…', disabled: true, hint: 'org-only policy — ask an org admin' });
  }
  items.push({ icon: '▦', label: 'New screen', action: () => a.addScreen() });
  items.push({ kind: 'sep' });
  items.push({ icon: '⧉', label: 'Bring windows on-screen', hint: 'pop-ups, floats', action: () => a.fitWindows(true) });
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
    { icon: '>_', mono: true, label: 'terminal', title: `terminal on ${path}`, action: () => a.frameOpen(path, 'term') },
    { icon: '▤', label: 'logs', title: 'backend logs', action: () => a.frameOpen(path, 'logs') },
    { icon: '{ }', mono: true, label: 'source', title: 'code browser + review', action: () => a.frameOpen(path, 'code') },
    { icon: '⇄', label: 'proposals', badge: s.prs?.[path] || null, title: 'change proposals from other tiles', action: () => a.frameOpen(path, 'prs') },
  ] }, { kind: 'sep' }];
  if (open) {
    items.push({ icon: '✕', label: 'Close on this screen', disabled: !s.canMutate, hint: s.canMutate ? '' : 'view mode',
      action: () => a.toggle(path) });
    items.push({ icon: tile?.float ? '▣' : '⧉', label: tile?.float ? 'Pin to the grid' : 'Unpin into a window',
      disabled: !s.canMutate, action: () => a.togglePin(path) });
  } else {
    items.push({ icon: '▢', label: os && !os.canEdit ? 'Open on my screen' : os && !draft ? 'Open here (starts a draft)' : 'Open on this screen',
      action: () => a.openTile(path) });
  }
  items.push({ icon: '⤢', label: 'Open full page', action: () => a.openFullPage(path) });
  if (s.canAdminTile?.(path)) {
    items.push({ kind: 'sep' }, { kind: 'header', label: 'admin' });
    if (state !== 'enabled') {
      items.push({ icon: '▶', label: state === 'hidden' ? 'Unhide' : 'Enable', action: () => a.lifecycle(path, 'enabled') });
    } else {
      items.push({ icon: '⏸', label: 'Disable', danger: true,
        action: () => a.confirm(`Disable ${path}? Its backend stops now.`) && a.lifecycle(path, 'disabled') });
    }
    if (state !== 'hidden' && !offloaded(c)) {
      items.push({ icon: '⊘', label: 'Hide', danger: true,
        action: () => a.confirm(`Hide ${path}? It is disabled and drops out of sidebars until unhidden.`) && a.lifecycle(path, 'hidden') });
    }
    for (const [sec, label] of [['access', 'Access…'], ['runtime', 'Runtime…'], ['vault', 'Vault…'], ['grants', 'Roles & grants…'],
      ['interfaces', 'Interfaces…'], ['backup', 'Backup…'], ['cron', 'Cron…']]) {
      items.push({ icon: sec === 'access' ? '⚙' : '', label, action: () => a.openAdminWin(path, sec) });
    }
  }
  return items;
}
