/**
 * frame-titlebar.js — the floating terminal window's title bar, extracted
 * from bx-frame (its size budget) . `f` is the BxFrame; this reads its state
 * and calls its `_` handlers. Two tab kinds share the bar: a shell tab
 * (kind:"shell") carries the layout switcher and the net/API/GPU pickers; an
 * AGENT tab (kind:"agent", D74) has a fixed sandbox and shows none of them —
 * just the tab strip and the close controls. The `+` opens a shell, `+🤖`
 * an agent.
 */
import { html, nothing } from 'lit';
import { scopeIcon } from '/vendor/bx-netrules.js';

export function titlebar(f) {
  const cur = f._sessions[f._active];
  const isAgent = cur?.kind === 'agent';
  return html`
    <div class="titlebar" @pointerdown=${f._dragStart}>
      <span class="path">${f.src}</span>
      ${f._sessions.map((s, i) => html`
        <span class="tab ${i === f._active ? 'on' : ''} ${s.kind === 'agent' ? 'agent' : ''}"
              @click=${() => { f._active = i; }}
              @dblclick=${() => f._renameTerm(i)}
              title=${s.name ? `${s.name} — double-click to rename` : 'double-click to rename'}>
          <span class="lbl">${s.kind === 'agent' ? `🤖 ${s.name || 'Agent'}` : (s.name || (i + 1))}</span>
          <button class="tabx" title="close this ${s.kind === 'agent' ? 'agent' : 'terminal'}"
                  @click=${(e) => { e.stopPropagation(); f._closeTerm(i); }}>✕</button>
        </span>`)}
      <button title="new terminal" @click=${f._newTerm}>+</button>
      <button title="new agent session" @click=${f._newAgent}>+🤖</button>
      ${isAgent ? nothing : html`
        <span class="lyt">
          <button class=${f._layout === 'term' ? 'on' : ''} title="terminal only"
                  @click=${() => f._setLayout('term')}>&gt;_</button>
          <button class=${f._layout === 'code' ? 'on' : ''} title="code browser + review"
                  @click=${() => f._setLayout('code')}>{ }</button>
          <button class=${f._layout === 'split' ? 'on' : ''} title="code + terminal side by side"
                  @click=${() => f._setLayout('split')}>⇋</button>
          <button class=${f._layout === 'logs' ? 'on' : ''} title="backend logs (read-only)"
                  @click=${() => f._setLayout('logs')}>▤</button>
          <button class=${f._layout === 'prs' ? 'on' : ''}
                  title="change proposals — patches other tiles' agents suggested for this one"
                  @click=${() => f._setLayout('prs')}>⇄${f._prCount ? ` ${f._prCount}` : ''}</button>
        </span>`}
      <span class="spacer"></span>
      ${isAgent ? nothing : pickers(f)}
      <button title="close (session keeps running)"
              @click=${() => { f._termOpen = false; }}>✕</button>
    </div>`;
}

function pickers(f) {
  const cur = f._sessions[f._active];
  const scopes = cur?.scopes ?? [
    { id: 'internet', label: 'internet' }, { id: 'host', label: 'host net' }, { id: 'none', label: 'offline' }];
  const now = scopes.find((s) => s.id === (cur?.net || scopes[0].id)) ?? scopes[0];
  return html`
    <select class="scope"
            title=${'network scope (switching restarts the terminal)' + (now?.desc ? '\n' + now.desc : '')}
            .value=${now.id}
            @change=${(e) => f._setNet(f._active, e.target.value)}>
      ${scopes.map((s) => html`<option value=${s.id} title=${s.desc ?? ''}>${scopeIcon(s.id)} ${s.label}</option>`)}
    </select>
    <select class="scope" title="live tile API access — off = the shell can read/edit code but every API call is unauthorized (switching restarts the terminal)"
            .value=${cur?.api === false ? 'off' : 'on'}
            @change=${(e) => f._setApi(f._active, e.target.value === 'on')}>
      <option value="on">🔌 tile API</option>
      <option value="off">⛔ no API</option>
    </select>
    ${f._gpus.length ? html`
      <select class="scope" title="GPU (switching restarts the terminal)"
              .value=${cur?.gpu || 'none'}
              @change=${(e) => f._setGpu(f._active, e.target.value)}>
        <option value="none">no GPU</option>
        ${f._gpus.map((g) => html`<option value=${g.index}>🎮 GPU ${g.index}</option>`)}
        ${f._gpus.length > 1 ? html`<option value="all">🎮 all</option>` : nothing}
      </select>` : nothing}
    ${cur?.baseOutdated ? html`
      <button class="upgrade" title="a newer base image is installed — upgrade rebuilds this terminal on it (wipes installed packages; your files & $HOME are kept)"
              @click=${f._resetEnv}>⬆ base update</button>` : nothing}
    <button title="reset this component's sandbox (wipe installed packages)"
            @click=${f._resetEnv}>⟲</button>`;
}
