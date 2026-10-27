/**
 * frame-testapi.js — <bx-frame>'s test surface, extracted from bx-frame (the
 * frame is near its size budget): the stable names the UI harness
 * (hack/ui-harness) drives instead of the frame's private members, which a
 * refactor may rename. `f` is the BxFrame; this reads and writes its existing
 * state only, and nothing in the frame calls it. New test names go here,
 * never in the element.
 *
 * The window is an IDE since D202 (frame-ide-view.js): the names from
 * before it keep their meaning where one is left — `layout` is what open()
 * would show, `beside` whether a panel (the side list or the logs dock)
 * shows beside the sessions — and `ide` drives the panes, tabs and tools.
 */
import { launcherItems, openHistory, resumeHistory } from '/vendor/frame-launcher.js';
import { deployTestApi, hasLayout } from '/vendor/frame-deploy.js';
import { legacyOf } from '/vendor/frame-ide.js';
import { ideLayout, ideTestApi } from '/vendor/frame-ide-view.js';

// The layouts open(layout) takes (the shell's tile menu): 'deployments'
// once the tile's deployments state has loaded (frame-deploy.js hasLayout);
// 'split', the old code + terminal layout, last.
const LAYOUTS = ['term', 'code', 'logs', 'prs'];

// a session tab's element (the stage holds them keyed, in no tab order)
const elOf = (f, sel, t) => (t ? f.renderRoot.querySelector(`${sel}[data-key="${t.key}"]`) : null);

export function testApi(f) {
  return {
    get layouts() { return [...LAYOUTS, ...(hasLayout(f) ? ['deployments'] : []), 'split']; },
    get beside() { return legacyOf(f._ide).beside; }, // a side list or the logs dock shows beside the sessions
    get paneW() { return legacyOf(f._ide).paneW; }, // what older frames read (the side panel has its own width)
    get iframe() { return f._iframe; },
    get hovered() { return f.hovered; },
    setHover(v) { f._hover = !!v; },
    get reloading() { return f.reloading; },
    get reloads() { return f._loadGen || 0; }, // reloads completed (a deploy that reloads the frame shows as +1)
    get deploy() { return deployTestApi(f); }, // the live reload state and controls (frame-deploy.js)
    beginReload: () => f._beginReload(),
    notifyLoad: () => f._onFrameLoad(),
    get terminalOpen() { return f._termOpen; },
    closeTerminal() { f._termOpen = false; },
    open: (layout) => f.open(layout),
    get pop() { return f._pop ? f._popBox() : null; }, // the viewport box
    setPop(box) { f._setPopBox(box); f.requestUpdate(); f._popChanged(); },
    popElement: () => f.renderRoot.querySelector('.pop'),
    focusTerminal() { elOf(f, 'bx-terminal', f._sessions[f._active])?.shadowRoot?.querySelector('textarea')?.focus(); },
    get tabs() { return f._sessions.map((s) => ({ kind: s.kind || 'shell', id: s.id, name: s.name, provider: s.provider, status: s.status, ended: !!s.ended, history: s.history || null, resume: s.resume || null, net: s.net, api: s.api !== false, deployment: s.deployment || '', gpu: s.gpu, vm: !!s.vm, run: s.run || null })); },
    get history() { return f._history || []; }, openHistory(id) { const r = (f._history || []).find((x) => x.id === id); if (r) openHistory(f, r); }, resumeHistory(id) { const r = (f._history || []).find((x) => x.id === id); if (r) resumeHistory(f, r); },
    get activeTab() { return f._active; }, // the session in view (the status bar's)
    setActiveTab(i) { f._setActive(i | 0); },
    get layout() { return ideLayout(f); },
    get narrow() { return f._narrow; },
    // the folded status bar's menu (a narrow window's pickers) open or shut — before D202 the tools row
    setTools(v) { if (!v) f._menu = null; else f.renderRoot.querySelector('.status button.fold')?.click(); },
    newTerm() { f._newTerm(); },
    newAgent() { f._newAgent(); },
    startKind(kind, provider, opts) { f._startKind(kind, provider, opts || {}); }, // launcher path (a provider eager-creates)
    launcherItems() { return launcherItems(f).map((it) => it.label || it.kind || (it.kind === 'sep' ? '—' : '')); },
    closeTab(i) { f._closeTerm(i | 0); },
    get dialog() { return f._dialog?.spec ?? null; },
    answerDialog(button, values = {}) { f._dialogDone({ detail: { button, values } }); },
    // the <bx-agent> testApi for session tab i (default: the one in view); null for a shell tab
    agent(i = f._active) {
      const t = f._sessions[i];
      return t?.kind === 'agent' ? elOf(f, 'bx-agent', t)?.testApi?.() ?? null : null;
    },
    // the panes, tabs, tools, dock and status bar (frame-ide-view.js ideTestApi)
    get ide() { return ideTestApi(f); },
  };
}
