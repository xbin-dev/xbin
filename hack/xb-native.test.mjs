// web/xb-native.js — the native runtime's template layer (native/spec/tree.md).
// Parser, key rules, validation, the keyed diff (randomized against the
// reference patch application), scheduling, controlled props, events, the
// xbin.native API, markdown tokens, transports, and the vocabulary export.
// The design's eight tile trees are hack/xb-native-examples.test.mjs.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { installHooks } from './xbn/hooks.mjs';

installHooks();
const XB = new URL('../web/xb-native.js', import.meta.url).href;
const { html, repeat, nothing, createRuntime } = await import(XB);
const { diff, applyOps, normalize, deepEqual, cloneJSON } = await import('/vendor/xb/rt-diff.js');
const { markdownTokens, MarkdownCache } = await import('/vendor/xb/rt-markdown.js');
const { VOCAB, fullCaps } = await import('/vendor/xb/vocab.js');

// mk(): a runtime flushed by hand; r(value) renders and returns the message.
function mk(opts = {}) {
  const msgs = [];
  const rt = createRuntime({ post: (m) => msgs.push(m), schedule: () => {}, log: false, ...opts });
  const r = (v) => { rt.render(v); return rt.flush(); };
  const diags = () => msgs.filter((m) => m.op === 'diag').map((m) => m.code);
  const errors = () => msgs.filter((m) => m.op === 'error');
  return { rt, msgs, r, diags, errors };
}
const root = (m) => m.root;

// ── templates ────────────────────────────────────────────────────────────────
test('attribute kinds: static, bare, raw value, interpolation, ?bool, .alias, @event', () => {
  const { r, rt } = mk();
  const fn = () => {};
  const m = r(html`<row title="static" nav detail=${42} subtitle="a ${'b'} c" ?selected=${1} ?disabled=${nothing} .badge=${'x'} @tap=${fn}/>`);
  assert.deepEqual(root(m), { k: 'r', t: 'row', p: { title: 'static', nav: true, detail: '42', subtitle: 'a b c', selected: true, disabled: false, badge: 'x' }, e: ['tap'] });
  assert.equal(rt.tree.root.p.detail, '42');
});

test('bindings that are nothing, null or undefined leave the prop out; quoted single bindings stay raw', () => {
  const { r } = mk();
  const m = r(html`<progress value="${0.5}" label=${undefined}/>`);
  assert.deepEqual(root(m).p, { value: 0.5 });
  assert.deepEqual(root(r(html`<row title=${null} detail=${nothing}/>`)), { k: 'r', t: 'row' });
});

test('entities, comments and whitespace', () => {
  const { r } = mk();
  const m = r(html`
    <!-- a comment ${'ignored'} -->
    <section title="a &amp; b &lt;c&gt; &quot;d&quot; &#39;e&#39; &#x2713;">
      <text>
        one   two
        &amp; three ${'  four  '}
      </text>
      <button>  go  </button>
      <code>
line 1
  line 2
      </code>
    </section>`);
  const s = root(m);
  assert.equal(s.p.title, 'a & b <c> "d" \'e\' ✓');
  assert.deepEqual(s.c.map((c) => c.p), [{ text: 'one two & three   four  ' }, { label: 'go' }, { text: 'line 1\n  line 2' }]);
});

test('strings, numbers, arrays and static text become text nodes; booleans and nothing render nothing', () => {
  const { r } = mk();
  const m = r(html`<section>hello ${'x'}${7}${[html`<row/>`, 'y']}${false}${true}${nothing}${null}</section>`);
  assert.deepEqual(root(m).c, [
    { k: 'r.0', t: 'text', p: { text: 'hello' } },
    { k: 'r.1', t: 'text', p: { text: 'x' } },
    { k: 'r.2', t: 'text', p: { text: '7' } },
    { k: 'r.3.0', t: 'row' },
    { k: 'r.3.1', t: 'text', p: { text: 'y' } },
  ]);
});

test('lit habits: its nothing is ours, a lit TemplateResult is a diagnostic', () => {
  assert.equal(nothing, Symbol.for('lit-nothing'));
  const { r, diags } = mk();
  r(html`<section>${{ _$litType$: 1, strings: [''], values: [] }}</section>`);
  assert.deepEqual(diags(), ['lit-template']);
});

test('template errors are render exceptions with the call site; the previous tree stays', () => {
  const { r, rt, errors } = mk();
  r(html`<text>ok</text>`);
  for (const bad of [
    () => html`<section><row></section>`,
    () => html`<section>`,
    () => html`<row ${'x'}/>`,
    () => html`<${'row'}/>`,
    () => html`<row @tap="x"/>`,
    () => html`</row>`,
  ]) {
    assert.equal(r(bad()), null);
  }
  const errs = errors();
  assert.equal(errs.length, 6);
  for (const e of errs) { assert.equal(e.kind, 'exception'); assert.match(e.message, /xb-native\.test\.mjs:\d+:\d+/); }
  assert.equal(rt.tree.root.p.text, 'ok');
});

// ── keys (native/spec/tree.md "Keys") ────────────────────────────────────────
test('keys: slots count elements, holes and text; a single-root hole takes the hole key; multi-root gets <hole>.<i>', () => {
  const { r } = mk();
  const multi = html`${html`<text>a</text>`}<text>b</text>`;
  const m = r(html`<screen>${nothing}<section>${html`<row/>`}${multi}<row/></section></screen>`);
  assert.deepEqual(root(m).c[0].c.map((c) => c.k), ['r.1.0', 'r.1.1.0', 'r.1.1.1', 'r.1.2']);
  assert.equal(root(m).c[0].k, 'r.1');
});

