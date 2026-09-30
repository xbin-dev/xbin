/**
 * The "usage by partition" section of the llm-gw page (API.md "Usage by
 * partition"): GET /stats' `callers` rows (calls from partitioned tiles, per
 * tile, deployment and partition — the ones this viewer may see: their own
 * partitions', the global instances' for a manager, every one for the owner
 * token), its `callerTotals` (a manager's: each tile's people's partitions
 * together) and the optional fairness limit (PUT /config {partitionLimit}),
 * offered when `canManage`. Nothing renders until a partitioned tile has
 * called or a limit is set, so the page of a workspace without partitioned
 * tiles is unchanged.
 */
import { html, nothing } from 'lit';

const fmtN = (n) => String(Math.round(Number(n) || 0)).replace(/\B(?=(\d{3})+(?!\d))/g, ',');
const cost = (c) => (c ? '$' + Number(c).toFixed(2) : '—');

// "3 min ago" from unix ms.
function ago(ms) {
  if (!ms) return '—';
  const s = Math.max(0, (Date.now() - ms) / 1000);
  if (s < 60) return 'just now';
  if (s < 3600) return `${Math.floor(s / 60)} min ago`;
  if (s < 86400) return `${Math.floor(s / 3600)} h ago`;
  return `${Math.floor(s / 86400)} d ago`;
}

const tile = (r) => html`<td class="mono">${r.from}${r.deployment ? html`<span class="muted">#${r.deployment}</span>` : nothing}</td>`;

// who a row is: the global instance, or the person whose partition it is.
const whose = (r) => r.partition === 'global' ? html`<span class="muted">global instance</span>`
  : html`${String(r.partition).replace(/^user:/, '')}<span class="muted">'s partition</span>`;

const num = (n) => html`<td class="mono" style="text-align:right">${fmtN(n)}</td>`;

/**
 * callersView renders the section. s: {callers, callerTotals, canManage}
 * from GET /stats; limit: GET /config's partitionLimit (0 = off); onLimit(n)
 * saves one.
 */
export function callersView(s, limit, onLimit) {
  const rows = s?.callers ?? [], totals = s?.callerTotals ?? [], manage = !!s?.canManage;
  if (!rows.length && !totals.length && !manage) return nothing;
  const save = (e) => {
    e.preventDefault();
    const n = Number(e.target.limit.value);
    if (Number.isInteger(n) && n >= 0 && n <= 64) onLimit(n);
  };
  return html`
    <h4>usage by partition</h4>
    <div class="muted" style="font-size:11px; margin-bottom:5px">
      Calls from partitioned tiles: requests and tokens only, never what was
      said. Each person sees their own partitions; a manager of this tile
      each tile's people together and its global instance.
    </div>
    ${totals.length ? html`<table data-caller-totals style="margin-bottom:6px">
      <tr><th>tile</th><th>people's partitions</th><th style="text-align:right">reqs</th>
          <th style="text-align:right">tok in</th><th style="text-align:right">tok out</th><th style="text-align:right">cost</th></tr>
      ${totals.map((t) => html`<tr>${tile(t)}<td>${t.partitions} together</td>${num(t.reqs)}${num(t.tokIn)}${num(t.tokOut)}
        <td class="mono muted" style="text-align:right">${cost(t.cost)}</td></tr>`)}
    </table>` : nothing}
    ${rows.length ? html`<table data-callers>
      <tr><th>tile</th><th>partition</th><th style="text-align:right">reqs</th>
          <th style="text-align:right">tok in</th><th style="text-align:right">tok out</th>
          <th style="text-align:right">cost</th><th style="text-align:right">active</th><th style="text-align:right">last</th></tr>
      ${rows.map((r) => html`<tr>
        ${tile(r)}<td>${whose(r)}</td>${num(r.reqs)}${num(r.tokIn)}${num(r.tokOut)}
        <td class="mono muted" style="text-align:right">${cost(r.cost)}</td>
        <td class="mono" style="text-align:right">${r.active ? html`<span class="ok">${r.active}</span>` : '0'}${r.waiting
          ? html` <span class="warn" title="waiting under the fairness limit">+${r.waiting}</span>` : nothing}</td>
        <td class="muted" style="text-align:right; white-space:nowrap">${ago(r.last)}</td>
      </tr>`)}
    </table>` : nothing}
    ${manage ? html`<form class="row" style="margin-top:6px" @submit=${save}>
      <span class="muted" style="font-size:11px">Fairness limit: at most</span>
      <input name="limit" type="number" min="0" max="64" size="3" style="width:4em" .value=${String(limit || 0)}>
      <span class="muted" style="font-size:11px">calls at once from one person's partition (0 = off; more wait up to 20 s, then are told to retry)</span>
      <button class="act">save</button>
    </form>` : nothing}`;
}
