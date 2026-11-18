/**
 * shell-agent.js — the shell's one hook for the workspace agent (D232): the
 * top-bar chip, and the dock beside the canvas.
 *
 * Everything the chip opens is xbind's own chrome, served with xbind
 * (/vendor/bx-agent-dock.js and what it loads): this module only decides
 * whether the chip shows, counts what waits, and makes room. It loads
 * nothing of the agent until the chip is pressed, and on an xbind from
 * before the workspace agent (no /vendor/agent-chrome-words.js, no
 * /api/xbin/delegate/state) it draws nothing at all.
 *
 *   followAgent(shell)  start: read the state, follow the `delegate` events;
 *                       → stop()
 *   agentChip(shell)    the chip, left of `settings`: hidden for a person
 *                       with no agent (and for everyone but admins before
 *                       setup — an admin's reads "set up workspace agent");
 *                       its count is what waits for the person; a pause
 *                       glyph while their agent is paused
 *   agentDock(shell)    the dock, after <main>: it pushes the canvas; on a
 *                       narrow screen it is the whole page
 *   openAgent(shell, {tile})  open it about a tile
 *   agentTileItems(shell, path, items)  the tile menu with "Ask my agent
 *                       about this tile" in it (the last of the tile's own
 *                       lines, before the workspace section):
 *                       the dock, opened about that tile — not where the
 *                       chip is hidden, before setup or while turned off
 *
 * Ctrl+J / ⌘J toggles the dock unless the person is typing somewhere (a
 * terminal, a field): there the keys are theirs.
 */
import { html, nothing } from 'lit';
import { onEvent } from '/vendor/events-socket.js';
import { deepActive } from '/vendor/bx-kit.js';

const get = (path) => fetch('/api/xbin' + path, { credentials: 'same-origin' }).then((r) => (r.ok ? r.json() : null)).catch(() => null);

/** shownAlert(a): false for the one alert that only names /xbin/agent (kind agent-page) — for shells without this chip. */
export const shownAlert = (a) => !a || a.kind !== 'agent-page';

export function followAgent(shell) {
  const a = shell._agent = { words: null, state: null, llm: null, open: false, loaded: false, context: null };
  const read = async () => {
    if (!a.words) return;
    [a.state, a.llm] = await Promise.all([get('/delegate/state'), get('/llm')]);
    shell.requestUpdate();
  };
  // xbind's own words for the chip: an xbind without them has no workspace agent, and the shell stays as it was
  import('/vendor/agent-chrome-words.js').then((m) => { a.words = m; read(); }, () => {});
  const off = onEvent((e) => { if (e && e.type === 'delegate') read(); });
  const key = (e) => {
    if (!a.words || !(e.ctrlKey || e.metaKey) || e.shiftKey || e.altKey || e.key.toLowerCase() !== a.words.CHOICES.shortcut.key) return;
    const at = deepActive();
    if (at && (at.isContentEditable || /^(input|textarea|select)$/i.test(at.localName) || at.closest?.('.xterm'))) return; // typing: the keys are theirs
    if (!chipOf(shell).shown) return;
    e.preventDefault();
    openAgent(shell, about(shell), !a.open);
  };
  window.addEventListener('keydown', key);
  return () => { off(); window.removeEventListener('keydown', key); };
}

// what the person has in front when they open the dock: the active tile's window, if any (the page is told: "about <tile>")
const about = (shell) => (typeof shell._front === 'string' && shell._front.startsWith('tile:') ? { tile: shell._front.slice(5) } : null);

const chipOf = (shell) => {
  const a = shell._agent;
  if (!a || !a.words) return { shown: false };
  return a.words.chip({ state: a.state, llm: a.llm, me: shell._who, count: a.words.needCount(a.state) });
};

