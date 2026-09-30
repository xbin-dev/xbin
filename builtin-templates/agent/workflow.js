// workflow.js — the workflow pane (#workflow; split out of agent.js, which is
// at its size budget): the tree of a conversation's runs — its root, their
// subagents and theirs (GET /runs/{root}/tree) — a row per run with its
// status, its spend and what it waits on; a click opens that run, Stop
// cancels the whole subtree. makeWorkflow({selectRun}) wires the pane and
// answers open(rootId) (the top bar's ⑂ chip), close(), dirty() (re-read
// after the stream reported a change: agent.js paint), load() and shown.
import { esc } from '/vendor/bx-kit.js';
import * as actions from './model/actions.js';

const $ = (id) => document.getElementById(id);
const num = (v) => Number(v) || 0;
const clip = (s, n) => { s = String(s ?? ''); return s.length > n ? s.slice(0, n) + '…' : s; };
// Group digits for readability: 123123 → "123 123" (narrow no-break space).
const fmtN = (n) => String(Math.round(Number(n) || 0)).replace(/\B(?=(\d{3})+(?!\d))/g, ' ');

const WF_WORDS = { dep: 'waiting on', slot: 'queued — at the concurrency limit',
                   human: 'waiting on you', sleeping: 'sleeping', cancelling: 'cancelling…' };

// A run with no relatives gets no chip at all, so the workflow layer costs a
// plain single run nothing: no extra element in an already-crowded top bar,
// and no /tree request.
function wfCostOf(n) { return num(n.promptTokens) + num(n.completionTokens); }

// nodeRow is a row of the pane; wfSel: the selected run.
function nodeRow(n, maxCost, wfSel) {
  const cost = wfCostOf(n);
  const pct = maxCost > 0 ? Math.round(100 * cost / maxCost) : 0;
  const blocked = n.blockReason === 'dep' && (n.blockedOn || []).length;
  let sub = '', cls = '';
  if (blocked) { sub = `⛔ waiting on ${n.blockedOn.map((i) => '#' + i).join(', ')}`; cls = 'blk'; }
  else if (n.blockReason) { sub = '⏳ ' + (WF_WORDS[n.blockReason] || n.blockReason); cls = 'blk'; }
  else if (n.status === 'error') { sub = '⚠ ' + (n.result || 'failed'); cls = 'bad'; }
  else if (n.lastStep) { sub = n.lastStep; }
  return `<div class="wfn${wfSel === n.id ? ' on' : ''}" data-n="${num(n.id)}" style="--d:${Math.min(num(n.depth), 4)}">
    <span class="nm"><span class="dot ${esc(n.status)}"></span><span class="tt">${esc(n.title || 'run ' + n.id)}</span></span>
    <span class="cost">${cost ? fmtN(cost) : ''}${cost ? `<i class="share"><i style="width:${pct}%"></i></i>` : ''}</span>
    <span class="sub ${cls}">${esc(clip(sub, 160))}</span>
  </div>`;
}

