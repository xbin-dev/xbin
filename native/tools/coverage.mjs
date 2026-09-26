// native/tools/coverage.mjs — what the fixtures exercise, against what the
// vocabulary (web/xb/vocab.js) defines. The fixtures are the contract every
// renderer is tested against, so each of these must appear in at least one
// expected.json:
//
//   prim  <name>              every primitive (fragment included)
//   event <prim>@<type>       every event, listened to by some node (in its `e`)
//   prop  <prim>.<path>       every prop a tile sets, and every field of
//                             object/array props (options[].icon, confirm.destructive)
//   value <prim>.<path>=<v>   every enum value, per prop; every tone/noticeTone/
//                             type/gap token, per prop; `true` of every bool prop
//   token icon=<v>, height=<v>  every icon and height token, anywhere
//   child <parent>><child>    every child a restricted parent allows (list>notice)
//   md    <what>              every markdown token shape (native/spec/tree.md §11)
//
// Props the runtime sets (`tokens`) or replaces (markdown `source`) are not
// required; their markdown is.

// Token sets whose every value must appear on every prop that takes them;
// the others (icon, height) are covered once anywhere.
const PER_PROP_TOKENS = new Set(['tone', 'noticeTone', 'type', 'gap']);

const MD_REQUIRED = [
  'block heading', 'heading depth=1', 'heading depth=2', 'heading depth=3', 'heading depth=4', 'heading depth=5', 'heading depth=6',
  'block paragraph', 'block list', 'list ordered', 'list bullet', 'list loose', 'list tight', 'list start>1', 'list task checked', 'list task unchecked',
  'block code', 'code lang', 'code no-lang', 'block blockquote', 'block table',
  'table align=left', 'table align=center', 'table align=right', 'table align=none', 'block hr',
  'inline text', 'inline strong', 'inline em', 'inline del', 'inline codespan', 'inline link', 'inline br',
];

// Props a tile sets that never reach the wire: the runtime replaces them
// (markdown `source` → `tokens`, covered by the md items).
const NOT_ON_WIRE = new Set(['markdown.source']);

const types = (sch) => [].concat(sch?.type ?? []);

function requireProp(req, vocab, prim, path, sch) {
  req.add(`prop ${prim}.${path}`);
  if (sch.enum) for (const v of sch.enum) req.add(`value ${prim}.${path}=${v}`);
  if (sch.token) {
    if (PER_PROP_TOKENS.has(sch.token)) for (const v of vocab.tokens[sch.token]) req.add(`value ${prim}.${path}=${v}`);
    else for (const v of vocab.tokens[sch.token]) req.add(`token ${sch.token}=${v}`);
  }
  const t = types(sch);
  if (t.length === 1 && t[0] === 'bool') req.add(`value ${prim}.${path}=true`);
  if (sch.shape) for (const [f, fs] of Object.entries(sch.shape)) requireProp(req, vocab, prim, `${path}.${f}`, fs);
  if (sch.of?.shape) for (const [f, fs] of Object.entries(sch.of.shape)) requireProp(req, vocab, prim, `${path}[].${f}`, fs);
}

// required(vocab) → the Set of coverage items the vocabulary defines.
export function required(vocab) {
  const req = new Set();
  for (const [name, P] of Object.entries(vocab.prims)) {
    req.add(`prim ${name}`);
    for (const ev of Object.keys(P.events || {})) req.add(`event ${name}@${ev}`);
    for (const [prop, sch] of Object.entries(P.props || {})) {
      if (!sch.runtime && !NOT_ON_WIRE.has(`${name}.${prop}`)) requireProp(req, vocab, name, prop, sch);
    }
    for (const c of P.children?.only || []) req.add(`child ${name}>${c}`);
  }
  for (const m of MD_REQUIRED) req.add(`md ${m}`);
  return req;
}

function seeProp(seen, vocab, prim, path, sch, v) {
  if (v === undefined || v === null || !sch) return;
  seen.add(`prop ${prim}.${path}`);
  if (Array.isArray(v)) {
    for (const x of v) {
      if (sch.of?.shape && x && typeof x === 'object') {
        for (const [f, fs] of Object.entries(sch.of.shape)) seeProp(seen, vocab, prim, `${path}[].${f}`, fs, x[f]);
      } else if (typeof x === 'string' && (sch.of?.enum || sch.enum)) seen.add(`value ${prim}.${path}=${x}`);
    }
    return;
  }
  if (typeof v === 'string') {
    if (sch.enum) seen.add(`value ${prim}.${path}=${v}`);
    if (sch.token) seen.add(PER_PROP_TOKENS.has(sch.token) ? `value ${prim}.${path}=${v}` : `token ${sch.token}=${v}`);
  }
  if (v === true && types(sch).length === 1) seen.add(`value ${prim}.${path}=true`);
  if (sch.shape && typeof v === 'object') for (const [f, fs] of Object.entries(sch.shape)) seeProp(seen, vocab, prim, `${path}.${f}`, fs, v[f]);
}

function seeInline(seen, list) {
  for (const t of list || []) {
    seen.add(`md inline ${t.t}`);
    if (t.c) seeInline(seen, t.c);
  }
}
function seeBlocks(seen, list) {
  for (const b of list || []) {
    seen.add(`md block ${b.t}`);
    switch (b.t) {
      case 'heading': seen.add(`md heading depth=${b.depth}`); seeInline(seen, b.c); break;
      case 'paragraph': seeInline(seen, b.c); break;
      case 'code': seen.add(b.lang ? 'md code lang' : 'md code no-lang'); break;
      case 'blockquote': seeBlocks(seen, b.c); break;
      case 'list':
        seen.add(b.ordered ? 'md list ordered' : 'md list bullet');
        seen.add(b.loose ? 'md list loose' : 'md list tight');
        if (b.ordered && b.start > 1) seen.add('md list start>1');
        for (const it of b.items || []) {
          if (it.task) seen.add(`md list task ${it.checked ? 'checked' : 'unchecked'}`);
          seeBlocks(seen, it.c);
        }
        break;
      case 'table':
        for (const a of b.align || []) seen.add(`md table align=${a ?? 'none'}`);
        for (const h of b.header || []) seeInline(seen, h);
        for (const r of b.rows || []) for (const cell of r) seeInline(seen, cell);
        break;
      default: break;
    }
  }
}

// seen(vocab, tree, into?) → the coverage items one tree exercises.
export function seen(vocab, tree, into = new Set()) {
  const walk = (n, parent) => {
    const P = vocab.prims[n.t];
    into.add(`prim ${n.t}`);
    if (parent && vocab.prims[parent.t]?.children?.only) into.add(`child ${parent.t}>${n.t}`);
    if (P) {
      for (const ev of n.e || []) if (P.events?.[ev]) into.add(`event ${n.t}@${ev}`);
      for (const [prop, v] of Object.entries(n.p || {})) {
        if (prop === 'tokens') seeBlocks(into, v);
        else seeProp(into, vocab, n.t, prop, P.props?.[prop], v);
      }
    }
    for (const c of n.c || []) walk(c, n);
  };
  if (tree?.root) walk(tree.root, null);
  return into;
}

// report(vocab, trees: {name: tree}) → {missing: [...], total, covered, by: {item: [fixture…]}}
export function report(vocab, trees) {
  const req = required(vocab);
  const by = {};
  for (const [name, tree] of Object.entries(trees)) {
    for (const item of seen(vocab, tree)) (by[item] ||= []).push(name);
  }
  const missing = [...req].filter((x) => !by[x]).sort();
  return { missing, total: req.size, covered: req.size - missing.length, by };
}
