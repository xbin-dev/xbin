/**
 * xb/rt-template.js — the lit-shaped template layer of the native runtime:
 * `html`, `repeat`, `nothing`, and the parser that turns a template's static
 * strings into a slot structure once per call site (strings identity).
 *
 * The markup is the vocabulary (web/xb/vocab.js), not HTML: tags are
 * primitives, self-closing tags are allowed, attribute names keep their
 * case, and `&amp; &lt; &gt; &quot; &#39; &apos; &nbsp; &#N; &#xH;` decode in
 * static text and static attribute values. Bindings:
 *   name="static"   a string (a bare `name` is true)
 *   name=${v}       the raw JS value (`.name=${v}` is an alias)
 *   name="a ${v} b" string interpolation
 *   ?name=${v}      a boolean (!!v; `nothing` is false)
 *   @event=${fn}    an event handler
 * Wire contract: native/spec/tree.md.
 */

// The same symbol lit uses, so lit's `nothing` means nothing here too.
export const nothing = Symbol.for('lit-nothing');
const TPL = '_$xbnTemplate$';
const REP = '_$xbnRepeat$';

const sites = new WeakMap(); // strings -> call-site location (first html`` call)
const parsed = new WeakMap(); // strings -> parsed template

export function html(strings, ...values) {
  if (!sites.has(strings)) sites.set(strings, callSite());
  return { [TPL]: 1, strings, values };
}

// repeat(items, keyFn, tplFn) — keyed children; repeat(items, tplFn) keys by index.
export function repeat(items, keyFn, tplFn) {
  if (tplFn === undefined) { tplFn = keyFn; keyFn = null; }
  return { [REP]: 1, items, keyFn, tplFn };
}

export const isTemplate = (v) => v != null && typeof v === 'object' && v[TPL] === 1;
export const isRepeat = (v) => v != null && typeof v === 'object' && v[REP] === 1;
export const isLitTemplate = (v) => v != null && typeof v === 'object' && '_$litType$' in v;

// The tile source location of a template's call site, from a stack captured
// once per call site: the first frame that is not this runtime.
function callSite() {
  const stack = String(new Error().stack || '').split('\n');
  for (const line of stack) {
    const m = line.match(/((?:https?|file|xbin-ws):\/\/[^\s()]+|\/[^\s()@]+):(\d+):(\d+)/);
    if (!m) continue;
    if (/\/xb-native\.js$|\/xb\/rt-[\w-]+\.js$/.test(m[1])) continue;
    const file = m[1].replace(/^[a-z-]+:\/\/[^/]*/, '');
    return `${file}:${m[2]}:${m[3]}`;
  }
  return '';
}

export const whereOf = (strings) => sites.get(strings) || '';

// ── entities and whitespace ─────────────────────────────────────────────────
const NAMED = { amp: '&', lt: '<', gt: '>', quot: '"', apos: "'", nbsp: ' ' };
export function decodeEntities(s) {
  if (s.indexOf('&') < 0) return s;
  return s.replace(/&(#x[0-9a-fA-F]+|#[0-9]+|[a-zA-Z]+);/g, (all, e) => {
    if (e[0] === '#') {
      const n = e[1] === 'x' || e[1] === 'X' ? parseInt(e.slice(2), 16) : parseInt(e.slice(1), 10);
      return Number.isFinite(n) && n > 0 && n <= 0x10ffff ? String.fromCodePoint(n) : all;
    }
    return Object.prototype.hasOwnProperty.call(NAMED, e) ? NAMED[e] : all;
  });
}
const collapse = (s) => s.replace(/[ \t\n\r\f]+/g, ' ');
const blank = (s) => /^[ \t\n\r\f]*$/.test(s);

// ── the parser ─────────────────────────────────────────────────────────────
// Template: {roots: Slot[], where}
// Slot: {k:'hole', i} | {k:'text', s} | {k:'el', tag, attrs, kids|null, text|null, at}
//   kids: Slot[] for containers; text: (string|{i})[] parts for text-content
//   primitives (textProps has the tag). at: '<tag>' path for diagnostics.
// Attr: {name, kind:'static'|'value'|'bool'|'event', value?, parts?}
export class TemplateError extends Error {}

