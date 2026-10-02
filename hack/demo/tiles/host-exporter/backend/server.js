// apps/host-exporter — the machine this workspace runs on, in Prometheus
// text format at GET /metrics: load, memory, CPU time, network and disk,
// read from /proc and statfs at every scrape (nothing cached, nothing
// invented). Bound into the metrics dashboard's `sources` interface.
'use strict';
const fs = require('fs');
const os = require('os');
const { json, serve } = require('./tile');

const started = Date.now();
let scrapes = 0;

const read = (p) => { try { return fs.readFileSync(p, 'utf8'); } catch { return ''; } };

function metrics() {
  const out = [];
  const fam = (name, type, help, samples) => {
    out.push(`# HELP ${name} ${help}`, `# TYPE ${name} ${type}`);
    for (const [labels, v] of samples) out.push(`${name}${labels ? `{${labels}}` : ''} ${Number.isFinite(v) ? v : 0}`);
  };
  const [l1, l5, l15] = os.loadavg();
  fam('node_load1', 'gauge', '1-minute load average', [['', l1]]);
  fam('node_load5', 'gauge', '5-minute load average', [['', l5]]);
  fam('node_load15', 'gauge', '15-minute load average', [['', l15]]);

  const mem = {};
  for (const line of read('/proc/meminfo').split('\n')) {
    const m = line.match(/^(\w+):\s+(\d+)\s*kB/);
    if (m) mem[m[1]] = Number(m[2]) * 1024;
  }
  fam('node_memory_total_bytes', 'gauge', 'Physical memory', [['', mem.MemTotal ?? os.totalmem()]]);
  fam('node_memory_available_bytes', 'gauge', 'Memory available for new work', [['', mem.MemAvailable ?? os.freemem()]]);

  const cpu = read('/proc/stat').split('\n').find((l) => l.startsWith('cpu '));
  if (cpu) {
    const hz = 100;
    const [user, nice, system, idle, iowait] = cpu.trim().split(/\s+/).slice(1).map(Number);
    fam('node_cpu_seconds_total', 'counter', 'Seconds all CPUs spent in each mode', [
      ['mode="user"', (user + nice) / hz], ['mode="system"', system / hz], ['mode="iowait"', iowait / hz], ['mode="idle"', idle / hz]]);
  }
  fam('node_cpus', 'gauge', 'Logical CPUs', [['', os.cpus().length]]);

  let rx = 0, tx = 0;
  for (const line of read('/proc/net/dev').split('\n').slice(2)) {
    const [iface, rest] = line.split(':');
    if (!rest || iface.trim() === 'lo') continue;
    const f = rest.trim().split(/\s+/).map(Number);
    rx += f[0]; tx += f[8];
  }
  fam('node_network_receive_bytes_total', 'counter', 'Bytes received on every interface but loopback', [['', rx]]);
  fam('node_network_transmit_bytes_total', 'counter', 'Bytes sent on every interface but loopback', [['', tx]]);

  try {
    const s = fs.statfsSync('/');
    fam('node_filesystem_avail_bytes', 'gauge', 'Free space for the workspace', [['mountpoint="/"', s.bavail * s.bsize]]);
  } catch { /* no statfs */ }

  fam('node_uptime_seconds', 'gauge', 'Seconds since the host booted', [['', os.uptime()]]);
  fam('process_uptime_seconds', 'gauge', "Seconds since this exporter's backend started", [['', Math.round((Date.now() - started) / 1000)]]);
  scrapes++;
  return out.join('\n') + '\n';
}

serve([
  ['GET', '/metrics', (req, res) => { res.writeHead(200, { 'content-type': 'text/plain; version=0.0.4' }); res.end(metrics()); }],
  ['GET', '/status', (req, res) => json(res, { host: os.hostname(), cpus: os.cpus().length, scrapes, uptime: Math.round((Date.now() - started) / 1000) })],
]);
