// stack-layout — an on-call dashboard laid out with stacks: vertical and
// horizontal, every gap token, start/center/end alignment, a wrapping row
// of tags, spacers pushing content apart, and dividers.
import { html, render, repeat, nothing } from '/vendor/xb-native.js';

let d = null;

async function load() {
  d = await (await xbin.fetch(`/api/${xbin.self}/today`)).json();
  paint();
}

const kpi = (k) => html`
  <stack axis="v" gap="xs" align="center">
    <text style="title" tone=${k.tone ?? nothing}>${k.value}</text>
    <text style="caption" tone="muted">${k.label}</text>
  </stack>`;

const paint = () => render(!d ? nothing : html`
  <screen title="On call" style="scroll">
    <stack axis="v" gap="xxl">
      <stack axis="h" gap="m" align="start">
        <icon name="bell" tone="accent"/>
        <stack axis="v" gap="none">
          <text style="headline">${d.person}</text>
          <text style="subheadline" tone="muted">${`on call until ${d.until}`}</text>
        </stack>
        <spacer/>
        <badge tone=${d.pages ? 'warn' : 'ok'}>${d.pages ? `${d.pages} pages` : 'quiet'}</badge>
      </stack>
      <stack axis="h" gap="xl" align="center">
        ${d.kpis.map(kpi)}
      </stack>
      <divider/>
      <stack axis="v" gap="l">
        <text style="title3">Services</text>
        ${repeat(d.services, (s) => s.name, (s) => html`
          <stack axis="h" gap="s" align="center">
            <icon name=${s.ok ? 'check' : 'warning'} tone=${s.ok ? 'ok' : 'danger'}/>
            <text>${s.name}</text>
            <spacer/>
            <text style="footnote" tone="muted">${s.latency}</text>
          </stack>`)}
      </stack>
      <divider/>
      <stack axis="v" gap="s" align="end">
        <text style="footnote" tone="muted">Handover notes</text>
        <text>${d.handover}</text>
      </stack>
      <stack axis="h" gap="xs" wrap>
        ${repeat(d.tags, (t) => t, (t) => html`<badge tone="muted">${t}</badge>`)}
      </stack>
    </stack>
  </screen>`);

load();
