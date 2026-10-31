/**
 * frame-status.js — the tile window's status bar (D202): what the session
 * in view runs with, along the bottom of the window. `f` is the BxFrame;
 * this reads its state and calls its handlers.
 *
 * The session in view is the frame's active session (f._active): the one
 * whose tab was last chosen or focused. For it the bar holds the pickers
 * that used to fill the title bar — the network scope, the tile API or its
 * target (frame-deploy.js targetSelect), the VM toggle (D89) and, on a GPU
 * host, the GPU — each a restart of that session when changed (it asks
 * first; a declined change snaps the picker back). Then the tile's own
 * controls: the live reload chip and Reload now (frame-deploy.js
 * barDeploy), a base update when the tile's terminal layer is on an older
 * base, and the reset. An ended or read-only (past) session has no
 * sandbox: the tile's controls only. With no session, no pickers at all.
 *
 * A narrow window (under 640 px, and the phone's sheet) folds the session's
 * pickers into one chip whose menu holds the same choices; the live reload
 * chip stays on the bar in its compact form, so its state never hides.
 * The classes stay those the pickers had on the title bar (select.scope,
 * button.vm, button.upgrade, …).
 */
import { html, css, nothing, live } from 'lit';
import '/vendor/bx-icons.js';
import { rememberVM } from '/vendor/frame-launcher.js';
import { barDeploy, titleChip, targetSelect, targetChoices } from '/vendor/frame-deploy.js';

const SHEET = typeof matchMedia === 'function' ? matchMedia('(max-width: 820px)') : { matches: false };

// a session's tab label (the tab strips say the same)
export const sessionLabel = (s) => (s.kind === 'agent' ? s.name || s.provider || 'Agent' : s.name || 'Bash');

const restartsOf = (f) => `switching restarts the ${f._isAgent ? 'agent (its conversation resumes)' : 'terminal'}`;

// the network scopes the session may pick (a VM can't share the host network)
function scopesOf(cur) {
  return (cur?.scopes ?? [{ id: 'internet', label: 'internet' }, { id: 'host', label: 'host net' }, { id: 'none', label: 'offline' }])
    .filter((s) => !(cur?.vm && s.id === 'host'));
}

// statusBar(f): the bar.
export function statusBar(f) {
  const cur = f._sessions[f._active];
  const live = cur && !cur.ended && !cur.history;
  return html`<div class="status" role="toolbar" aria-label="session settings">
    ${cur ? html`<span class="who" title=${cur.ended ? 'this session has ended' : 'the session in view: these settings are its'}>
        <bx-icon name=${cur.kind === 'agent' ? 'agent' : 'terminal'}></bx-icon><span class="lbl">${sessionLabel(cur)}</span>${cur.ended ? html`<span class="ended">ended</span>` : nothing}</span>`
      : html`<span class="who none">no session</span>`}
    ${f._narrow
      ? html`${titleChip(f)}<span class="grow"></span>${live ? html`<button class="fold" title="this session's network, tile API, VM and GPU"
          aria-label="session settings" @click=${(e) => openFolded(f, e)}><bx-icon name="settings"></bx-icon><span class="lbl">${summary(f, cur)}</span><bx-icon name="chevron-down"></bx-icon></button>` : nothing}${layerButtons(f, true)}`
      : html`${live ? pickers(f, cur) : nothing}<span class="grow"></span>${layerButtons(f, false)}`}
  </div>`;
}

// The pickers mark their choice on the OPTIONS (live .selected): a select's
// .value is committed before its options exist when the select is new.
function pickers(f, cur) {
  const scopes = scopesOf(cur);
  const now = scopes.find((s) => s.id === (cur.net || scopes[0].id)) ?? scopes[0];
  const api = cur.api === false ? 'off' : 'on';
  const gpu = cur.gpu || 'none';
  const restarts = restartsOf(f);
  return html`
    <select class="scope net" aria-label="network scope"
            title=${`network scope (${restarts})` + (now?.desc ? '\n' + now.desc : '')}
            @change=${async (e) => { if (!(await f._setNet(f._active, e.target.value))) e.target.value = now.id; }}>
      ${scopes.map((s) => html`<option value=${s.id} title=${s.desc ?? ''} .selected=${live(s.id === now.id)}>${s.label}</option>`)}
    </select>
    ${targetSelect(f, restarts) || html`<select class="scope" aria-label="tile API" title=${`live tile API access — off = the ${f._isAgent ? 'agent' : 'shell'} can read/edit code but every API call is unauthorized (${restarts})`}
            @change=${async (e) => { if (!(await f._setApi(f._active, e.target.value))) e.target.value = api; }}>
      <option value="on" .selected=${live(api === 'on')}>tile API</option>
      <option value="off" .selected=${live(api === 'off')}>no API</option>
    </select>`}
    ${vmToggle(f, cur, restarts)}
    ${f._gpus.length && !cur.vm ? html`
      <select class="scope" aria-label="GPU" title=${`GPU (${restarts})`}
              @change=${async (e) => { if (!(await f._setGpu(f._active, e.target.value))) e.target.value = gpu; }}>
        <option value="none" .selected=${live(gpu === 'none')}>no GPU</option>
        ${f._gpus.map((g) => html`<option value=${g.index} .selected=${live(String(gpu) === String(g.index))}>GPU ${g.index}</option>`)}
        ${f._gpus.length > 1 ? html`<option value="all" .selected=${live(gpu === 'all')}>all GPUs</option>` : nothing}
      </select>` : nothing}`;
}

