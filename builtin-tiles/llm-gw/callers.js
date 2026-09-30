/**
 * The "usage by partition" section of the llm-gw page (API.md "Usage by
 * partition"): GET /stats' `callers` rows — calls from partitioned tiles,
 * per (tile, deployment, person's partition), metadata only — and the
 * optional fairness limit (PUT /config {partitionLimit}). Nothing renders
 * until a partitioned tile has called or a limit is set, so the page of a
 * workspace without partitioned tiles is unchanged.
 */
import { html, nothing } from 'lit';

const fmtN = (n) => String(Math.round(Number(n) || 0)).replace(/\B(?=(\d{3})+(?!\d))/g, ',');

// "3 min ago" from unix ms.
function ago(ms) {
  if (!ms) return '—';
  const s = Math.max(0, (Date.now() - ms) / 1000);
  if (s < 60) return 'just now';
  if (s < 3600) return `${Math.floor(s / 60)} min ago`;
  if (s < 86400) return `${Math.floor(s / 3600)} h ago`;
  return `${Math.floor(s / 86400)} d ago`;
}

// who a row is: the global instance, or the person whose partition it is.
const whose = (r) => r.partition === 'global' ? html`<span class="muted">global instance</span>`
  : html`${String(r.partition).replace(/^user:/, '')}<span class="muted">'s partition</span>`;

/**
 * callersView renders the section. rows: GET /stats' callers (or []);
 * limit: GET /config's partitionLimit (0 = off); onLimit(n) saves one.
 */
export function callersView(rows, limit, onLimit) {
  if (!rows?.length && !limit) return nothing;
  const save = (e) => {
    e.preventDefault();
    const n = Number(e.target.limit.value);
    if (Number.isInteger(n) && n >= 0 && n <= 64) onLimit(n);
  };
  return html`
    <h4>usage by partition</h4>
    <div class="muted" style="font-size:11px; margin-bottom:5px">
      Calls from partitioned tiles, one row per person's partition (and the
      tile's global instance): requests and tokens only, never what was said.
      A manager of this tile sees every row; anyone else sees their own.
    </div>
    <table data-callers>
      <tr><th>tile</th><th>partition</th><th style="text-align:right">reqs</th>
          <th style="text-align:right">tok in</th><th style="text-align:right">tok out</th>
          <th style="text-align:right">cost</th><th style="text-align:right">active</th><th style="text-align:right">last</th></tr>
      ${(rows ?? []).map((r) => html`<tr>
        <td class="mono">${r.from}${r.deployment ? html`<span class="muted">#${r.deployment}</span>` : nothing}</td>
        <td>${whose(r)}</td>
        <td class="mono" style="text-align:right">${fmtN(r.reqs)}</td>
        <td class="mono" style="text-align:right">${fmtN(r.tokIn)}</td>
        <td class="mono" style="text-align:right">${fmtN(r.tokOut)}</td>
        <td class="mono muted" style="text-align:right">${r.cost ? '$' + Number(r.cost).toFixed(2) : '—'}</td>
        <td class="mono" style="text-align:right">${r.active ? html`<span class="ok">${r.active}</span>` : '0'}${r.waiting
          ? html` <span class="warn" title="waiting under the fairness limit">+${r.waiting}</span>` : nothing}</td>
        <td class="muted" style="text-align:right; white-space:nowrap">${ago(r.last)}</td>
      </tr>`)}
    </table>
    <form class="row" style="margin-top:6px" @submit=${save}>
      <span class="muted" style="font-size:11px">Fairness limit: at most</span>
      <input name="limit" type="number" min="0" max="64" size="3" style="width:4em" .value=${String(limit || 0)}>
      <span class="muted" style="font-size:11px">calls at once from one person's partition (0 = off; more wait their turn)</span>
      <button class="act">save</button>
    </form>`;
}
