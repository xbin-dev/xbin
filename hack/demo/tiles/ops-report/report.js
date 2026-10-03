// report.js — the nightly ops report page: one night's report (newest by
// default), a 14-night trend, the customers' table, notes and incidents.
import { LitElement, html, css, svg, nothing } from 'lit';
import { scrollCss } from '/vendor/scroll-css.js';
import { getJSON, api, num, dateShort, timeShort, avatar, STATUS_ICON, baseCss } from './ui.js';

const longDate = (k) => new Date(k + 'T12:00:00').toLocaleDateString('en-US', { weekday: 'long', month: 'long', day: 'numeric' });
const pct = (v, n = 1) => (v == null ? '—' : `${Number(v).toFixed(n)}%`);
// a change against the night before: its sign, good or bad in colour
const delta = (a, b, n = 1, unit = '', invert = false) => {
  if (a == null || b == null) return nothing;
  const d = a - b;
  if (Math.abs(d) < 10 ** -n / 2) return html`<span class="d flat">±0${unit}</span>`;
  const good = invert ? d < 0 : d > 0;
  return html`<span class="d ${good ? 'up' : 'down'}">${d > 0 ? '+' : '−'}${Math.abs(d).toFixed(n)}${unit}</span>`;
};

class OpsReport extends LitElement {
  static properties = { _list: { state: true }, _runs: { state: true }, _rep: { state: true }, _prev: { state: true }, _sel: { state: true }, _me: { state: true }, _busy: { state: true }, _err: { state: true } };

