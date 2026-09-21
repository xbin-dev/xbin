/**
 * frame-launcher.js — how you start a session in the terminal window,
 * extracted from bx-frame (markup, styles and the per-browser helpers: the
 * frame is at its size budget). One `+` menu and a first-open card chooser
 * both offer the same set — Bash, or one of the coding-agent providers.
 * `f` is the BxFrame; this reads its state and calls its handlers. Picking a
 * provider eager-creates the agent session so its model/mode pickers load
 * before the first prompt (bx-agent).
 */
import { html, css, nothing } from 'lit';

// The agent providers offered in the launcher (id,name,login,modes,…),
// fetched once and shared across all frames (like the GPU inventory).
let _providersP = null;
export const agentProviders = () => (_providersP ||= fetch('/api/xbin/agent/providers').then((r) => (r.ok ? r.json() : [])).catch(() => []));

// LAST_KIND: the launcher's remembered choice (per browser).
const LAST_KIND = 'bx-term-lastkind';
export const lastKind = () => { try { return JSON.parse(localStorage.getItem(LAST_KIND) || 'null'); } catch { return null; } };
export const rememberKind = (k) => { try { localStorage.setItem(LAST_KIND, JSON.stringify(k)); } catch { } };

// the launcher menu items (used by the + button and the empty-state cards):
// last choice on top, then Bash and one entry per agent provider.
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
  return items;
}

// The empty-window launcher: a card per session kind (Bash + each agent
// provider). Clicking a card starts that kind — the same set as the + menu.
export function launcher(f) {
  const provs = f._providers || [];
  const card = (label, sub, onClick) => html`<button class="lcard" @click=${onClick}>
    <span class="lname">${label}</span>${sub ? html`<span class="lsub">${sub}</span>` : nothing}</button>`;
  return html`<div class="launcher">
    <div class="lhead">Start a session in ${f.src}</div>
    <div class="lcards">
      ${card('Bash', 'a shell in the sandbox', () => f._startKind('shell'))}
      ${provs.length
        ? provs.map((p) => card(p.name, 'coding agent', () => f._startKind('agent', p.id)))
        : html`<span class="lsub">loading agents…</span>`}
    </div>
  </div>`;
}

export const launcherCss = css`
  .launcher { flex: 1; display: flex; flex-direction: column; align-items: center; justify-content: center; gap: 14px; padding: 20px; }
  .launcher .lhead { color: var(--bx-muted, #868f9a); font: 12px var(--bx-mono, ui-monospace, monospace); }
  .launcher .lcards { display: flex; flex-wrap: wrap; gap: 10px; justify-content: center; max-width: 460px; }
  .launcher .lcard { display: flex; flex-direction: column; gap: 3px; align-items: flex-start; min-width: 130px;
    padding: 12px 14px; border: 1px solid var(--bx-border, #363c45); border-radius: 8px;
    background: var(--bx-panel-2, #2b3038); color: var(--bx-text, #d4d9e0); cursor: pointer; text-align: left; }
  .launcher .lcard:hover { border-color: var(--bx-accent, #f5a623); }
  .launcher .lname { font: 13px var(--bx-sans, system-ui); font-weight: 600; }
  .launcher .lsub { color: var(--bx-muted, #868f9a); font-size: 11px; }
`;
