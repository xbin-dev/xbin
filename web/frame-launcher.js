/**
 * frame-launcher.js — how you start a session in the terminal window,
 * extracted from bx-frame (markup, styles and the per-browser helpers: the
 * frame is at its size budget). One `+` menu and a first-open card chooser
 * both offer the same set — Bash, or one of the coding-agent providers — plus
 * the tile's RECENT agent sessions: the transcripts kept when a session
 * ended (GET /agent/history). Open one read-only, or resume it where the
 * agent can reopen its own session (`loadable`). `f` is the BxFrame; this
 * reads its state and calls its handlers. Picking a provider eager-creates
 * the agent session so its model/mode pickers load before the first prompt
 * (bx-agent).
 */
import { html, css, nothing } from 'lit';
import { uid } from '/vendor/term-sessions.js';

// The agent providers offered in the launcher (id,name,login,modes,…),
// fetched once and shared across all frames (like the GPU inventory).
let _providersP = null;
export const agentProviders = () => (_providersP ||= fetch('/api/xbin/agent/providers').then((r) => (r.ok ? r.json() : [])).catch(() => []));

// The tile's past agent sessions, newest first — per frame, refetched when
// the directory changes (a session that ended is history now).
export const agentHistory = (cwd) => fetch(`/api/xbin/agent/history?cwd=${encodeURIComponent(cwd)}`).then((r) => (r.ok ? r.json() : [])).catch(() => []);
export function loadHistory(f) { agentHistory(f.src).then((h) => { if (f.isConnected) f._history = h; }); }

// The tile's persistent terminal layer: f._envOld when it was built on an
// older base image — the chooser and the title bar offer the base update
// before any terminal is open (GET /ws/term/env). With the history, the
// tile state the window refreshes on open and on every directory change.
export const envStatus = (cwd) => fetch(`/ws/term/env?cwd=${encodeURIComponent(cwd)}`).then((r) => (r.ok ? r.json() : {})).catch(() => ({}));
export function loadTileState(f) {
  loadHistory(f);
  envStatus(f.src).then((s) => { if (f.isConnected) f._envOld = !!s.baseOutdated; });
}

// LAST_KIND: the launcher's remembered choice (per browser).
const LAST_KIND = 'bx-term-lastkind';
export const lastKind = () => { try { return JSON.parse(localStorage.getItem(LAST_KIND) || 'null'); } catch { return null; } };
export const rememberKind = (k) => { try { localStorage.setItem(LAST_KIND, JSON.stringify(k)); } catch { } };

// ago: a short relative time for a listing ("now", "3m", "2h", "4d").
function ago(iso) {
  const s = Math.max(0, (Date.now() - Date.parse(iso)) / 1000);
  if (s < 60) return 'now';
  if (s < 3600) return `${Math.floor(s / 60)}m ago`;
  if (s < 86400) return `${Math.floor(s / 3600)}h ago`;
  return `${Math.floor(s / 86400)}d ago`;
}
const rowTitle = (r) => r.name || r.preview || `${r.provider} session`;
const provName = (f, id) => (f._providers || []).find((p) => p.id === id)?.name || id;

// openHistory shows a past session read-only: a <bx-agent> in history mode
// (no process, no id — never absorbed into a live row, term-sessions.js).
export function openHistory(f, row) {
  f._layout = 'term';
  f._sessions = [...f._sessions, { key: uid(), id: null, kind: 'agent', history: row.id, name: row.name || '', ended: true }];
  f._setActive(f._sessions.length - 1);
}

// resumeHistory reopens a past session live (POST /term/sessions {resume}:
// the agent replays the earlier turns, then continues). replaceKey swaps the
// read-only tab it was being viewed in for the live one, in place.
export function resumeHistory(f, row, replaceKey) {
  const tab = { key: uid(), id: null, kind: 'agent', provider: row.provider, resume: row.id, name: row.name || '' };
  f._layout = 'term';
  const i = replaceKey ? f._sessions.findIndex((t) => t.key === replaceKey) : -1;
  f._sessions = i >= 0 ? f._sessions.map((t, j) => (j === i ? tab : t)) : [...f._sessions, tab];
  f._setActive(i >= 0 ? i : f._sessions.length - 1);
}

// the launcher menu items (used by the + button and the empty-state cards):
// last choice on top, then Bash and one entry per agent provider, then the
// recent sessions as a submenu.
export function launcherItems(f) {
  const provs = f._providers || [];
  const label = (k) => (k.kind === 'shell' ? 'Bash' : (provs.find((p) => p.id === k.provider)?.name || k.provider || 'Agent'));
  const start = (k) => (k.kind === 'shell' ? f._startKind('shell') : f._startKind('agent', k.provider));
  const items = [];
  const last = lastKind();
  if (last && (last.kind === 'shell' || provs.some((p) => p.id === last.provider))) {
    items.push({ label: `${label(last)}  ·  last`, icon: '↺', action: () => start(last) });
    items.push({ kind: 'sep' });
  }
  items.push({ label: 'Bash', mono: true, action: () => f._startKind('shell') });
  for (const p of provs) items.push({ label: p.name, action: () => f._startKind('agent', p.id) });
  const recent = (f._history || []).slice(0, 8);
  if (recent.length) {
    items.push({ kind: 'sep' });
    items.push({ label: 'Recent sessions', items: recent.map((r) => ({ label: rowTitle(r), hint: `${provName(f, r.provider)} · ${ago(r.ended)}`, action: () => openHistory(f, r) })) });
  }
  return items;
}

