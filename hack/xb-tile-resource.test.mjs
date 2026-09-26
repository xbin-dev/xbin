// web/xb/tile-resource.js — the browser's copy of the app's confinement of
// what a native tile hands over (XbinCore TileResources.swift, D103): the
// vectors of native/ios/Packages/XbinCore/Tests/XbinCoreTests/
// TileResourceTests.swift, so `bx preview --native` refuses what the app
// refuses and resolves what it resolves the same way.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { apiPath, assetPath, pagePath, uploadMethod } from '../web/xb/tile-resource.js';

test('upload targets', () => {
  assert.equal(apiPath('/api/agent/runs/5/upload?name={name}', 'agent', [], 'IMG 1.jpg'), '/api/agent/runs/5/upload?name=IMG%201.jpg');
  assert.equal(apiPath('/api/agent/ask/upload?draft=k-1_x&name={name}', 'agent', [], 'a b&c.png'), '/api/agent/ask/upload?draft=k-1_x&name=a%20b%26c.png');
  assert.equal(apiPath('upload', 'apps/x'), '/api/apps/x/upload');
  assert.equal(apiPath('/upload', 'apps/x'), '/api/apps/x/upload');
  assert.equal(apiPath('./files/{name}', 'apps/x', [], 'b?#.txt'), '/api/apps/x/files/b%3F%23.txt');
  assert.equal(apiPath('up?name={name}&k=1', 'apps/x', [], 'a&b=c.txt'), '/api/apps/x/up?name=a%26b%3Dc.txt&k=1');
  assert.equal(apiPath('/c/x', 'apps/x'), '/api/apps/x/c/x');
  assert.equal(apiPath('/api/apps/x', 'apps/x'), '/api/apps/x');
  assert.equal(apiPath('/api/apps/x/', 'apps/x'), '/api/apps/x/');
  assert.equal(apiPath('?q=1', 'apps/x'), '/api/apps/x/?q=1');
  assert.equal(apiPath('.', 'apps/x'), '/api/apps/x/');
  assert.equal(apiPath('up', 'apps/my tile'), '/api/apps/my%20tile/up');
  assert.equal(apiPath('/api/apps/my%20tile/up', 'apps/my tile'), '/api/apps/my%20tile/up');
  assert.equal(apiPath('dir/é x?n=a b&m=%41', 't'), '/api/t/dir/%C3%A9%20x?n=a%20b&m=%41');
  assert.equal(apiPath('up#frag', 't'), '/api/t/up');
});

test('upload targets refused', () => {
  const refused = [
    '/api/other/x', '/api/agent2/x', '/api/xbin/frame-token',
    '/api/agent/../other/x', '/api/agent/%2e%2e/other', '/api/agent/a%2fb', '/api/agent/a%5cb',
    '../other/x', './../x', 'a/../../b', 'a/./b',
    'https://evil.example/x', 'http:x', 'javascript:alert(1)', 'data:text/plain,x', 'a:b',
    '//evil.example/x', '\\\\evil\\x', '/api/agent\\..\\x',
    '/api//agent/x', 'up//x', '/api/agent/%zz', '/api/agent/%ff',
    'up\nx', 'up\u007f', '', '/api', '/api/',
  ];
  for (const r of refused) assert.equal(apiPath(r, 'agent'), null, JSON.stringify(r));
  assert.equal(apiPath('/api/agent/{name}', 'agent', [], '..'), null);
  assert.equal(apiPath('/api/agent/f/{name}', 'agent', [], '../../x'), null);
  assert.equal(apiPath('/api/agent/f?n={name}', 'agent', [], '../../x'), '/api/agent/f?n=..%2F..%2Fx');
  for (const tile of ['xbin', 'xbin/x', '', 'a/../b']) assert.equal(apiPath('up', tile), null, tile);
});

test('nested tiles own their paths', () => {
  const known = ['apps', 'apps/other', 'apps/x'];
  assert.equal(apiPath('other/up', 'apps', known), null);
  assert.equal(apiPath('/api/apps/other/up', 'apps', known), null);
  assert.equal(apiPath('/api/apps/other', 'apps', known), null);
  assert.equal(apiPath('otherwise/up', 'apps', known), '/api/apps/otherwise/up');
  assert.equal(apiPath('up', 'apps/x', known), '/api/apps/x/up');
  assert.equal(assetPath('other/logo.png', 'apps', known), null);
  assert.equal(pagePath('x/', 'apps', known), null);
});

test('frame parameters are dropped', () => {
  assert.equal(apiPath('pty?frame=abc&cols=80', 't'), '/api/t/pty?cols=80');
  assert.equal(apiPath('pty?fr%61me=abc', 't'), '/api/t/pty');
  assert.equal(apiPath('pty?frame', 't'), '/api/t/pty');
  assert.equal(apiPath('pty?framed=1', 't'), '/api/t/pty?framed=1');
});

test('assets and pages', () => {
  assert.equal(assetPath('logo.png', 'apps/x'), '/c/apps/x/logo.png');
  assert.equal(assetPath('./img/a b.png', 'apps/x'), '/c/apps/x/img/a%20b.png');
  assert.equal(assetPath('/c/apps/x/logo.png', 'apps/x'), '/c/apps/x/logo.png');
  assert.equal(assetPath('/api/agent/runs/3/thumb?path=a%2Fb.png&w=480', 'agent'), '/api/agent/runs/3/thumb?path=a%2Fb.png&w=480');
  for (const r of ['/logo.png', '/c/apps/y/logo.png', '../y/logo.png', 'https://cdn.example/x.png', '/api/xbin/whoami']) {
    assert.equal(assetPath(r, 'apps/x'), null, r);
  }
  assert.equal(pagePath('chart.html', 'apps/x'), '/c/apps/x/chart.html');
  assert.equal(pagePath('chart.html?d=1#dark', 'apps/x'), '/c/apps/x/chart.html?d=1#dark');
  assert.equal(pagePath('/c/apps/x/sub/', 'apps/x'), '/c/apps/x/sub/');
  assert.equal(pagePath('./', 'apps/x'), '/c/apps/x/');
  assert.equal(pagePath('/api/apps/x/page', 'apps/x'), null);
  assert.equal(pagePath('/c/apps/y/', 'apps/x'), null);
  assert.equal(pagePath('//evil/', 'apps/x'), null);
  assert.equal(pagePath('reports/5/coverage.html#top', 'apps/ci'), '/c/apps/ci/reports/5/coverage.html#top');
});

test('upload methods', () => {
  assert.equal(uploadMethod(undefined), 'PUT');
  assert.equal(uploadMethod(''), 'PUT');
  assert.equal(uploadMethod('post'), 'POST');
  assert.equal(uploadMethod('PATCH'), 'PATCH');
  assert.equal(uploadMethod('DELETE'), null);
  assert.equal(uploadMethod('GET'), null);
});
