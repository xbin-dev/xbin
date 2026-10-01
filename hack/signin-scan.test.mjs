// hack/signin-scan.test.mjs — the guided sign-in's reader (web/signin-scan.js,
// D178) on Claude Code's real output: the captures and expectations
// sdk/acp's Go twin is tested on (sdk/acp/testdata/signin). Run by
// `make js-test`.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { scanSignin, allowedURL, SigninReader, typedLine, cleanCode, screenText } from '../web/signin-scan.js';

const dir = new URL('../sdk/acp/testdata/signin/', import.meta.url);
const cases = JSON.parse(readFileSync(new URL('cases.json', dir), 'utf8'));
// claude's signin as GET /agent/providers serves it (internal/server's golden)
const spec = {
  command: 'claude auth login --claudeai', argv: ['claude', 'auth', 'login', '--claudeai'], tty: false, fallback: 'claude /exit',
  url: 'https://\\S+/oauth/authorize\\?\\S+', hosts: ['claude.com', 'claude.ai', 'anthropic.com'],
  code: 'Paste code here if prompted', invalid: 'Invalid code', done: 'Login successful', fail: 'Login failed',
};

test('the captures read as the sign-ins they were', () => {
  assert.ok(cases.length >= 6);
  for (const c of cases) {
    const raw = readFileSync(new URL(c.file, dir), 'utf8');
    const st = scanSignin(spec, raw);
    assert.deepEqual({ url: st.url, link: st.link, code: st.code, invalid: st.invalid, done: st.done, failed: st.failed },
      { url: c.url, link: c.link, code: c.code, invalid: c.invalid, done: c.done, failed: c.failed }, `${c.file}: ${c.about}`);
  }
});

test('fed in pieces, the reader ends on the whole URL (a part of it at most before)', () => {
  for (const c of cases) {
    const raw = readFileSync(new URL(c.file, dir));
    const r = new SigninReader(spec);
    for (let i = 0; i < raw.length; i += 61) {
      const st = r.push(raw.subarray(i, i + 61));
      assert.ok(st.url === '' || c.url.startsWith(st.url), `${c.file} at ${i}: ${st.url}`);
    }
    assert.equal(r.state.url, c.url, c.file);
    r.reset();
    assert.equal(r.state.url, '', 'reset starts over');
  }
});

test('done, an old CLI, and the echoed command', () => {
  const ok = "Opening browser to sign in…\nIf the browser didn't open, visit: https://claude.ai/oauth/authorize?code=true&state=x\nPaste code here if prompted > Login successful.\n";
  const st = scanSignin(spec, ok);
  assert.ok(st.done && st.code && !st.failed);
  assert.equal(st.url, 'https://claude.ai/oauth/authorize?code=true&state=x');
  const old = scanSignin(spec, "$ claude auth login --claudeai; exit\r\nerror: unknown option '--claudeai'\r\n");
  assert.deepEqual([old.url, old.code, old.done, old.failed, old.last], ['', false, false, '', "error: unknown option '--claudeai'"]);
  const echo = scanSignin(spec, typedLine(spec) + '\n');
  assert.ok(!echo.url && !echo.code && !echo.done && !echo.failed, 'the typed line says nothing');
});

test('only an https URL on the provider\'s hosts is offered', () => {
  for (const u of ['https://claude.ai/oauth/authorize?code=true', 'https://platform.claude.com/oauth/authorize?x=1', 'https://console.anthropic.com/oauth/authorize?x=1']) {
    assert.ok(allowedURL(spec, u), u);
  }
  for (const u of ['http://claude.ai/oauth/authorize?code=true', 'https://claude.ai.evil.example/oauth/authorize?x=1', 'https://evilclaude.ai/oauth/authorize?x=1',
    'https://claude.ai@evil.example/oauth/authorize?x=1', 'https://evil.example/oauth/authorize?next=https://claude.ai/', 'javascript:alert(1)//https://claude.ai/oauth/authorize?x',
    'https://claude.ai/somewhere/else', '']) {
    assert.ok(!allowedURL(spec, u), u);
  }
  const evil = '\x1b]8;;https://evil.example/oauth/authorize?x=1\x07click\x1b]8;;\x07\r\n' +
    'see http://claude.ai/oauth/authorize?code=1 or https://claude.ai.evil.example/oauth/authorize?x=2\r\n';
  assert.equal(scanSignin(spec, evil).url, '');
  assert.equal(scanSignin(spec, evil + '\x1b]8;;https://claude.ai/oauth/authorize?ok=1\x1b\\link\x1b]8;;\x1b\\\r\n').url, 'https://claude.ai/oauth/authorize?ok=1');
});

test('a hard-wrapped URL is rejoined; one ending inside its line is not', () => {
  const u = 'https://claude.ai/oauth/authorize?code=true&client_id=abc&state=0123456789abcdefghij';
  const rows = [u.slice(0, 30), u.slice(30, 60), u.slice(60)];
  assert.equal(scanSignin(spec, 'Use the url below:\r\n\r\n' + rows.join('\r\n') + '\r\n\r\nPaste code here if prompted >').url, u);
  assert.equal(scanSignin(spec, rows.map((r) => '  ' + r).join('\r\n') + '\r\n').url, u, 'indented');
  assert.equal(scanSignin(spec, 'visit: ' + u + '\nPaste code here if prompted > ').url, u, 'one line');
  assert.equal(scanSignin(spec, rows[0] + '\r\n' + rows[1].slice(0, 10) + '\r\nnext\r\n').url, rows[0] + rows[1].slice(0, 10), 'a shorter row ends it');
  assert.deepEqual(screenText('a\x1b[5Gb\x1b[3Cc\r\r\nd').lines, ['a b c', 'd'], 'cursor moves along a line are spaces');
});

test('typedLine and cleanCode', () => {
  assert.equal(typedLine(spec), ' claude auth login --claudeai; exit\r');
  assert.equal(cleanCode('  abc#def\r\n'), 'abc#def');
  assert.equal(cleanCode('ab c\x1b[2~d'), 'abc[2~d', 'controls and spaces go, nothing else changes');
  assert.equal(cleanCode(null), '');
});