// The empty-window launcher: a card per session kind (Bash + each agent
// provider) — the same set as the + menu — and the tile's recent sessions.
export function launcher(f) {
  const provs = f._providers || [];
  const card = (label, sub, onClick) => html`<button class="lcard" @click=${onClick}>
    <span class="lname">${label}</span>${sub ? html`<span class="lsub">${sub}</span>` : nothing}</button>`;
  const recent = (f._history || []).slice(0, 8);
  return html`<div class="launcher">
    <div class="lhead">Start a session in ${f.src}</div>
    ${f._envOld ? html`<div class="lbase">
      <span>This tile's terminals still run on an older base image.</span>
      <button class="lupdate" title="rebuild this tile's terminal layer on the newer base (installed packages are wiped; your files & $HOME are kept)"
              @click=${() => f._resetEnv(true)}>⬆ base update</button>
    </div>` : nothing}
    <div class="lcards">
      ${card('Bash', 'a shell in the sandbox', () => f._startKind('shell'))}
      ${provs.length
        ? provs.map((p) => card(p.name, 'coding agent', () => f._startKind('agent', p.id)))
        : html`<span class="lsub">loading agents…</span>`}
    </div>
    ${recent.length ? html`<div class="lrecent">
      <div class="lhead">Recent sessions</div>
      ${recent.map((r) => html`<div class="lrow">
        <button class="lopen" title="open the transcript (read-only)" @click=${() => openHistory(f, r)}>
          <span class="lname">${rowTitle(r)}</span>
          <span class="lsub">${provName(f, r.provider)} · ${r.turns} turn${r.turns === 1 ? '' : 's'} · ${ago(r.ended)}</span></button>
        ${r.loadable
          ? html`<button class="lresume" title="continue this conversation: the agent reopens it and replays the turns" @click=${() => resumeHistory(f, r)}>Resume</button>`
          : html`<span class="lsub" title="this agent cannot reopen a session">read-only</span>`}
      </div>`)}
    </div>` : nothing}
  </div>`;
}

export const launcherCss = css`
  .launcher { flex: 1; display: flex; flex-direction: column; align-items: center; justify-content: center; gap: 14px; padding: 20px; overflow: auto; }
  .launcher .lhead { color: var(--bx-muted, #868f9a); font: 12px var(--bx-mono, ui-monospace, monospace); }
  .launcher .lcards { display: flex; flex-wrap: wrap; gap: 10px; justify-content: center; max-width: 460px; }
  .launcher .lcard { display: flex; flex-direction: column; gap: 3px; align-items: flex-start; min-width: 130px;
    padding: 12px 14px; border: 1px solid var(--bx-border, #363c45); border-radius: 8px;
    background: var(--bx-panel-2, #2b3038); color: var(--bx-text, #d4d9e0); cursor: pointer; text-align: left; }
  .launcher .lcard:hover { border-color: var(--bx-accent, #f5a623); }
  .launcher .lname { font: 13px var(--bx-sans, system-ui); font-weight: 600; }
  .launcher .lsub { color: var(--bx-muted, #868f9a); font-size: 11px; }
  .launcher .lrecent { display: flex; flex-direction: column; gap: 6px; width: min(460px, 100%); }
  .launcher .lrow { display: flex; align-items: center; gap: 8px; }
  .launcher .lopen { flex: 1; min-width: 0; display: flex; flex-direction: column; gap: 2px; align-items: flex-start; padding: 8px 10px;
    border: 1px solid var(--bx-border, #363c45); border-radius: 6px; background: var(--bx-panel-2, #2b3038);
    color: var(--bx-text, #d4d9e0); cursor: pointer; text-align: left; }
  .launcher .lopen:hover { border-color: var(--bx-accent, #f5a623); }
  .launcher .lopen .lname { font-weight: 500; max-width: 100%; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .launcher .lresume { border: 1px solid var(--bx-accent, #f5a623); background: transparent; color: var(--bx-accent, #f5a623);
    border-radius: 6px; padding: 6px 10px; cursor: pointer; font: 12px var(--bx-sans, system-ui); font-weight: 600; white-space: nowrap; }
  .launcher .lresume:hover { background: var(--bx-accent, #f5a623); color: #1b1e24; }
  .launcher .lbase { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; justify-content: center; max-width: 460px;
    padding: 8px 10px; border: 1px solid var(--bx-amber, #f2a71b); border-radius: 6px; background: var(--bx-panel-2, #2b3038);
    color: var(--bx-text, #d4d9e0); font-size: 12px; }
  .launcher .lupdate { border: 1px solid var(--bx-amber, #f2a71b); background: var(--bx-amber, #f2a71b); color: #23272e;
    border-radius: 5px; padding: 4px 10px; cursor: pointer; font: 12px var(--bx-sans, system-ui); font-weight: 700; white-space: nowrap; }
  .launcher .lupdate:hover { filter: brightness(1.06); }
`;
