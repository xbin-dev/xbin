#!/usr/bin/env node
// hack/native-docs.mjs — the generated parts of docs/native.md: every
// primitive's props, events and children, the controlled props, the tokens,
// the icons and the feature flags, all from the vocabulary (web/xb/vocab.js).
//
//   node hack/native-docs.mjs            print the generated blocks
//   node hack/native-docs.mjs --write    rewrite them in docs/native.md
//   node hack/native-docs.mjs --check    exit 1 when docs/native.md is stale
//
// A block sits between `<!-- generated:<name> … -->` and
// `<!-- /generated:<name> -->` in the page; everything else is prose.
// hack/native-docs.test.mjs (make js-test) fails when they disagree.
import { readFileSync, writeFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

export const DOC = new URL('../docs/native.md', import.meta.url);
const VOCAB_URL = new URL('../web/xb/vocab.js', import.meta.url);

// Group order and headings of the primitive tables.
const GROUPS = ['structure', 'content', 'control', 'chat', 'escape'];

// One line for primitives whose vocabulary entry carries no `doc`.
const DESCRIBE = {
  spacer: 'flexible space in a stack',
  divider: 'a separator line',
  icon: 'a named icon (Icons below)',
  badge: 'a small capsule label',
  chart: 'a line, bar, area or spark chart',
  button: 'a button; `confirm` asks first, `copy` copies without a round trip',
  toggle: 'an on/off switch',
  picker: 'one value out of `options`',
  message: 'one chat message (a user bubble, assistant text, a system line)',
  thinking: 'the model\'s reasoning, folded to "Thought for Ns"',
  toolcard: 'a tool call: title, state, chips; its children show when open',
  approval: 'a permission request: the options to choose from, optional feedback',
  question: 'a form built from a flat JSON Schema',
  plan: 'a checklist of plan entries',
  diff: 'changed files (+/-) and a unified patch',
  activity: 'a status line, shimmering while `live`',
  step: 'a glyph and a line of text',
};

// Reference sizes of the gap and height tokens (points; the reference
// renderer's px) — the renderer owns every other size.
const SIZES = {
  gap: { none: 0, xs: 4, s: 8, m: 12, l: 16, xl: 24, xxl: 32 },
  height: { xs: 48, s: 96, m: 160, l: 240, xl: 360 },
};

// What each token set is for.
const TOKEN_DOC = {
  tone: 'a semantic colour; the renderer picks the shade for text, icons or fills',
  noticeTone: '`tone` plus `info`, a neutral banner',
  type: 'a type role (Dynamic Type text styles on iOS, so text scales with the user\'s setting)',
  gap: 'the space between a stack\'s children',
  height: 'the height of an image, chart or canvas',
  icon: 'a curated icon name; an unknown one draws a neutral placeholder',
};

// Feature flags the runtime acts on itself (not through a prop's `features`).
const FEATURE_DOC = {
  'markdown.tables': 'markdown tables; without it the runtime sends each table as a `code` block of its source',
};

const esc = (s) => String(s).replace(/\|/g, '\\|');
const code = (s) => `\`${s}\``;
const own = (o, k) => o != null && Object.prototype.hasOwnProperty.call(o, k);

// fmtType(schema) → a short type annotation ('' for a plain string).
function fmtType(s) {
  if (!s) return '';
  const types = [].concat(s.type);
  if (s.token) return `*${s.token}*`;
  if (s.enum && types.length > 1 && s.of) return `${s.enum.join('·')}, or a list of them`;
  if (s.enum) return s.enum.join('·');
  if (types.length > 1) return types.join('|');
  switch (types[0]) {
    case 'string': return '';
    case 'bool': return 'bool';
    case 'number': return 'number';
    case 'json': return 'JSON';
    case 'object': return s.shape ? `{${Object.keys(s.shape).join(', ')}}` : 'object';
    case 'array': {
      if (!s.of) return '[…]';
      const of = s.of;
      if ([].concat(of.type)[0] === 'object' && of.shape) return `[{${Object.keys(of.shape).join(', ')}}]`;
      if ([].concat(of.type)[0] === 'array') return '[[…]]';
      const t = fmtType(of);
      return t ? `[${t}]` : '[string]';
    }
    default: return types[0];
  }
}

function fmtProps(spec) {
  const out = [];
  for (const [name, s] of Object.entries(spec.props)) {
    if (s.runtime) continue;
    const t = fmtType(s);
    let x = code(name) + (t ? ` ${t}` : '');
    if (s.since) x += ` (rev ${s.since}+)`;
    out.push(x);
  }
  return out.length ? out.join(', ') : '—';
}

function fmtEvents(spec) {
  const out = [];
  for (const [name, ev] of Object.entries(spec.events)) {
    let x = code(name);
    if (ev.payload && Object.keys(ev.payload).length) x += ` {${Object.keys(ev.payload).join(', ')}}`;
    out.push(x);
  }
  return out.length ? out.join(', ') : '—';
}

function fmtChildren(spec) {
  const c = spec.children || {};
  if (spec.text) return `text → ${code(spec.text)}`;
  if (c.none) return '—';
  let x;
  if (c.only) x = c.only.map(code).join(', ');
  else x = 'any';
  if (c.min != null && c.max != null) x = c.min === c.max ? `exactly ${c.min}${c.only ? ` (${x})` : ''}` : `${c.min}–${c.max} (${x})`;
  else if (c.min != null) x = `${x}, at least ${c.min}`;
  else if (c.max != null) x = `${x}, at most ${c.max}`;
  if (c.lazy === 'selected') x += '; only the selected one is built';
  return x;
}

function primTable(V, group) {
  const rows = ['| Primitive | What | Props | Events | Children |', '|---|---|---|---|---|'];
  const notes = [];
  for (const [name, spec] of Object.entries(V.prims)) {
    if (spec.group !== group) continue;
    if (spec.runtime) {
      notes.push(`- ${code(name)} is made by the runtime, never written${spec.doc ? `: ${spec.doc}` : ''}.`);
      continue;
    }
    const what = spec.doc || DESCRIBE[name] || '';
    const title = code(name) + (spec.rev > 1 ? ` (rev ${spec.rev})` : '');
    rows.push(`| ${title} | ${esc(what)} | ${esc(fmtProps(spec))} | ${esc(fmtEvents(spec))} | ${esc(fmtChildren(spec))} |`);
    for (const [p, s] of Object.entries(spec.props)) {
      if (s.runtime) notes.push(`- ${code(name)} ${code(p)} is set by the runtime, never by a tile.`);
      else if (s.doc) notes.push(`- ${code(name)} ${code(p)}: ${s.doc}.`);
      if (s.features) {
        for (const [v, f] of Object.entries(s.features)) notes.push(`- ${code(name)} ${code(`${p}="${v}"`)} needs the app feature ${code(f)}.`);
      }
    }
  }
  return rows.join('\n') + (notes.length ? `\n\n${notes.join('\n')}` : '');
}

function controlled(V) {
  const rows = ['| Primitive | Event → controlled prop |', '|---|---|'];
  for (const [name, spec] of Object.entries(V.prims)) {
    const by = new Map(); // prop → [event text]
    for (const [ev, e] of Object.entries(spec.events)) {
      if (!e.reports) continue;
      const r = e.reports;
      const key = own(r, 'value') ? `${code(r.prop)} = ${JSON.stringify(r.value)}` : code(r.prop);
      const list = by.get(key) || [];
      list.push(own(r, 'value') ? code(ev) : `${code(ev)} {${r.from || r.prop}}`);
      by.set(key, list);
    }
    if (!by.size) continue;
    const cells = [...by].map(([prop, evs]) => `${evs.join(', ')} → ${prop}`);
    rows.push(`| ${code(name)} | ${esc(cells.join('; '))} |`);
  }
  return rows.join('\n');
}

// Where each token set is used: prim.prop, including object fields.
function tokenUses(V) {
  const uses = {};
  const add = (set, where) => { (uses[set] ||= []).push(where); };
  const walk = (s, where) => {
    if (!s || typeof s !== 'object') return;
    if (s.token) add(s.token, where);
    if (s.of) walk(s.of, `${where}[]`);
    if (s.shape) for (const [f, fs] of Object.entries(s.shape)) walk(fs, `${where}.${f}`);
  };
  for (const [name, spec] of Object.entries(V.prims)) {
    for (const [p, s] of Object.entries(spec.props)) walk(s, `${name} ${p}`);
  }
  return uses;
}

function tokens(V) {
  const uses = tokenUses(V);
  const rows = ['| Token set | Values | What | Used by |', '|---|---|---|---|'];
  for (const [set, values] of Object.entries(V.tokens)) {
    const vals = set === 'icon' ? `${values.length} names (Icons below)`
      : values.map((v) => (SIZES[set] && own(SIZES[set], v) ? `${code(v)} ${SIZES[set][v]}` : code(v))).join(', ');
    const used = (uses[set] || []).map(code).join(', ') || '—';
    rows.push(`| ${code(set)} | ${esc(vals)} | ${esc(TOKEN_DOC[set] || '')} | ${esc(used)} |`);
  }
  return rows.join('\n');
}

function icons(V) {
  const PER = 3;
  const head = [];
  for (let i = 0; i < PER; i++) head.push('Name', 'SF Symbol');
  const rows = [`| ${head.join(' | ')} |`, `|${'---|'.repeat(PER * 2)}`];
  const all = Object.entries(V.icons);
  for (let i = 0; i < all.length; i += PER) {
    const cells = [];
    for (let j = 0; j < PER; j++) {
      const e = all[i + j];
      cells.push(e ? code(e[0]) : '', e ? code(e[1]) : '');
    }
    rows.push(`| ${cells.join(' | ')} |`);
  }
  return rows.join('\n');
}

function features(V) {
  const needs = {};
  for (const [name, spec] of Object.entries(V.prims)) {
    for (const [p, s] of Object.entries(spec.props)) {
      for (const [v, f] of Object.entries(s.features || {})) (needs[f] ||= []).push(`${code(`<${name} ${p}="${v}">`)}`);
    }
  }
  const rows = ['| Feature | What needs it |', '|---|---|'];
  for (const f of V.features) {
    const what = [...(needs[f] || []), ...(FEATURE_DOC[f] ? [FEATURE_DOC[f]] : [])].join('; ') || '—';
    rows.push(`| ${code(f)} | ${esc(what)} |`);
  }
  return rows.join('\n');
}

// blocks(VOCAB) → {name: markdown}
export function blocks(V) {
  const out = {};
  for (const g of GROUPS) out[`prims-${g}`] = primTable(V, g);
  const seen = new Set(Object.values(V.prims).map((p) => p.group));
  for (const g of seen) if (!GROUPS.includes(g)) throw new Error(`native-docs: primitive group ${g} has no table — add it to GROUPS`);
  out.controlled = controlled(V);
  out.tokens = tokens(V);
  out.icons = icons(V);
  out.features = features(V);
  return out;
}

const OPEN = /<!-- generated:([a-z-]+)[^>]*-->\n/g;
const close = (name) => `<!-- /generated:${name} -->`;

// extract(doc) → Map(name → body)
export function extract(doc) {
  const found = new Map();
  for (const m of doc.matchAll(OPEN)) {
    const start = m.index + m[0].length;
    const end = doc.indexOf(close(m[1]), start);
    if (end < 0) throw new Error(`docs/native.md: ${m[0].trim()} has no ${close(m[1])}`);
    found.set(m[1], doc.slice(start, end).replace(/\n$/, ''));
  }
  return found;
}

// splice(doc, blocks) → {text, missing, unknown}
export function splice(doc, b) {
  const found = extract(doc);
  const missing = Object.keys(b).filter((n) => !found.has(n));
  const unknown = [...found.keys()].filter((n) => !own(b, n));
  const text = doc.replace(/(<!-- generated:([a-z-]+)[^>]*-->\n)[\s\S]*?(<!-- \/generated:\2 -->)/g,
    (all, open, name, end) => (own(b, name) ? `${open}${b[name]}\n${end}` : all));
  return { text, missing, unknown };
}

export async function loadVocab() {
  return (await import(VOCAB_URL.href)).VOCAB;
}

if (process.argv[1] && fileURLToPath(import.meta.url) === process.argv[1]) {
  const mode = process.argv[2] || '';
  const b = blocks(await loadVocab());
  if (mode === '') {
    for (const [name, body] of Object.entries(b)) process.stdout.write(`<!-- generated:${name} -->\n${body}\n${close(name)}\n\n`);
  } else if (mode === '--write' || mode === '--check') {
    const doc = readFileSync(DOC, 'utf8');
    const { text, missing, unknown } = splice(doc, b);
    const problems = [...missing.map((n) => `no <!-- generated:${n} --> block`), ...unknown.map((n) => `unknown block generated:${n}`)];
    if (mode === '--write') {
      if (text !== doc) writeFileSync(DOC, text);
      process.stdout.write(`docs/native.md: ${text === doc ? 'up to date' : 'rewritten'}\n`);
    } else if (text !== doc) problems.push('stale — run: node hack/native-docs.mjs --write');
    for (const p of problems) process.stderr.write(`docs/native.md: ${p}\n`);
    process.exit(problems.length ? 1 : 0);
  } else {
    process.stderr.write('usage: node hack/native-docs.mjs [--write | --check]\n');
    process.exit(2);
  }
}
