// styles.js — the tracker's look, on the workspace theme tokens.
import { css } from 'lit';

export const styles = css`
  :host { display: block; height: 100%; overflow: auto; font: var(--bx-font); color: var(--bx-text); background: var(--bx-panel);
          --ok: var(--bx-green); --bad: var(--bx-red); --warn: var(--bx-amber); }
  * { box-sizing: border-box; }
  .wrap { padding: 14px 18px 20px; }
  header { display: flex; align-items: baseline; gap: 10px; margin-bottom: 12px; }
  h1 { margin: 0; font-size: 17px; font-weight: 650; }
  .sub { color: var(--bx-muted); font-size: 12px; }
  .grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(330px, 1fr)); gap: 12px; }
  .card { background: var(--bx-panel-2); border: 1px solid var(--bx-border); border-radius: 10px; padding: 11px 13px 9px; }
  .top { display: flex; align-items: flex-start; gap: 10px; }
  .name { font-weight: 650; font-size: 13.5px; }
  .what { color: var(--bx-muted); font-size: 11.8px; margin-top: 1px; }
  .facts { display: flex; flex-wrap: wrap; gap: 6px; margin: 8px 0 9px; }
  .chip { display: inline-flex; align-items: center; gap: 5px; padding: 1px 8px; border-radius: 999px; font-size: 10.8px; border: 1px solid var(--bx-border); color: var(--bx-muted); }
  .dot { width: 7px; height: 7px; border-radius: 50%; background: var(--c, var(--bx-muted)); }
  .av { margin-left: auto; display: inline-grid; place-items: center; width: 26px; height: 26px; border-radius: 50%; flex: none;
        font-size: 10.5px; font-weight: 700; color: #fff; background: hsl(var(--h, 210) 45% 42%); }
  .bar { height: 7px; border-radius: 4px; background: var(--bx-border); overflow: hidden; }
  .bar i { display: block; height: 100%; border-radius: 4px; background: linear-gradient(90deg, #12857a, #3fbfae); }
  .prog { display: flex; justify-content: space-between; font-size: 11.3px; color: var(--bx-muted); margin: 5px 0 6px; }
  .prog b { color: var(--bx-text); font-weight: 600; }
  .next { font-size: 12px; padding: 6px 9px; border-radius: 7px; background: color-mix(in srgb, var(--bx-accent) 8%, transparent);
          border: 1px solid color-mix(in srgb, var(--bx-accent) 25%, transparent); margin-bottom: 4px; }
  .next.late { background: color-mix(in srgb, var(--bad) 10%, transparent); border-color: color-mix(in srgb, var(--bad) 35%, transparent); }
  .next span { color: var(--bx-muted); }
  .steps { margin-top: 6px; border-top: 1px solid var(--bx-border); padding-top: 4px; }
  .step { display: grid; grid-template-columns: 18px 1fr auto; gap: 8px; align-items: center; padding: 3px 0; font-size: 12px; cursor: pointer; }
  .step input { margin: 0; accent-color: #12857a; }
  .step.done .st { color: var(--bx-muted); text-decoration: line-through; text-decoration-color: color-mix(in srgb, var(--bx-muted) 60%, transparent); }
  .step .due { font-size: 10.8px; color: var(--bx-muted); }
  .step.late .due { color: var(--bad); }
  .fold { background: none; border: 0; color: var(--bx-muted); font: inherit; font-size: 11.3px; padding: 2px 0; cursor: pointer; }
  .toggle { background: none; border: 0; color: var(--bx-muted); font: inherit; font-size: 11.3px; cursor: pointer; padding: 3px 0 0; }
  footer { margin-top: 14px; color: var(--bx-muted); font-size: 11px; }
  .err { color: var(--bad); }
`;
