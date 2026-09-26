#!/usr/bin/env node
// hack/xbn/node.mjs — run a tile's native.js in node and get the tree it
// renders (xb-native's JSON target). Each run is a fresh worker thread: its
// own module graph, globals and virtual clock.
//
//   import { runNative } from './hack/xbn/node.mjs';
//   const r = await runNative({ entry: 'examples/counter-go/native.js', data, steps });
//   r.tree        // {v: 1, root} — the tree the app would show after the steps
//   r.messages    // every runtime → app message, in order (mount, patch, diag, error, call, meta, state)
//   r.diagnostics // the {op:"diag"} ones; r.errors the {op:"error"} ones
//   r.requests    // [{method, url, body, at}] the tile's xbin.fetch calls
//   r.unmatched   // requests no route answered (they got a 404)
//   r.snapshots   // {name: tree} taken by {snapshot} steps; r.extra: data.setup's result()
//
// CLI: node hack/xbn/node.mjs <native.js> [data.json] [steps.json] → the tree JSON on stdout.
//
// data (all optional):
//   self     xbin.self (default "apps/tile")
//   now      the pinned clock, ms since the epoch — Date, setTimeout/Interval
//            and requestAnimationFrame run on it; it moves only on {wait}
//   tz       the time zone for the run (IANA name, e.g. "Europe/Warsaw")
//   locale   the default locale for the run (BCP 47, e.g. "de-DE")
//            ICU reads both once per process, so a run whose tz/locale differ
//            from this process's runs in a child node process instead
//   iface    {slot: value} returned by xbin.iface(slot)
//   routes   {"METHOD /path?query" | "METHOD /path" | "/path": response | [response, …]}
//            response: {status?, json? | text? | sse?: [{event?, id?, data, after?}], open?, headers?, delay? (virtual ms), error? (fetch rejects)}
//            sse frames with `after` (virtual ms after the previous frame) stream as the clock
//            moves; `open: true` keeps the stream open after the last frame
//            an array answers successive calls in turn (the last one repeats)
//   calls    {copy|share|open: value} how xbin.native calls resolve (default null)
//   dialog   what xbin.dialog() resolves to
//   setup    a module path imported before the tile: its default export gets
//            {data, touch} and may replace parts of xbin (a test's own fake
//            backend); `call` steps call its other exports; its result() comes
//            back as r.extra
// steps (run in order after the first render settles):
//   {wait: ms} · {tap: key} · {input: [key, value]} · {event: [key, type, payload, n?]}
//   {event: {k | select, type, payload?, n?}} — checked: the node must exist and take the
//            event; `select` is a CSS-like selector (hack/xbn/select.mjs) matching one node
//   {bus: [topic, data]} · {visibility: "hidden"|"visible"} · {resolve: [id, value]}
//   {snapshot: name} (r.snapshots[name] = the tree now) · {call: [export, …args]} (data.setup's)
//   A key in tap/input/event may be a matcher instead: {t?, p?: {prop: value},
//   has?: substring of the props' JSON, in?: an ancestor's matcher, nth?} — the
//   first (nth) node in tree order that matches.
// caps / state: what the app would inject (default: the full vocabulary, null).
import { Worker } from 'node:worker_threads';
import { spawn } from 'node:child_process';
import { readFileSync } from 'node:fs';
import { resolve as resolvePath } from 'node:path';
import { fileURLToPath } from 'node:url';

const here = () => Intl.DateTimeFormat().resolvedOptions();
// posixLocale('de-DE') → 'de_DE.UTF-8' (what ICU reads from LC_ALL)
const posixLocale = (l) => `${String(l).replace(/-/g, '_')}.UTF-8`;
const sameZone = (tz) => !tz || process.env.TZ === tz || here().timeZone === tz;
const sameLocale = (l) => !l || here().locale === l;

