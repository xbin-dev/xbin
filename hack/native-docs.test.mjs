// Keeps docs/native.md (the builder reference for a tile's native.js) true to
// the code: its generated tables equal what hack/native-docs.mjs makes from
// web/xb/vocab.js; every template in its examples (and in the workspace
// AGENTS.md "Native app UI" section) uses only primitives, props and events
// the vocabulary has; the quick start is examples/counter-go/native.js and
// renders the tree printed under it; complete examples render cleanly; and
// every xbin.native member and xb-native.js export is documented.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync, mkdtempSync, writeFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { blocks, extract, splice, loadVocab, DOC } from './native-docs.mjs';
import { parse } from '../web/xb/rt-template.js';
import { runNative } from './xbn/node.mjs';
import { installHooks } from './xbn/hooks.mjs';

const read = (rel) => readFileSync(new URL(`../${rel}`, import.meta.url), 'utf8');
const doc = readFileSync(DOC, 'utf8');
const VOCAB = await loadVocab();
const PRIMS = VOCAB.prims;
const TEXT_PROPS = Object.fromEntries(Object.entries(PRIMS).filter(([, p]) => p.text).map(([n, p]) => [n, p.text]));
const own = (o, k) => o != null && Object.prototype.hasOwnProperty.call(o, k);

// fenced(md, lang) → [{code, line}] for every ```lang block.
function fenced(md, lang) {
  const out = [];
  const re = new RegExp(`^\`\`\`${lang}\\n([\\s\\S]*?)^\`\`\`$`, 'gm');
  for (const m of md.matchAll(re)) out.push({ code: m[1], line: md.slice(0, m.index).split('\n').length });
  return out;
}

// The workspace AGENTS.md's native section: from its heading to the next one.
function agentsSection() {
  const md = read('workspace-template/AGENTS.md');
  const start = md.indexOf('\n## Native app UI');
  assert.ok(start >= 0, 'workspace-template/AGENTS.md has no "## Native app UI" section');
  const end = md.indexOf('\n## ', start + 1);
  return md.slice(start, end < 0 ? undefined : end);
}

// ── template extraction: every html`…` in a JS source, as its static strings ──
function skipString(src, i) {
  const q = src[i];
  for (i++; i < src.length; i++) {
    if (src[i] === '\\') i++;
    else if (src[i] === q) return i + 1;
  }
  throw new Error('unterminated string');
}
function skipExpr(src, i) { // i: after "${"; returns the index after its "}"
  let depth = 0;
  while (i < src.length) {
    const ch = src[i];
    if (ch === '`') { i = scanTemplate(src, i + 1).end; continue; }
    if (ch === '"' || ch === "'") { i = skipString(src, i); continue; }
    if (ch === '{') depth++;
    else if (ch === '}') { if (depth === 0) return i + 1; depth--; }
    i++;
  }
  throw new Error('unterminated ${…}');
}
function scanTemplate(src, i) { // i: after the opening backtick
  const strings = [];
  let cur = '';
  while (i < src.length) {
    const ch = src[i];
    if (ch === '\\') { cur += src[i + 1]; i += 2; continue; }
    if (ch === '`') { strings.push(cur); return { strings, end: i + 1 }; }
    if (ch === '$' && src[i + 1] === '{') { strings.push(cur); cur = ''; i = skipExpr(src, i + 2); continue; }
    cur += ch;
    i++;
  }
  throw new Error('unterminated template');
}
function templates(src) {
  const out = [];
  for (const m of src.matchAll(/\bhtml`/g)) out.push(scanTemplate(src, m.index + m[0].length).strings);
  return out;
}

// problems(strings) → what the template uses that the vocabulary lacks.
function problems(strings) {
  const bad = [];
  const T = parse(strings, TEXT_PROPS);
  const walk = (slots, parent) => {
    for (const s of slots) {
      if (s.k !== 'el') continue;
      const spec = own(PRIMS, s.tag) ? PRIMS[s.tag] : null;
      if (!spec || spec.runtime) { bad.push(`<${s.tag}> is not a primitive`); continue; }
      if (parent?.children?.only && !parent.children.only.includes(s.tag)) bad.push(`<${s.tag}> may not be a child of <${parent.name}>`);
      if (parent?.children?.none) bad.push(`<${parent.name}> takes no children`);
      for (const a of s.attrs) {
        if (a.kind === 'event') {
          if (!own(spec.events, a.name)) bad.push(`<${s.tag}> has no event ${a.name}`);
          continue;
        }
        if (a.name === 'key' && !own(spec.props, 'key')) continue;
        const schema = own(spec.props, a.name) ? spec.props[a.name] : null;
        if (!schema || schema.runtime) { bad.push(`<${s.tag}> has no prop ${a.name}`); continue; }
        const types = [].concat(schema.type);
        if (a.kind === 'bool' && !types.includes('bool')) bad.push(`<${s.tag}> ?${a.name} is not a bool prop`);
        if (a.kind === 'static' && typeof a.value === 'string' && types.includes('string')) {
          if (schema.enum && !schema.enum.includes(a.value)) bad.push(`<${s.tag}> ${a.name}="${a.value}" is not one of ${schema.enum.join(', ')}`);
          if (schema.token && !VOCAB.tokens[schema.token].includes(a.value)) bad.push(`<${s.tag}> ${a.name}="${a.value}" is not a ${schema.token} token`);
        }
        if (a.kind === 'static' && a.value === true && !types.includes('bool') && !types.includes('json')) bad.push(`<${s.tag}> bare ${a.name} is not a bool prop`);
      }
      if (s.badKids) bad.push(`<${s.tag}> takes text only`);
      if (s.kids) walk(s.kids, { ...spec, name: s.tag });
    }
  };
  walk(T.roots, null);
  return bad;
}

// ── tests ─────────────────────────────────────────────────────────────────
test('docs/native.md generated tables match web/xb/vocab.js', () => {
  const b = blocks(VOCAB);
  const { text, missing, unknown } = splice(doc, b);
  assert.deepEqual(missing, [], 'docs/native.md lacks generated blocks');
  assert.deepEqual(unknown, [], 'docs/native.md has generated blocks the generator does not make');
  assert.ok(text === doc, 'docs/native.md is stale against web/xb/vocab.js — run: node hack/native-docs.mjs --write');
  const found = extract(doc);
  for (const name of Object.keys(PRIMS)) {
    const inTables = [...found.values()].some((body) => body.includes(`\`${name}\``));
    assert.ok(inTables, `primitive ${name} is in no generated table`);
  }
});

