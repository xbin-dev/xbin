// partition-ui.js — the web view's part of the three layouts
// (model/partition.js; API.md "Partitioned instances"): a notice above the
// main pane — at the global instance, to sign in as a person; in a person's
// partition, when a bound sandbox manager can't keep people apart, one banner
// naming it and the update — and, in the settings' MCP tab, the static MCP
// servers with the ones people's conversations don't get marked. An
// unpartitioned instance's page gets nothing here: no element, no call.
import { html, render, nothing } from '/vendor/lit-all.min.js';
import { partitionState, appNotices, staticMcp, MCP_GLOBAL_ONLY } from './model/partition.js';

/** mountPartitionUI adds the notice to #main and keeps it current; returns {state}. */
export function mountPartitionUI(app) {
  const state = partitionState();
  if (state === 'legacy') return { state };
  const el = Object.assign(document.createElement('div'), { id: 'partnote', className: 'partnote' });
  document.getElementById('main').prepend(el);
  const paint = () => {
    const ns = appNotices(app, state);
    el.hidden = !ns.length;
    render(html`${ns.map((n) => html`<div class="pn ${n.kind}" title=${n.title || ''}>${n.text}</div>`)}`, el);
  };
  app.on('sandboxes', paint);
  paint();
  return { state, paint };
}

/**
 * mountStaticMcp adds to the MCP tab (bd) of a partitioned instance the
 * config's static MCP servers (getConfig: GET /config, a manager's), saying
 * of each one with headers that it works in shared (global) conversations
 * only. Unpartitioned, or without servers: nothing.
 */
export async function mountStaticMcp(bd, getConfig, state = partitionState()) {
  if (state === 'legacy') return;
  const box = Object.assign(document.createElement('div'), { className: 'sec', id: 'mcp-static' });
  box.hidden = true;
  bd.append(box);
  let list = [];
  try { list = staticMcp(await getConfig(), state); } catch { /* not a manager: the tab lists the bound ones only */ }
  if (!list.length) return;
  box.hidden = false;
  render(html`<h4>Static servers (config)</h4>
    <table class="tbl"><tr><th>server</th><th>endpoint</th><th></th></tr>
      ${list.map((s) => html`<tr><td class="mono">${s.name}</td><td class="mono muted">${s.url}</td>
        <td class=${s.globalOnly ? 'muted' : nothing}>${s.globalOnly ? MCP_GLOBAL_ONLY : 'every conversation'}</td></tr>`)}</table>`, box);
}