// The VM toggle (D89): the session restarts as a Firecracker microVM — root
// in its own kernel, the same files and network scope. Disabled with the
// reason when this host or the workspace policy can't run one; a switch
// that went through is also the tile's choice for new sessions.
function vmTip(f, cur, restarts) {
  const st = f._vmStatus, who = f._isAgent ? 'agent' : 'shell';
  const size = (st.memMiB ? ` (${st.memMiB} MiB, ${st.vcpus} vCPU)` : '') + (st.emulated ? ' — emulated: this host has no KVM, so it runs several times slower' : '');
  return cur.vm ? `VM sandbox: this ${who} is root in its own kernel${size} — click to leave the VM (${restarts})`
    : st.available ? `run this ${who} in a VM sandbox: root in its own kernel${size}, the same files and network (${restarts})`
      : `VM sandbox unavailable: ${st.reason}`;
}
async function switchVM(f, cur) {
  const on = !!cur.vm, patch = { vm: !on };
  if (!on && cur.net === 'host') patch.net = null; // back to the tile's default scope
  if (await f._respawn(f._active, patch, on ? 'outside the VM' : 'in a VM sandbox')) rememberVM(f, !on);
}
function vmToggle(f, cur, restarts) {
  if (!f._vmStatus) return nothing;
  const on = !!cur.vm, tip = vmTip(f, cur, restarts);
  return html`<button class=${'vm' + (on ? ' on' : '')} ?disabled=${!on && !f._vmStatus.available} title=${tip} aria-label=${tip} aria-pressed=${String(on)}
      @click=${() => switchVM(f, cur)}><bx-icon name="vm"></bx-icon>VM</button>`;
}

// The tile's persistent terminal layer, shared by its shells and agents:
// rebuild it on a newer base image (offered when it is outdated), or reset
// it. The live reload controls come first.
function layerButtons(f, narrow) {
  const cur = f._sessions[f._active];
  return html`${barDeploy(f)}
    ${cur?.baseOutdated || f._envOld ? html`
      <button class="upgrade" title="a newer base image is installed — rebuild this tile's terminals on it (installed packages are wiped; your files & $HOME are kept)"
              aria-label="base update" @click=${() => f._resetEnv(true)}><bx-icon name="upload"></bx-icon>${narrow ? nothing : html`<span class="lbl">base update</span>`}</button>` : nothing}
    <button class="ib" title="reset this component's sandbox (wipe installed packages)" aria-label="reset the sandbox"
            @click=${() => f._resetEnv(false)}><bx-icon name="refresh"></bx-icon></button>`;
}

// ---- the folded pickers (a narrow window) ----

function summary(f, cur) {
  const net = scopesOf(cur).find((s) => s.id === (cur.net || scopesOf(cur)[0].id));
  const parts = [net?.label || cur.net || 'internet', cur.api === false ? 'no API' : cur.deployment ? `API: ${cur.deployment}` : 'API'];
  if (cur.vm) parts.push('VM');
  return parts.join(' · ');
}

