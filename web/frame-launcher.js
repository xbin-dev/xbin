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
 * (bx-agent). Where VM sandboxes can run, the chooser also sets whether new
 * sessions on the tile start in one — a per-user, per-tile choice
 * (wantVM) that the title bar's ⧉ VM toggle updates too. The tile-wide live
 * reload state (frame-deploy.js) loads with the rest of the tile state, and
 * the chooser leads with its banner while live reload is paused.
 */
import { html, css, nothing } from 'lit';
import { uid, makeStore } from '/vendor/term-sessions.js';
import { loadDeployIfShown, launchBanner, launchTarget, targetQuery, targetRefused } from '/vendor/frame-deploy.js';

const prefs = makeStore();

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
// before any terminal is open (GET /ws/term/env); f._envAuto when the
// workspace's base auto-update is on, so the chooser says the next session
// moves to it instead (D175). With the history, the tile state the window
// refreshes on open and on every directory change; the layer's state alone
// again on a `workspace-settings` event (an admin turned it on or off).
export const envStatus = (cwd) => fetch(`/ws/term/env?cwd=${encodeURIComponent(cwd)}`).then((r) => (r.ok ? r.json() : {})).catch(() => ({}));
export function loadEnvStatus(f) {
  // vm: whether a VM terminal can open here, and why not (the title bar's toggle)
  envStatus(f.src).then((s) => {
    if (f.isConnected) { f._vmStatus = s.vm || null; f._envOld = !!s.baseOutdated; f._envAuto = !!s.baseAutoUpdate; f.requestUpdate(); }
  });
}
export function loadTileState(f) {
  loadHistory(f);
  loadEnvStatus(f);
  prefs.loadVM(f.src).then((on) => { if (f.isConnected) { f._vmPref = on; f.requestUpdate(); } });
  loadDeployIfShown(f); // the live reload state: the chip, the offer, the banner below
}

// wantVM: whether a new session on this tile starts in a VM sandbox — the
// user's remembered choice for the tile, while VMs can run here at all.
export const wantVM = (f) => !!(f._vmPref && f._vmStatus?.available);
// rememberVM records that choice (the launcher's switch, the title bar's toggle).
export function rememberVM(f, on) {
  f._vmPref = on;
  prefs.saveVM(f.src, on);
  f.requestUpdate();
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
  f._revealTerm(); // the terminal host shows (a panel beside it stays)
  f._sessions = [...f._sessions, { key: uid(), id: null, kind: 'agent', history: row.id, name: row.name || '', ended: true }];
  f._setActive(f._sessions.length - 1);
}

// resumeHistory reopens a past session live (POST /term/sessions {resume}:
// the agent replays the earlier turns, then continues). replaceKey swaps the
// read-only tab it was being viewed in for the live one, in place.
export function resumeHistory(f, row, replaceKey) {
  const tab = { key: uid(), id: null, kind: 'agent', provider: row.provider, resume: row.id, name: row.name || '', vm: wantVM(f) };
  f._revealTerm(); // the terminal host shows (a panel beside it stays)
  const i = replaceKey ? f._sessions.findIndex((t) => t.key === replaceKey) : -1;
  f._sessions = i >= 0 ? f._sessions.map((t, j) => (j === i ? tab : t)) : [...f._sessions, tab];
  f._setActive(i >= 0 ? i : f._sessions.length - 1);
}