export function makeWorkflow({ selectRun }) {
  let wfOpen = false, wfRoot = null, wfSetKey = '', wfValKey = '', wfSel = null;

  function openWorkflow(rootId) {
    if (rootId == null) return;
    wfOpen = true; wfRoot = rootId; wfSetKey = ''; wfValKey = '';
    $('workflow').hidden = false;
    $('main').classList.add('wfon');
    loadTree();
  }

  function closeWorkflow() {
    wfOpen = false;
    $('workflow').hidden = true;
    $('main').classList.remove('wfon');
  }

  async function loadTree() {
    if (!wfOpen || wfRoot == null) return;
    let t;
    try { t = await actions.tree(wfRoot); } catch { return; }
    if (!wfOpen) return;
    renderWorkflow(t);
  }

  // treeDirty re-reads the tree after the stream reported a change: at most one
  // request in flight, and one more if anything changed while it was.
  let treeBusy = false, treeAgain = false;
  function treeDirty() {
    if (treeBusy) { treeAgain = true; return; }
    treeBusy = true;
    loadTree().finally(() => {
      treeBusy = false;
      if (treeAgain) { treeAgain = false; treeDirty(); }
    });
  }

  // The tree is re-read on every link/status event. A wholesale rebuild would
  // drop the hovered row out from under the pointer and kill a button
  // mid-click, so rebuild only when the node SET changes, and patch values
  // otherwise.
  function renderWorkflow(t) {
    const nodes = t.nodes || [];
    const setKey = nodes.map((n) => n.id).join(',');
    const valKey = JSON.stringify(nodes.map((n) => [n.status, n.updated, n.promptTokens, n.blockReason]));
    paintWorkflowHeader(t);
    if (setKey !== wfSetKey) { wfSetKey = setKey; wfValKey = valKey; return buildWorkflow(t); }
    if (valKey === wfValKey) return;
    wfValKey = valKey;
    patchWorkflow(t);
  }

  function paintWorkflowHeader(t) {
    const by = (t.totals && t.totals.byStatus) || {};
    const root = (t.nodes || []).find((n) => n.id === t.root);
    $('wf-title').textContent = root ? (root.title || 'run ' + t.root) : 'workflow';
    $('wf-title').title = $('wf-title').textContent;
    const parts = [];
    for (const k of ['running', 'queued', 'blocked', 'done', 'error', 'cancelled']) {
      if (by[k]) parts.push(`${by[k]} ${k}`);
    }
    $('wf-counts').textContent = `${(t.totals || {}).nodes || 0} nodes · ${parts.join(' · ') || 'idle'}`;
    const tot = t.totals || {};
    // A rate, not just a total: a total is alarming, a rate is actionable.
    $('wf-cost').textContent =
      `Σ ${fmtN(tot.promptTokens)}↑ ${fmtN(tot.completionTokens)}↓ · ${fmtN(tot.llmCalls)} calls · ${tot.active}/${tot.limit} running`;
  }

  function buildWorkflow(t) {
    const nodes = t.nodes || [];
    const maxCost = Math.max(1, ...nodes.map(wfCostOf));
    // Sorted by creation within a parent, never by status: status-sorting makes
    // rows jump under the cursor on every poll.
    const byParent = new Map();
    for (const n of nodes) {
      const k = n.id === t.root ? -1 : n.parentId;
      if (!byParent.has(k)) byParent.set(k, []);
      byParent.get(k).push(n);
    }
    const out = [];
    const walk = (list) => {
      for (const n of (list || []).sort((a, b) => a.created - b.created)) {
        out.push(nodeRow(n, maxCost, wfSel));
        walk(byParent.get(n.id));
      }
    };
    walk(byParent.get(-1));
    const body = $('wf-body');
    body.innerHTML = out.join('') || '<div class="empty">no background runs</div>';
    body.querySelectorAll('[data-n]').forEach((el) => el.onclick = () => selectRun(+el.dataset.n));
  }

  function patchWorkflow(t) {
    const nodes = t.nodes || [];
    const maxCost = Math.max(1, ...nodes.map(wfCostOf));
    for (const n of nodes) {
      const el = $('wf-body').querySelector(`[data-n="${num(n.id)}"]`);
      if (!el) continue;
      const dot = el.querySelector('.dot');
      if (dot) dot.className = 'dot ' + n.status;
      const tmp = document.createElement('div');
      tmp.innerHTML = nodeRow(n, maxCost, wfSel);
      el.querySelector('.cost').innerHTML = tmp.querySelector('.cost').innerHTML;
      const sub = tmp.querySelector('.sub');
      el.querySelector('.sub').className = sub.className;
      el.querySelector('.sub').textContent = sub.textContent;
    }
  }

  // The pane's header: ✕ closes it; Stop cancels the whole subtree, after a confirm.
  $('wf-close').onclick = () => closeWorkflow();
  $('wf-stop').onclick = async () => {
    if (wfRoot == null || !confirm('Cancel this workflow and every run below it?')) return;
    try { await actions.cancelTree(wfRoot); }
    catch (e) { return alert(e.message); }
    loadTree();
  };

  return { open: openWorkflow, close: closeWorkflow, dirty: treeDirty, load: loadTree, get shown() { return wfOpen; } };
}
