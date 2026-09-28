// widget-wide — a deploy pipeline's widget at the wide size (tree.md §13):
// every primitive a widget may use — a row with an icon and a badge, the
// rollout's progress, and a line of the time, error rate, a sparkline and
// the button; a narrower layout (headline, badge, progress, button) small.
import { html, render, widget, native, nothing } from '/vendor/xb-native.js';
import { selfApi } from '/vendor/bx-kit.js';

let d = null;
const pct = (x) => `${Math.round(x * 100)}%`;
const toneOf = (s) => (s === 'failed' ? 'danger' : s === 'rolling' ? 'accent' : 'ok');

async function load() {
  d = await selfApi('/pipeline');
  paint();
}
async function promote() {
  await selfApi('/promote', { method: 'POST' });
  await load();
}
function paint() {
  render(html`<screen title="Deploys"><section><row title=${d?.service ?? '…'} detail=${d ? pct(d.progress) : ''}/></section></screen>`);
  if (!d) { widget(html`<text tone="muted">Loading…</text>`); return; }
  const button = html`<button role="primary" icon="forward" ?disabled=${d.state !== 'rolling'} @tap=${promote}>Promote</button>`;
  const progress = html`<progress value=${d.progress} label=${`${pct(d.progress)} of hosts`}/>`;
  if (native.widgetSize !== 'wide') { // one column: the name, the state, the rollout, the action
    widget(html`<stack gap="xs">
      <stack axis="h" gap="xs" align="center"><icon name="box" tone=${toneOf(d.state)}/><text style="headline" lines="1">${d.service}</text></stack>
      <badge tone=${toneOf(d.state)}>${d.state}</badge>
      ${progress}${button}
    </stack>`);
    return;
  }
  widget(html`
    <stack gap="xs">
      <row title=${d.service} subtitle=${`${d.version} → ${d.env}`} icon="box" badge=${d.state} tone=${toneOf(d.state)}/>
      ${progress}
      <stack axis="h" gap="s" align="center">
        <icon name="clock" tone="muted"/>
        <text style="caption" tone="muted">${`${d.minutes} min in`}</text>
        <badge tone=${d.errors > 0.02 ? 'warn' : 'ok'}>${`${(d.errors * 100).toFixed(1)}% errors`}</badge>
        <chart kind="spark" y="percent" height="xs" series=${[{ name: 'errors', points: d.errorSeries }]}/>
        ${button}
      </stack>
    </stack>`);
}
native.on('widgetsize', paint);
paint();
load();