// restartAgent: an agent tab's network / tile-API / GPU changed. They are
// fixed when its sandbox starts, so — like a shell — it restarts; unlike a
// shell it keeps its conversation: the server ends the session and opens a
// new one that resumes it where the agent can (POST …/restart). The tab
// turns PENDING (new key, no id) at once, so a listing arriving meanwhile
// absorbs the new row into it (term-sessions.js) instead of a ghost tab;
// its <bx-agent> shows "restarting" and creates nothing (bx-frame withholds
// the provider). A tab whose target changed restarts onto it (?deployment=);
// one the server doesn't echo ends (targetRefused, 11-contract §7.4).
// Resolves false when declined (the picker snaps back).
export async function restartAgent(f, i, patch, what) {
  const cur = f._sessions[i];
  if (!cur?.id || cur.ended) return false;
  const ok = await f._confirm(`Restart this agent ${what}?`,
    'Its sandbox restarts with the new setting. The conversation continues where the agent can reopen its own session (Claude Code, OpenCode); otherwise it starts fresh, and this conversation stays under Recent sessions.',
    'Restart');
  if (!ok) return false;
  const key = uid();
  f._sessions = f._sessions.map((t, j) => (j === i ? { ...t, ...patch, key, id: null, restarting: true } : t));
  f._setActive(i);
  const want = { ...cur, ...patch };
  try {
    const r = await fetch(`/api/xbin/term/sessions/${encodeURIComponent(cur.id)}/restart${targetQuery(want)}`, {
      method: 'POST', headers: { 'content-type': 'application/json' },
      body: JSON.stringify({ net: want.net || undefined, api: want.api !== false, gpu: want.gpu || 'none', vm: !!want.vm }),
    });
    const j = await r.json().catch(() => ({}));
    if (!r.ok) throw new Error(j.error || `restart failed (${r.status})`);
    const s = j.session;
    const refused = targetRefused(f, want, s);
    if (refused) throw new Error(refused);
    f._sessions = f._sessions
      .filter((t) => t.id !== s.id || t.key === key) // a listing may already have shown the new row
      .map((t) => (t.key === key ? { ...t, id: s.id, restarting: false, net: t.net ?? s.net ?? null, scopes: s.scopes ?? t.scopes, label: s.label || '' } : t));
    f._reindex();
  } catch (e) {
    // ended, with its (now gone) id: a pending-looking tab would absorb an unrelated row
    f._sessions = f._sessions.map((t) => (t.key === key ? { ...t, id: cur.id, restarting: false, ended: true } : t));
    f._confirm('The agent could not restart', String(e.message || e), 'OK');
  }
  return true;
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
    items.push({ label: `${label(last)}  ·  last`, icon: 'refresh', action: () => start(last) });
    items.push({ kind: 'sep' });
  }
  items.push({ label: 'Bash', icon: 'terminal', action: () => f._startKind('shell') });
  for (const p of provs) items.push({ label: p.name, icon: 'agent', action: () => f._startKind('agent', p.id) });
  const recent = (f._history || []).slice(0, 8);
  if (recent.length) {
    items.push({ kind: 'sep' });
    items.push({ label: 'Recent sessions', items: recent.map((r) => ({ label: rowTitle(r), hint: `${provName(f, r.provider)} · ${ago(r.ended)}`, action: () => openHistory(f, r) })) });
  }
  return items;
}