test('keys: repeat and key= give <parent>.<slot>:<key>; unkeyed repeat keys by index; arrays <hole>.<i>', () => {
  const { r } = mk();
  const items = [{ id: 'a' }, { id: 'b' }];
  const m = r(html`<list>
    ${repeat(items, (x) => x.id, (x) => html`<row title=${x.id}/>`)}
    ${repeat(items, (x, i) => html`<row title=${`${x.id}${i}`}/>`)}
    ${items.map((x) => html`<row key=${x.id}/>`)}
    <row key="z"/>
  </list>`);
  assert.deepEqual(root(m).c.map((c) => c.k), ['r.0:a', 'r.0:b', 'r.1:0', 'r.1:1', 'r.2.0:a', 'r.2.1:b', 'r.3:z']);
  assert.ok(root(m).c.every((c) => !c.p || !('key' in c.p)), 'key= is not a prop');
});

test('keys: a multi-root template from repeat, nested', () => {
  const { r } = mk();
  const turn = (m) => html`${m.think ? html`<thinking text=${m.think}/>` : nothing}<message role="assistant" text=${m.text}/>`;
  const out = r(html`<transcript>${repeat([{ id: 3, think: 'hm', text: 'x' }, { id: 4, text: 'y' }], (m) => m.id, turn)}</transcript>`);
  assert.deepEqual(root(out).c.map((c) => c.k), ['r.0:3.0', 'r.0:3.1', 'r.0:4.1']);
});

test('keys: duplicates are renamed with a diagnostic', () => {
  const { r, diags } = mk();
  const m = r(html`<list>${repeat([1, 1, 2], (x) => x, (x) => html`<row title=${String(x)}/>`)}</list>`);
  assert.deepEqual(root(m).c.map((c) => c.k), ['r.0:1', 'r.0:1~2', 'r.0:2']);
  assert.deepEqual(diags(), ['duplicate-key']);
});

test('several top-level nodes render as a fragment "r"; tab keeps key as a prop', () => {
  const { r } = mk();
  const m = r(html`<nav><screen/></nav><sheet open=${false}/>`);
  assert.deepEqual(root(m), { k: 'r', t: 'fragment', c: [{ k: 'r.0', t: 'nav', c: [{ k: 'r.0.0', t: 'screen' }] }, { k: 'r.1', t: 'sheet', p: { open: false } }] });
  const t = r(html`<tabs><tab key="a" title="A"><text>1</text></tab><tab key="b"><text>2</text></tab></tabs>`);
  assert.deepEqual(root(t).c.map((c) => [c.k, c.p.key, c.c.length]), [['r.0', 'a', 1], ['r.1', 'b', 1]], 'uncontrolled tabs materialize every tab');
});

test('tabs with selected materialize only the selected tab', () => {
  const { r } = mk();
  let calls = 0;
  const body = () => { calls++; return html`<text>x</text>`; };
  const m = r(html`<tabs selected="b">
    <tab key="a">${repeat([1], (x) => x, body)}</tab>
    ${html`<tab key="b">${repeat([1], (x) => x, body)}</tab>`}
  </tabs>`);
  assert.deepEqual(root(m).c.map((c) => [c.k, c.c]), [['r.0', []], ['r.1', [{ k: 'r.1.0:1', t: 'text', p: { text: 'x' } }]]]);
  assert.equal(calls, 1, "an unselected tab's content is not even evaluated");
});

test('c is present when the template gives an element children, even if none render', () => {
  assert.deepEqual(root(mk().r(html`<section>${nothing}</section>`)), { k: 'r', t: 'section', c: [] });
  assert.deepEqual(root(mk().r(html`<section></section>`)), { k: 'r', t: 'section' });
});

// ── validation ───────────────────────────────────────────────────────────────
test('validation: unknown tag, prop and event; wrong types; tokens; icons; child rules', () => {
  const { r, msgs, errors } = mk();
  const m = r(html`<screen>
    <blink/>
    <row title=${{}} detial="x" tone="#ff0000" icon="nope" @swipe=${() => {}} @tap=${'x'}/>
    <stack gap="7px"/><text lines="3" style="headline">t</text>
    <toolbar><row/></toolbar>
    <split><text>1</text></split>
    <icon name="plus"><text>x</text></icon>
  </screen>`);
  const d = msgs.filter((x) => x.op === 'diag').map((x) => x.code);
  assert.deepEqual(d.sort(), ['bad-children', 'bad-handler', 'bad-token', 'bad-token', 'bad-type', 'bad-value', 'child-rule', 'child-rule', 'unknown-event', 'unknown-prop', 'unknown-tag'].sort());
  assert.deepEqual(errors().map((e) => e.kind), ['unsupported']);
  const [blink, row, stack, text] = root(m).c;
  assert.equal(blink.t, 'blink');
  assert.deepEqual(row.p, { icon: 'nope' }, 'bad values are dropped; an unknown icon is kept (the renderer draws a placeholder)');
  assert.equal(row.e, undefined);
  assert.equal(stack.p, undefined);
  assert.deepEqual(text.p, { lines: 3, style: 'headline', text: 't' });
  for (const x of msgs.filter((y) => y.op === 'diag')) assert.match(x.where, /xb-native\.test\.mjs:\d+:\d+ <\w+>/);
});

