// canvas-parity.test.mjs — a `canvas html` island is the same page in the app
// (CanvasDocument.wrap, XbinCore) and in the reference renderer
// (canvasDocument, web/xb/render-content.js), which `bx preview --native`
// shows builders: the same CSP (loads nothing), and a transparent page in
// the light/dark default colours. Run by `make native-check`.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { join, resolve } from 'node:path';

const ROOT = resolve(new URL('../..', import.meta.url).pathname);
const swift = readFileSync(join(ROOT, 'native/ios/Packages/XbinCore/Sources/XbinCore/Client/TileResources.swift'), 'utf8');
const lit = readFileSync(join(ROOT, 'web/xb/render-content.js'), 'utf8');

const wrap = swift.slice(swift.indexOf('public enum CanvasDocument'));

test('the same CSP', () => {
  const app = wrap.match(/static let csp = "([^"]+)"/)?.[1];
  const web = lit.match(/const CANVAS_CSP = "([^"]+)"/)?.[1];
  assert.ok(app, 'CanvasDocument.csp not found');
  assert.equal(web, app);
  assert.match(app, /default-src 'none'/);
});

test('the same page around the markup', () => {
  const metas = (s) => [...s.matchAll(/<meta name=\\?"([a-z-]+)\\?" content=\\?"([^"\\]+)\\?"/g)].map((m) => `${m[1]}=${m[2]}`);
  const web = lit.slice(lit.indexOf('export const canvasDocument'));
  assert.deepEqual(metas(web), metas(wrap));
  assert.ok(metas(wrap).includes('color-scheme=light dark'));
  for (const s of [wrap, web]) assert.match(s, /html,body\{margin:0;padding:0;background:transparent;/);
});

test('the same address for an island, and the same zoom range (rev 2, D208)', () => {
  const app = wrap.match(/static let base = "([^"]+)"/)?.[1];
  const web = lit.match(/export const CANVAS_BASE = '([^']+)'/)?.[1];
  assert.ok(app, 'CanvasDocument.base not found');
  assert.equal(web, app);
  assert.match(wrap, /min\(3, max\(0\.5, value\)\)/);
  assert.match(lit, /Math\.min\(3, Math\.max\(0\.5, z\)\)/);
  // the zoom is the same CSS zoom in the head, after the base style
  assert.match(wrap, /<style>html\{zoom:\\\(cssNumber\(z\)\)\}<\/style>/);
  assert.match(lit, /<style>html\{zoom:\$\{canvasZoom\(zoom\)\}\}<\/style>/);
});

test('a link comes back relative to the island, as the app reports it (CanvasDocument.href)', () => {
  // the reference's helpers, sliced out (the module imports /vendor/, a browser's)
  const src = lit.slice(lit.indexOf('export const CANVAS_BASE'), lit.indexOf('export const canvasDocument')).replace(/^export /gm, '');
  const { canvasHref, canvasZoom } = new Function(`${src}; return { canvasHref, canvasZoom };`)();
  assert.equal(canvasHref('out/report.pdf'), 'out/report.pdf');
  assert.equal(canvasHref('./out/../report.pdf'), 'report.pdf', 'resolved as a browser resolves it');
  assert.equal(canvasHref('my report.pdf'), 'my%20report.pdf');
  assert.equal(canvasHref('#top'), '#top');
  assert.equal(canvasHref('https://example.com/a'), 'https://example.com/a');
  assert.deepEqual([canvasZoom(undefined), canvasZoom(0), canvasZoom(0.1), canvasZoom(1.25), canvasZoom(9)], [1, 1, 0.5, 1.25, 3]);
});
