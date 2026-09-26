// text — an incident report: every type role (largeTitle … caption2, mono),
// every tone, monospaced and selectable text, and a summary clamped to two
// lines. Times are formatted with Intl in the fixture's pinned zone/locale.
import { html, render, repeat, nothing } from '/vendor/xb-native.js';

let inc = null;

const time = (iso) => new Intl.DateTimeFormat(undefined, { hour: '2-digit', minute: '2-digit' }).format(new Date(iso));
const day = (iso) => new Intl.DateTimeFormat(undefined, { weekday: 'long', month: 'long', day: 'numeric' }).format(new Date(iso));
const mins = (a, b) => Math.round((Date.parse(b) - Date.parse(a)) / 60000);
const stateTone = { investigating: 'danger', identified: 'warn', monitoring: 'accent', resolved: 'ok' };

async function load() {
  const r = await xbin.fetch(`/api/${xbin.self}/incidents/INC-2041`);
  inc = await r.json();
  paint();
}

const paint = () => render(!inc ? nothing : html`
  <screen title=${inc.id} style="scroll">
    <stack gap="l">
      <stack gap="xs">
        <text style="caption" tone="muted">${day(inc.started)}</text>
        <text style="largeTitle">${inc.title}</text>
        <text style="subheadline" tone=${stateTone[inc.state]}>${`${inc.state} · SEV-${inc.sev} · ${mins(inc.started, inc.updated)} min`}</text>
      </stack>
      <text style="body" lines=${2}>${inc.summary}</text>
      <stack gap="s">
        <text style="title">Impact</text>
        <text style="callout" tone="danger">${inc.impact}</text>
        <text style="callout" tone="ok">${inc.recovery}</text>
        <text style="footnote" tone="muted">${`Reported by ${inc.reporter} · owner ${inc.owner}`}</text>
      </stack>
      <stack gap="s">
        <text style="title2">Timeline</text>
        ${repeat(inc.timeline, (t) => t.at, (t) => html`
          <stack gap="none">
            <text style="caption2" tone="muted">${time(t.at)}</text>
            <text style="headline" tone=${stateTone[t.state] ?? nothing}>${t.state}</text>
            <text>${t.text}</text>
          </stack>`)}
      </stack>
      <stack gap="s">
        <text style="title3">Root cause</text>
        <text>${inc.cause}</text>
        <text style="mono">${inc.logLine}</text>
        <text mono selectable style="footnote">${inc.traceId}</text>
      </stack>
      <text style="caption" tone="accent">Postmortem due Thursday</text>
    </stack>
  </screen>`);

load();
