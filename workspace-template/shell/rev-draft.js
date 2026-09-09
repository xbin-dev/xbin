// shell/rev-draft.js — the revisioned-draft flow shared by org screens,
// shared sidebar folders and share-to-org (D55): edit a copy, publish
// explicitly naming the revision the copy was based on; a stale save comes
// back 409 and the human picks (keep editing / reload theirs / overwrite).
// Plain helpers over plain objects — the shell keeps the draft maps as
// reactive state and applies the outcome; no element state, no lit, so
// hack/rev-draft.test.mjs runs them under node.

// ago('2026-09-05T10:11:12Z') → 'just now' | '3 min ago' | '2 h ago' | '4 d ago' ('' when unknown).
export function ago(iso, now = Date.now()) {
  if (!iso) return '';
  const s = Math.max(0, (now - Date.parse(iso)) / 1000);
  if (!(s >= 0)) return '';
  if (s < 60) return 'just now';
  if (s < 3600) return `${Math.floor(s / 60)} min ago`;
  if (s < 86400) return `${Math.floor(s / 3600)} h ago`;
  return `${Math.floor(s / 86400)} d ago`;
}

// A draft is the editable copy plus the revision it was taken from; dirty
// flips on the first gesture. The maps are treated as immutable so lit sees
// the change.
export const newDraft = (seed, baseRev) => ({ ...seed, baseRev, dirty: false });
export const withDraft = (map, id, draft) => ({ ...(map ?? {}), [id]: draft });
export function withoutDraft(map, id) { const { [id]: _, ...rest } = map ?? {}; return rest; }

// publish(url, body): PUT the draft and classify the answer —
//   { status: 'ok', body }                       saved; body carries rev/updatedBy/updatedAt
//   { status: 'conflict', body, message }        409: someone saved first; body.rev + the live entry
//   { status: 'error', message, body }           refused
//   { status: 'offline', message }               no answer
export async function publish(url, body, fetchFn = fetch) {
  try {
    const r = await fetchFn(url, { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
    const d = await r.json().catch(() => ({}));
    if (r.status === 409) return { status: 'conflict', body: d, message: d.error ?? 'saved by someone else first' };
    if (!r.ok) return { status: 'error', message: d.error ?? `save failed (${r.status})`, body: d };
    return { status: 'ok', body: d };
  } catch { return { status: 'offline', message: 'offline — try again' }; }
}

// conflictDialog: the bx-dialog spec for "someone saved this first". `by` is
// already a label ("you" / an id / "someone"); replace = a share-to-org
// replacing an existing screen (no draft to keep).
export function conflictDialog({ what, rev, by, at, baseRev, replace = false, now }) {
  return replace ? {
    title: 'Someone saved this first',
    message: `${what} is now at rev ${rev}, saved by ${by} ${ago(at, now)}; you were replacing rev ${baseRev}.`,
    buttons: [{ label: 'Cancel', value: null }, { label: 'Reload theirs', value: 'reload' }, { label: 'Replace anyway', value: 'force', danger: true }],
  } : {
    title: 'Someone saved this first',
    message: `${what} is now at rev ${rev}, saved by ${by} ${ago(at, now)}. Your draft is based on rev ${baseRev ?? '?'}.`,
    buttons: [
      { label: 'Keep editing', value: null },
      { label: 'Reload theirs (drop my draft)', value: 'reload' },
      { label: 'Overwrite with mine', value: 'force', danger: true },
    ],
  };
}
