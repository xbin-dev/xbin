// partition-ui.js — the web view's part of the three layouts
// (model/partition.js; API.md "Partitioned instances"): a notice above the
// main pane — at the global instance, to sign in as a person; in a person's
// partition, when a bound sandbox manager can't keep people apart, one banner
// naming it and the update. An unpartitioned instance's page gets nothing
// here: no element, no call.
import { html, render } from '/vendor/lit-all.min.js';
import { partitionState, appNotices } from './model/partition.js';

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
