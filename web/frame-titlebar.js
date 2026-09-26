/**
 * frame-titlebar.js — the floating terminal window's title bar and tools
 * row, extracted from bx-frame (markup and styles: the frame is at its size
 * budget). `f` is the BxFrame; this reads its state and calls its handlers.
 *
 * Both tab kinds — a shell (kind:"shell") and an agent (kind:"agent", D74)
 * — share one bar: the layout switcher (code, logs, PRs beside either), the
 * net/API/GPU pickers, the tile layer's base update / reset. The pickers
 * are fixed when a sandbox starts: a change restarts a shell, and restarts
 * an agent resuming its conversation (frame-launcher.js restartAgent). An
 * ended or read-only (history) agent tab has no sandbox: layer buttons only. The bar DEGRADES, it never clips:
 * when the pop is narrower than the bar's content (f._narrow — below
 * ~640 px, or the phone sheet) the layout switcher and the pickers move
 * into a tools row behind a "⋯" toggle, and the tab strip scrolls, so the
 * tabs and the window's ✕ are always reachable. `+` opens a launcher menu
 * (Bash, or a coding agent). An ended agent tab (its session gone,
 * transcript kept) is greyed and dismissed with its ✕.
 */
import { html, css, nothing } from 'lit';
import { scopeIcon } from '/vendor/bx-netrules.js';

export function titlebar(f) {
  return html`
    <div class="titlebar" @pointerdown=${(e) => f._dragStart(e)}>
      <span class="path" title=${f.src}>${f.src}</span>
      <span class="tabs">
        ${f._sessions.map((s, i) => html`
          <span class="tab ${i === f._active ? 'on' : ''} ${s.kind === 'agent' ? 'agent' : ''} ${s.ended ? 'ended' : ''}"
                @click=${() => f._setActive(i)}
                @dblclick=${() => f._renameTerm(i)}
                title=${tabTitle(s)}>
            <span class="lbl">${tabLabel(s, i)}</span>
            <button class="tabx" title=${s.ended ? 'dismiss (the session has ended)' : `close this ${s.kind === 'agent' ? 'agent' : 'terminal'}`}
                    @click=${(e) => { e.stopPropagation(); f._closeTerm(i); }}>✕</button>
          </span>`)}
      </span>
      <button class="mknew" title="new session (Bash, or a coding agent)" @click=${(e) => f._openLauncher(e)}>+</button>
      ${f._narrow
        ? html`<button class="more ${f._tools ? 'on' : ''}" title="layout and session settings"
                  @click=${() => { f._tools = !f._tools; }}>⋯</button>`
        : html`${layoutGroup(f)}<span class="spacer"></span>${settings(f)}`}
      <button class="winx" title="close (session keeps running)"
              @click=${() => { f._termOpen = false; }}>✕</button>
    </div>`;
}

// toolsRow: where the layout switcher and the pickers live when the bar is
// narrow (rendered by the frame under the title bar while f._tools is on).
export function toolsRow(f) {
  return html`<div class="toolsrow">${layoutGroup(f)}<span class="spacer"></span>${settings(f)}</div>`;
}

// the right side of the bar: the pickers for a tab with a (live or
// restarting) sandbox, just the layer buttons for an ended / past agent tab
function settings(f) {
  const cur = f._sessions[f._active];
  return cur && (cur.ended || cur.history) ? layerButtons(f) : pickers(f);
}

function tabLabel(s, i) {
  if (s.kind === 'agent') return s.name || s.provider || 'Agent';
  return s.name || 'Bash';
}

function tabTitle(s) {
  const t = s.name ? `${s.name} — double-click to rename` : 'double-click to rename';
  return s.ended ? `ended · ${t}` : t;
}

function layoutGroup(f) {
  return html`
    <span class="lyt">
      <button class=${f._layout === 'term' ? 'on' : ''} title=${f._isAgent ? 'agent only' : 'terminal only'}
              @click=${() => f._setLayout('term')}>&gt;_</button>
      <button class=${f._layout === 'code' ? 'on' : ''} title="code browser + review"
              @click=${() => f._setLayout('code')}>{ }</button>
      <button class=${f._layout === 'split' ? 'on' : ''} title=${f._isAgent ? 'code + agent side by side' : 'code + terminal side by side'}
              @click=${() => f._setLayout('split')}>⇋</button>
      <button class=${f._layout === 'logs' ? 'on' : ''} title="backend logs (read-only)"
              @click=${() => f._setLayout('logs')}>▤</button>
      <button class=${f._layout === 'prs' ? 'on' : ''}
              title="change proposals — patches other tiles' agents suggested for this one"
              @click=${() => f._setLayout('prs')}>⇄${f._prCount ? ` ${f._prCount}` : ''}</button>
    </span>`;
}

