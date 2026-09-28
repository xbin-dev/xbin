// shell/layout-sync.js — the shell follows its layout pref when another
// client (the app, another tab) saves it: xbind publishes a `prefs` event to
// the user's own clients on every write (docs/protocol.md), naming the
// bucket (`root`: the shell's) and the key, plus the writer id the client
// sent (X-Prefs-Writer) — the shell skips its own. The layout reloads,
// keeping the screen the tab shows, unless an edit is under way here (a
// drag, a pending save, an org/folder draft, a dialog or menu); then it
// looks again in a second, and a save of this tab's in between wins, as
// before this event existed. Plain helpers over the shell element (passed
// in), so hack/layout-sync.test.mjs runs them under node.

// foreignWrite(e, key, writer): e is a prefs event about the shell's `key`
// that another client wrote.
export const foreignWrite = (e, key, writer) =>
  e?.type === 'prefs' && e.component === 'root' && e.data?.key === key && e.data?.writer !== writer;

// editing(s, doc): the shell s is mid-edit — a reload now would lose it.
export function editing(s, doc = globalThis.document) {
  return !s._layoutLoaded || !!s._saveTimer || !!doc?.querySelector?.('[data-drag-shield]') || !!s._canvas?._drag
    || Object.keys(s._orgDrafts ?? {}).length > 0 || Object.keys(s._folderDrafts ?? {}).length > 0
    || !!(s._create || s._folderEdit || s._conflict || s._menu || s._dialogs?.length);
}

// follow(s, e, key): reload s's layout for the event e now, or once the edit ends.
export function follow(s, e, key) {
  if (!foreignWrite(e, key, s._writer)) return;
  clearTimeout(s._staleTimer);
  if (editing(s)) { s._staleTimer = setTimeout(() => follow(s, e, key), 1000); return; }
  const active = s._active;
  return s._loadLayout().then(() => {
    if (s._screens.some((x) => x.id === active) || (s._orgScreens ?? []).some((x) => x.id === active)) s._active = active;
  });
}
