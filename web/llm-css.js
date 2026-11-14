/**
 * llm-css.js — what the LLM connector's elements share (D226): the slice of
 * styles over /vendor/bx-limits.js's plates and buttons (sections, the
 * settings tables, a form's grid, a limit's bar, the host's lock), and the
 * two templates every one of them draws — the "Set by your host" badge and
 * an error line. Lit by its /vendor URL (xbind's own page has no import map).
 */
import { html, css, nothing } from '/vendor/lit-all.min.js';
import '/vendor/bx-icons.js';
import { lockLabel } from '/vendor/llm-kit.js';

export const llmCss = css`
  h3 { margin: 28px 0 4px; display: flex; gap: 8px; align-items: center; flex-wrap: wrap;
    font: var(--bx-font-title, 600 15px/20px "Instrument Sans", system-ui, sans-serif); }
  h3:first-child { margin-top: 0; }
  h4 { margin: 16px 0 4px; display: flex; gap: 8px; align-items: center; flex-wrap: wrap;
    font: var(--bx-font, 13px/18px "Instrument Sans", system-ui, sans-serif); font-weight: 600; }
  .hint { color: var(--bx-muted, #A3A6B6); max-width: 76ch; margin: 0 0 8px; }
  a { color: var(--bx-link, #8C9BFF); text-decoration: none; }
  a:hover { text-decoration: underline; }
  code { font-family: var(--bx-mono, "JetBrains Mono", ui-monospace, monospace); }
  table { border-collapse: collapse; width: 100%; }
  th { text-align: left; padding: 4px 12px 4px 0; color: var(--bx-muted, #A3A6B6); border-bottom: 1px solid var(--bx-border-strong, #666A7E); white-space: nowrap;
    font: var(--bx-font-micro, 600 11px/14px "Instrument Sans", system-ui, sans-serif); letter-spacing: var(--bx-tracking-micro, 0.06em); text-transform: uppercase; }
  td { padding: 6px 12px 6px 0; vertical-align: top; border-top: 1px solid var(--bx-border, #33353F); }
  tr.sub > td { border-top: 0; padding-top: 0; }
  th.num, td.num { text-align: right; white-space: nowrap; }
  td.acts { text-align: right; white-space: nowrap; padding-right: 0; }
  td.acts button + button { margin-left: 6px; }
  th button.sort { min-height: 0; padding: 0; border: 0; background: transparent; color: inherit; font: inherit; letter-spacing: inherit;
    text-transform: inherit; display: inline-flex; gap: 4px; align-items: center; }
  th button.sort:hover:not(:disabled) { background: transparent; color: var(--bx-text, #E9EAF0); }
  th[aria-sort="ascending"] button.sort, th[aria-sort="descending"] button.sort { color: var(--bx-text, #E9EAF0); }
  tfoot td { font-weight: 600; border-top: 1px solid var(--bx-border-strong, #666A7E); }
  .empty { color: var(--bx-muted, #A3A6B6); padding: 8px 0; }
  .line { display: flex; flex-wrap: wrap; gap: 8px; align-items: center; margin-top: 8px; }
  .grow { flex: 1; }
  .ok { color: var(--bx-ok, #A3CF5E); }
  .bad { color: var(--bx-danger, #FF7A7A); }
  .warnc { color: var(--bx-warn, #F2994A); }
  /* the host's lock reads as a sentence, not as micro caps: "Set by your host" */
  .badge.lock { color: var(--bx-info, #A9B4C6); border-color: var(--bx-info, #A9B4C6); background: var(--bx-info-bg, #30323B);
    font: var(--bx-font-meta, 400 12px/16px "Instrument Sans", system-ui, sans-serif); letter-spacing: 0; text-transform: none; }
  .note { display: flex; gap: 8px; align-items: flex-start; padding: 8px 12px; margin: 0 0 12px; border-radius: var(--bx-radius, 2px);
    border: 1px solid var(--bx-info, #A9B4C6); background: var(--bx-info-bg, #30323B); color: var(--bx-text, #E9EAF0); }
  .note.warn { border-color: var(--bx-warn, #F2994A); background: var(--bx-warn-bg, #382F2C); }
  .note.crit { border-color: var(--bx-danger, #FF7A7A); background: var(--bx-danger-bg, #3A2B32); }
  .note bx-icon { flex: none; margin-top: 1px; }
  .note > span { flex: 1; min-width: 0; }
  .form { display: grid; grid-template-columns: minmax(8em, max-content) minmax(0, 1fr); gap: 6px 12px; align-items: center; max-width: 720px; }
  .form > label { color: var(--bx-muted, #A3A6B6); }
  .form input:not([type=checkbox]):not([type=radio]) { width: 100%; }
  .form .wide { grid-column: 1 / -1; }
  .form .meta { grid-column: 2; margin-top: -2px; }
  .editor { margin: 8px 0 12px; padding: 12px; border: 1px solid var(--bx-border, #33353F); border-radius: var(--bx-radius, 2px); background: var(--bx-panel-2, #262730); }
  label.sw { display: flex; gap: 8px; align-items: flex-start; margin: 6px 0; }
  label.sw input { margin-top: 2px; flex: none; height: auto; }
  input:disabled, select:disabled { opacity: .6; cursor: default; }
  .limits { display: grid; grid-template-columns: repeat(4, minmax(7em, 1fr)) minmax(8em, max-content); gap: 4px 8px; align-items: end; max-width: 760px; }
  .limits label { display: flex; flex-direction: column; gap: 2px; color: var(--bx-muted, #A3A6B6);
    font: var(--bx-font-meta, 400 12px/16px "Instrument Sans", system-ui, sans-serif); }
  .limits input, .limits select { width: 100%; }
  .use { display: grid; grid-template-columns: minmax(9em, max-content) minmax(6em, 14em) minmax(0, 1fr); gap: 4px 12px; align-items: center; margin: 4px 0; }
  .track { height: 6px; background: var(--bx-border, #33353F); border-radius: var(--bx-radius, 2px); overflow: hidden; }
  .track > i { display: block; height: 100%; background: var(--bx-accent, #8C9BFF); }
  .track.warn > i { background: var(--bx-warn, #F2994A); }
  .track.stop > i { background: var(--bx-danger, #FF7A7A); }
  details.sub { border-top: 1px solid var(--bx-border, #33353F); padding: 6px 0; }
  details.sub > summary { cursor: pointer; display: flex; gap: 8px; align-items: baseline; flex-wrap: wrap; }
  details.sub > summary + * { margin-top: 8px; }
  @media (max-width: 720px) {
    .form, .use { grid-template-columns: minmax(0, 1fr); }
    .form .meta { grid-column: 1; }
    .limits { grid-template-columns: minmax(0, 1fr) minmax(0, 1fr); }
    thead { display: none; }
    table, tbody, tfoot, tr, td { display: block; }
    tr { border-top: 1px solid var(--bx-border, #33353F); padding: 6px 0; }
    td, tfoot td { border: 0; padding: 1px 0; text-align: left; }
    td.num, td.acts { text-align: left; }
    td[data-l]::before { content: attr(data-l) " "; color: var(--bx-muted, #A3A6B6);
      font: var(--bx-font-micro, 600 11px/14px "Instrument Sans", system-ui, sans-serif); letter-spacing: var(--bx-tracking-micro, 0.06em); text-transform: uppercase; }
  }
`;

// lockTpl(lock) → the badge beside a setting the host's file holds
// (lockOf()'s answer): "Set by your host". Nothing for a setting that isn't.
export const lockTpl = (lock) => (lock?.locked ? html`<span class="badge lock" data-locked
  title="your host’s deployment file sets this; it can’t be changed here"><bx-icon name="lock"></bx-icon>${lockLabel(lock.reason)}</span>` : nothing);

// errTpl(message) → an error line, or nothing.
export const errTpl = (msg) => (msg ? html`<div class="err" role="alert"><bx-icon name="error"></bx-icon><span>${msg}</span></div>` : nothing);
