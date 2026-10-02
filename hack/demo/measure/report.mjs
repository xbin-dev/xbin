#!/usr/bin/env node
// hack/demo/measure/report.mjs — Markdown tables from a measurement run's
// summaries (<MEASURE_OUT>/*.summary.json, written by run.sh): one table per
// measurement, every series with n, min, median, p90 and max. Values are
// rounded UP to 0.1 ms, never in our favour.
//
//   node hack/demo/measure/report.mjs [dir]     (default: $MEASURE_OUT)
import { readFileSync, readdirSync } from 'node:fs';
import { join } from 'node:path';

const dir = process.argv[2] || process.env.MEASURE_OUT;
if (!dir) {
  console.error('usage: report.mjs <dir with *.summary.json>');
  process.exit(2);
}
const up = (v) => (Math.ceil(v * 10 - 1e-9) / 10).toFixed(1);
for (const f of readdirSync(dir).filter((n) => n.endsWith('.summary.json')).sort()) {
  const s = JSON.parse(readFileSync(join(dir, f), 'utf8'));
  console.log(`### ${s.name || f.replace(/\.summary\.json$/, "")} (${f})\n`);
  if (Array.isArray(s.series)) {
    console.log('| series | n | min | median | p90 | max |');
    console.log('|---|---:|---:|---:|---:|---:|');
    for (const r of s.series) console.log(`| ${r.series} | ${r.n} | ${up(r.minMs)} | ${up(r.medianMs)} | ${up(r.p90Ms)} | ${up(r.maxMs)} |`);
    console.log();
  }
  const rest = Object.fromEntries(Object.entries(s).filter(([k]) => !['name', 'series', 'slowest', 'failures', 'swaps'].includes(k)));
  console.log('```json\n' + JSON.stringify(rest, null, 1) + '\n```\n');
}
