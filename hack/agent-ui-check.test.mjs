// The agent template's browser-script runner (hack/agent-ui-check.mjs, `make
// agent-ui-check`) against a fake template and a fake Playwright — no
// browser: what counts as a pass (exit 0, no SKIP, the verdict line), that a
// script's import('playwright') is served from PLAYWRIGHT_DIR, that a script
// past the timeout is stopped, and what it says without Playwright.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { mkdirSync, mkdtempSync, readdirSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { NOT_SCRIPTS, scriptsIn, verdict } from './agent-ui-check.mjs';

const here = dirname(fileURLToPath(import.meta.url));
const RUNNER = join(here, 'agent-ui-check.mjs');

// fake(scripts): a template directory with test/<name>.mjs, and a Playwright
// whose chromium "launches".
function fake(scripts) {
  const root = mkdtempSync(join(tmpdir(), 'agent-ui-check-'));
  mkdirSync(join(root, 'tpl', 'test'), { recursive: true });
  for (const [name, body] of Object.entries(scripts)) writeFileSync(join(root, 'tpl', 'test', `${name}.mjs`), body);
  mkdirSync(join(root, 'pw', 'node_modules', 'playwright'), { recursive: true });
  writeFileSync(join(root, 'pw', 'node_modules', 'playwright', 'index.mjs'),
    'export const chromium = { launch: async () => ({ fake: true, close: async () => {} }) };\n');
  return root;
}
const runner = (root, env = {}, args = []) => {
  const r = spawnSync(process.execPath, [RUNNER, ...args], { encoding: 'utf8',
    env: { ...process.env, AGENT_UI_DIR: join(root, 'tpl'), PLAYWRIGHT_DIR: join(root, 'pw'), ...env } });
  return { code: r.status, out: r.stdout + r.stderr };
};

test('verdict: only exit 0 with the verdict line and no SKIP passes', () => {
  assert.equal(verdict({ code: 0, out: 'all attach checks passed\n' }, 9), '');
  assert.equal(verdict({ code: 0, out: 'all layout checks passed (6 heights × 7 states × 3 widths)\n' }, 9), '');
  assert.match(verdict({ code: 1, out: 'FAIL  x\n\n1 FAILURE(S)\n' }, 9), /^exit 1$/);
  assert.match(verdict({ code: 0, out: 'SKIP: playwright not installed\n' }, 9), /skipped.*playwright not installed/);
  assert.match(verdict({ code: 0, out: '' }, 9), /without its "all … checks passed" line/);
  assert.match(verdict({ code: null, signal: 'SIGTERM', timedOut: true, out: 'all x checks passed\n' }, 9), /still running after 9 s/);
  assert.match(verdict({ code: null, signal: 'SIGSEGV', out: '' }, 9), /killed by SIGSEGV/);
});

test('every file of the template\'s test/ is a script or a named helper', () => {
  const dir = join(here, '..', 'builtin-templates', 'agent');
  const files = readdirSync(join(dir, 'test')).filter((f) => f.endsWith('.mjs')).map((f) => f.slice(0, -4));
  for (const n of Object.keys(NOT_SCRIPTS)) assert.ok(files.includes(n), `NOT_SCRIPTS names ${n}, which test/ no longer has`);
  const scripts = scriptsIn(dir);
  assert.ok(scripts.length > 0 && scripts.length === files.length - Object.keys(NOT_SCRIPTS).length, `${scripts.length} scripts of ${files.length} files`);
});

test('the runner: passes, failures, skips and silent exits, with Playwright from PLAYWRIGHT_DIR', () => {
  const root = fake({
    good: 'const { chromium } = await import("playwright"); const b = await chromium.launch();\nconsole.log(b.fake ? "all good checks passed" : "not the fake"); await b.close();\n',
    bad: 'console.log("FAIL  the thing  ← 1"); console.log("\\n1 FAILURE(S)"); process.exit(1);\n',
    skips: 'console.log("SKIP: playwright not installed"); process.exit(0);\n',
    quiet: 'process.exit(0);\n',
    kit: 'throw new Error("a helper is never run");\n',
  });
  try {
    const all = runner(root, { AGENT_UI_TIMEOUT: '120' });
    assert.equal(all.code, 1, all.out);
    assert.match(all.out, /^ok {4}good /m);
    assert.match(all.out, /^FAIL {2}bad .* exit 1$/m);
    assert.match(all.out, /^ {6}FAIL {2}the thing/m); // a failure's own output is shown
    assert.match(all.out, /^FAIL {2}skips .*it skipped/m);
    assert.match(all.out, /^FAIL {2}quiet .*without its "all … checks passed" line/m);
    assert.doesNotMatch(all.out, /kit/);
    assert.match(all.out, /agent-ui-check: 3 of 4 scripts FAILED \(.*\): bad quiet skips$/m);

    const one = runner(root, { AGENT_UI_TIMEOUT: '120' }, ['good']);
    assert.equal(one.code, 0, one.out);
    assert.match(one.out, /agent-ui-check: 1 scripts passed/);
    const none = runner(root, {}, ['nope']);
    assert.equal(none.code, 2, none.out);
    assert.match(none.out, /no script nope/);
  } finally { rmSync(root, { recursive: true, force: true }); }
});

test('the runner: a script that never ends is stopped and fails', () => {
  const root = fake({ hangs: 'console.log("all hangs checks passed"); setInterval(() => {}, 1000);\n' });
  try {
    const r = runner(root, { AGENT_UI_TIMEOUT: '1' });
    assert.equal(r.code, 1, r.out);
    assert.match(r.out, /^FAIL {2}hangs .*still running after 1 s: stopped/m);
  } finally { rmSync(root, { recursive: true, force: true }); }
});

test('the runner: without Playwright it says what to do, and runs nothing', () => {
  const root = fake({ good: 'console.log("all good checks passed");\n' });
  try {
    const unset = runner(root, { PLAYWRIGHT_DIR: '' });
    assert.equal(unset.code, 2, unset.out);
    assert.match(unset.out, /no Playwright: PLAYWRIGHT_DIR isn't set .*hack\/dev-setup\.sh/);
    const empty = runner(root, { PLAYWRIGHT_DIR: join(root, 'tpl') });
    assert.equal(empty.code, 2, empty.out);
    assert.match(empty.out, /no Playwright in PLAYWRIGHT_DIR=.*no node_modules\/playwright there/);
    assert.doesNotMatch(unset.out + empty.out, /good/);
    // an install whose browser doesn't start
    writeFileSync(join(root, 'pw', 'node_modules', 'playwright', 'index.mjs'),
      'export const chromium = { launch: async () => { throw new Error("Executable doesn\'t exist at /x/chrome"); } };\n');
    const broken = runner(root);
    assert.equal(broken.code, 2, broken.out);
    assert.match(broken.out, /doesn't launch Chromium[\s\S]*Executable doesn't exist/);
  } finally { rmSync(root, { recursive: true, force: true }); }
});