test('validation: nested shapes, unions and coercion', () => {
  const { r, diags } = mk();
  const m = r(html`<screen>
    <picker value=${2} options=${[{ value: 1, label: 1 }, { value: 'x', label: 'X', extra: true }]}/>
    <sheet detents=${['medium', 'large']}/><sheet detents="large"/><sheet detents=${['huge']}/>
    <chart kind="line" series=${[{ name: 's', points: [[1, 2], ['a', 3]] }]}/>
    <button confirm=${{ title: 'Sure?', destructive: 'yes' }}/>
  </screen>`);
  const [picker, s1, s2, s3, chart, button] = root(m).c;
  assert.deepEqual(picker.p, { value: 2, options: [{ value: 1, label: '1' }, { value: 'x', label: 'X', extra: true }] });
  assert.deepEqual([s1.p, s2.p, s3.p], [{ detents: ['medium', 'large'] }, { detents: 'large' }, undefined]);
  assert.deepEqual(chart.p.series[0].points, [[1, 2], ['a', 3]]);
  assert.equal(button.p, undefined);
  assert.deepEqual(diags().sort(), ['bad-type', 'bad-type', 'bad-value']);
});

test('props are snapshots: mutating a bound array and re-rendering patches it', () => {
  const { r } = mk();
  const opts = [{ value: 'a', label: 'A' }];
  r(html`<picker options=${opts}/>`);
  opts.push({ value: 'b', label: 'B' });
  assert.deepEqual(r(html`<picker options=${opts}/>`).ops, [['set', 'r', { options: [{ value: 'a', label: 'A' }, { value: 'b', label: 'B' }] }]]);
});

test('caps: a primitive, prop revision or feature the app lacks is {op:"error",kind:"unsupported"}', () => {
  const caps = fullCaps();
  delete caps.prims.chart;
  caps.features = [];
  const { r, errors } = mk({ caps });
  r(html`<screen><chart kind="line"/><chart kind="line"/></screen>`);
  assert.deepEqual(errors().map((e) => [e.kind, e.message]), [['unsupported', 'the app does not support <chart>']]);
  const c2 = fullCaps(); c2.features = [];
  const b = mk({ caps: c2 });
  b.r(html`<chart kind="area"/>`);
  assert.deepEqual(b.errors().map((e) => e.message), ['the app does not support <chart> kind="area" (chart.area)']);
  const c3 = fullCaps(); c3.prims.gizmo = 1;
  const c = mk({ caps: c3 });
  const m = c.r(html`<gizmo size=${3}/>`);
  assert.deepEqual(root(m), { k: 'r', t: 'gizmo', p: { size: 3 } }, 'unknown to the runtime but known to the app: passed through');
  assert.deepEqual(c.errors(), []);
});

test('caps: a prop newer than the app\'s revision is unsupported', async () => {
  const { VOCAB: V } = await import('/vendor/xb/vocab.js');
  V.prims.badge.props.glow = { type: 'bool', since: 2 };
  try {
    const caps = fullCaps();
    const { r, errors } = mk({ caps });
    r(html`<badge glow>x</badge>`);
    assert.deepEqual(errors().map((e) => e.message), ['the app does not support <badge> glow (rev 2)']);
  } finally { delete V.prims.badge.props.glow; }
});

// ── the diff (randomized against applyOps) ──────────────────────────────────
function rng(seed) {
  return () => { seed |= 0; seed = (seed + 0x6d2b79f5) | 0; let t = Math.imul(seed ^ (seed >>> 15), 1 | seed); t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t; return ((t ^ (t >>> 14)) >>> 0) / 4294967296; };
}
const TYPES = ['row', 'section', 'text', 'stack', 'button'];
function genNode(R, k, depth) {
  const n = { k, t: TYPES[Math.floor(R() * TYPES.length)] };
  const p = {};
  for (const name of ['a', 'b', 'c', 'd']) if (R() < 0.5) p[name] = [Math.floor(R() * 3), `s${Math.floor(R() * 3)}`, [1, Math.floor(R() * 2)], { x: Math.floor(R() * 2) }][Math.floor(R() * 4)];
  if (Object.keys(p).length) n.p = p;
  if (R() < 0.3) n.e = R() < 0.5 ? ['tap'] : ['tap', 'change'];
  if (depth > 0 && R() < 0.8) {
    const ids = [...Array(10).keys()].filter(() => R() < 0.5);
    n.c = ids.map((id) => genNode(R, R() < 0.5 ? `${k}.${id}` : `${k}.0:${id}`, depth - 1));
    const seen = new Set();
    n.c = n.c.filter((c) => !seen.has(c.k) && seen.add(c.k));
  }
  return n;
}
function mutate(R, n, depth) {
  const m = cloneJSON(n);
  const walk = (x, d) => {
    if (R() < 0.3) x.p = { ...(x.p || {}), a: Math.floor(R() * 5) };
    if (x.p && R() < 0.2) { delete x.p.b; if (!Object.keys(x.p).length) delete x.p; }
    if (R() < 0.1) { if (x.e) delete x.e; else x.e = ['tap']; }
    if (x.c) {
      if (R() < 0.4) for (let i = x.c.length - 1; i > 0; i--) { const j = Math.floor(R() * (i + 1)); [x.c[i], x.c[j]] = [x.c[j], x.c[i]]; }
      if (R() < 0.3 && x.c.length) x.c.splice(Math.floor(R() * x.c.length), 1);
      if (R() < 0.3) {
        const k = `${x.k}.0:n${Math.floor(R() * 1e6)}`;
        if (!x.c.some((c) => c.k === k)) x.c.splice(Math.floor(R() * (x.c.length + 1)), 0, genNode(R, k, Math.max(0, d - 1)));
      }
      if (R() < 0.1 && x.c.length) x.c[0].t = TYPES[Math.floor(R() * TYPES.length)];
      for (const c of x.c) walk(c, d - 1);
    }
  };
  walk(m, depth);
  return m;
}