test('templates in docs/native.md and the AGENTS.md native section use only the vocabulary', () => {
  const sources = [
    ...fenced(doc, 'js').map((b) => ({ ...b, file: 'docs/native.md' })),
    ...fenced(agentsSection(), 'js').map((b) => ({ ...b, file: 'workspace-template/AGENTS.md (Native app UI)' })),
  ];
  let n = 0;
  const bad = [];
  for (const { code, line, file } of sources) {
    for (const strings of templates(code)) {
      n++;
      for (const p of problems(strings)) bad.push(`${file} (block at line ${line}): ${p}`);
    }
  }
  assert.ok(n >= 8, `found only ${n} templates — did the extraction break?`);
  assert.deepEqual(bad, []);
});

// Comment-only lines and trailing `// …` comments differ; the code must not.
const codeLines = (src) => src.split('\n')
  .map((l) => l.replace(/\s+\/\/ .*$/, '').trimEnd())
  .filter((l) => l && !/^\s*\/\//.test(l));

test('the quick start is examples/counter-go/native.js and renders the tree shown under it', async () => {
  const start = doc.indexOf('## Quick start');
  const section = doc.slice(start, doc.indexOf('\n## ', start + 1));
  const [js] = fenced(section, 'js');
  const [tree] = fenced(section, 'json');
  assert.ok(js && tree, 'the quick start needs its js block and the json tree after it');
  assert.deepEqual(codeLines(js.code), codeLines(read('examples/counter-go/native.js')),
    'the quick start differs from examples/counter-go/native.js');
  const dir = mkdtempSync(join(tmpdir(), 'native-docs-'));
  try {
    const entry = join(dir, 'native.js');
    writeFileSync(entry, js.code);
    const r = await runNative({ entry, data: { routes: { 'GET /api/apps/tile/count': { json: { count: 42 } } } } });
    assert.deepEqual(r.errors, []);
    assert.deepEqual(r.tree, JSON.parse(tree.code));
  } finally { rmSync(dir, { recursive: true, force: true }); }
});

test('complete examples render without errors or warnings', async () => {
  const complete = [...fenced(doc, 'js'), ...fenced(agentsSection(), 'js')]
    .filter((b) => /^import .* from '\/vendor\/xb-native\.js';/.test(b.code) && /\brender\(/.test(b.code) && !/\.\.\./.test(b.code));
  assert.ok(complete.length >= 1, 'no complete example found');
  const dir = mkdtempSync(join(tmpdir(), 'native-docs-'));
  try {
    for (const [i, b] of complete.entries()) {
      const entry = join(dir, `ex${i}.js`);
      writeFileSync(entry, b.code);
      const r = await runNative({ entry, data: {} });
      const noisy = r.diagnostics.filter((d) => d.level !== 'info').map((d) => `${d.code}: ${d.message}`);
      assert.deepEqual([...r.errors.map((e) => `${e.kind}: ${e.message}`), ...noisy], [], `example at line ${b.line}`);
      assert.ok(r.tree?.root, `example at line ${b.line} rendered no tree`);
    }
  } finally { rmSync(dir, { recursive: true, force: true }); }
});

test('every xbin.native member and xb-native.js export is documented', async () => {
  installHooks();
  const xb = await import('../web/xb-native.js');
  for (const k of Object.keys(xb.native)) assert.ok(doc.includes(`xbin.native.${k}`), `docs/native.md does not document xbin.native.${k}`);
  for (const k of Object.keys(xb)) assert.ok(new RegExp(`\`${k}\\b`).test(doc), `docs/native.md does not mention the export ${k}`);
});
