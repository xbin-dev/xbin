// hack/pair-kit.test.mjs — adding a phone over the app tunnel (D204), run by
// `make js-test`: web/pair-kit.js — the confirmation code as people compare
// it ("012 345", native/spec/tunnel.md §5), the pairing's states, the
// address rewrite that keeps a tunnel link's parameters (§2), the QR code's
// size (level M, ≥ 288 px, ≥ 4 px per module) with the vendored encoder,
// the device limit's words and the confirmation's body.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import qrcode from '../web/vendor/qrcode.mjs';
import {
  formatSas, pairState, isFinal, phoneOrigin, withOrigin, linkParams, isTunnelLink, qrGeometry, qrModel, QR_MIN_PX,
  QR_MIN_MODULE, expiresAt, clock, limitInfo, confirmBody, endWords, platformName, ago,
} from '../web/pair-kit.js';

const B64 = (n) => 'A'.repeat(n);
const TICKET = `endpoint${'a'.repeat(110)}`;
const TUNNEL = `xbin://enroll?u=${encodeURIComponent('https://xbin.example.com')}&c=ABCDEFGHIJKLMNOPQRSTUVWXYZ&tv=1&t=${TICKET}&k=${B64(43)}&p=${B64(43)}`;

test('formatSas: six digits, zero-padded, grouped by three', () => {
  assert.equal(formatSas(12345), '012 345');
  assert.equal(formatSas('012345'), '012 345');
  assert.equal(formatSas(0), '000 000');
  assert.equal(formatSas('999 999'), '999 999');
  assert.equal(formatSas(7), '000 007');
  for (const bad of [null, undefined, '', 'abc', 1234567, '12-345', true, -1, 1.5]) assert.equal(formatSas(bad), null, String(bad));
});

test('pairState: xbind\'s states and what the page shows', () => {
  assert.equal(pairState('minted'), 'waiting');
  assert.equal(pairState('handshaken'), 'connecting');
  assert.equal(pairState('confirm'), 'confirm');
  assert.equal(pairState('enrolled'), 'enrolled');
  assert.equal(pairState('done'), 'enrolled');
  assert.equal(pairState('rejected'), 'rejected');
  assert.equal(pairState('abandoned'), 'abandoned');
  assert.equal(pairState('expired'), 'expired');
  assert.equal(pairState('something-newer'), 'waiting', 'a newer xbind\'s state keeps the poll going');
  assert.deepEqual(['waiting', 'connecting', 'confirm', 'enrolled', 'rejected', 'abandoned', 'expired'].filter(isFinal),
    ['enrolled', 'rejected', 'abandoned', 'expired']);
});

test('phoneOrigin: an http(s) origin or null', () => {
  assert.equal(phoneOrigin(' HTTPS://Phone.Example.test:8443/ '), 'https://phone.example.test:8443');
  assert.equal(phoneOrigin('xbin.example.com'), 'https://xbin.example.com');
  assert.equal(phoneOrigin('http://10.0.0.2:8080'), 'http://10.0.0.2:8080');
  for (const bad of ['ftp://x.test', 'https://x.test/path', 'https://u:p@x.test', 'https://x.test/?q=1', '']) assert.equal(phoneOrigin(bad), null, bad);
});

test('withOrigin: only u= changes; a tunnel link keeps every other parameter byte for byte', () => {
  const r = `${TUNNEL}&r=${encodeURIComponent('tok+en/=1')}&rq=1`;
  const got = withOrigin(r, 'https://phone.example.test:8443');
  assert.equal(got, r.replace(`u=${encodeURIComponent('https://xbin.example.com')}`, `u=${encodeURIComponent('https://phone.example.test:8443')}`));
  const p = linkParams(got);
  assert.equal(p.u, 'https://phone.example.test:8443');
  assert.equal(p.r, 'tok+en/=1', 'the relay token is not re-encoded');
  assert.equal(p.rq, '1');
  assert.equal(p.t, TICKET);
  // the classic link as xbind writes it: the same as the shell's enrollLink
  assert.equal(withOrigin('xbin://enroll?u=https%3A%2F%2Fa.test&c=CODE', 'https://b.test'), 'xbin://enroll?u=https%3A%2F%2Fb.test&c=CODE');
  // no u=: added first; no origin or no query: unchanged
  assert.equal(withOrigin('xbin://enroll?c=CODE', 'https://b.test'), 'xbin://enroll?u=https%3A%2F%2Fb.test&c=CODE');
  assert.equal(withOrigin('xbin://enroll?u=x&c=CODE', ''), 'xbin://enroll?u=x&c=CODE');
  assert.equal(isTunnelLink(TUNNEL), true);
  assert.equal(isTunnelLink('xbin://enroll?u=x&c=CODE'), false);
});