test('diff: applying the patch to the old tree yields the new one (2000 random pairs)', () => {
  const R = rng(1234);
  let ops = 0;
  for (let i = 0; i < 2000; i++) {
    const a = genNode(R, 'r', 3);
    a.t = 'screen';
    let b = mutate(R, a, 3);
    b.t = 'screen';
    for (let steps = Math.floor(R() * 3); steps > 0; steps--) { b = mutate(R, b, 3); b.t = 'screen'; }
    const patch = diff(cloneJSON(a), cloneJSON(b));
    ops += patch.length;
    const got = applyOps(cloneJSON(a), JSON.parse(JSON.stringify(patch)));
    assert.deepEqual(normalize(got), normalize(b), `pair ${i}`);
    assert.deepEqual(diff(cloneJSON(b), cloneJSON(b)), [], 'an unchanged tree has no ops');
  }
  assert.ok(ops > 2000);
});

test('diff: the fewest moves (LIS), removes before inserts, type changes re-insert', () => {
  const kids = (keys) => ({ k: 'r', t: 'list', c: keys.map((k) => ({ k: `r.0:${k}`, t: 'row' })) });
  const moves = (a, b) => diff(kids(a), kids(b)).filter((o) => o[0] === 'move').length;
  const abc = [...'abcdefghij'];
  assert.equal(moves(abc, [...abc].reverse()), 9);
  assert.equal(moves(abc, [...abc.slice(1), 'a']), 1);
  assert.equal(moves(abc, ['j', ...abc.slice(0, 9)]), 1);
  assert.deepEqual(diff(kids(['a', 'b']), kids(['b', 'c'])), [['remove', 'r.0:a'], ['insert', 'r', 1, { k: 'r.0:c', t: 'row' }]]);
  const a = { k: 'r', t: 'list', c: [{ k: 'r.0', t: 'row' }] };
  const b = { k: 'r', t: 'list', c: [{ k: 'r.0', t: 'empty' }] };
  assert.deepEqual(diff(a, b), [['remove', 'r.0'], ['insert', 'r', 0, { k: 'r.0', t: 'empty' }]]);
  assert.deepEqual(diff({ k: 'r', t: 'row', p: { a: 1, b: 2 }, e: ['tap'] }, { k: 'r', t: 'row', p: { a: 1, c: 3 } }),
    [['set', 'r', { c: 3 }], ['unset', 'r', ['b']], ['events', 'r', []]]);
});

test('end to end: an app copy kept from mount/patch messages always equals the shadow (random renders and reports)', () => {
  const R = rng(99);
  for (let run = 0; run < 50; run++) {
    let app = null;
    const rt = createRuntime({ log: false, schedule: () => {}, post(m) {
      if (m.op === 'mount') app = cloneJSON(m.root);
      else if (m.op === 'patch') applyOps(app, m.ops);
    } });
    let items = [...Array(6).keys()].map((i) => ({ id: `i${i}`, v: i, on: false }));
    let draft = '';
    const view = () => html`
      <screen title=${`n=${items.length}`}>
        ${R() < 0.2 ? html`<notice tone="warn" text="!"/>` : nothing}
        <section>
          ${repeat(items, (x) => x.id, (x) => html`
            <row title=${x.id} detail=${x.v}>${x.on ? html`<actions><button>x</button></actions>` : nothing}</row>
            ${x.v % 3 === 0 ? html`<toggle value=${x.on} @change=${(e) => { x.on = e.value; }}/>` : nothing}`)}
        </section>
        <field value=${draft} @input=${(e) => { draft = e.value; }}/>
      </screen>`;
    for (let step = 0; step < 40; step++) {
      const r = R();
      if (r < 0.3) items = items.sort(() => R() - 0.5);
      else if (r < 0.45) items.push({ id: `n${step}`, v: Math.floor(R() * 9), on: false });
      else if (r < 0.6 && items.length) items.splice(Math.floor(R() * items.length), 1);
      else if (r < 0.75 && items.length) items[Math.floor(R() * items.length)].v = Math.floor(R() * 9);
      else if (r < 0.85 && app) { // the user types: the app shows it and reports it
        const v = `t${step}`;
        const f = app.c.find((c) => c.t === 'field');
        f.p = { ...(f.p || {}), value: v };
        rt.xbn.event(f.k, 'input', { value: v });
      } else if (app) { // the user flips a toggle the tile may keep
        const sec = app.c.find((c) => c.t === 'section');
        const tg = (sec.c || []).find((c) => c.t === 'toggle');
        if (tg) { tg.p = { ...(tg.p || {}), value: !tg.p?.value }; rt.xbn.event(tg.k, 'change', { value: tg.p.value }); }
      }
      if (R() < 0.1) draft = '';
      rt.render(view());
      rt.flush();
      assert.deepEqual(normalize(app), normalize(rt.tree.root), `run ${run} step ${step}`);
    }
  }
});

