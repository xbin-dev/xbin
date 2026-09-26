// charts — a node-metrics tile: a line chart over time (percent), a stacked
// memory area chart (bytes), a bar chart by category (numbers), and
// sparklines inside rows. The exporter comes from the tile's bound `metrics`
// interface; the tile turns its samples into series;
// time points are ms since the epoch, percent values are fractions (0.42 = 42%).
import { html, render, repeat, nothing } from '/vendor/xb-native.js';

const source = xbin.iface('metrics'); // the bound exporter: {url, provider}
let m = null;
let range = '1h';

const at = (i) => m.start + i * m.step * 1000;
const series = (name, values) => ({ name, points: values.map((v, i) => [at(i), v]) });
const gb = (b) => `${(b / 2 ** 30).toFixed(1)} GB`;

async function load() {
  const r = await xbin.fetch(`${source.url}/series?range=${range}`);
  m = await r.json();
  paint();
}

const paint = () => render(!m ? nothing : html`
  <screen title="Metrics" subtitle=${`${m.node} via ${source.provider} · last ${range}`} style="list" refreshable @refresh=${load}>
    <toolbar>
      <picker style="segmented" value=${range} @change=${(e) => { range = e.value; load(); }}
              options=${[{ value: '1h', label: '1h' }, { value: '6h', label: '6h' }, { value: '24h', label: '24h' }]}/>
    </toolbar>
    <section title="CPU" footer=${`peak ${Math.round(Math.max(...m.cpu.user) * 100)}% user`}>
      <chart kind="line" x="time" y="percent" height="m" series=${[series('user', m.cpu.user), series('system', m.cpu.system)]}/>
    </section>
    <section title="Memory" footer=${`${gb(m.mem.used.at(-1))} used of ${gb(m.mem.total)}`}>
      <chart kind="area" x="time" y="bytes" height="l" series=${[series('used', m.mem.used), series('cache', m.mem.cache)]}/>
    </section>
    <section title="Requests by status">
      <chart kind="bar" x="category" y="number" height="s"
             series=${[{ name: 'requests', points: Object.entries(m.status) }]}/>
    </section>
    <section title="Disk latency (ms) by queue depth">
      <chart kind="line" x="number" y="number" height="xl"
             series=${m.latency.map((d) => ({ name: d.disk, points: d.points }))}/>
    </section>
    <section title="Hosts">
      ${repeat(m.hosts, (h) => h.name, (h) => html`
        <row title=${h.name} detail=${`${h.load.at(-1).toFixed(2)}`} mono="detail" icon="server">
          <chart kind="spark" x="number" y="number" height="xs" series=${[{ name: 'load', points: h.load.map((v, i) => [i, v]) }]}/>
        </row>`)}
    </section>
  </screen>`);

load();
