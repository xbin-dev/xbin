// report.js — the nightly ops report page: one night's report (newest by
// default), a 14-night trend, the customers' table, notes and incidents.
import { LitElement, html, css, svg, nothing } from 'lit';
import { scrollCss } from '/vendor/scroll-css.js';
import { getJSON, api, num, dateShort, timeShort, initials, hue, baseCss } from './ui.js';

const longDate = (k) => new Date(k + 'T12:00:00').toLocaleDateString('en-US', { weekday: 'long', month: 'long', day: 'numeric' });
const pct = (v, n = 1) => (v == null ? '—' : `${Number(v).toFixed(n)}%`);
const delta = (a, b, n = 1, unit = '', invert = false) => {
  if (a == null || b == null) return nothing;
  const d = a - b;
  if (Math.abs(d) < 10 ** -n / 2) return html`<span class="d flat">±0${unit}</span>`;
  const good = invert ? d < 0 : d > 0;
  return html`<span class="d ${good ? 'up' : 'down'}">${d > 0 ? '▲' : '▼'} ${Math.abs(d).toFixed(n)}${unit}</span>`;
};

class OpsReport extends LitElement {
  static properties = { _list: { state: true }, _runs: { state: true }, _rep: { state: true }, _prev: { state: true }, _sel: { state: true }, _me: { state: true }, _busy: { state: true }, _err: { state: true } };