  static styles = [scrollCss, baseCss, css`
    :host { overflow: auto; container-type: inline-size; }
    .wrap { padding: 16px 20px 24px; max-width: 1100px; }
    header { display: flex; align-items: center; gap: 12px; flex-wrap: wrap; margin-bottom: 12px; }
    h1 { margin: 0; font: var(--bx-font-heading); letter-spacing: var(--bx-tracking-heading); }
    .sub { color: var(--bx-muted); margin-top: 2px; }
    .sp { flex: 1; }
    .nav { display: flex; align-items: center; gap: 4px; }
    .status { display: flex; align-items: center; gap: 8px; padding: 8px 12px; border-radius: var(--bx-radius); margin-bottom: 16px;
              background: var(--st-bg); border: 1px solid color-mix(in srgb, var(--st) 45%, transparent); }
    .status bx-icon, .status b { color: var(--st); }
    .status b { font-weight: 600; }
    .grid { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 12px; margin-bottom: 16px; }
    .k { background: var(--bx-panel-2); border: 1px solid var(--bx-border); border-radius: var(--bx-radius); padding: 10px 12px; }
    .k b { display: block; margin: 4px 0 2px; font: var(--bx-font-heading); font-family: var(--bx-sans); font-variant-numeric: tabular-nums; }
    .d { font: var(--bx-font-meta); font-weight: 600; font-variant-numeric: tabular-nums; }
    .d.up { color: var(--bx-ok); } .d.down { color: var(--bx-danger); } .d.flat { color: var(--bx-muted); }
    .k small { font: var(--bx-font-meta); color: var(--bx-muted); }
    .row2 { display: grid; grid-template-columns: 1.15fr 1fr; gap: 12px; margin-bottom: 16px; }
    .panel { background: var(--bx-panel-2); border: 1px solid var(--bx-border); border-radius: var(--bx-radius); padding: 10px 12px; min-width: 0; }
    .panel .label { display: block; margin-bottom: 8px; }
    /* the trend: two series in the theme's categorical order (blue, magenta) */
    .trend svg { display: block; width: 100%; height: 92px; }
    .trend rect { fill: color-mix(in srgb, var(--bx-term-blue) 45%, transparent); }
    .trend rect.sel { fill: var(--bx-term-blue); }
    .trend polyline { fill: none; stroke: var(--bx-term-magenta); }
    /* a night that needed a look: an 8 px square, ringed off the line it sits on */
    .trend .flag { fill: var(--bx-danger); stroke: var(--bx-panel-2); stroke-width: 2px; paint-order: stroke; vector-effect: non-scaling-stroke; }
    .legend { display: flex; gap: 12px; font: var(--bx-font-meta); color: var(--bx-muted); margin-top: 4px; }
    .legend i { display: inline-block; width: 10px; height: 10px; border-radius: var(--bx-radius); margin-right: 4px; vertical-align: -1px; }
    .legend .stops { background: color-mix(in srgb, var(--bx-term-blue) 45%, transparent); }
    .legend .ontime { background: var(--bx-term-magenta); }
    .legend .flag { background: var(--bx-danger); }
    ul.notes { margin: 0; padding: 0; list-style: none; }
    ul.notes li { padding: 4px 0 4px 16px; position: relative; line-height: 1.45; }
    ul.notes li::before { content: ''; position: absolute; left: 3px; top: 11px; width: 5px; height: 5px; background: var(--bx-muted); }
    .inc { padding: 6px 10px; margin-bottom: 6px; border: 1px solid var(--bx-border); border-left: 3px solid var(--st); border-radius: var(--bx-radius);
           background: var(--st-bg); }
    .inc .t { display: flex; align-items: center; gap: 6px; font-weight: 600; }
    .inc .t bx-icon { color: var(--st); }
    .inc .m { font: var(--bx-font-meta); color: var(--bx-muted); margin-top: 4px; display: flex; align-items: center; gap: 6px; }
    table { width: 100%; border-collapse: collapse; font-variant-numeric: tabular-nums; }
    th { text-align: left; font: var(--bx-font-micro); letter-spacing: var(--bx-tracking-micro); text-transform: uppercase; color: var(--bx-muted);
         padding: 6px 8px; border-bottom: 2px solid var(--bx-text); }
    td { height: var(--bx-row); padding: 4px 8px; border-bottom: 1px solid var(--bx-border); }
    tr:last-child td { border-bottom: 0; }
    td.r, th.r { text-align: right; }
    .bar { display: flex; align-items: center; gap: 8px; justify-content: flex-end; }
    .bar span.b { display: inline-block; height: 6px; background: var(--bx-panel); border: 1px solid var(--bx-border); width: 72px; position: relative; overflow: hidden; }
    .bar span.b i { position: absolute; inset: 0 auto 0 0; background: var(--st); }
    .runs { font: var(--bx-font-meta); color: var(--bx-muted); margin-top: 12px; }
    /* a narrow card (beside the metrics): two columns of numbers (last, so it wins) */
    @container (max-width: 720px) {
      .grid { grid-template-columns: repeat(2, minmax(0, 1fr)); }
      .row2 { grid-template-columns: 1fr; }
    }
  `];

  connectedCallback() { super.connectedCallback(); this._load(); }

  async _load(date) {
    try {
      const l = await getJSON('/reports');
      this._list = l.reports; this._runs = l.runs;
      this._me ??= await getJSON('/me');
      const k = date || this._sel || l.reports[0]?.date;
      if (!k) { this._rep = null; return; }
      this._sel = k;
      this._rep = await getJSON(`/reports/${k}`);
      const i = l.reports.findIndex((x) => x.date === k);
      this._prev = l.reports[i + 1] ? await getJSON(`/reports/${l.reports[i + 1].date}`) : null;
      this._err = '';
    } catch (e) { this._err = e.message; }
  }
  _step(d) {
    const i = this._list.findIndex((x) => x.date === this._sel);
    const n = this._list[i - d];
    if (n) this._load(n.date);
  }
  async _runNow() {
    this._busy = true;
    try { await api('/run', { method: 'POST' }); this._sel = null; await this._load(); } finally { this._busy = false; }
  }