test('qrGeometry: at least 288 px, whole pixels, at least 4 per module', () => {
  for (const n of [21, 29, 37, 57, 65, 73, 85, 97, 121, 177]) {
    const g = qrGeometry(n);
    assert.ok(g.px >= QR_MIN_PX, `${n}: ${g.px}px`);
    assert.ok(g.ppm >= QR_MIN_MODULE && Number.isInteger(g.ppm), `${n}: ${g.ppm} px per module`);
    assert.equal(g.px, g.ppm * (n + 8));
  }
  assert.equal(qrGeometry(65).px, 292, 'a typical tunnel link (version 12): 4 px per module');
});

test('qrModel: the vendored encoder at level M; the path covers exactly the dark modules', () => {
  for (const link of [TUNNEL, `${TUNNEL}&r=${'x'.repeat(256)}&rq=1`, 'xbin://enroll?u=https%3A%2F%2Fa.test&c=CODE']) {
    const m = qrModel(qrcode, link);
    const ref = qrcode(0, 'M');
    ref.addData(link);
    ref.make();
    assert.equal(m.n, ref.getModuleCount());
    assert.ok(m.px >= QR_MIN_PX && m.ppm >= QR_MIN_MODULE);
    let dark = 0;
    for (let r = 0; r < m.n; r++) for (let c = 0; c < m.n; c++) if (ref.isDark(r, c)) dark++;
    const runs = [...m.d.matchAll(/M(\d+) (\d+)h(\d+)v1h-(\d+)z/g)];
    assert.equal(runs.reduce((s, x) => s + Number(x[3]), 0), dark);
    for (const [, x, y, w] of runs) {
      for (let i = 0; i < Number(w); i++) assert.ok(ref.isDark(Number(y) - 4, Number(x) - 4 + i));
    }
  }
  // a tunnel link with a custom relay's token still fits a 504 px wide panel at 4 px per module
  assert.ok(qrModel(qrcode, `${TUNNEL}&r=${'x'.repeat(64)}`).px <= 504);
});

test('expiresAt and clock', () => {
  assert.equal(expiresAt(1761600000), 1761600000);
  assert.equal(expiresAt('2026-10-27T22:00:00Z'), Date.parse('2026-10-27T22:00:00Z') / 1000);
  assert.equal(expiresAt(''), null);
  assert.equal(expiresAt('soon'), null);
  assert.equal(clock(299.4), '4:59');
  assert.equal(clock(-3), '0:00');
  assert.equal(clock(61), '1:01');
});

test('limitInfo: GET /devices {limit, count} in words; none from an xbind before D204', () => {
  assert.equal(limitInfo({ devices: [] }), null);
  assert.deepEqual(limitInfo({ devices: [{}], limit: 1, count: 1 }), { limit: 1, count: 1, at: true, words: '1 of 1 device' });
  assert.deepEqual(limitInfo({ devices: [{}], limit: 3, count: 1 }), { limit: 3, count: 1, at: false, words: '1 of 3 devices' });
  assert.equal(limitInfo({ devices: [{}, {}], limit: 3 }).count, 2, 'count falls back to the list');
  assert.equal(limitInfo({ devices: [], limit: 0, count: 0 }).at, true, 'a limit of 0: no devices at all');
});

test('confirmBody: {match}; a match at the limit names the device it replaces', () => {
  assert.deepEqual(confirmBody(true), { match: true });
  assert.deepEqual(confirmBody(true, 'dev-1'), { match: true, replace: 'dev-1' });
  assert.deepEqual(confirmBody(false, 'dev-1'), { match: false }, 'They don\'t never replaces anything');
});

test('endWords: what a pairing that added nothing says', () => {
  assert.match(endWords('rejected', 'mismatch').title, /didn't match/);
  assert.match(endWords('rejected', 'mismatch').body, /someone else may have scanned it/);
  assert.equal(endWords('rejected', 'mismatch').tone, 'danger');
  assert.match(endWords('rejected', 'limit').title, /device limit/);
  assert.match(endWords('rejected', 'refused', 'this account is disabled').body, /disabled/);
  assert.match(endWords('abandoned').body, /Nothing was added/);
  assert.match(endWords('expired').title, /expired/);
  assert.equal(endWords('enrolled'), null);
  assert.equal(endWords('confirm'), null);
});

test('platformName and ago', () => {
  assert.equal(platformName('ios'), 'iOS');
  assert.equal(platformName('plan9'), 'plan9');
  assert.equal(platformName(undefined), '');
  assert.equal(ago(0), 'never');
  assert.equal(ago(1000, 1030), 'just now');
  assert.equal(ago(1000, 1000 + 3 * 86400), '3 d ago');
});
