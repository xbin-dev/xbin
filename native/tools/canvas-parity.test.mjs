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
