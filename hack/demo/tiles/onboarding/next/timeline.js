// timeline.js — the go-lives ahead on one line: today on the left, a mark
// per customer at its go-live date, red where one of its steps is late.
import { html, css } from 'lit';

const DAY = 864e5;
const day = (t) => new Date(t).toLocaleDateString('en-US', { month: 'short', day: 'numeric' });
const firstWords = (n) => String(n).split(/\s+/).slice(0, 2).join(' ');

export const timelineCss = css`
  .tl { position: relative; margin: 0 0 16px; padding: 10px 14px 12px; border: 1px solid var(--bx-border); border-radius: var(--bx-radius); background: var(--bx-panel-2); }
  .tl h2 { margin: 0 0 6px; font: var(--bx-font-micro); letter-spacing: var(--bx-tracking-micro); text-transform: uppercase; color: var(--bx-muted); }
  .tl .lane { position: relative; height: 104px; margin: 0 8px; }
  .tl .track { position: absolute; left: 0; right: 0; bottom: 18px; height: 4px; background: var(--bx-border); }
  .tl .mark { position: absolute; bottom: 14px; width: 12px; height: 12px; margin-left: -6px; background: var(--bx-ok); box-shadow: 0 0 0 3px var(--bx-panel-2); z-index: 1; }
  /* a late go-live's mark is drawn over a neighbour's, never hidden under it */
  .tl .mark.late { background: var(--bx-danger); z-index: 2; }
  .tl .stem { position: absolute; bottom: 26px; width: 1px; background: var(--bx-border-strong); }
  .tl .tag { position: absolute; font: var(--bx-font-meta); white-space: nowrap; padding: 1px 0; }
  .tl .tag b { font-weight: 600; }
  .tl .tag span { color: var(--bx-muted); }
  .tl .tag.late b, .tl .tag.late span { color: var(--bx-danger); }
  .tl .tag.end { transform: translateX(-100%); text-align: right; }
  .tl .tick { position: absolute; bottom: 0; font: var(--bx-font-meta); color: var(--bx-muted); transform: translateX(-50%); }
  .tl .tick.now { color: var(--bx-text); font-weight: 600; transform: none; }
`;

// timeline(customers): soonest first is how the board sorts them already;
// labels stagger over four rows so close go-lives don't collide — the
// soonest on the top row, so no later mark's stem crosses an earlier label
export function timeline(cs, now = Date.now()) {
  if (!cs?.length) return '';
  const end = Math.max(...cs.map((c) => c.goLive)) + 7 * DAY;
  const at = (t) => Math.max(0, Math.min(100, (100 * (t - now)) / (end - now)));
  const months = [];
  for (let d = new Date(now); d.getTime() < end;) {
    d = new Date(d.getFullYear(), d.getMonth() + 1, 1);
    if (d.getTime() < end) months.push(d.getTime());
  }
  return html`<section class="tl">
    <h2>Go-lives ahead</h2>
    <div class="lane">
      <div class="track"></div>
      ${cs.map((c, i) => {
        const late = c.steps.some((s) => !s.done && s.due && s.due < now);
        const x = at(c.goLive), row = 3 - (i % 4);
        return html`
          <div class="stem" style="left:${x}%; height:${8 + row * 18}px"></div>
          <div class="mark ${late ? 'late' : ''}" style="left:${x}%"></div>
          <div class="tag ${late ? 'late' : ''} ${x > 70 ? 'end' : ''}" style="left:${x}%; bottom:${34 + row * 18}px">
            <b>${firstWords(c.name)}</b> <span>${day(c.goLive)}${late ? ' · late step' : ''}</span></div>`;
      })}
      <div class="tick now" style="left:0">today</div>
      ${months.map((m) => html`<div class="tick" style="left:${at(m)}%">${new Date(m).toLocaleDateString('en-US', { month: 'short' })}</div>`)}
    </div>
  </section>`;
}