// The pickers restart the session (netns/relay, device binds and the token
// are fixed at spawn), so each change asks first; a declined change snaps
// the select back to the tab's value (f._set* resolve false).
function pickers(f) {
  const cur = f._sessions[f._active];
  // a VM can't share the host network (the guest sits behind the relay)
  const scopes = (cur?.scopes ?? [
    { id: 'internet', label: 'internet' }, { id: 'host', label: 'host net' }, { id: 'none', label: 'offline' }])
    .filter((s) => !(cur?.vm && s.id === 'host'));
  const now = scopes.find((s) => s.id === (cur?.net || scopes[0].id)) ?? scopes[0];
  const api = cur?.api === false ? 'off' : 'on';
  const gpu = cur?.gpu || 'none';
  const restarts = `switching restarts the ${f._isAgent ? 'agent (its conversation resumes)' : 'terminal'}`;
  return html`
    <select class="scope"
            title=${`network scope (${restarts})` + (now?.desc ? '\n' + now.desc : '')}
            .value=${now.id}
            @change=${async (e) => { if (!(await f._setNet(f._active, e.target.value))) e.target.value = now.id; }}>
      ${scopes.map((s) => html`<option value=${s.id} title=${s.desc ?? ''}>${scopeIcon(s.id)} ${s.label}</option>`)}
    </select>
    <select class="scope" title=${`live tile API access — off = the ${f._isAgent ? 'agent' : 'shell'} can read/edit code but every API call is unauthorized (${restarts})`}
            .value=${api}
            @change=${async (e) => { if (!(await f._setApi(f._active, e.target.value === 'on'))) e.target.value = api; }}>
      <option value="on">🔌 tile API</option>
      <option value="off">⛔ no API</option>
    </select>
    ${vmToggle(f, restarts)}
    ${f._gpus.length && !cur?.vm ? html`
      <select class="scope" title=${`GPU (${restarts})`}
              .value=${gpu}
              @change=${async (e) => { if (!(await f._setGpu(f._active, e.target.value))) e.target.value = gpu; }}>
        <option value="none">no GPU</option>
        ${f._gpus.map((g) => html`<option value=${g.index}>🎮 GPU ${g.index}</option>`)}
        ${f._gpus.length > 1 ? html`<option value="all">🎮 all</option>` : nothing}
      </select>` : nothing}
    ${layerButtons(f)}`;
}

// The VM toggle (D89): the session restarts as a Firecracker
// microVM — root in its own kernel, the same files and network scope. Shown
// disabled with the reason when this host or the workspace policy can't run
// one (GET /ws/term/env's vm block, loaded with the tile state); a host
// without KVM emulates the VM, and the tooltip says it is slower.
function vmToggle(f, restarts) {
  const cur = f._sessions[f._active];
  const st = f._vmStatus;
  if (!cur || !st) return nothing;
  const on = !!cur.vm;
  const who = f._isAgent ? 'agent' : 'shell';
  const size = (st.memMiB ? ` (${st.memMiB} MiB, ${st.vcpus} vCPU)` : '')
    + (st.emulated ? ' — emulated: this host has no KVM, so it runs several times slower' : '');
  const tip = on ? `VM sandbox: this ${who} is root in its own kernel${size} — click to leave the VM (${restarts})`
    : st.available ? `run this ${who} in a VM sandbox: root in its own kernel${size}, the same files and network (${restarts})`
      : `VM sandbox unavailable: ${st.reason}`;
  const patch = { vm: !on };
  if (!on && cur.net === 'host') patch.net = null; // back to the tile's default scope
  return html`<button class=${'vm' + (on ? ' on' : '')} ?disabled=${!on && !st.available} title=${tip}
      @click=${() => f._respawn(f._active, patch, on ? 'outside the VM' : 'in a VM sandbox')}>⧉ VM</button>`;
}

// The tile's persistent terminal layer — shared by its shells and agents:
// rebuild it on a newer base image (offered when it is outdated), or reset it.
function layerButtons(f) {
  const cur = f._sessions[f._active];
  return html`
    ${cur?.baseOutdated || f._envOld ? html`
      <button class="upgrade" title="a newer base image is installed — rebuild this tile's terminals on it (installed packages are wiped; your files & $HOME are kept)"
              @click=${() => f._resetEnv(true)}>⬆ base update</button>` : nothing}
    <button title="reset this component's sandbox (wipe installed packages)"
            @click=${() => f._resetEnv(false)}>⟲</button>`;
}

