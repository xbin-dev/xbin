// The node runner for native.js (hack/xbn/node.mjs) as the fixtures use it:
// a pinned time zone and locale, events addressed by selector and checked
// against the tree, and event streams whose frames arrive as the virtual
// clock moves.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, writeFileSync, rmSync } from 'node:fs';
import { join } from 'node:path';
import { tmpdir } from 'node:os';
import { runNative } from './xbn/node.mjs';
import { parseSelector, selectAll, selectKey } from './xbn/select.mjs';

function tile(src) {
  const dir = mkdtempSync(join(tmpdir(), 'xbn-runner-'));
  const entry = join(dir, 'native.js');
  writeFileSync(entry, src);
  return { entry, done: () => rmSync(dir, { recursive: true, force: true }) };
}

const NOW = Date.parse('2026-09-21T14:13:20Z');

test('a pinned time zone and locale apply to Date and Intl (a child process when they differ)', async () => {
  const t = tile(`import { html, render } from '/vendor/xb-native.js';
    render(html\`<screen title="t"><text>\${new Date().getHours()}</text>
      <text>\${new Intl.DateTimeFormat(undefined, { year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hourCycle: 'h23' }).format(Date.now())}</text>
      <text>\${(1234.5).toLocaleString()}</text></screen>\`);`);
  try {
    const tokyo = await runNative({ entry: t.entry, data: { now: NOW, tz: 'Asia/Tokyo', locale: 'de-DE' } });
    assert.deepEqual(tokyo.tree.root.c.map((c) => c.p.text), ['23', '21.09.2026, 23:13', '1.234,5']);
    // (numeric fields only: ICU versions disagree on "Sep"/"Sept" and the space before "PM")
    const utc = await runNative({ entry: t.entry, data: { now: NOW, tz: 'UTC', locale: 'en-GB' } });
    assert.deepEqual(utc.tree.root.c.map((c) => c.p.text), ['14', '21/09/2026, 14:13', '1,234.5']);
  } finally { t.done(); }
});

test('selectors: tags, props, descendants, ambiguity', () => {
  const root = { k: 'r', t: 'screen', p: { title: 'S' }, c: [
    { k: 'r.0', t: 'section', p: { title: 'A' }, c: [
      { k: 'r.0.0', t: 'field', p: { label: 'Name', value: '' }, e: ['input'] },
      { k: 'r.0.1', t: 'button', p: { label: 'Save', disabled: true }, e: ['tap'] },
    ] },
    { k: 'r.1', t: 'sheet', p: { title: 'New event', open: true }, c: [
      { k: 'r.1.0', t: 'field', p: { label: 'Title' }, e: ['input'] },
      { k: 'r.1.1', t: 'button', p: { label: 'Save' }, e: ['tap'] },
    ] },
  ] };
  assert.equal(selectKey(root, 'field[label=Name]'), 'r.0.0');
  assert.equal(selectKey(root, 'sheet[title="New event"] button[label=Save]'), 'r.1.1');
  assert.equal(selectKey(root, 'button[disabled=true]'), 'r.0.1');
  assert.equal(selectKey(root, 'section button'), 'r.0.1');
  assert.equal(selectKey(root, '[k=r.1.0]'), 'r.1.0');
  assert.equal(selectKey(root, 'sheet[title*=event]'), 'r.1');
  assert.equal(selectAll(root, '[@input]').length, 2);
  assert.throws(() => selectKey(root, 'button[label=Save]'), /ambiguous: r\.0\.1 \(button "Save"\), r\.1\.1/);
  assert.throws(() => selectKey(root, 'toggle'), /matches no node/);
  assert.throws(() => parseSelector('button[label=Save'), /"]"/);
  assert.throws(() => parseSelector(''), /empty selector/);
});

test('event steps by selector are checked against the tree', async () => {
  const t = tile(`import { html, render } from '/vendor/xb-native.js';
    let n = 0, draft = '';
    const paint = () => render(html\`<screen title="t" style="form"><section>
      <row title="Count" detail=\${n}/>
      <field label="Note" value=\${draft}/>
      <button @tap=\${() => { n++; paint(); }}>+1</button></section></screen>\`);
    paint();`);
  try {
    const r = await runNative({ entry: t.entry, data: { now: NOW }, steps: [
      { event: { select: 'button[label="+1"]', type: 'tap' } }, { event: { select: 'button', type: 'tap' } },
      // a report of a controlled prop needs no listener
      { event: { select: 'field[label=Note]', type: 'input', payload: { value: 'hi' } } },
    ] });
    assert.equal(r.tree.root.c[0].c[0].p.detail, '2');
    await assert.rejects(runNative({ entry: t.entry, data: { now: NOW }, steps: [{ event: { select: 'row', type: 'tap' } }] }),
      /event "tap" on r\.0\.0 \(row\): the node does not listen to it/);
    await assert.rejects(runNative({ entry: t.entry, data: { now: NOW }, steps: [{ event: { k: 'r.9', type: 'tap' } }] }), /no node "r\.9"/);
    await assert.rejects(runNative({ entry: t.entry, data: { now: NOW }, steps: [{ event: { select: 'toggle', type: 'change' } }] }), /matches no node/);
  } finally { t.done(); }
});

test('event-stream frames with `after` arrive as the clock moves; `open` keeps the stream open', async () => {
  const t = tile(`import { html, render } from '/vendor/xb-native.js';
    let text = '', state = 'waiting';
    const paint = () => render(html\`<screen title="t"><message role="assistant" markdown text=\${text} ?streaming=\${state === 'streaming'}/><text>\${state}</text></screen>\`);
    (async () => {
      const r = await xbin.fetch('/api/apps/t/stream');
      const rd = r.body.pipeThrough(new TextDecoderStream()).getReader();
      let buf = '';
      state = 'streaming'; paint();
      for (;;) {
        const { value, done } = await rd.read();
        if (done) break;
        buf += value;
        let i;
        while ((i = buf.indexOf('\\n\\n')) >= 0) {
          const f = buf.slice(0, i); buf = buf.slice(i + 2);
          const data = JSON.parse(f.split('\\n').find((l) => l.startsWith('data: ')).slice(6));
          text += data.delta; paint();
        }
      }
      state = 'done'; paint();
    })();
    paint();`);
  const frames = [{ data: { delta: 'Hello' } }, { data: { delta: ', **world**' }, after: 100 }, { data: { delta: '!' }, after: 100 }];
  try {
    const partial = await runNative({ entry: t.entry, data: { now: NOW, routes: { '/api/apps/t/stream': { sse: frames } } }, steps: [{ wait: 150 }] });
    assert.deepEqual(partial.tree.root.c.map((c) => c.p.text), ['Hello, **world**', 'streaming']);
    assert.equal(partial.tree.root.c[0].p.streaming, true);
    const all = await runNative({ entry: t.entry, data: { now: NOW, routes: { '/api/apps/t/stream': { sse: frames } } }, steps: [{ wait: 1000 }] });
    assert.deepEqual(all.tree.root.c.map((c) => c.p.text), ['Hello, **world**!', 'done']);
    const open = await runNative({ entry: t.entry, data: { now: NOW, routes: { '/api/apps/t/stream': { sse: frames, open: true } } }, steps: [{ wait: 1000 }] });
    assert.deepEqual(open.tree.root.c.map((c) => c.p.text), ['Hello, **world**!', 'streaming']);
  } finally { t.done(); }
});
