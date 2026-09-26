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
//   tz       TZ for the run
//   iface    {slot: value} returned by xbin.iface(slot)
//   routes   {"METHOD /path?query" | "METHOD /path" | "/path": response | [response, …]}
//            response: {status?, json? | text? | sse?: [{event?, id?, data}], headers?, delay? (virtual ms), error? (fetch rejects)}
//            an array answers successive calls in turn (the last one repeats)
//   calls    {copy|share|open: value} how xbin.native calls resolve (default null)
//   dialog   what xbin.dialog() resolves to
//   setup    a module path imported before the tile: its default export gets
//            {data, touch} and may replace parts of xbin (a test's own fake
//            backend); `call` steps call its other exports; its result() comes
//            back as r.extra
// steps (run in order after the first render settles):
//   {wait: ms} · {tap: key} · {input: [key, value]} · {event: [key, type, payload, n?]}
//   {bus: [topic, data]} · {visibility: "hidden"|"visible"} · {resolve: [id, value]}
//   {snapshot: name} (r.snapshots[name] = the tree now) · {call: [export, …args]} (data.setup's)
//   A key in tap/input/event may be a matcher instead: {t?, p?: {prop: value},
//   has?: substring of the props' JSON, in?: an ancestor's matcher, nth?} — the
//   first (nth) node in tree order that matches.
// caps / state: what the app would inject (default: the full vocabulary, null).
import { Worker } from 'node:worker_threads';
import { readFileSync } from 'node:fs';
import { resolve as resolvePath } from 'node:path';
import { fileURLToPath } from 'node:url';

export function runNative({ entry, data = {}, steps = [], caps, state = null, timeout = 30000, quiet = true } = {}) {
  if (!entry) return Promise.reject(new Error('runNative: entry (a native.js path) is required'));
  const file = String(entry).startsWith('file:') ? fileURLToPath(entry) : resolvePath(String(entry));
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

if (process.argv[1] && resolvePath(process.argv[1]) === fileURLToPath(import.meta.url)) {
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
