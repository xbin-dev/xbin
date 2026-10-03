// styles.js — the tracker's look, on the workspace theme tokens.
import { css } from 'lit';

export const styles = css`
  :host { display: block; height: 100%; overflow: auto; font: var(--bx-font); color: var(--bx-text); background: var(--bx-panel); }
  * { box-sizing: border-box; }
  ::selection { background: var(--bx-selection); color: var(--bx-selection-text); }
  :focus-visible { outline: var(--bx-focus-outline); outline-offset: var(--bx-focus-offset); box-shadow: var(--bx-focus-halo); }
  .ok { --st: var(--bx-ok); --st-bg: var(--bx-ok-bg); }
  .warn { --st: var(--bx-warn); --st-bg: var(--bx-warn-bg); }
  .bad { --st: var(--bx-danger); --st-bg: var(--bx-danger-bg); }
  .wrap { padding: 16px 20px 20px; }
  header { display: flex; align-items: baseline; gap: 12px; margin-bottom: 16px; }
  h1 { margin: 0; font: var(--bx-font-heading); letter-spacing: var(--bx-tracking-heading); }
  .sub { color: var(--bx-muted); }
  .grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(330px, 1fr)); gap: 12px; }
  .card { background: var(--bx-panel); border: 1px solid var(--bx-border); border-radius: var(--bx-radius); padding: 12px 12px 8px; }
  .top { display: flex; align-items: flex-start; gap: 10px; }
  .name { font: var(--bx-font-title); }
  .what { color: var(--bx-muted); margin-top: 2px; }
  .facts { display: flex; flex-wrap: wrap; gap: 6px; margin: 8px 0 10px; }
  .chip { display: inline-flex; align-items: center; gap: 4px; height: 20px; padding: 0 6px; border: 1px solid var(--bx-border); border-radius: var(--bx-radius);
          font: var(--bx-font-micro); letter-spacing: var(--bx-tracking-micro); text-transform: uppercase; color: var(--bx-muted); white-space: nowrap; }
  .chip bx-icon { --bx-icon-size: 14px; margin-left: -2px; }
  .chip.ok, .chip.warn, .chip.bad { color: var(--st); background: var(--st-bg); border-color: color-mix(in srgb, var(--st) 45%, transparent); }
  .av { margin-left: auto; display: inline-grid; place-items: center; width: 28px; height: 28px; flex: none; border-radius: var(--bx-radius);
        background: var(--bx-panel-2); border: 1px solid var(--bx-border); color: var(--bx-text); font: var(--bx-font-micro); letter-spacing: 0.02em; }
  /* progress: what's done in the ok colour, on a concrete track */
  .bar { height: 8px; background: var(--bx-panel-2); border: 1px solid var(--bx-border); overflow: hidden; }
  .bar i { display: block; height: 100%; background: var(--bx-ok); }
  .prog { display: flex; justify-content: space-between; font: var(--bx-font-meta); font-variant-numeric: tabular-nums; color: var(--bx-muted); margin: 6px 0; }
  .prog b { color: var(--bx-text); font-weight: 600; }
  .next { padding: 6px 10px; border-radius: var(--bx-radius); background: var(--bx-panel-2); border: 1px solid var(--bx-border); margin-bottom: 4px; }
  .next span { color: var(--bx-muted); }
  .next.late { background: var(--bx-danger-bg); border-color: color-mix(in srgb, var(--bx-danger) 45%, transparent); }
  .next.late bx-icon, .next.late span { color: var(--bx-danger); }
  .steps { margin-top: 6px; border-top: 1px solid var(--bx-border); padding-top: 4px; }
  .step { display: grid; grid-template-columns: 18px 1fr auto; gap: 8px; align-items: center; min-height: var(--bx-row); cursor: pointer; }
  .step input { margin: 0; }
  .step.done .st { color: var(--bx-muted); text-decoration: line-through; }
  .step .due { display: inline-flex; align-items: center; gap: 4px; font: var(--bx-font-meta); font-variant-numeric: tabular-nums; color: var(--bx-muted); }
  .step.late .due { color: var(--bx-danger); }
  .fold, .toggle { display: inline-flex; align-items: center; gap: 4px; background: none; border: 0; color: var(--bx-muted); font: inherit; padding: 4px 0; cursor: pointer; }
  .fold:hover, .toggle:hover { color: var(--bx-accent); }
  footer { margin-top: 16px; font: var(--bx-font-meta); color: var(--bx-muted); }
  .err { color: var(--bx-danger); }
`;