  render() {
    if (this._err) return html`<div class="err"><bx-icon name="error"></bx-icon>${this._err}</div>`;
    const r = this._rep, p = this._prev;
    if (!r) return html`<div class="empty">Loading the latest report…</div>`;
    const i = this._list.findIndex((x) => x.date === this._sel);
    const run = this._runs?.find((x) => x.date === r.date);
    const attention = r.status !== 'ok';
    const st = attention ? 'warn' : 'ok';
    return html`<div class="wrap">
      <header>
        <div>
          <h1>Nightly ops report</h1>
          <div class="sub">${longDate(r.date)} · delivered ${dateShort(r.renderedAt)} at ${timeShort(r.renderedAt)} ${run?.by === 'schedule' || !run ? 'by the 05:30 schedule' : `by ${run.by}`}</div>
        </div>
        <span class="sp"></span>
        <div class="nav">
          <button class="btn icon" title="Older" aria-label="Older" ?disabled=${i >= this._list.length - 1} @click=${() => this._step(-1)}><bx-icon name="chevron-left"></bx-icon></button>
          <select @change=${(e) => this._load(e.target.value)}>
            ${this._list.map((x) => html`<option value=${x.date} ?selected=${x.date === this._sel}>${dateShort(new Date(x.date + 'T12:00:00'))}${x.status !== 'ok' ? ' · needs a look' : ''}</option>`)}
          </select>
          <button class="btn icon" title="Newer" aria-label="Newer" ?disabled=${i <= 0} @click=${() => this._step(1)}><bx-icon name="chevron-right"></bx-icon></button>
        </div>
        ${this._me?.canRun ? html`<button class="btn" ?disabled=${this._busy} @click=${this._runNow}>${this._busy ? 'Running…' : 'Run now'}</button>` : nothing}
      </header>

      <div class="status ${st}">
        <bx-icon name=${STATUS_ICON[st]}></bx-icon>
        <span><b>${attention ? 'Needs a look' : 'All clear'}</b> — ${attention
          ? `${r.incidents.length} incident${r.incidents.length > 1 ? 's' : ''}; on-time ${pct(r.totals.onTime)} across ${num(r.totals.stops)} stops.`
          : `every planning batch finished; ${pct(r.totals.onTime)} of ${num(r.totals.stops)} stops arrived on time.`}</span>
      </div>

      <div class="grid">
        <div class="k"><span class="label">Routes planned</span><b>${num(r.totals.routes)}</b>${delta(r.totals.routes, p?.totals.routes, 0)}<small> vs night before</small></div>
        <div class="k"><span class="label">Stops</span><b>${num(r.totals.stops)}</b><small>${num(r.totals.exceptions)} exceptions</small></div>
        <div class="k"><span class="label">On time</span><b>${pct(r.totals.onTime)}</b>${delta(r.totals.onTime, p?.totals.onTime, 1, ' pt')}</div>
        <div class="k"><span class="label">Plan time p95</span><b>${r.platform.planSecondsP95.toFixed(1)} s</b>${delta(r.platform.planSecondsP95, p?.platform.planSecondsP95, 1, ' s', true)}</div>
        <div class="k"><span class="label">API p95</span><b>${num(r.platform.apiP95ms)} ms</b><small>${r.platform.apiRequestsM.toFixed(1)}M requests · ${r.platform.apiErrorPct.toFixed(2)}% errors</small></div>
        <div class="k"><span class="label">Uptime</span><b>${pct(r.platform.uptimePct, r.platform.uptimePct === 100 ? 0 : 2)}</b><small>routing API and dispatch</small></div>
        <div class="k"><span class="label">Drivers on the road</span><b>${num(r.platform.drivers)}</b><small>${pct(r.platform.crashFreePct, 2)} crash-free</small></div>
        <div class="k"><span class="label">Support</span><b>${r.support.opened} in · ${r.support.closed} out</b><small>first reply in ${r.support.firstResponseMin} min</small></div>
      </div>

      <div class="row2">
        <div class="panel trend"><span class="label">Last ${this._list.length} nights</span>${this._trend()}
          <div class="legend"><span><i class="stops"></i>stops</span><span><i class="ontime"></i>on-time %</span><span><i class="flag"></i>needed a look</span></div></div>
        <div class="panel"><span class="label">${r.incidents.length ? 'Incidents and notes' : 'Notes'}</span>
          ${r.incidents.map((x) => {
            const s = x.sev <= 3 ? 'bad' : 'warn';
            return html`<div class="inc ${s}">
              <div class="t"><bx-icon name=${STATUS_ICON[s]}></bx-icon>SEV-${x.sev} · ${x.title}</div>
              <div class="m">${x.window} · ${x.impact} ${avatar(x.owner, 'sm')}</div></div>`;
          })}
          ${r.notes.length ? html`<ul class="notes">${r.notes.map((n) => html`<li>${n}</li>`)}</ul>`
            : r.incidents.length ? nothing : html`<div class="muted">A quiet night: nothing beyond the numbers.</div>`}
        </div>
      </div>

      <div class="panel">
        <span class="label">Customers</span>
        <table>
          <thead><tr><th>Customer</th><th class="r">Routes</th><th class="r">Stops</th><th class="r">On time</th><th class="r">Exceptions</th></tr></thead>
          <tbody>${[...r.customers].sort((a, b) => b.stops - a.stops).map((c) => {
            const s = c.onTime >= 96 ? 'ok' : c.onTime >= 94 ? 'warn' : 'bad';
            return html`<tr><td>${c.name}</td><td class="r">${num(c.routes)}</td><td class="r">${num(c.stops)}</td>
              <td class="r"><span class="bar ${s}"><span class="b"><i style="width:${Math.max(4, (c.onTime - 85) / 15 * 100)}%"></i></span><span>${pct(c.onTime)}</span></span></td>
              <td class="r">${num(c.exceptions)}</td></tr>`;
          })}</tbody>
        </table>
      </div>
    </div>`;
  }