test('deepEqual and cloneJSON follow JSON', () => {
  assert.ok(deepEqual({ a: [1, { b: 2 }] }, { a: [1, { b: 2 }] }));
  assert.ok(!deepEqual([1], { 0: 1 }));
  assert.deepEqual(cloneJSON({ a: undefined, b: () => 1, c: NaN, d: new Date(0), e: [undefined] }), { c: null, d: '1970-01-01T00:00:00.000Z', e: [null] });
});

// ── the runtime ──────────────────────────────────────────────────────────────
test('render: first a mount, then patches; renders coalesce; an unchanged render sends nothing', () => {
  const { rt, msgs } = mk();
  const v = (n) => html`<screen><row title="n" detail=${n}/></screen>`;
  rt.render(v(1)); rt.render(v(2));
  assert.equal(rt.flush().op, 'mount');
  assert.equal(msgs.length, 1);
  assert.equal(msgs[0].root.c[0].p.detail, '2');
  rt.render(v(2));
  assert.equal(rt.flush(), null);
  rt.render(v(3));
  assert.deepEqual(rt.flush(), { op: 'patch', n: 2, ops: [['set', 'r.0', { detail: '3' }]] });
  assert.equal(rt.flush(), null, 'nothing pending');
  rt.render(html`<text>x</text>`);
  const m = rt.flush();
  assert.equal(m.op, 'mount', 'a new root type is a new mount');
  assert.equal(m.n, 3);
});

test('xbn.remount() sends the whole tree again, reported values included', () => {
  const { r, rt, msgs } = mk();
  r(html`<field value="" @input=${() => {}}/>`);
  rt.xbn.event('r', 'input', { value: 'typed' });
  assert.equal(rt.xbn.remount(), true);
  assert.deepEqual(msgs.at(-1), { op: 'mount', v: 1, n: 2, root: { k: 'r', t: 'field', p: { value: 'typed' }, e: ['input'] } });
  assert.equal(mk().rt.xbn.remount(), false, 'nothing rendered yet');
});

test('scheduling: the default is one timer per frame in node; xbn.frame() flushes at once', async () => {
  const msgs = [];
  const rt = createRuntime({ post: (m) => msgs.push(m), log: false });
  rt.render(html`<text>a</text>`); rt.render(html`<text>b</text>`);
  assert.equal(msgs.length, 0);
  await new Promise((r) => setTimeout(r, 5));
  assert.equal(msgs.length, 1);
  rt.render(html`<text>c</text>`);
  assert.equal(rt.xbn.frame(), true);
  assert.equal(msgs.length, 2);
  assert.equal(rt.xbn.frame(), false);
  await new Promise((r) => setTimeout(r, 5));
  assert.equal(msgs.length, 2, 'the timer found nothing left to do');
});

test('events arrive as {type, value, ...payload}; unknown keys and types are ignored', () => {
  const { r, rt } = mk();
  const got = [];
  r(html`<tabs @change=${(e) => got.push(e)}/><button @tap=${(e) => got.push(e)}/>`);
  assert.equal(rt.xbn.event('r.0', 'change', { key: 'b' }), true);
  assert.equal(rt.xbn.event('r.1', 'tap'), true);
  assert.equal(rt.xbn.event('r.9', 'tap'), false);
  assert.equal(rt.xbn.event('r.1', 'change', {}), false);
  assert.deepEqual(got, [{ type: 'change', value: undefined, key: 'b' }, { type: 'tap', value: undefined }]);
});

test('handler exceptions and rejections are {op:"error",kind:"uncaught"}', async () => {
  const { r, rt, errors } = mk();
  r(html`<button @tap=${() => { throw new Error('boom'); }}/><button @tap=${async () => { throw new Error('later'); }}/>`);
  rt.xbn.event('r.0', 'tap');
  rt.xbn.event('r.1', 'tap');
  await new Promise((res) => setTimeout(res, 1));
  assert.deepEqual(errors().map((e) => [e.kind, e.message, e.where]), [['uncaught', 'boom', '@tap on r.0'], ['uncaught', 'later', '@tap on r.1']]);
});

test('exceptions while rendering (repeat callbacks run in the render) are {op:"error",kind:"exception"}', () => {
  const { r, rt, errors } = mk();
  r(html`<text>ok</text>`);
  assert.equal(r(html`<list>${repeat([1], (x) => x, () => { throw new Error('bad row'); })}</list>`), null);
  assert.deepEqual(errors().map((e) => [e.kind, e.message]), [['exception', 'bad row']]);
  assert.ok(errors()[0].stack);
  assert.equal(rt.tree.root.t, 'text');
});

test('controlled field: what the field reported is not sent back; a different value is', () => {
  const { r, rt, msgs } = mk();
  let draft = '';
  const view = () => html`<field value=${draft} @input=${(e) => { draft = e.value; }}/>`;
  r(view());
  for (const v of ['h', 'he', 'hel']) { rt.xbn.event('r', 'input', { value: v }); assert.equal(r(view()), null); }
  draft = '';
  assert.deepEqual(r(view()).ops, [['set', 'r', { value: '' }]]);
  // uncontrolled: an unbound value is never touched
  const u = mk();
  u.r(html`<field @input=${() => {}}/>`);
  u.rt.xbn.event('r', 'input', { value: 'x' });
  assert.equal(u.r(html`<field @input=${() => {}}/>`), null);
  assert.equal(u.rt.tree.root.p, undefined);
  assert.equal(msgs.filter((m) => m.op === 'patch').length, 1);
});