// The empty-window launcher: a card per session kind (Bash + each agent
// provider) — the same set as the + menu — and the tile's recent sessions;
// where VMs can run, the switch for starting them in a VM sandbox.
export function launcher(f) {
  const provs = f._providers || [];
  const vm = wantVM(f);
  const card = (label, sub, onClick, icon) => html`<button class="lcard" @click=${onClick}>
    <span class="lname">${icon ? html`<bx-icon name=${icon}></bx-icon>` : nothing}${label}</span>${sub ? html`<span class="lsub">${sub}</span>` : nothing}</button>`;
  const recent = (f._history || []).slice(0, 8);
  const target = launchTarget(f) ? ` ${launchTarget(f)}` : ''; // what a new session calls (10-ux §2.7); '' in the zero state
  return html`<div class="launcher">
    <div class="lhead">Start a session in ${f.src}</div>
    ${f._envOld && f._envAuto ? html`<div class="lbase auto">
      <span>A newer base image is installed: the next session here starts on it, and everything outside your files &amp; $HOME is reset (installed packages, /etc, /var, /opt…, a VM terminal's disk).</span>
    </div>` : f._envOld ? html`<div class="lbase">
      <bx-icon name="info" label="Info"></bx-icon>
      <span>This tile's terminals still run on an older base image.</span>
      <button class="lupdate" title="rebuild this tile's terminal layer on the newer base (installed packages are wiped; your files & $HOME are kept)"
              @click=${() => f._resetEnv(true)}><bx-icon name="upload"></bx-icon>base update</button>
    </div>` : nothing}
    ${launchBanner(f)}
    ${vmSwitch(f, vm)}
    <div class="lcards">
      ${card('Bash', (vm ? 'a shell in a VM sandbox' : 'a shell in the sandbox') + target, () => f._startKind('shell'), 'terminal')}
      ${provs.length
        ? provs.map((p) => card(p.name, (vm ? 'coding agent, in a VM' : 'coding agent') + target, () => f._startKind('agent', p.id), 'agent'))
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

// vmSwitch: the launcher's VM sandbox choice for the tile — offered only
// where VMs can run (the title bar's toggle says why not elsewhere).
function vmSwitch(f, on) {
  const st = f._vmStatus;
  if (!st?.available) return nothing;
  const size = st.memMiB ? ` · ${st.memMiB} MiB, ${st.vcpus} vCPU` : '';
  return html`<button class=${'lvm' + (on ? ' on' : '')} role="switch" aria-checked=${on ? 'true' : 'false'}
      title="start this tile's new sessions — shells and agents — in a VM sandbox; remembered for this tile"
      @click=${() => rememberVM(f, !on)}>
    <span class="lname"><bx-icon name="vm"></bx-icon>VM sandbox: ${on ? 'on' : 'off'}</span>
    <span class="lsub">root in its own kernel${size}${st.emulated ? ' · emulated — several times slower' : ''}</span></button>`;
}

export const launcherCss = css`
  .launcher { flex: 1; display: flex; flex-direction: column; align-items: center; justify-content: center; gap: 16px; padding: 20px; overflow: auto;
    color: var(--bx-text, #E9EAF0); background: var(--bx-panel, #1F2028); font: var(--bx-font-ui, 400 13px/18px system-ui, sans-serif); }
  .launcher .lhead { color: var(--bx-muted, #A3A6B6); font: var(--bx-font-code, 12px/18px ui-monospace, monospace); }
  .launcher .lrecent > .lhead { font: var(--bx-font-micro, 600 11px/14px system-ui, sans-serif); letter-spacing: var(--bx-tracking-micro, 0.06em); text-transform: uppercase; }
  .launcher .lcards { display: flex; flex-wrap: wrap; gap: 8px; justify-content: center; max-width: 460px; }
  .launcher .lcard { display: flex; flex-direction: column; gap: 4px; align-items: flex-start; min-width: 140px;
    padding: 12px; border: 1px solid var(--bx-border-strong, #666A7E); border-radius: var(--bx-radius, 2px);
    background: var(--bx-panel, #1F2028); color: var(--bx-text, #E9EAF0); cursor: pointer; text-align: left; }
  .launcher .lcard:hover { background: var(--bx-hover, #2A2B34); }
  .launcher .lname { display: inline-flex; align-items: center; gap: 6px; font-weight: 600; }
  .launcher .lname bx-icon { color: var(--bx-muted, #A3A6B6); }
  .launcher .lsub { color: var(--bx-muted, #A3A6B6); font: var(--bx-font-meta, 400 12px/16px system-ui, sans-serif); }
  .launcher .lrecent { display: flex; flex-direction: column; gap: 6px; width: min(460px, 100%); }
  .launcher .lrow { display: flex; align-items: center; gap: 8px; }
  .launcher .lopen { flex: 1; min-width: 0; display: flex; flex-direction: column; gap: 2px; align-items: flex-start; padding: 6px 10px;
    border: 1px solid var(--bx-border, #33353F); border-radius: var(--bx-radius, 2px); background: var(--bx-panel, #1F2028);
    color: var(--bx-text, #E9EAF0); cursor: pointer; text-align: left; }
  .launcher .lopen:hover { background: var(--bx-hover, #2A2B34); }
  .launcher .lopen .lname { display: block; font-weight: 400; max-width: 100%; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .launcher .lresume { box-sizing: border-box; height: var(--bx-control-h, 28px); padding: 0 10px; cursor: pointer; font-weight: 600; white-space: nowrap;
    color: var(--bx-text, #E9EAF0); background: var(--bx-panel, #1F2028);
    border: 1px solid var(--bx-border-strong, #666A7E); border-radius: var(--bx-radius, 2px); }
  .launcher .lresume:hover { background: var(--bx-hover, #2A2B34); }
  .launcher .lbase { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; justify-content: center; max-width: 460px;
    padding: 8px 12px; border: 1px solid var(--bx-border-strong, #666A7E); border-radius: var(--bx-radius, 2px);
    background: var(--bx-info-bg, #30323B); color: var(--bx-text, #E9EAF0); }
  .launcher .lbase > bx-icon { color: var(--bx-info, #A9B4C6); }
  .launcher .lbase.auto { border-color: var(--bx-border, #33353F); background: var(--bx-panel-2, #262730); color: var(--bx-muted, #A3A6B6); }
  .launcher .lupdate { display: inline-flex; align-items: center; gap: 6px; box-sizing: border-box; height: var(--bx-control-h, 28px); padding: 0 10px;
    cursor: pointer; font-weight: 600; white-space: nowrap;
    color: var(--bx-accent-ink, #0B0C12); background: var(--bx-accent, #8C9BFF);
    border: 1px solid var(--bx-accent, #8C9BFF); border-radius: var(--bx-radius, 2px); }
  .launcher .lupdate:hover { background: var(--bx-accent-hover, #A9B4FF); border-color: var(--bx-accent-hover, #A9B4FF); }
  /* the VM sandbox choice: a switch; on, it is the selection */
  .launcher .lvm { display: flex; flex-direction: column; gap: 2px; align-items: center; padding: 6px 14px;
    border: 1px solid var(--bx-border-strong, #666A7E); border-radius: var(--bx-radius, 2px); background: var(--bx-panel, #1F2028);
    color: var(--bx-text, #E9EAF0); cursor: pointer; }
  .launcher .lvm:hover { background: var(--bx-hover, #2A2B34); }
  .launcher .lvm.on { border-color: var(--bx-accent, #8C9BFF); background: var(--bx-selection, #262C5C); color: var(--bx-selection-text, #E9EAF0); }
`;
