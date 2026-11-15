#!/usr/bin/env node
// hack/agent-ui-check.mjs — `make agent-ui-check`: the agent template's
// browser scripts (builtin-templates/agent/test/*.mjs), each the real page
// in Chromium against the stubbed backend of test/backend.mjs. They were
// run by hand, one at a time, and where Playwright isn't beside them they
// print SKIP and exit 0 — so four of them rotted unseen (a raised upload
// cap, three modules the terminal gained, a canvas sandbox, a deferred
// teardown). This runs every one, several at once, each in its own node and
// browser, and a script passes only when it
//
//   - exits 0,
//   - prints no SKIP line (a skip here is a check that didn't run), and
//   - ends by saying `all … checks passed` — every script's last word, so
//     one that left early, or a helper run by mistake, isn't a pass;
//
// a script still running after the timeout is stopped and fails.
//
//   make agent-ui-check                      # all of them
//   make agent-ui-check AGENT_UI="attach ci" # these
//   AGENT_UI_JOBS=4 AGENT_UI_TIMEOUT=300 …   # at a time (8, at most the cores); seconds each (180)
//
// Playwright comes from PLAYWRIGHT_DIR (hack/dev-setup.sh installs it and
// writes .dev.mk), handed to the scripts' own import('playwright') by
// hack/playwright-dir.mjs. Not in `make check`: it needs a browser.
import { spawn } from 'node:child_process';
import { existsSync, readdirSync } from 'node:fs';
import * as nodeModule from 'node:module';
import { availableParallelism } from 'node:os';
import { dirname, join } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
const HOOK = pathToFileURL(join(here, 'playwright-dir.mjs')).href;

// What in test/ is not a script to run, and why. Anything else is run: a new
// script needs no line here, and a new helper that isn't named fails the
// check (it ends without `all … checks passed`) until it is.
export const NOT_SCRIPTS = {
  backend: 'the stubbed backend and the page server the scripts import',
  kit: 'serves the frontend kit to the page',
  'fake-tty': 'a sandbox manager\'s terminal, faked in the page',
  'harness-fixtures': 'seeds for the coding-agent scripts',
  'ci-stub': 'the CI routes of the stubbed backend',
  'projects-stub': 'the Projects routes of the stubbed backend',
  'projects-more-stub': 'more of them',
  'native-stub': 'the stubbed backend for the native view in node (hack/agent-template-native*.test.mjs)',
  'native-projects-stub': 'the same, with Projects',
  'native-ext-probe': 'a feature module for hack/agent-template-harness.test.mjs',
  'native-shots': 'draws screenshots to look at; it asserts nothing',
  'ws-stub': 'the workspace agent\'s page served against a stubbed backend in workspace mode, and its fixtures',
  'ws-native-stub': 'the same stubbed backend for the workspace agent\'s native view in node (hack/agent-template-native-ws.test.mjs)',
};

// scriptsIn(dir): the scripts of <dir>/test, by name.
export const scriptsIn = (dir, not = NOT_SCRIPTS) => readdirSync(join(dir, 'test'))
  .filter((f) => f.endsWith('.mjs')).map((f) => f.slice(0, -'.mjs'.length))
  .filter((n) => !Object.hasOwn(not, n)).sort();

// verdict(run): '' when the script passed, else why it didn't.
export function verdict({ code, signal, timedOut, out }, timeoutSec) {
  if (timedOut) return `still running after ${timeoutSec} s: stopped`;
  const skip = /^SKIP\b.*$/m.exec(out);
  if (skip) return `it skipped — "${skip[0].trim()}" — and a skip is a check that didn't run`;
  if (code !== 0) return code == null ? `killed by ${signal}` : `exit ${code}`;
  if (!/^all .+ checks passed/m.test(out)) return 'exit 0 without its "all … checks passed" line (a helper? name it in NOT_SCRIPTS, hack/agent-ui-check.mjs)';
  return '';
}

const live = new Set(); // the scripts running now (each its own process group)
const stop = (child, sig) => { try { process.kill(-child.pid, sig); } catch { /* gone already */ } };