/** openAgent(shell, {tile} | {screen} | null, open = true): show the dock (about a tile or a screen, when one is named), or close it. */
export async function openAgent(shell, context = null, open = true) {
  const a = shell._agent;
  if (!a) return;
  if (open && !a.loaded) {
    try { await import('/vendor/bx-agent-dock.js'); a.loaded = true; } catch { return; } // an older xbind: nothing to open
  }
  a.open = open;
  if (open) a.context = context;
  shell.requestUpdate();
}

export function agentTileItems(shell, path, items) {
  const a = shell._agent, c = chipOf(shell);
  if (!c.shown || !a.words) return items;
  const st = a.words.dockState({ state: a.state, llm: a.llm, me: shell._who });
  if (st === 'setup' || st === 'off') return items; // no agent to ask yet, or none now
  const item = { icon: 'agent', label: 'Ask my agent about this tile', action: () => openAgent(shell, { tile: path }) };
  const at = items.findIndex((x) => x.kind === 'header' && (x.label === 'workspace' || x.label === 'admin')); // the tile's own lines end there (D247)
  const out = [...items];
  if (at > 0 && out[at - 1].kind === 'sep') out.splice(at - 1, 0, item); else out.push(item);
  return out;
}

export function agentChip(shell) {
  const c = chipOf(shell), a = shell._agent;
  if (!c.shown) return nothing;
  return html`<button class="chip agent ${a.open ? 'on' : ''}" title=${c.spoken} aria-label=${c.spoken} aria-pressed=${a.open ? 'true' : 'false'}
      @click=${() => openAgent(shell, about(shell), !a.open)}><bx-icon name="agent"></bx-icon>${shell._mobile ? nothing : c.label}${c.paused ? html`<bx-icon name="pause" label="paused"></bx-icon>`
        : c.count ? html`<span class="agent-count" style="display:inline-flex;align-items:center;justify-content:center;min-width:16px;height:16px;padding:0 4px;margin-left:6px;background:var(--bx-accent, #8C9BFF);color:var(--bx-accent-ink, #0B0C12);font:var(--bx-font-micro, 600 11px/14px system-ui, sans-serif)">${c.count}</span>` : nothing}</button>`;
}

// a waiting tile agent (the tray's row, or the agent's page asking): the
// shell lands on the tile and its session as for a link (shell-route.js
// takes bx-open-session; a tile the person doesn't list is refused there)
const openSession = (d) => { if (d && d.tile && d.session) window.dispatchEvent(new CustomEvent('bx-open-session', { cancelable: true, detail: { from: 'agent', tile: d.tile, session: d.session } })); };

// a new session of a tile's own agent with the person's words in its
// composer, unsent (the agent's page: "Hand this to <tile>'s own agent"):
// taken here — the dock shows a plain link to a shell that doesn't — and
// landed on as a session link is, the tile's window opening on a new
// session's launcher (bx-frame open({session: "new", draft}))
const newSession = (e) => {
  const d = e.detail;
  if (!d || !d.tile) return;
  e.preventDefault();
  window.dispatchEvent(new CustomEvent('bx-open-session', { cancelable: true, detail: { from: 'agent', tile: d.tile, session: 'new', draft: typeof d.draft === 'string' ? d.draft : '' } }));
};

export function agentDock(shell) {
  const a = shell._agent;
  if (!a || !a.loaded) return nothing;
  // on a narrow screen the dock is the whole page, over the canvas
  const full = !!shell._mobile;
  return html`<bx-agent-dock ?open=${a.open} ?full=${full} .context=${a.context} style=${full ? 'position:absolute;inset:0;z-index:40' : nothing}
    @bx-agent-dock=${(e) => { if (a.open !== e.detail.open) { a.open = e.detail.open; shell.requestUpdate(); } }}
    @bx-agent-open-session=${(e) => { openSession(e.detail); if (full) openAgent(shell, null, false); }}
    @bx-agent-new-session=${(e) => { newSession(e); if (full && e.defaultPrevented) openAgent(shell, null, false); }}></bx-agent-dock>`;
}
