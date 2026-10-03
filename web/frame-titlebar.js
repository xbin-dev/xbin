/**
 * frame-titlebar.js — the floating terminal window's title bar and tools
 * row, extracted from bx-frame (markup and styles: the frame is at its size
 * budget). `f` is the BxFrame; this reads its state and calls its handlers.
 *
 * Both tab kinds — a shell (kind:"shell") and an agent (kind:"agent", D74)
 * — share one bar: the layout switcher (code, logs, PRs, deployments, each
 * full width or beside either: ⇋), the
 * net/API/GPU pickers, the tile layer's base update / reset. The pickers
 * are fixed when a sandbox starts: a change restarts a shell, and restarts
 * an agent resuming its conversation (frame-launcher.js restartAgent). An
 * ended or read-only (history) agent tab has no sandbox: layer buttons only. The bar DEGRADES, it never clips:
 * when the pop is narrower than the bar's content (f._narrow — below
 * ~640 px, the phone sheet, or whenever the full bar measures wider than
 * the pop: fitBar) the layout switcher and the pickers move into a tools
 * row behind a "⋯" toggle; tabs shrink to a legible floor, then the tab
 * strip scrolls, so the tabs and the window's ✕ are always reachable. The
 * bar's width depends on the host (a GPU picker, the VM toggle) and the
 * tab, so it is measured, never assumed. `+` opens a launcher menu
 * (Bash, or a coding agent). An ended agent tab (its session gone,
 * transcript kept) is greyed and dismissed with its ✕. The tile's live
 * reload controls (frame-deploy.js) sit among the settings, before the layer
 * buttons: the chip and the Reload now offer on the full bar, the zero
 * state's one entry point wherever the settings are; on the degraded bar the
 * compact chip stays in the title row, so the state never hides behind ⋯.
 * The layout switcher's ⇈ (before ⇋) opens the Deployments panel; once
 * a tile has deployments to choose from, the tile API select lists them as
 * the session's target ("target: dev"), and shows today's two entries
 * otherwise.
 *
 * Base Two (D184): the bar is the window's 28 px title bar — the live
 * square (the frame's buildState), the tile's name and path, text tabs
 * underlined in the accent, then the controls as 28 × 28 squares with
 * 16 px glyphs (/vendor/bx-icons.js), each named for a screen reader.
 * Option text carries no glyph: an <option> can't draw one.
 */
import { html, css, nothing, live } from 'lit';
import '/vendor/bx-icons.js';
import { rememberVM } from '/vendor/frame-launcher.js';
import { barDeploy, titleChip, deployKey, layoutButton, targetSelect } from '/vendor/frame-deploy.js';
import { split, toggleBeside, besideTitle } from '/vendor/frame-panels.js';

// The live square (product-ui 3): the tile's code is live (the accent),
// building (hollow) or its build failed (a danger outline and the word).
const LIVE = { live: 'live', building: 'building', failed: 'build failed' };
function liveSquare(state) {
  const t = LIVE[state] || LIVE.live;
  return html`<span class="lsq ${state}" role="img" aria-label=${t} title=${t}></span>${state === 'failed'
    ? html`<span class="lfail">failed</span>` : nothing}`;
}

export function titlebar(f) {
  const name = f.src.slice(f.src.lastIndexOf('/') + 1);
  return html`
    <div class="titlebar" @pointerdown=${(e) => f._dragStart(e)}>
      ${liveSquare(f.buildState)}
      <span class="path" title=${f.src}><b class="nm">${name}</b><span class="dir">${f.src}</span></span>
      <span class="tabs" @wheel=${scrollTabs}>
        ${f._sessions.map((s, i) => html`
          <span class="tab ${i === f._active ? 'on' : ''} ${s.kind === 'agent' ? 'agent' : ''} ${s.ended ? 'ended' : ''}"
                @click=${() => f._setActive(i)}
                @dblclick=${() => f._renameTerm(i)}
                title=${tabTitle(s)}>
            ${s.kind === 'agent' ? html`<bx-icon name="agent"></bx-icon>` : nothing}
            <span class="lbl">${tabLabel(s, i)}</span>
            <button class="tabx" title=${s.ended ? 'dismiss (the session has ended)' : `close this ${s.kind === 'agent' ? 'agent' : 'terminal'}`}
                    aria-label=${s.ended ? 'dismiss' : `close this ${s.kind === 'agent' ? 'agent' : 'terminal'}`}
                    @click=${(e) => { e.stopPropagation(); f._closeTerm(i); }}><bx-icon name="xmark"></bx-icon></button>
          </span>`)}
      </span>
      <button class="mknew ib" title="new session (Bash, or a coding agent)" aria-label="new session" @click=${(e) => f._openLauncher(e)}><bx-icon name="plus"></bx-icon></button>
      ${f._narrow
        ? html`${titleChip(f)}<button class="more ib ${f._tools ? 'on' : ''}" title="layout and session settings" aria-label="layout and session settings"
                  aria-expanded=${f._tools ? 'true' : 'false'} @click=${() => { f._tools = !f._tools; }}><bx-icon name="ellipsis"></bx-icon></button>`
        : html`${layoutGroup(f)}<span class="spacer"></span>${settings(f)}`}
      <button class="winx ib" title="close (session keeps running)" aria-label="close the window (sessions keep running)"
              @click=${() => { f._termOpen = false; }}><bx-icon name="xmark"></bx-icon></button>
    </div>`;
}