// sandboxItems(f): the same choices as <bx-menu> items, for the session in view.
export function sandboxItems(f) {
  const cur = f._sessions[f._active];
  if (!cur || cur.ended || cur.history) return [];
  const i = f._active, scopes = scopesOf(cur), net = cur.net || scopes[0]?.id;
  const items = [{ label: 'Network', icon: 'network', hint: scopes.find((s) => s.id === net)?.label || '',
    items: scopes.map((s) => ({ label: s.label, title: s.desc || '', checked: s.id === net, action: () => f._setNet(i, s.id) })) }];
  const t = targetChoices(f), api = cur.api === false ? 'off' : 'on';
  items.push(t
    ? { label: 'Tile API', icon: 'plug', hint: t.options.find((o) => o.value === t.value)?.label || '', items: t.options.map((o) => ({ label: o.label, checked: o.value === t.value, action: () => f._setApi(i, o.value) })) }
    : { label: 'Tile API', icon: 'plug', hint: api === 'on' ? 'on' : 'off', items: [
      { label: 'tile API', checked: api === 'on', action: () => f._setApi(i, 'on') },
      { label: 'no API', checked: api === 'off', action: () => f._setApi(i, 'off') }] });
  if (f._vmStatus) items.push({ label: cur.vm ? 'Leave the VM' : 'Run in a VM sandbox', icon: 'vm', checked: !!cur.vm, title: vmTip(f, cur, restartsOf(f)),
    disabled: !cur.vm && !f._vmStatus.available, hint: !cur.vm && !f._vmStatus.available ? 'unavailable' : '', action: () => switchVM(f, cur) });
  if (f._gpus.length && !cur.vm) {
    const gpu = String(cur.gpu || 'none');
    items.push({ label: 'GPU', icon: 'cpu', hint: gpu === 'none' ? 'none' : gpu, items: [
      { label: 'no GPU', checked: gpu === 'none', action: () => f._setGpu(i, 'none') },
      ...f._gpus.map((g) => ({ label: `GPU ${g.index}`, checked: gpu === String(g.index), action: () => f._setGpu(i, String(g.index)) })),
      ...(f._gpus.length > 1 ? [{ label: 'all GPUs', checked: gpu === 'all', action: () => f._setGpu(i, 'all') }] : [])] });
  }
  return items;
}
function openFolded(f, e) {
  f._menu = { items: sandboxItems(f), anchor: e.currentTarget.getBoundingClientRect(), sheet: SHEET.matches };
}

// The bar's styles (adopted by bx-frame), the pickers' among them.
export const statusCss = css`
  .status {
    display: flex; align-items: center; gap: 4px; flex: none; box-sizing: border-box;
    height: var(--bx-titlebar-h, 28px); padding: 0 14px 0 8px; min-width: 0; overflow: hidden; white-space: nowrap;
    color: var(--bx-muted, #A3A6B6); background: var(--bx-titlebar, #1F2028);
    border-top: 1px solid var(--bx-border, #33353F);
    font: var(--bx-font-meta, 400 12px/16px system-ui, sans-serif);
  }
  .status .who { display: inline-flex; align-items: center; gap: 6px; min-width: 0; margin-right: 6px; color: var(--bx-text, #E9EAF0); }
  .status .who .lbl { overflow: hidden; text-overflow: ellipsis; max-width: 18ch; }
  .status .who.none { color: var(--bx-muted, #A3A6B6); }
  .status .who .ended { color: var(--bx-muted, #A3A6B6); }
  .status .grow { flex: 1; min-width: 0; }
  .status button {
    display: inline-flex; align-items: center; justify-content: center; gap: 4px; flex: none; box-sizing: border-box;
    height: 24px; min-width: 24px; padding: 0 6px; cursor: pointer; white-space: nowrap;
    border: 0; border-radius: var(--bx-radius, 2px); background: transparent; color: var(--bx-muted, #A3A6B6);
  }
  .status button.ib { width: 28px; padding: 0; }
  .status button:hover { color: var(--bx-text, #E9EAF0); background: var(--bx-control-hover, #33353F); }
  .status button.on { color: var(--bx-selection-text, #E9EAF0); background: var(--bx-selection, #262C5C); }
  .status button:disabled { opacity: 0.45; cursor: default; background: transparent; }
  .status button.fold { min-width: 0; flex: 0 1 auto; border: 1px solid var(--bx-border, #33353F); }
  .status button.fold .lbl { min-width: 0; overflow: hidden; text-overflow: ellipsis; }
  .status button.upgrade, .status button.offer {
    height: 22px; padding: 0 8px; font-weight: 600;
    color: var(--bx-accent-ink, #0B0C12); background: var(--bx-accent, #8C9BFF);
  }
  .status button.upgrade:hover, .status button.offer:hover { color: var(--bx-accent-ink, #0B0C12); background: var(--bx-accent-hover, #A9B4FF); }
  .status button.lr, .status button.dentry { color: var(--bx-text, #E9EAF0); }
  .status button.lr.compact { max-width: 12ch; overflow: hidden; text-overflow: ellipsis; }
  .status button:focus-visible { outline-offset: calc(-1 * var(--bx-focus-width, 3px)); box-shadow: none; }
  select.scope {
    flex: 0 1 auto; box-sizing: border-box; height: 22px; padding: 0 4px; cursor: pointer; min-width: 9ch;
    color: var(--bx-text, #E9EAF0); background: var(--bx-panel, #1F2028);
    border: 1px solid var(--bx-border-strong, #666A7E); border-radius: var(--bx-radius, 2px);
    /* the org scope's label names the sets ("org network (devs-net + …)"): capped, the tooltip has it all */
    max-width: 24ch; text-overflow: ellipsis;
  }
  select.scope:focus-visible { outline-offset: 0; box-shadow: none; }
  /* the bar's right 14 px stay clear for the window's resize corner (win-resize.js), which phones don't have */
  @media (max-width: 820px) { .status { padding-right: 2px; } }
`;