export function parse(strings, textProps) {
  let t = parsed.get(strings);
  if (!t) { t = parseStrings(strings, textProps, whereOf(strings)); parsed.set(strings, t); }
  return t;
}

const NAME_CH = /[A-Za-z0-9_-]/;
const WS = /[ \t\n\r\f]/;

function parseStrings(strings, textProps, where) {
  const fail = (msg) => { throw new TemplateError(`${msg}${where ? ` (template at ${where})` : ''}`); };
  const root = { tag: null, raw: [] };
  const stack = [root];
  let state = 'text';
  let text = '';
  let tag = null; // element being opened: {tag, attrs}
  let name = '';
  let parts = null; // attribute value parts
  let quote = '';
  let close = '';

  const top = () => stack[stack.length - 1];
  const flushText = () => { if (text) { top().raw.push({ k: 'text', s: text }); text = ''; } };
  const pushAttr = (a) => {
    if (a.name === '') fail('empty attribute name');
    tag.attrs.push(a);
  };
  const endAttrValue = () => {
    const n = name;
    const pre = n[0];
    parts = parts.filter((p) => p !== ''); // name="${v}" is name=${v}, as in lit
    if (pre === '@' || pre === '?') {
      if (parts.length !== 1 || typeof parts[0] !== 'object') fail(`${n} needs exactly one binding: ${n}=\${…}`);
      pushAttr({ name: n.slice(1), kind: pre === '@' ? 'event' : 'bool', i: parts[0].i });
    } else {
      const nm = pre === '.' ? n.slice(1) : n;
      if (parts.every((p) => typeof p === 'string')) pushAttr({ name: nm, kind: 'static', value: decodeEntities(parts.join('')) });
      else pushAttr({ name: nm, kind: 'value', parts: parts.map((p) => (typeof p === 'string' ? decodeEntities(p) : p)) });
    }
    parts = null; name = '';
  };
  const bareAttr = () => {
    if (name[0] === '@' || name[0] === '?') fail(`${name} needs a binding: ${name}=\${…}`);
    pushAttr({ name: name[0] === '.' ? name.slice(1) : name, kind: 'static', value: true });
    name = '';
  };
  const openTag = (selfClosing) => {
    const el = { k: 'el', tag: tag.tag, attrs: tag.attrs, raw: [], at: `<${tag.tag}>` };
    top().raw.push(el);
    if (!selfClosing) stack.push(el);
    else finish(el);
    tag = null;
    state = 'text';
  };

  for (let si = 0; si < strings.length; si++) {
    const s = strings[si];
    for (let j = 0; j < s.length; j++) {
      const ch = s[j];
      switch (state) {
        case 'text':
          if (ch === '<') {
            if (s[j + 1] === '/') { flushText(); state = 'close'; close = ''; j++; }
            else if (s.startsWith('!--', j + 1)) { flushText(); state = 'comment'; j += 3; }
            else if (j + 1 < s.length && /[A-Za-z]/.test(s[j + 1])) { flushText(); state = 'tagname'; tag = { tag: '', attrs: [] }; }
            else if (j + 1 >= s.length) fail('dynamic tag names are not supported (<${…}>)');
            else text += ch;
          } else text += ch;
          break;
        case 'comment':
          if (ch === '-' && s.startsWith('-->', j)) { state = 'text'; j += 2; }
          break;
        case 'tagname':
          if (NAME_CH.test(ch)) tag.tag += ch;
          else if (WS.test(ch)) state = 'intag';
          else if (ch === '>') openTag(false);
          else if (ch === '/') state = 'selfclose';
          else fail(`unexpected ${JSON.stringify(ch)} in tag name <${tag.tag}`);
          break;
        case 'intag':
          if (WS.test(ch)) break;
          if (ch === '>') openTag(false);
          else if (ch === '/') state = 'selfclose';
          else if (ch === '"' || ch === "'" || ch === '=') fail(`unexpected ${ch} in <${tag.tag}>`);
          else { name = ch; state = 'attrname'; }
          break;
        case 'attrname':
          if (ch === '=') { parts = []; state = 'attreq'; }
          else if (WS.test(ch)) state = 'afterattr';
          else if (ch === '>') { bareAttr(); openTag(false); }
          else if (ch === '/' && s[j + 1] === '>') { bareAttr(); state = 'selfclose'; }
          else name += ch;
          break;
        case 'afterattr':
          if (WS.test(ch)) break;
          if (ch === '=') { parts = []; state = 'attreq'; break; }
          bareAttr();
          state = 'intag';
          j--; // reprocess in 'intag'
          break;
        case 'attreq':
          if (WS.test(ch)) break;
          if (ch === '"' || ch === "'") { quote = ch; parts = ['']; state = 'quoted'; }
          else { parts = [ch]; state = 'unquoted'; }
          break;
        case 'quoted':
          if (ch === quote) { endAttrValue(); state = 'intag'; }
          else if (typeof parts[parts.length - 1] === 'string') parts[parts.length - 1] += ch;
          else parts.push(ch);
          break;
        case 'unquoted':
          if (WS.test(ch)) { endAttrValue(); state = 'intag'; }
          else if (ch === '>') { endAttrValue(); openTag(false); }
          else if (ch === '/' && s[j + 1] === '>') { endAttrValue(); state = 'selfclose'; }
          else if (typeof parts[parts.length - 1] === 'string') parts[parts.length - 1] += ch;
          else parts.push(ch);
          break;
        case 'selfclose':
          if (ch === '>') openTag(true);
          else if (!WS.test(ch)) fail(`expected > after / in <${tag.tag}>`);
          break;
        case 'close':
          if (ch === '>') {
            const want = close.trim();
            const el = top();
            if (el === root) fail(`</${want}> closes nothing`);
            if (want !== el.tag) fail(`</${want}> closes <${el.tag}>`);
            stack.pop();
            finish(el);
            state = 'text';
          } else close += ch;
          break;
      }
    }
    if (si === strings.length - 1) break;
    // a hole between strings[si] and strings[si+1]
    const hole = { i: si };
    switch (state) {
      case 'text': flushText(); top().raw.push({ k: 'hole', i: si }); break;
      case 'comment': break;
      case 'attreq': parts = [hole]; state = 'unquoted'; break;
      case 'quoted': parts.push(hole); break;
      case 'unquoted': parts.push(hole); break;
      case 'tagname': case 'close': fail('dynamic tag names are not supported');
      // falls through (fail throws)
      default: fail(`a binding must be an attribute value (name=\${…})${tag ? ` in <${tag.tag}>` : ''}`);
    }
  }
  if (state !== 'text') fail(`unterminated ${state === 'comment' ? 'comment' : `tag <${tag?.tag ?? close}`}`);
  flushText();
  if (stack.length > 1) fail(`<${top().tag}> is not closed`);
  return { roots: containerKids(root.raw), where };

  // finish(el): an element's raw children become kids (a container) or
  // text parts (a text-content primitive: text, button, badge, code).
  function finish(el) {
    const raw = el.raw;
    delete el.raw;
    const prop = textProps[el.tag];
    if (prop) {
      el.textProp = prop;
      el.kids = null;
      el.badKids = raw.some((r) => r.k === 'el');
      const ps = raw.filter((r) => r.k !== 'el').map((r) => (r.k === 'text' ? r.s : { i: r.i }));
      el.text = ps.length ? textParts(ps, el.tag === 'code') : null;
    } else {
      el.text = null;
      el.kids = containerKids(raw);
    }
  }
}

// Container children: whitespace-only text dropped, other text collapsed.
function containerKids(raw) {
  const out = [];
  for (const r of raw) {
    if (r.k !== 'text') { out.push(r); continue; }
    if (blank(r.s)) continue;
    out.push({ k: 'text', s: decodeEntities(collapse(r.s).trim()) });
  }
  return out;
}

// Text-content parts: static runs collapse like HTML and the ends trim; in
// `code` static text is verbatim except a leading line break and trailing
// whitespace. Hole values are always verbatim.
function textParts(ps, verbatim) {
  const out = ps.map((p) => (typeof p === 'string' && !verbatim ? collapse(p) : p));
  const first = out[0], last = out.length - 1;
  if (typeof first === 'string') out[0] = verbatim ? first.replace(/^\r?\n/, '') : first.replace(/^ /, '');
  if (typeof out[last] === 'string') out[last] = verbatim ? out[last].replace(/[ \t\n\r\f]+$/, '') : out[last].replace(/ $/, '');
  return out.map((p) => (typeof p === 'string' ? decodeEntities(p) : p));
}