  static styles = [scrollCss, baseCss, css`
    :host { overflow: auto; container-type: inline-size; }
    .wrap { padding: 14px 18px 22px; max-width: 1100px; }
    header { display: flex; align-items: center; gap: 12px; flex-wrap: wrap; margin-bottom: 12px; }
    h1 { margin: 0; font-size: 17px; font-weight: 650; letter-spacing: -.01em; }
    .sub { color: var(--bx-muted); font-size: 12px; margin-top: 2px; }
    .sp { flex: 1; }
    .nav { display: flex; align-items: center; gap: 4px; }
    .nav button { background: var(--bx-panel-2); border: 1px solid var(--bx-border); border-radius: 6px; width: 26px; height: 26px; }
    .nav button:disabled { opacity: .35; cursor: default; }
    .nav select { background: var(--bx-panel-2); border: 1px solid var(--bx-border); border-radius: 6px; color: inherit; font: inherit; font-size: 12px; padding: 4px 6px; }
    .status { display: flex; align-items: center; gap: 10px; padding: 9px 12px; border-radius: 9px; margin-bottom: 14px; font-size: 12.5px;
              background: color-mix(in srgb, var(--c) 9%, var(--bx-panel)); border: 1px solid color-mix(in srgb, var(--c) 35%, var(--bx-border)); }
    .status b { color: var(--c); }
    .grid { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 10px; margin-bottom: 14px; }
    .k { background: var(--bx-panel-2); border: 1px solid var(--bx-border); border-radius: 9px; padding: 9px 12px; }
    .k b { display: block; font-size: 19px; font-weight: 650; margin: 3px 0 1px; letter-spacing: -.01em; }
    .d { font-size: 11px; }
    .d.up { color: var(--ok); } .d.down { color: var(--bad); } .d.flat { color: var(--bx-muted); }
    .k small { color: var(--bx-muted); font-size: 11px; }
    .row2 { display: grid; grid-template-columns: 1.15fr 1fr; gap: 12px; margin-bottom: 14px; }
    .panel { background: var(--bx-panel-2); border: 1px solid var(--bx-border); border-radius: 9px; padding: 10px 12px; min-width: 0; }
    .panel .label { display: block; margin-bottom: 8px; }
    .trend svg { display: block; width: 100%; height: 92px; }
    .legend { display: flex; gap: 12px; font-size: 11px; color: var(--bx-muted); margin-top: 4px; }
    .legend i { display: inline-block; width: 9px; height: 9px; border-radius: 2px; margin-right: 4px; vertical-align: -1px; }
    ul.notes { margin: 0; padding: 0; list-style: none; }
    ul.notes li { padding: 5px 0 5px 16px; position: relative; font-size: 12.3px; line-height: 1.45; }
    ul.notes li::before { content: ''; position: absolute; left: 3px; top: 12px; width: 5px; height: 5px; border-radius: 50%; background: var(--bx-accent); }
    .inc { border-left: 3px solid var(--c); padding: 6px 10px; margin-bottom: 6px; background: color-mix(in srgb, var(--c) 7%, transparent); border-radius: 0 6px 6px 0; font-size: 12.3px; }
    .inc .t { font-weight: 600; }
    .inc .m { color: var(--bx-muted); font-size: 11.3px; margin-top: 2px; display: flex; align-items: center; gap: 6px; }
    table { width: 100%; border-collapse: collapse; font-size: 12.3px; }
    th { text-align: left; font-size: 10.5px; letter-spacing: .06em; text-transform: uppercase; color: var(--bx-muted); font-weight: 600; padding: 6px 8px; border-bottom: 1px solid var(--bx-border); }
    td { padding: 6px 8px; border-bottom: 1px solid color-mix(in srgb, var(--bx-border) 55%, transparent); }
    td.r, th.r { text-align: right; }
    .bar { display: flex; align-items: center; gap: 8px; justify-content: flex-end; }
    .bar span.b { display: inline-block; height: 6px; border-radius: 3px; background: var(--bx-border); width: 70px; position: relative; overflow: hidden; }
    .bar span.b i { position: absolute; inset: 0 auto 0 0; background: var(--c); border-radius: 3px; }
    .runs { font-size: 11.5px; color: var(--bx-muted); margin-top: 12px; }
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
    if (this._err) return html`<div class="err">${this._err}</div>`;
    const r = this._rep, p = this._prev;
    if (!r) return html`<div class="empty">Loading the latest report…</div>`;
    const i = this._list.findIndex((x) => x.date === this._sel);
    const run = this._runs?.find((x) => x.date === r.date);
    const attention = r.status !== 'ok';
    return html`<div class="wrap">
      <header>
        <div>
          <h1>Nightly ops report</h1>
          <div class="sub">${longDate(r.date)} · delivered ${dateShort(r.renderedAt)} at ${timeShort(r.renderedAt)} ${run?.by === 'schedule' || !run ? 'by the 05:30 schedule' : `by ${run.by}`}</div>
        </div>
        <span class="sp"></span>
        <div class="nav">
          <button title="Older" ?disabled=${i >= this._list.length - 1} @click=${() => this._step(-1)}>‹</button>
          <select @change=${(e) => this._load(e.target.value)}>
            ${this._list.map((x) => html`<option value=${x.date} ?selected=${x.date === this._sel}>${dateShort(new Date(x.date + 'T12:00:00'))}${x.status !== 'ok' ? ' ⚠' : ''}</option>`)}
          </select>
          <button title="Newer" ?disabled=${i <= 0} @click=${() => this._step(1)}>›</button>
        </div>
        ${this._me?.canRun ? html`<button class="btn" ?disabled=${this._busy} @click=${this._runNow}>${this._busy ? 'Running…' : 'Run now'}</button>` : nothing}
      </header>

      <div class="status ${attention ? 'warn' : 'ok'}" style="--c:${attention ? 'var(--warn)' : 'var(--ok)'}">
        <span class="dot ${attention ? 'warn' : 'ok'}"></span>
        <span><b>${attention ? 'Needs a look' : 'All clear'}</b> — ${attention
          ? `${r.incidents.length} incident${r.incidents.length > 1 ? 's' : ''}; on-time ${pct(r.totals.onTime)} across ${num(r.totals.stops)} stops.`
          : `every planning batch finished; ${pct(r.totals.onTime)} of ${num(r.totals.stops)} stops arrived on time.`}</span>
      </div>

      <div class="grid">
        <div class="k"><span class="label">Routes planned</span><b class="num">${num(r.totals.routes)}</b>${delta(r.totals.routes, p?.totals.routes, 0)}<small> vs night before</small></div>
        <div class="k"><span class="label">Stops</span><b class="num">${num(r.totals.stops)}</b><small>${num(r.totals.exceptions)} exceptions</small></div>
        <div class="k"><span class="label">On time</span><b class="num">${pct(r.totals.onTime)}</b>${delta(r.totals.onTime, p?.totals.onTime, 1, ' pt')}</div>
        <div class="k"><span class="label">Plan time p95</span><b class="num">${r.platform.planSecondsP95.toFixed(1)} s</b>${delta(r.platform.planSecondsP95, p?.platform.planSecondsP95, 1, ' s', true)}</div>
        <div class="k"><span class="label">API p95</span><b class="num">${num(r.platform.apiP95ms)} ms</b><small>${r.platform.apiRequestsM.toFixed(1)}M requests · ${r.platform.apiErrorPct.toFixed(2)}% errors</small></div>
        <div class="k"><span class="label">Uptime</span><b class="num">${pct(r.platform.uptimePct, r.platform.uptimePct === 100 ? 0 : 2)}</b><small>routing API and dispatch</small></div>
        <div class="k"><span class="label">Drivers on the road</span><b class="num">${num(r.platform.drivers)}</b><small>${pct(r.platform.crashFreePct, 2)} crash-free</small></div>
        <div class="k"><span class="label">Support</span><b class="num">${r.support.opened} in · ${r.support.closed} out</b><small>first reply in ${r.support.firstResponseMin} min</small></div>
      </div>

      <div class="row2">
        <div class="panel trend"><span class="label">Last ${this._list.length} nights</span>${this._trend()}
          <div class="legend"><span><i style="background:color-mix(in srgb, var(--blue) 70%, transparent)"></i>stops</span><span><i style="background:var(--bx-accent)"></i>on-time %</span></div></div>
        <div class="panel"><span class="label">${r.incidents.length ? 'Incidents and notes' : 'Notes'}</span>
          ${r.incidents.map((x) => html`<div class="inc" style="--c:${x.sev <= 3 ? 'var(--bad)' : 'var(--warn)'}">
            <div class="t">SEV-${x.sev} · ${x.title}</div>
            <div class="m">${x.window} · ${x.impact} <span class="av" style="--h:${hue(x.owner)};width:18px;height:18px;font-size:8.5px">${initials(x.owner)}</span></div></div>`)}
          ${r.notes.length ? html`<ul class="notes">${r.notes.map((n) => html`<li>${n}</li>`)}</ul>`
            : r.incidents.length ? nothing : html`<div class="muted" style="font-size:12px">A quiet night: nothing beyond the numbers.</div>`}
        </div>
      </div>

      <div class="panel">
        <span class="label">Customers</span>
        <table>
          <thead><tr><th>Customer</th><th class="r">Routes</th><th class="r">Stops</th><th class="r">On time</th><th class="r">Exceptions</th></tr></thead>
          <tbody>${[...r.customers].sort((a, b) => b.stops - a.stops).map((c) => {
            const col = c.onTime >= 96 ? 'var(--ok)' : c.onTime >= 94 ? 'var(--warn)' : 'var(--bad)';
            return html`<tr><td>${c.name}</td><td class="r num">${num(c.routes)}</td><td class="r num">${num(c.stops)}</td>
              <td class="r"><span class="bar"><span class="b"><i style="--c:${col};width:${Math.max(4, (c.onTime - 85) / 15 * 100)}%"></i></span><span class="num">${pct(c.onTime)}</span></span></td>
              <td class="r num">${num(c.exceptions)}</td></tr>`;
          })}</tbody>
        </table>
      </div>
    </div>`;
  }

  // the trend: a bar per night (stops) with the on-time line over it
  _trend() {
    const L = [...(this._list || [])].reverse();
    if (L.length < 2) return nothing;
    const W = 420, H = 92, pad = 4, bw = (W - pad * 2) / L.length;
    const maxS = Math.max(...L.map((x) => x.stops));
    const lo = Math.min(...L.map((x) => x.onTime)) - 0.5, hi = Math.max(...L.map((x) => x.onTime)) + 0.5;
    const y = (v) => H - 8 - ((v - lo) / (hi - lo)) * (H - 22);
    const pts = L.map((x, i) => `${(pad + bw * i + bw / 2).toFixed(1)},${y(x.onTime).toFixed(1)}`).join(' ');
    return html`<svg viewBox="0 0 ${W} ${H}" preserveAspectRatio="none">
      ${L.map((x, i) => svg`<rect x=${(pad + bw * i + 2).toFixed(1)} width=${(bw - 4).toFixed(1)} y=${(H - 4 - (x.stops / maxS) * (H - 24)).toFixed(1)}
          height=${((x.stops / maxS) * (H - 24)).toFixed(1)} rx="2" fill=${x.date === this._sel ? 'color-mix(in srgb, var(--blue) 95%, white)' : 'color-mix(in srgb, var(--blue) 45%, transparent)'}><title>${x.date}: ${x.stops} stops, ${x.onTime}% on time</title></rect>`)}
      <polyline points=${pts} fill="none" stroke="var(--bx-accent)" stroke-width="2" stroke-linejoin="round" stroke-linecap="round" vector-effect="non-scaling-stroke"></polyline>
      ${L.map((x, i) => (x.status !== 'ok' ? svg`<circle cx=${(pad + bw * i + bw / 2).toFixed(1)} cy=${y(x.onTime).toFixed(1)} r="3.2" fill="var(--bad)"></circle>` : nothing))}
    </svg>`;
  }
}

customElements.define('ops-report', OpsReport);