test('controlled: toggles, sheets, tabs and disclosure report their props', () => {
  const { r, rt } = mk();
  const v = (open, sel, on) => html`<sheet open=${open} @dismiss=${() => {}}/><tabs selected=${sel} @change=${() => {}}/><toggle value=${on} @change=${() => {}}/><disclosure open=${false} @toggle=${() => {}}/>`;
  r(v(true, 'a', false));
  rt.xbn.event('r.0', 'dismiss');
  rt.xbn.event('r.1', 'change', { key: 'b' });
  rt.xbn.event('r.2', 'change', { value: true });
  rt.xbn.event('r.3', 'toggle', { open: true });
  assert.deepEqual(rt.tree.root.c.map((c) => c.p), [{ open: false }, { selected: 'b' }, { value: true }, { open: true }]);
  // the tile accepted three and refused the disclosure: only that is corrected
  assert.deepEqual(r(v(false, 'b', true)).ops, [['set', 'r.3', { open: false }]]);
});

test('controlled: a report the app made before applying our last set of that prop is stale', () => {
  const { r, rt } = mk();
  const v = (x) => html`<field value=${x} @input=${() => {}}/>`;
  r(v('abc'));
  const reset = r(v(''));
  assert.equal(reset.n, 2);
  rt.xbn.event('r', 'input', { value: 'abcd' }, 1); // typed before the app applied patch 2
  assert.equal(rt.tree.root.p.value, '', 'the app shows our reset, not the stale report');
  assert.deepEqual(r(v('abcd')).ops, [['set', 'r', { value: 'abcd' }]], 'so a tile that accepts the typing resends it');
  rt.xbn.event('r', 'input', { value: 'abcde' }, 3);
  assert.equal(r(v('abcde')), null);
});

test('xbin.native: supports, meta, copy/share/open calls resolved by xbn.resolve, state', async () => {
  const caps = fullCaps(); caps.features = ['markdown.tables'];
  const { rt, msgs } = mk({ caps, state: { scroll: 3 } });
  const nat = rt.native;
  assert.equal(nat.supports('chart'), true);
  assert.equal(nat.supports('chart', 2), false);
  assert.equal(nat.supports('markdown.tables'), true);
  assert.equal(nat.supports('chart.area'), false);
  assert.deepEqual(nat.state, { scroll: 3 });
  nat.meta({ title: 'T', badge: 3, bogus: 1 });
  const copied = nat.copy('hi');
  const shared = nat.share({ url: 'https://x.example', file: 'out/a.pdf' });
  await assert.rejects(nat.open('http://x.example'), /https URLs only/);
  const opened = nat.open('https://x.example');
  const calls = msgs.filter((m) => m.op === 'call');
  assert.deepEqual(calls, [
    { op: 'call', id: 'c1', what: 'copy', args: { text: 'hi' } },
    { op: 'call', id: 'c2', what: 'share', args: { url: 'https://x.example', file: 'out/a.pdf' } },
    { op: 'call', id: 'c3', what: 'open', args: { url: 'https://x.example' } },
  ]);
  assert.equal(rt.xbn.resolve('c1', true), true);
  assert.equal(rt.xbn.resolve('c1', true), false, 'once');
  rt.xbn.resolve('c2', null, 'cancelled');
  rt.xbn.resolve('c3', { opened: true });
  assert.equal(await copied, true);
  await assert.rejects(shared, /cancelled/);
  assert.deepEqual(await opened, { opened: true });
  nat.saveState({ scroll: 9 });
  assert.deepEqual(nat.state, { scroll: 9 });
  assert.throws(() => nat.saveState({ big: 'x'.repeat(70000) }), /64 KiB/);
  assert.deepEqual(msgs.filter((m) => m.op === 'meta' || m.op === 'state'), [{ op: 'meta', title: 'T', badge: '3' }, { op: 'state', state: { scroll: 9 } }]);
});

test('xbn.visibility drives document.visibilityState and fires visibilitychange', () => {
  const fired = [];
  const doc = { visibilityState: 'visible', dispatchEvent: (e) => fired.push(e.type) };
  const { rt } = mk({ document: doc });
  rt.xbn.visibility('hidden');
  assert.equal(doc.visibilityState, 'hidden');
  assert.equal(doc.hidden, true);
  assert.equal(rt.visibility, 'hidden');
  rt.xbn.visibility('visible');
  assert.equal(doc.visibilityState, 'visible');
  assert.deepEqual(fired, ['visibilitychange', 'visibilitychange']);
});