// A mouse wheel over a tab strip that overflows scrolls it sideways.
function scrollTabs(e) {
  const s = e.currentTarget;
  if (s.scrollWidth <= s.clientWidth + 1 || Math.abs(e.deltaX) >= Math.abs(e.deltaY)) return;
  s.scrollLeft += e.deltaY;
  e.preventDefault();
}

// barKey: what the full bar's width depends on. When it changes, the width
// the bar last needed (f._barNeed) is stale and fitBar measures again.
export const barKey = (f) => JSON.stringify([f._active, f._gpus.length, !!f._vmStatus, !!f._envOld, f._prCount || 0,
  f._sessions.map((s) => [s.kind, s.name, s.provider, !!s.ended, !!s.history, !!s.vm, s.net, !!s.baseOutdated, s.api !== false, s.deployment || '']), deployKey(f)]);

// fitBar(f, pop, sheet): whether the bar must degrade (the new f._narrow).
// Under 640 px or on the phone sheet it always does. Otherwise the full bar
// is measured while it shows: when it overflows — the pickers pushed past
// the ✕, or the tabs squeezed below their floor — it degrades, remembering
// the width it needed (f._barNeed), and comes back once the pop is that
// wide. The frame calls it after every render and on every resize, and
// resets f._barNeed when barKey changes; the tab strip then scrolls its
// active tab into view — only when that could have moved it out (another
// active tab, the bar's content or mode, the width, a new strip): a render
// for anything else (a press in the window fronts it, a status or PR count
// arriving) must not undo the user's own scrolling, or a press on a tab
// scrolled into view lands elsewhere by the time it is released.
export function fitBar(f, pop, sheet) {
  const w = pop.offsetWidth, bar = pop.querySelector('.titlebar'), tabs = bar?.querySelector('.tabs');
  let narrow = sheet || w < 640;
  if (!narrow && bar && tabs) {
    if (f._narrow) narrow = w < (f._barNeed || 0);
    else {
      const over = Math.max(0, bar.scrollWidth - bar.clientWidth) + Math.max(0, tabs.scrollWidth - tabs.clientWidth);
      if (over > 1) { f._barNeed = w + over; narrow = true; }
    }
  }
  const on = tabs?.querySelector('.tab.on');
  const why = JSON.stringify([f._sessions[f._active]?.key, !!f._narrow, w, f._barKey]);
  if (on && (tabs !== f._scrollStrip || why !== f._scrollWhy)) {
    f._scrollStrip = tabs;
    f._scrollWhy = why;
    const s = tabs.getBoundingClientRect(), t = on.getBoundingClientRect();
    if (t.left < s.left) tabs.scrollLeft -= s.left - t.left;
    else if (t.right > s.right) tabs.scrollLeft += t.right - s.right;
  }
  return narrow;
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

// The layout switcher: the terminal alone, then one button per panel (a
// panel keeps its place beside the terminal when it has one), then ⇋, the
// toggle that puts the terminal beside the panel (frame-panels.js, D129).
function layoutGroup(f) {
  const beside = split(f);
  const at = (l) => (f._layout === l ? 'on' : '');
  return html`
    <span class="lyt">
      <button class=${at('term')} title=${f._isAgent ? 'agent only' : 'terminal only'} aria-label=${f._isAgent ? 'agent only' : 'terminal only'}
              aria-pressed=${String(f._layout === 'term')} @click=${() => f._setLayout('term')}><bx-icon name="terminal"></bx-icon></button>
      <button class=${at('code')} title="code browser + review" aria-label="code browser and review"
              aria-pressed=${String(f._layout === 'code')} @click=${() => f._setLayout('code')}><bx-icon name="code"></bx-icon></button>
      <button class=${at('logs')} title="backend logs (read-only)" aria-label="backend logs"
              aria-pressed=${String(f._layout === 'logs')} @click=${() => f._setLayout('logs')}><bx-icon name="list"></bx-icon></button>
      <button class=${at('prs')}
              title="change proposals — patches other tiles' agents suggested for this one"
              aria-label=${`change proposals${f._prCount ? ` (${f._prCount} open)` : ''}`}
              aria-pressed=${String(f._layout === 'prs')} @click=${() => f._setLayout('prs')}><bx-icon name="diff"></bx-icon>${f._prCount ? html`<span class="n">${f._prCount}</span>` : ''}</button>
      ${layoutButton(f)}
      <button class=${'beside' + (beside ? ' on' : '')} aria-pressed=${String(beside)} title=${besideTitle(f)} aria-label=${besideTitle(f)}
              @click=${() => toggleBeside(f)}><bx-icon name="split"></bx-icon></button>
    </span>`;
}

// The pickers restart the session (netns/relay, device binds and the token
// are fixed at spawn), so each change asks first; a declined change snaps
// the select back to the tab's value (f._set* resolve false). A picker whose
// options are rendered from data marks its choice on the OPTIONS (live
// .selected): the select's .value is committed before its options exist
// when the select is new (the bar degrading or coming back re-creates it)
// or its options were rebuilt, and the browser then shows the first option.
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
    <select class="scope net"
            title=${`network scope (${restarts})` + (now?.desc ? '\n' + now.desc : '')}
            .value=${now.id}
            @change=${async (e) => { if (!(await f._setNet(f._active, e.target.value))) e.target.value = now.id; }}>
      ${scopes.map((s) => html`<option value=${s.id} title=${s.desc ?? ''} .selected=${live(s.id === now.id)}>${s.label}</option>`)}
    </select>
    ${targetSelect(f, restarts) || html`<select class="scope" title=${`live tile API access — off = the ${f._isAgent ? 'agent' : 'shell'} can read/edit code but every API call is unauthorized (${restarts})`}
            .value=${api}
            @change=${async (e) => { if (!(await f._setApi(f._active, e.target.value))) e.target.value = api; }}>
      <option value="on">tile API</option>
      <option value="off">no API</option>
    </select>`}
    ${vmToggle(f, restarts)}
    ${f._gpus.length && !cur?.vm ? html`
      <select class="scope" title=${`GPU (${restarts})`}
              .value=${gpu}
              @change=${async (e) => { if (!(await f._setGpu(f._active, e.target.value))) e.target.value = gpu; }}>
        <option value="none" .selected=${live(gpu === 'none')}>no GPU</option>
        ${f._gpus.map((g) => html`<option value=${g.index} .selected=${live(String(gpu) === String(g.index))}>GPU ${g.index}</option>`)}
        ${f._gpus.length > 1 ? html`<option value="all" .selected=${live(gpu === 'all')}>all GPUs</option>` : nothing}
      </select>` : nothing}
    ${layerButtons(f)}`;
}

// The VM toggle (D89): the session restarts as a Firecracker
// microVM — root in its own kernel, the same files and network scope. Shown
// disabled with the reason when this host or the workspace policy can't run
// one (GET /ws/term/env's vm block, loaded with the tile state); a host
// without KVM emulates the VM, and the tooltip says it is slower. A switch
// that went through is also the tile's choice for new sessions (rememberVM).
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
  return html`<button class=${'vm' + (on ? ' on' : '')} ?disabled=${!on && !st.available} title=${tip} aria-pressed=${String(on)}
      @click=${async () => { if (await f._respawn(f._active, patch, on ? 'outside the VM' : 'in a VM sandbox')) rememberVM(f, !on); }}><bx-icon name="vm"></bx-icon>VM</button>`;
}

// The tile's persistent terminal layer — shared by its shells and agents:
// rebuild it on a newer base image (offered when it is outdated), or reset it.
// The live reload controls come first: both settings branches end here.
function layerButtons(f) {
  const cur = f._sessions[f._active];
  return html`${barDeploy(f)}
    ${cur?.baseOutdated || f._envOld ? html`
      <button class="upgrade" title="a newer base image is installed — rebuild this tile's terminals on it (installed packages are wiped; your files & $HOME are kept)"
              @click=${() => f._resetEnv(true)}><bx-icon name="upload"></bx-icon><span class="lbl">base update</span></button>` : nothing}
    <button class="ib" title="reset this component's sandbox (wipe installed packages)" aria-label="reset the sandbox"
            @click=${() => f._resetEnv(false)}><bx-icon name="refresh"></bx-icon></button>`;
}

// The bar's styles (adopted by bx-frame alongside its own).
export const titlebarCss = css`
  .titlebar {
    display: flex; align-items: center; gap: 2px;
    height: var(--bx-titlebar-h, 28px); padding: 0 0 0 10px;
    color: var(--bx-title-text-inactive, #8E91A2); background: var(--bx-titlebar, #1F2028);
    border-bottom: 1px solid var(--bx-border, #33353F);
    user-select: none; cursor: grab;
    touch-action: none; flex: none;
    /* one row, never clipped: the tab strip absorbs a tight width */
    flex-wrap: nowrap; overflow: hidden; min-width: 0;
  }
  .pop.active .titlebar { color: var(--bx-title-text, #E9EAF0); background: var(--bx-titlebar-active, #262730); }
  .titlebar:active { cursor: grabbing; }
  /* the live square (product-ui 3) */
  .lsq { flex: none; box-sizing: border-box; width: 8px; height: 8px; margin-right: 6px; background: var(--bx-accent, #8C9BFF); }
  .lsq.building { background: transparent; border: 1px solid var(--bx-border-strong, #666A7E); }
  .lsq.failed { background: transparent; border: 1px solid var(--bx-danger, #FF7A7A); }
  .lfail { flex: none; margin-right: 6px; font: var(--bx-font-meta, 400 12px/16px system-ui, sans-serif); font-weight: 600; color: var(--bx-danger, #FF7A7A); }
  /* the name (UI 600) and the path (mono, muted): the path gives way first */
  .titlebar .path {
    display: flex; align-items: baseline; gap: 8px; padding-right: 8px; white-space: nowrap;
    overflow: hidden; flex: 0 1 auto; min-width: 48px;
  }
  .titlebar .path .nm { flex: 0 1 auto; min-width: 0; overflow: hidden; text-overflow: ellipsis; font-weight: 600; }
  .titlebar .path .dir { flex: 0 100 auto; min-width: 0; overflow: hidden; text-overflow: ellipsis;
    font: var(--bx-font-code, 12px/18px ui-monospace, monospace); color: var(--bx-muted, #A3A6B6); }
  /* On the full bar the tabs keep their width: the path and the network
     picker give way, then the bar degrades (fitBar). On the degraded bar
     the strip absorbs the width: its tabs shrink to a legible floor, then
     it scrolls (a wheel scrolls it sideways). */
  .titlebar .tabs {
    display: flex; align-items: stretch; align-self: stretch;
    flex: 0 1 auto; min-width: 0;
    overflow-x: auto; overflow-y: hidden; scrollbar-width: none;
  }
  .pop:not(.narrow) .titlebar .tabs { flex-shrink: 0; }
  .titlebar .tabs::-webkit-scrollbar { display: none; }
  .titlebar .spacer, .toolsrow .spacer { flex: 1; }
  /* the controls: 28 × 28 squares (16 px glyphs), a word beside the glyph
     where the control needs one */
  .titlebar button, .toolsrow button {
    display: inline-flex; align-items: center; justify-content: center; gap: 4px; flex: none; box-sizing: border-box;
    height: 28px; min-width: 28px; padding: 0 6px; cursor: pointer; white-space: nowrap;
    border: 0; border-radius: 0; background: transparent; color: var(--bx-muted, #A3A6B6);
  }
  .titlebar button.ib, .toolsrow button.ib { width: 28px; padding: 0; }
  .titlebar button:hover, .toolsrow button:hover { color: var(--bx-text, #E9EAF0); background: var(--bx-control-hover, #33353F); }
  .titlebar button.on, .toolsrow button.on {
    color: var(--bx-selection-text, #E9EAF0); background: var(--bx-selection, #262C5C);
    box-shadow: inset 0 -2px 0 var(--bx-accent, #8C9BFF);
  }
  .titlebar button:disabled, .toolsrow button:disabled { opacity: 0.45; cursor: default; background: transparent; }
  .titlebar button.winx:hover { color: var(--bx-close-hover-ink, #0B0C12); background: var(--bx-close-hover, #FF7A7A); }
  .titlebar button:focus-visible, .toolsrow button:focus-visible { outline-offset: calc(-1 * var(--bx-focus-width, 3px)); box-shadow: none; }
  /* a newer base image: the one call to act, a primary button, the one
     that gives way (its word first) before the window's close is pushed out */
  .titlebar button.upgrade, .toolsrow button.upgrade {
    height: 22px; margin: 0 2px; padding: 0 8px; border-radius: var(--bx-radius, 2px); font-weight: 600;
    color: var(--bx-accent-ink, #0B0C12); background: var(--bx-accent, #8C9BFF);
    flex: 0 1 auto; min-width: 28px; overflow: hidden;
  }
  .titlebar button.upgrade .lbl, .toolsrow button.upgrade .lbl { min-width: 0; overflow: hidden; text-overflow: ellipsis; }
  .titlebar button.upgrade:hover, .toolsrow button.upgrade:hover { color: var(--bx-accent-ink, #0B0C12); background: var(--bx-accent-hover, #A9B4FF); }
  /* Tabs are spans (not buttons) so each can hold a close button — nested
     buttons are invalid HTML: text tabs, the active one underlined. An
     agent session's tab carries the agents glyph (two linked squares); an
     ended one is greyed and struck. */
  .titlebar .tab {
    display: inline-flex; align-items: center; gap: 4px; max-width: 160px; min-width: 56px; flex: 0 1 auto; box-sizing: border-box;
    padding: 0 2px 0 8px; cursor: pointer; color: var(--bx-muted, #A3A6B6);
  }
  .titlebar .tab:hover { color: var(--bx-text, #E9EAF0); background: var(--bx-control-hover, #33353F); }
  .titlebar .tab.on { color: var(--bx-text, #E9EAF0); box-shadow: inset 0 -2px 0 var(--bx-accent, #8C9BFF); }
  .titlebar .tab .lbl { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .titlebar .tab .tabx {
    width: 20px; height: 20px; min-width: 0; padding: 0; border-radius: var(--bx-radius, 2px);
    color: inherit; opacity: 0.6;
  }
  .titlebar .tab .tabx:hover { opacity: 1; color: var(--bx-close-hover-ink, #0B0C12); background: var(--bx-close-hover, #FF7A7A); }
  .titlebar .tab.ended { opacity: 0.55; }
  .titlebar .tab.ended .lbl { text-decoration: line-through; }
  select.scope {
    flex: none; box-sizing: border-box; height: 22px; margin: 0 2px; padding: 0 4px; cursor: pointer;
    color: var(--bx-text, #E9EAF0); background: var(--bx-panel, #1F2028);
    border: 1px solid var(--bx-border-strong, #666A7E); border-radius: var(--bx-radius, 2px);
    /* the org scope's label names the sets ("org network (devs-net + …)")
       — cap it so the API/GPU pickers stay on the bar; the tooltip has it all */
    max-width: 24ch; text-overflow: ellipsis;
  }
  select.scope:focus-visible { outline-offset: 0; box-shadow: none; }
  /* the network picker's label is the long one: it shortens before the bar degrades */
  .titlebar select.scope.net { flex-shrink: 1; min-width: 12ch; }
  .toolsrow {
    display: flex; align-items: center; gap: 2px; flex-wrap: wrap;
    background: var(--bx-panel-2, #262730);
    border-bottom: 1px solid var(--bx-border, #33353F);
    padding: 0 4px; flex: none;
  }
  .lyt { display: inline-flex; margin-left: 4px; flex: none; }
  .lyt button { padding: 0 6px; }
  .lyt button .n { font: var(--bx-font-meta, 400 12px/16px system-ui, sans-serif); font-weight: 600; font-variant-numeric: tabular-nums; }
  /* ⇋ is a toggle beside the panel buttons, not one of them */
  .lyt button.beside { margin-left: 4px; }
`;
