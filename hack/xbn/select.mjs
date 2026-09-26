// hack/xbn/select.mjs — find a node in a rendered tree by a small CSS-like
// selector, so scripted interactions (native fixtures, runNative steps) can
// name "the Save button" instead of a key that shifts when the template does.
//
//   selector  := compound (" " compound)*        descendant, like CSS
//   compound  := tag? attr*                       tag: a primitive or "*"
//   attr      := "[" name "]"                     the prop is present
//              | "[" name "=" value "]"           equal (strings exactly; numbers,
//                                                 bools and JSON by their text)
//              | "[" name "*=" value "]"          the prop's text contains value
//   value     := bare text up to "]" | "…" | '…'
//   name      := a prop, or "k" (the node key)
//
//   selectKey(root, 'sheet[title="New event"] field[label=Title]') → "r.1.0"
//
// A selector must match exactly one node: none or several is an error that
// lists what it did match, so a fixture fails loudly instead of tapping the
// wrong thing.

const own = (o, k) => o != null && Object.prototype.hasOwnProperty.call(o, k);

export function parseSelector(src) {
  const s = String(src);
  const parts = [];
  let i = 0;
  const fail = (why) => { throw new Error(`select ${JSON.stringify(s)}: ${why} at ${i}`); };
  while (i < s.length) {
    while (s[i] === ' ') i++;
    if (i >= s.length) break;
    const c = { tag: null, attrs: [] };
    const tag = /^[A-Za-z*][\w-]*/.exec(s.slice(i));
    if (tag) { c.tag = tag[0] === '*' ? null : tag[0]; i += tag[0].length; }
    while (s[i] === '[') {
      i++;
      const name = /^@?[\w-]+/.exec(s.slice(i));
      if (!name) fail('a prop name');
      i += name[0].length;
      const a = { name: name[0], op: 'has', value: null };
      if (s[i] === '=' || (s[i] === '*' && s[i + 1] === '=')) {
        a.op = s[i] === '=' ? 'eq' : 'has-text';
        i += a.op === 'eq' ? 1 : 2;
        if (s[i] === '"' || s[i] === "'") {
          const q = s[i];
          const end = s.indexOf(q, i + 1);
          if (end < 0) fail('a closing quote');
          a.value = s.slice(i + 1, end);
          i = end + 1;
        } else {
          const end = s.indexOf(']', i);
          if (end < 0) fail('"]"');
          a.value = s.slice(i, end).trim();
          i = end;
        }
      }
      if (s[i] !== ']') fail('"]"');
      i++;
      c.attrs.push(a);
    }
    if (!c.tag && !c.attrs.length && s[i - 1] !== '*') fail('a tag or [attribute]');
    if (i < s.length && s[i] !== ' ') fail('a space between compounds');
    parts.push(c);
  }
  if (!parts.length) fail('an empty selector');
  return parts;
}

const text = (v) => (typeof v === 'string' ? v : JSON.stringify(v));

function matches(node, c) {
  if (c.tag && node.t !== c.tag) return false;
  for (const a of c.attrs) {
    if (a.name.startsWith('@')) { if (!(node.e || []).includes(a.name.slice(1))) return false; continue; }
    const has = a.name === 'k' ? true : own(node.p, a.name);
    if (!has) return false;
    const v = a.name === 'k' ? node.k : node.p[a.name];
    if (a.op === 'eq' && text(v) !== a.value) return false;
    if (a.op === 'has-text' && !text(v).includes(a.value)) return false;
  }
  return true;
}

// selectAll(root, sel) → the matching nodes, in tree order.
export function selectAll(root, sel) {
  const parts = typeof sel === 'string' ? parseSelector(sel) : sel;
  const out = [];
  const walk = (node, stack) => {
    if (matches(node, parts[parts.length - 1])) {
      // the earlier compounds must match ancestors, in order (greedy from the nearest)
      let j = parts.length - 2;
      for (let a = stack.length - 1; a >= 0 && j >= 0; a--) if (matches(stack[a], parts[j])) j--;
      if (j < 0) out.push(node);
    }
    stack.push(node);
    for (const ch of node.c || []) walk(ch, stack);
    stack.pop();
  };
  if (root) walk(root, []);
  return out;
}

const label = (n) => `${n.k} (${n.t}${n.p?.title != null ? ` "${n.p.title}"` : n.p?.label != null ? ` "${n.p.label}"` : n.p?.text != null ? ` "${String(n.p.text).slice(0, 30)}"` : ''})`;

// selectKey(root, sel) → the key of the one node sel matches; throws otherwise.
export function selectKey(root, sel) {
  const found = selectAll(root, sel);
  if (found.length === 1) return found[0].k;
  if (!found.length) throw new Error(`select ${JSON.stringify(sel)} matches no node`);
  throw new Error(`select ${JSON.stringify(sel)} is ambiguous: ${found.slice(0, 6).map(label).join(', ')}${found.length > 6 ? ', …' : ''}`);
}

export function findNode(root, k) {
  if (!root) return null;
  if (root.k === k) return root;
  for (const c of root.c || []) { const f = findNode(c, k); if (f) return f; }
  return null;
}