export function runNative({ entry, data = {}, steps = [], caps, state = null, timeout = 30000, quiet = true } = {}) {
  if (!entry) return Promise.reject(new Error('runNative: entry (a native.js path) is required'));
  const file = String(entry).startsWith('file:') ? fileURLToPath(entry) : resolvePath(String(entry));
  if (!sameZone(data.tz) || !sameLocale(data.locale)) return runInChild({ entry: file, data, steps, caps, state, timeout, quiet });
  return new Promise((res, rej) => {
    const w = new Worker(new URL('./worker.mjs', import.meta.url), {
      workerData: { entry: file, data, steps, caps, state },
      env: data.tz ? { ...process.env, TZ: data.tz } : process.env,
      stdout: quiet, stderr: quiet, // quiet: the tile's console stays in the worker (diagnostics come back as messages)
    });
    if (quiet) { w.stdout.resume(); w.stderr.resume(); }
    let done = false;
    const timer = setTimeout(() => { if (!done) { done = true; w.terminate(); rej(new Error(`runNative: ${file} did not finish in ${timeout} ms`)); } }, timeout);
    w.once('message', (r) => { done = true; clearTimeout(timer); w.terminate(); r.fatal ? rej(Object.assign(new Error(r.fatal), { result: r })) : res(r); });
    w.once('error', (e) => { if (!done) { done = true; clearTimeout(timer); rej(e); } });
    w.once('exit', (code) => { if (!done) { done = true; clearTimeout(timer); rej(new Error(`runNative: worker exited (${code}) without a result`)); } });
  });
}

// A worker thread shares its process's ICU time zone and locale; a run that
// pins others gets a node process of its own (this file with --child, the
// options on stdin, the result as JSON on stdout).
function runInChild(opts) {
  return new Promise((res, rej) => {
    const env = { ...process.env };
    if (opts.data.tz) env.TZ = opts.data.tz;
    if (opts.data.locale) { env.LC_ALL = posixLocale(opts.data.locale); env.LANG = env.LC_ALL; }
    const p = spawn(process.execPath, [fileURLToPath(import.meta.url), '--child'], { env, stdio: ['pipe', 'pipe', opts.quiet ? 'ignore' : 'inherit'] });
    let out = '';
    p.stdout.setEncoding('utf8');
    p.stdout.on('data', (d) => { out += d; });
    const timer = setTimeout(() => p.kill('SIGKILL'), opts.timeout + 2000);
    p.once('error', (e) => { clearTimeout(timer); rej(e); });
    p.once('close', (code) => {
      clearTimeout(timer);
      let r;
      try { r = JSON.parse(out); } catch { rej(new Error(`runNative: the child run for ${opts.entry} exited (${code}) without a result`)); return; }
      if (r.error) rej(Object.assign(new Error(r.error), r.result ? { result: r.result } : {}));
      else res(r.result);
    });
    p.stdin.end(JSON.stringify(opts));
  });
}

if (process.argv[1] && resolvePath(process.argv[1]) === fileURLToPath(import.meta.url) && process.argv[2] === '--child') {
  let input = '';
  process.stdin.setEncoding('utf8');
  for await (const d of process.stdin) input += d;
  const opts = JSON.parse(input);
  let reply;
  try {
    const run = { ...opts, data: { ...opts.data } };
    if (!sameZone(run.data.tz)) throw new Error(`runNative: time zone ${JSON.stringify(run.data.tz)} could not be pinned (ICU has ${here().timeZone})`);
    if (!sameLocale(run.data.locale)) throw new Error(`runNative: locale ${JSON.stringify(run.data.locale)} could not be pinned (ICU has ${here().locale})`);
    delete run.data.tz; // this process has them now: the run takes a worker here
    delete run.data.locale;
    reply = { result: await runNative(run) };
  } catch (e) { reply = { error: String(e?.message ?? e), result: e?.result }; }
  process.stdout.write(JSON.stringify(reply));
} else if (process.argv[1] && resolvePath(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const [entry, dataFile, stepsFile] = process.argv.slice(2);
  if (!entry) { console.error('usage: node hack/xbn/node.mjs <native.js> [data.json] [steps.json]'); process.exit(2); }
  const read = (f) => (f ? JSON.parse(readFileSync(f, 'utf8')) : undefined);
  try {
    const r = await runNative({ entry, data: read(dataFile) ?? {}, steps: read(stepsFile) ?? [] });
    for (const m of r.messages) if (m.op === 'diag' || m.op === 'error') console.error(`${m.op === 'error' ? `error ${m.kind}` : m.level}: ${m.message}${m.where ? ` (${m.where})` : ''}`);
    process.stdout.write(`${JSON.stringify(r.tree, null, 1)}\n`);
    process.exit(r.errors.length ? 1 : 0);
  } catch (e) { console.error(String(e?.stack ?? e)); process.exit(1); }
}