// ── markdown ────────────────────────────────────────────────────────────────
test('markdown: the sanitized token subset', () => {
  const src = '# Hi &amp; <b>bye</b>\n\nA **b** _c_ ~~d~~ `x<y` [ok](https://a.example) [m](mailto:a@b.c) [bad](javascript:alert(1)) [rel](/c/x) ![img](http://x/y.png) a\\*b &copy;\nnext\n\n- [ ] task\n- [x] done\n  - nested\n\n3. three\n\n> quote\n\n```js\ncode <x> &amp;\n```\n\n| a | b |\n|:--|--:|\n| 1 | 2 |\n\n<div>html</div>\n\n---\n';
  assert.deepEqual(markdownTokens(src), [
    { t: 'heading', depth: 1, c: [{ t: 'text', text: 'Hi & bye' }] },
    { t: 'paragraph', c: [
      { t: 'text', text: 'A ' }, { t: 'strong', c: [{ t: 'text', text: 'b' }] }, { t: 'text', text: ' ' },
      { t: 'em', c: [{ t: 'text', text: 'c' }] }, { t: 'text', text: ' ' }, { t: 'del', c: [{ t: 'text', text: 'd' }] },
      { t: 'text', text: ' ' }, { t: 'codespan', text: 'x<y' }, { t: 'text', text: ' ' },
      { t: 'link', href: 'https://a.example', c: [{ t: 'text', text: 'ok' }] }, { t: 'text', text: ' ' },
      { t: 'link', href: 'mailto:a@b.c', c: [{ t: 'text', text: 'm' }] }, { t: 'text', text: ' bad rel [image: img] a*b ©' },
      { t: 'br' }, { t: 'text', text: 'next' }] },
    { t: 'list', ordered: false, loose: false, items: [
      { c: [{ t: 'paragraph', c: [{ t: 'text', text: 'task' }] }], task: true, checked: false },
      { c: [{ t: 'paragraph', c: [{ t: 'text', text: 'done' }] }, { t: 'list', ordered: false, loose: false, items: [{ c: [{ t: 'paragraph', c: [{ t: 'text', text: 'nested' }] }] }] }], task: true, checked: true }] },
    { t: 'list', ordered: true, loose: false, start: 3, items: [{ c: [{ t: 'paragraph', c: [{ t: 'text', text: 'three' }] }] }] },
    { t: 'blockquote', c: [{ t: 'paragraph', c: [{ t: 'text', text: 'quote' }] }] },
    { t: 'code', text: 'code <x> &amp;', lang: 'js' },
    { t: 'table', align: ['left', 'right'], header: [[{ t: 'text', text: 'a' }], [{ t: 'text', text: 'b' }]], rows: [[[{ t: 'text', text: '1' }], [{ t: 'text', text: '2' }]]] },
    { t: 'hr' },
  ]);
  assert.deepEqual(markdownTokens('| a |\n|---|\n| 1 |', { tables: false }), [{ t: 'code', text: '| a |\n|---|\n| 1 |' }]);
});

const LONG = `# Streaming answer

Here is a paragraph with **bold**, _em_ and \`code\`, spanning
two lines, then a list:

- one
- two with [a link](https://example.com)
- three

1. first
2. second

\`\`\`go
func main() {
\tfmt.Println("hi")
}
\`\`\`

| col | val |
|-----|----:|
| a   | 1   |
| b   | 2   |

> a quote
> over lines

Setext heading
--------------

Final words.
`;

test('markdown streaming: tail re-lexes converge on the full lex; the final render is a full lex', () => {
  const R = rng(7);
  for (let run = 0; run < 30; run++) {
    const md = new MarkdownCache();
    let at = 0, same = 0, steps = 0;
    while (at < LONG.length) {
      at = Math.min(LONG.length, at + 1 + Math.floor(R() * 12));
      md.begin();
      const got = md.tokens('k', LONG.slice(0, at), true);
      md.end();
      steps++;
      if (deepEqual(got, markdownTokens(LONG.slice(0, at)))) same++;
    }
    md.begin();
    assert.deepEqual(md.tokens('k', LONG, false), markdownTokens(LONG), 'the final render is exact');
    md.end();
    assert.ok(md.stats.tail > steps / 2, 'streaming mostly re-lexes the tail');
    assert.ok(same / steps > 0.9, `intermediate steps agree with a full lex (${same}/${steps})`);
  }
});

test('markdown nodes: source becomes tokens; message markdown adds tokens; a cache per key', () => {
  const { r, rt } = mk();
  const m = r(html`<markdown source="**hi**"/><message markdown text="*x*"/><message text="*y*"/>`);
  assert.deepEqual(root(m).c.map((c) => c.p), [
    { tokens: [{ t: 'paragraph', c: [{ t: 'strong', c: [{ t: 'text', text: 'hi' }] }] }] },
    { markdown: true, text: '*x*', tokens: [{ t: 'paragraph', c: [{ t: 'em', c: [{ t: 'text', text: 'x' }] }] }] },
    { text: '*y*' },
  ]);
  const before = rt.markdownStats.full;
  r(html`<markdown source="**hi**"/><message markdown text="*x*"/><message text="*y*"/>`);
  assert.equal(rt.markdownStats.full, before, 'unchanged sources are not lexed again');
  const d = mk();
  d.r(html`<markdown source=${'a'} ?streaming=${true}/>`);
  const p = d.r(html`<markdown source=${'a b'} ?streaming=${true}/>`);
  assert.deepEqual(p.ops, [['set', 'r', { tokens: [{ t: 'paragraph', c: [{ t: 'text', text: 'a b' }] }] }]]);
  d.r(html`<markdown tokens=${[]}/>`);
  assert.deepEqual(d.diags(), ['runtime-prop']);
});