  // the trend: a bar per night (stops) with the on-time line over it, and a
  // flag on the nights that needed a look
  _trend() {
    const L = [...(this._list || [])].reverse();
    if (L.length < 2) return nothing;
    const W = 420, H = 92, pad = 4, bw = (W - pad * 2) / L.length;
    const maxS = Math.max(...L.map((x) => x.stops));
    const lo = Math.min(...L.map((x) => x.onTime)) - 0.5, hi = Math.max(...L.map((x) => x.onTime)) + 0.5;
    const y = (v) => H - 8 - ((v - lo) / (hi - lo)) * (H - 22);
    const pts = L.map((x, i) => `${(pad + bw * i + bw / 2).toFixed(1)},${y(x.onTime).toFixed(1)}`).join(' ');
    return html`<svg viewBox="0 0 ${W} ${H}" preserveAspectRatio="none">
      ${L.map((x, i) => svg`<rect class=${x.date === this._sel ? 'sel' : ''} x=${(pad + bw * i + 2).toFixed(1)} width=${(bw - 4).toFixed(1)} y=${(H - 4 - (x.stops / maxS) * (H - 24)).toFixed(1)}
          height=${((x.stops / maxS) * (H - 24)).toFixed(1)}><title>${x.date}: ${x.stops} stops, ${x.onTime}% on time</title></rect>`)}
      <polyline points=${pts} stroke-width="2" stroke-linejoin="miter" vector-effect="non-scaling-stroke"></polyline>
      ${L.map((x, i) => (x.status !== 'ok' ? svg`<rect class="flag" x=${(pad + bw * i + bw / 2 - 4).toFixed(1)} y=${(y(x.onTime) - 4).toFixed(1)} width="8" height="8"></rect>` : nothing))}
    </svg>`;
  }
}

customElements.define('ops-report', OpsReport);
