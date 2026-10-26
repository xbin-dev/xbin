/**
 * agent-queue.js — the Agent tab's messages while a turn runs (D197), and
 * the composer's height.
 *
 * While a turn runs Send stays beside Stop. A message goes to the running
 * turn where the agent takes messages mid-turn (Claude Code, Codex: at its
 * next step — the transcript shows it, marked); otherwise, or behind
 * messages already waiting, it is held by xbind and goes in when the turn
 * ends, the waiting ones together as one message. The held messages are
 * the session's (POST/DELETE …/queue; their `queue` events fold into the
 * status digest, agent-fold.js), so every client shows the same chips over
 * the composer: × takes one back into the composer, Stop takes them all
 * back before it cancels, and Ctrl/⌘+Enter holds the message and cancels
 * the turn, so it goes in straight after.
 *
 * fitComposer: the textarea is as tall as its text — its border counted, so
 * a paste never leaves a sliver to scroll — up to 40% of the tab (the
 * window it sits in, not the browser's), and shrinks again when the text
 * goes (a send, a slash pick, a follow-up).
 */
import { html, nothing } from 'lit';

export const queueURL = (a, id = '') => `/api/xbin/term/sessions/${encodeURIComponent(a.session)}/queue${id ? '/' + encodeURIComponent(id) : ''}`;

// takeBack(a, id): held messages back into the composer, before what is
// typed there (id '' = all of them). One already sent or taken elsewhere
// (404) is simply gone.
export async function takeBack(a, id = '') {
  try {
    const r = await fetch(queueURL(a, id), { method: 'DELETE' });
    if (!r.ok) {
      if (r.status !== 404) a._error = (await r.json().catch(() => ({}))).error || `could not take it back (${r.status})`;
      return;
    }
    const texts = ((await r.json()).items || []).map((q) => q.text).filter(Boolean);
    if (texts.length) a._draft = [...texts, a._draft].filter(Boolean).join('\n\n');
    a._refetch();
  } catch (e) { a._error = String(e.message || e); }
}

// stop(a): Stop — what waits comes back into the composer, then the turn
// is cancelled (so nothing the person queued goes in behind their back).
export async function stop(a) {
  if ((a._tx.st.queue || []).length) await takeBack(a);
  a._cancel();
}

// queueChips(a): the held messages over the composer.
export function queueChips(a) {
  const q = a._tx.st.queue || [];
  if (!q.length || a.ended || a.history) return nothing;
  return html`<div class="queued" role="list" aria-label="messages waiting for the turn to end">
    ${q.map((m) => html`<span class="qchip" role="listitem" title=${m.text}>
      <span class="qt">${m.text || (m.files || []).join(', ')}</span>
      ${m.files?.length ? html`<span class="qf">+${m.files.length} ${m.files.length === 1 ? 'file' : 'files'}</span>` : nothing}
      <button class="qx" title="take it back into the message box" aria-label="take it back" @click=${() => takeBack(a, m.id)}><bx-icon name="xmark"></bx-icon></button>
    </span>`)}
    <span class="qhint">${q.length === 1 ? 'goes in' : 'go in together'} when the turn ends</span>
  </div>`;
}

// fitComposer(a, ta): see the file comment.
export function fitComposer(a, ta) {
  if (!ta) return;
  ta.style.height = 'auto';
  const border = ta.offsetHeight - ta.clientHeight; // border-box: the border (and a scrollbar) the content height leaves out
  const host = a.getBoundingClientRect().height || window.innerHeight;
  const cap = Math.max(48, host * 0.4);
  const want = ta.scrollHeight + border;
  ta.style.height = Math.min(want, cap) + 'px';
  ta.style.overflowY = want > cap ? 'auto' : 'hidden';
}