// ── transports and the default runtime ──────────────────────────────────────
test('the default runtime: webkit gets JSON strings; xbin.native is merged into the injected object', async () => {
  const posted = [];
  globalThis.webkit = { messageHandlers: { xbn: { postMessage: (s) => posted.push(s) } } };
  const injected = { caps: { v: 1, renderer: 'ios', app: '1.0', prims: { text: 1 }, features: [] }, state: { a: 1 } };
  globalThis.xbin = { self: 'apps/x', native: injected };
  try {
    const xb = await import(`${XB}?fresh=webkit`);
    xb.render(xb.html`<text>hi</text>`);
    globalThis.xbn.frame();
    assert.equal(typeof posted[0], 'string');
    assert.deepEqual(JSON.parse(posted[0]), { op: 'mount', v: 1, n: 1, root: { k: 'r', t: 'text', p: { text: 'hi' } } });
    assert.equal(typeof globalThis.xbin.native.copy, 'function');
    assert.equal(globalThis.xbin.native.caps.renderer, 'ios');
    assert.deepEqual(globalThis.xbin.native.state, { a: 1 });
    assert.equal(xb.native.supports('chart'), false);
    xb.render(xb.html`<row/>`);
    globalThis.xbn.frame();
    // the error comes before the tree it is about, so the app can fall back without drawing it
    assert.deepEqual(posted.slice(1).map((m) => JSON.parse(m)).map((m) => [m.op, m.kind ?? m.root?.t]), [['error', 'unsupported'], ['mount', 'row']]);
  } finally { delete globalThis.webkit; delete globalThis.xbin; }
});

test('the default runtime: a preview host attaches later and gets the queued messages', async () => {
  const xb = await import(`${XB}?fresh=attach`);
  xb.render(xb.html`<text>queued</text>`);
  globalThis.xbn.frame();
  const got = [];
  xb.attach((m) => got.push(m));
  assert.equal(got[0].op, 'mount');
  xb.render(xb.html`<text>live</text>`);
  globalThis.xbn.frame();
  assert.deepEqual(got[1].ops, [['set', 'r', { text: 'live' }]]);
});

test('boot() reports a module that fails to load as {op:"error",kind:"module"}', async () => {
  const xb = await import(`${XB}?fresh=boot`);
  const got = [];
  xb.attach((m) => got.push(m));
  const err = console.error;
  console.error = () => {}; // the default runtime logs to the console (the app's Xcode console)
  try { await xb.boot(new URL('./xbn/testdata/does-not-exist.js', import.meta.url).href); } finally { console.error = err; }
  assert.equal(got[0].op, 'error');
  assert.equal(got[0].kind, 'module');
});

// ── the vocabulary ───────────────────────────────────────────────────────────
test('native/spec/vocab.json is the JSON export of web/xb/vocab.js', () => {
  const file = new URL('../native/spec/vocab.json', import.meta.url);
  const want = JSON.parse(JSON.stringify(VOCAB));
  assert.deepEqual(JSON.parse(readFileSync(file, 'utf8')), want,
    'regenerate: node hack/xbn/vocab-json.mjs > native/spec/vocab.json');
});

test('the vocabulary is well formed', () => {
  const types = new Set(['string', 'number', 'bool', 'json', 'array', 'object']);
  const checkProp = (where, p) => {
    for (const t of [].concat(p.type)) assert.ok(types.has(t), `${where}: type ${t}`);
    if (p.token) assert.ok(VOCAB.tokens[p.token], `${where}: token ${p.token}`);
    if (p.of) checkProp(`${where}[]`, p.of);
    for (const [f, s] of Object.entries(p.shape || {})) checkProp(`${where}.${f}`, s);
    for (const f of Object.values(p.features || {})) assert.ok(VOCAB.features.includes(f), `${where}: feature ${f}`);
  };
  for (const [name, prim] of Object.entries(VOCAB.prims)) {
    assert.ok(Number.isInteger(prim.rev) && prim.rev >= 1, `${name}: rev`);
    for (const [pn, p] of Object.entries(prim.props)) { checkProp(`${name}.${pn}`, p); assert.ok(!p.since || p.since <= prim.rev, `${name}.${pn}: since`); }
    for (const [en, ev] of Object.entries(prim.events)) if (ev.reports) assert.ok(prim.props[ev.reports.prop], `${name} @${en} reports a prop it has`);
    for (const c of prim.children.only || []) assert.ok(VOCAB.prims[c], `${name}: child ${c}`);
    if (prim.text) assert.ok(prim.props[prim.text], `${name}: text prop`);
  }
  assert.ok(Object.keys(VOCAB.icons).length >= 60);
});

// ── cost ─────────────────────────────────────────────────────────────────────
test('a 2000-row list renders, and re-renders unchanged, quickly', () => {
  const { r } = mk();
  const rows = [...Array(2000).keys()].map((i) => ({ id: i, t: `row ${i}` }));
  const view = (xs) => html`<screen style="list"><section>${repeat(xs, (x) => x.id, (x) => html`<row title=${x.t} detail=${x.id} @tap=${() => {}}/>`)}</section></screen>`;
  let t = performance.now();
  r(view(rows));
  const mount = performance.now() - t;
  t = performance.now();
  assert.equal(r(view(rows)), null);
  const again = performance.now() - t;
  const moved = [rows[1999], ...rows.slice(0, 1999)];
  t = performance.now();
  assert.deepEqual(r(view(moved)).ops, [['move', 'r.0.0:1999', 'r.0', 0]]);
  const move = performance.now() - t;
  assert.ok(mount < 500 && again < 250 && move < 250, `mount ${mount.toFixed(0)} ms, unchanged ${again.toFixed(0)} ms, move ${move.toFixed(0)} ms`);
});