// The bar's styles (adopted by bx-frame alongside its own).
export const titlebarCss = css`
  .titlebar {
    display: flex; align-items: center; gap: 2px;
    background: var(--bx-panel-2, #2b3038);
    border-bottom: 1px solid var(--bx-border, #363c45);
    padding: 3px 6px; user-select: none; cursor: grab;
    touch-action: none; flex: none;
    /* one row, never clipped: the tab strip absorbs a tight width */
    flex-wrap: nowrap; overflow: hidden; min-width: 0;
  }
  .titlebar:active { cursor: grabbing; }
  .titlebar .path {
    color: var(--bx-text, #d4d9e0); font-weight: 600;
    font: 11px var(--bx-mono, ui-monospace, monospace);
    padding: 0 8px 0 4px; white-space: nowrap;
    overflow: hidden; text-overflow: ellipsis; flex: 0 1 auto; min-width: 48px;
  }
  .titlebar .tabs {
    display: flex; align-items: center; gap: 2px;
    flex: 0 1 auto; min-width: 0;
  }
  .titlebar .spacer, .toolsrow .spacer { flex: 1; }
  .titlebar button, .toolsrow button {
    border: 1px solid transparent; background: transparent;
    color: var(--bx-muted, #868f9a);
    font: 11px var(--bx-mono, ui-monospace, monospace); padding: 1px 7px;
    border-radius: 4px; cursor: pointer; white-space: nowrap; flex: none;
  }
  .titlebar button.on, .toolsrow button.on {
    background: var(--bx-panel, #23272e);
    border-color: var(--bx-border, #363c45);
    color: var(--bx-text, #d4d9e0);
  }
  .titlebar button:hover, .toolsrow button:hover { color: var(--bx-text, #d4d9e0); }
  .titlebar button:disabled, .toolsrow button:disabled { opacity: .45; cursor: default; }
  .titlebar button.vm.on, .toolsrow button.vm.on { color: var(--bx-accent, #7fb4ff); }
  .titlebar button.upgrade, .toolsrow button.upgrade {
    color: #23272e; background: var(--bx-amber, #f2a71b); font-weight: 600;
    border-radius: 5px; padding: 1px 8px; white-space: nowrap;
    /* the one button that gives way (to "⬆…") before the window's ✕ is pushed out */
    flex: 0 1 auto; min-width: 26px; overflow: hidden; text-overflow: ellipsis;
  }
  .titlebar button.upgrade:hover, .toolsrow button.upgrade:hover { color: #23272e; filter: brightness(1.06); }
  /* Tabs are spans (not buttons) so each can hold a close button — nested
     buttons are invalid HTML. Styled like the titlebar buttons. */
  .titlebar .tab {
    display: inline-flex; align-items: center; gap: 2px; max-width: 150px; min-width: 0; flex: 0 1 auto;
    border: 1px solid transparent; border-radius: 4px; padding: 1px 3px 1px 7px;
    color: var(--bx-muted, #868f9a);
    font: 11px var(--bx-mono, ui-monospace, monospace); cursor: pointer;
  }
  .titlebar .tab.on {
    background: var(--bx-panel, #23272e);
    border-color: var(--bx-border, #363c45);
    color: var(--bx-text, #d4d9e0);
  }
  .titlebar .tab:hover { color: var(--bx-text, #d4d9e0); }
  .titlebar .tab .lbl { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .titlebar .tab .tabx {
    flex: none; padding: 0 3px; border: 0; border-radius: 3px;
    background: transparent; color: inherit; opacity: .45;
    font-size: 12px; line-height: 1; cursor: pointer;
  }
  .titlebar .tab .tabx:hover { opacity: 1; background: var(--bx-border, #363c45); }
  /* Agent tabs read as agents without an emoji: an accent left edge + the
     accent colour on the label; an ended one is greyed and struck. */
  .titlebar .tab.agent { border-left: 2px solid var(--bx-accent, #f5a623); padding-left: 5px; }
  .titlebar .tab.agent.on .lbl, .titlebar .tab.agent:hover .lbl { color: var(--bx-accent, #f5a623); }
  .titlebar .tab.ended { opacity: .55; }
  .titlebar .tab.ended .lbl { text-decoration: line-through; }
  .titlebar button.mknew { color: var(--bx-accent, #f5a623); font-weight: 700; }
  select.scope {
    margin-left: 2px; border: 1px solid var(--bx-border, #363c45);
    background: var(--bx-panel, #23272e); color: var(--bx-text, #d4d9e0);
    font: 11px var(--bx-mono, ui-monospace, monospace);
    padding: 1px 4px; border-radius: 4px; cursor: pointer; flex: none;
    /* the org scope's label names the sets ("org network (devs-net + …)")
       — cap it so the API/GPU pickers stay on the bar; the tooltip has it all */
    max-width: 24ch; text-overflow: ellipsis;
  }
  .toolsrow {
    display: flex; align-items: center; gap: 2px; flex-wrap: wrap;
    background: var(--bx-panel-2, #2b3038);
    border-bottom: 1px solid var(--bx-border, #363c45);
    padding: 3px 6px; flex: none;
  }
  .lyt { display: inline-flex; margin-left: 2px; flex: none; }
  .lyt button { padding: 1px 6px; }
  .lyt button.on { background: var(--bx-panel, #23272e); border-color: var(--bx-border, #363c45); color: var(--bx-text, #d4d9e0); }
`;
