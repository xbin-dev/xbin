/**
 * bx-grant-row — the one rendering of a grant or pending request's
 * "from → target" with its tooltip: the policy block reason when the row
 * is blocked, else what the capability target means (bx-allow's capInfo),
 * else nothing. Used by the shell's grants panel, the admin console's
 * binding tab and the organisations tile, so a caller-target pair reads
 * the same everywhere. A template, not an element: it sits inside table
 * cells and spans that keep their own font and layout.
 *
 *   import { grantArrow } from '/vendor/bx-grant-row.js';
 *   html`<td class="mono">${grantArrow(g)}</td>`   // g: {from, target, blocked?}
 */
import { html } from 'lit';
import { capInfo } from '/vendor/bx-allow.js';

export function grantArrow(g) {
  return html`<span class="grant-arrow" title=${g.blocked ?? capInfo(g.target)?.desc ?? ''}>${g.from} → ${g.target}</span>`;
}
