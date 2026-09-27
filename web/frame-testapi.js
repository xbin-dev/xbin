/**
 * frame-testapi.js — <bx-frame>'s test surface, extracted from bx-frame (the
 * frame is near its size budget): the stable names the UI harness
 * (hack/ui-harness) drives instead of the frame's private members, which a
 * refactor may rename. `f` is the BxFrame; this reads and writes its existing
 * state only, and nothing in the frame calls it. New test names go here,
 * never in the element.
 */
import { launcherItems, openHistory, resumeHistory } from '/vendor/frame-launcher.js';
import { deployTestApi } from '/vendor/frame-deploy.js';

// The layouts the window's layout switcher offers, in bar order: the values
// open(layout) takes, one `.lyt` button each (frame-titlebar.js layoutGroup).
// A pass compares the bar against this instead of a magic number; the change
// that adds a layout button adds its name here.
const LAYOUTS = ['term', 'code', 'split', 'logs', 'prs'];

export function testApi(f) {
  return {
    get layouts() { return [...LAYOUTS]; },
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
    focusTerminal() { f.renderRoot.querySelector('bx-terminal')?.shadowRoot?.querySelector('textarea')?.focus(); },
    get tabs() { return f._sessions.map((s) => ({ kind: s.kind || 'shell', id: s.id, name: s.name, provider: s.provider, status: s.status, ended: !!s.ended, history: s.history || null, resume: s.resume || null, net: s.net, api: s.api !== false, gpu: s.gpu, vm: !!s.vm, run: s.run || null })); },
    get history() { return f._history || []; }, openHistory(id) { const r = (f._history || []).find((x) => x.id === id); if (r) openHistory(f, r); }, resumeHistory(id) { const r = (f._history || []).find((x) => x.id === id); if (r) resumeHistory(f, r); },
    get activeTab() { return f._active; },
    setActiveTab(i) { f._setActive(i | 0); },
    get layout() { return f._layout; },
    get narrow() { return f._narrow; },
    setTools(v) { f._tools = !!v; }, // the degraded bar's tools row (the pickers) open — a no-op on the full bar
    newTerm() { f._newTerm(); },
    newAgent() { f._newAgent(); },
    startKind(kind, provider, opts) { f._startKind(kind, provider, opts || {}); }, // launcher path (a provider eager-creates)
    launcherItems() { return launcherItems(f).map((it) => it.label || it.kind || (it.kind === 'sep' ? '—' : '')); },
    closeTab(i) { f._closeTerm(i | 0); },
    get dialog() { return f._dialog?.spec ?? null; },
    answerDialog(button, values = {}) { f._dialogDone({ detail: { button, values } }); },
    // the <bx-agent> testApi for tab i (default: the active one); null for a shell tab
    agent(i = f._active) {
      const t = f._sessions[i];
      if (t?.kind !== 'agent') return null;
      const idx = f._sessions.filter((s) => s.kind === 'agent').indexOf(t);
      return f.renderRoot.querySelectorAll('bx-agent')[idx]?.testApi?.() ?? null;
    },
  };
}