// run(dir, name): one script in its own node, from the template's directory.
function run(dir, name, timeoutSec) {
  return new Promise((done) => {
    const env = { ...process.env };
    // where an instance points the scripts at its xbind's files: in this checkout they are beside the template
    for (const k of ['BX_KIT', 'BX_ICONS', 'BX_CHAT', 'BX_WEB', 'BX_VENDOR']) delete env[k];
    const t0 = Date.now();
    const child = spawn(process.execPath, ['--import', HOOK, join('test', `${name}.mjs`)], { cwd: dir, env, stdio: ['ignore', 'pipe', 'pipe'], detached: true });
    live.add(child);
    let out = '';
    let timedOut = false;
    let hard = null;
    child.stdout.on('data', (d) => { out += d; });
    child.stderr.on('data', (d) => { out += d; });
    // SIGTERM first: Playwright closes its browser on it; then the group, hard
    const timer = setTimeout(() => { timedOut = true; stop(child, 'SIGTERM'); hard = setTimeout(() => stop(child, 'SIGKILL'), 5000); }, timeoutSec * 1000);
    child.on('error', (e) => { out += `\n${e.message}`; });
    child.on('close', (code, signal) => {
      clearTimeout(timer);
      clearTimeout(hard);
      live.delete(child);
      done({ name, out, sec: (Date.now() - t0) / 1000, why: verdict({ code, signal, timedOut, out }, timeoutSec) });
    });
  });
}

// preflight(): '' when a script would get a browser, else what is missing.
function preflight() {
  if (typeof nodeModule.registerHooks !== 'function') return `node ${process.version} has no module.registerHooks (needs node ≥ 22.15)`;
  const dir = process.env.PLAYWRIGHT_DIR;
  const how = 'hack/dev-setup.sh installs it and writes PLAYWRIGHT_DIR into .dev.mk';
  if (!dir) return `no Playwright: PLAYWRIGHT_DIR isn't set (a directory with node_modules/playwright; ${how})`;
  if (!existsSync(join(dir, 'node_modules', 'playwright', 'index.mjs'))) return `no Playwright in PLAYWRIGHT_DIR=${dir} (no node_modules/playwright there; ${how})`;
  return new Promise((done) => {
    const probe = 'const { chromium } = await import("playwright"); const b = await chromium.launch(); await b.close();';
    const child = spawn(process.execPath, ['--import', HOOK, '--input-type=module', '-e', probe], { stdio: ['ignore', 'ignore', 'pipe'] });
    let err = '';
    child.stderr.on('data', (d) => { err += d; });
    child.on('error', (e) => done(e.message));
    child.on('close', (code) => done(code === 0 ? '' : `Playwright in ${dir} doesn't launch Chromium (its browser: ./node_modules/.bin/playwright install chromium there; ${how}):\n${err.trim().split('\n').slice(0, 6).join('\n')}`));
  });
}

async function main() {
  const dir = process.env.AGENT_UI_DIR || join(here, '..', 'builtin-templates', 'agent');
  const say = (s) => process.stdout.write(`${s}\n`);
  const missing = await preflight();
  if (missing) { say(`agent-ui-check: ${missing}`); return 2; }
  const all = scriptsIn(dir);
  const asked = process.argv.slice(2).map((a) => a.replace(/^test\//, '').replace(/\.mjs$/, ''));
  const unknown = asked.filter((a) => !all.includes(a));
  if (unknown.length) { say(`agent-ui-check: no script ${unknown.join(', ')} in ${join(dir, 'test')} (${all.join(' ')})`); return 2; }
  const names = asked.length ? asked : all;
  if (!names.length) { say(`agent-ui-check: no scripts in ${join(dir, 'test')}`); return 2; }
  const num = (v, dflt) => (Number.isFinite(Number(v)) && Number(v) > 0 ? Number(v) : dflt);
  const jobs = Math.min(Math.floor(num(process.env.AGENT_UI_JOBS, Math.min(8, availableParallelism()))) || 1, names.length);
  const timeoutSec = num(process.env.AGENT_UI_TIMEOUT, 180);

  const t0 = Date.now();
  const failed = [];
  let next = 0;
  await Promise.all(Array.from({ length: jobs }, async () => {
    while (next < names.length) {
      const r = await run(dir, names[next++], timeoutSec);
      if (!r.why) { say(`ok    ${r.name}  ${r.sec.toFixed(1)}s`); continue; }
      failed.push(r.name);
      const tail = r.out.trimEnd().split('\n').slice(-40).map((l) => `      ${l}`).join('\n');
      say(`FAIL  ${r.name}  ${r.sec.toFixed(1)}s  ${r.why}${tail.trim() ? `\n${tail}` : ''}`);
    }
  }));
  const took = `${((Date.now() - t0) / 1000).toFixed(0)}s, ${jobs} at a time`;
  if (failed.length) { say(`agent-ui-check: ${failed.length} of ${names.length} scripts FAILED (${took}): ${failed.sort().join(' ')}`); return 1; }
  say(`agent-ui-check: ${names.length} scripts passed (${took})`);
  return 0;
}

if (process.argv[1] && fileURLToPath(import.meta.url) === process.argv[1]) {
  for (const sig of ['SIGINT', 'SIGTERM', 'SIGHUP']) {
    process.on(sig, () => { for (const c of live) stop(c, 'SIGTERM'); process.exit(130); });
  }
  process.exit(await main());
}
