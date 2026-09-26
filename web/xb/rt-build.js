/**
 * xb/rt-build.js — renders a template value into the wire tree
 * (native/spec/tree.md): node keys, props from bindings and text content,
 * events, validation against the vocabulary (web/xb/vocab.js) and the app's
 * caps, markdown tokens, and the lazily materialized tabs.
 *
 * Keys: a template child at slot i of parent K is `K.i`; a keyed child (from
 * repeat or key=) at slot i is `K.i:<key>`; a hole holding a single-root
 * template gives that root the hole's key, a multi-root one gives its roots
 * `<hole>.<i>`; array items are `<hole>.<i>`; strings become `text` nodes.
 */
import { VOCAB, TOKENS } from '/vendor/xb/vocab.js';
import { parse, isTemplate, isRepeat, isLitTemplate, nothing } from '/vendor/xb/rt-template.js';
import { cloneJSON } from '/vendor/xb/rt-diff.js';

const PRIMS = VOCAB.prims;
export const TEXT_PROPS = Object.fromEntries(Object.entries(PRIMS).filter(([, p]) => p.text).map(([n, p]) => [n, p.text]));
const own = (o, k) => Object.prototype.hasOwnProperty.call(o, k);

const describe = (v) => (v === null ? 'null' : Array.isArray(v) ? 'an array' : typeof v === 'object' ? 'an object' : `${typeof v} ${JSON.stringify(v)}`);

// checkValue(v, schema, warn) → {v} (possibly coerced) | {bad: reason}
export function checkValue(v, schema, warn = () => {}) {
  const types = [].concat(schema.type);
  const scalar = (s) => {
    if (schema.enum && !schema.enum.includes(s)) return { bad: `${JSON.stringify(s)} is not one of ${schema.enum.join(' · ')}` };
    if (schema.token) {
      const set = TOKENS[schema.token] || [];
      if (!set.includes(s)) {
        if (schema.token === 'icon') { warn(`unknown icon ${JSON.stringify(s)} (a placeholder is drawn)`); return { v: s }; }
        return { bad: `${JSON.stringify(s)} is not a ${schema.token} token (${set.join(' · ')})` };
      }
    }
    return { v: s };
  };
  for (const ty of types) {
    switch (ty) {
      case 'string': if (typeof v === 'string') return scalar(v); break;
      case 'number': if (typeof v === 'number') return Number.isFinite(v) ? { v } : { bad: `${v} is not a finite number` }; break;
      case 'bool': if (typeof v === 'boolean') return { v }; break;
      case 'json': return { v: cloneJSON(v) };
      case 'array':
        if (Array.isArray(v)) {
          if (!schema.of) return { v: cloneJSON(v) };
          const out = [];
          for (let i = 0; i < v.length; i++) {
            const r = checkValue(v[i], schema.of, warn);
            if (r.bad) return { bad: `[${i}]: ${r.bad}` };
            out.push(r.v);
          }
          return { v: out };
        }
        break;
      case 'object':
        if (v && typeof v === 'object' && !Array.isArray(v)) {
          if (!schema.shape) return { v: cloneJSON(v) };
          const out = {};
          for (const f of Object.keys(v)) {
            if (v[f] === undefined || v[f] === null) continue;
            if (!own(schema.shape, f)) { warn(`unknown field ${f}`); const c = cloneJSON(v[f]); if (c !== undefined) out[f] = c; continue; }
            const r = checkValue(v[f], schema.shape[f], warn);
            if (r.bad) return { bad: `.${f}: ${r.bad}` };
            out[f] = r.v;
          }
          return { v: out };
        }
        break;
    }
  }
  if (typeof v === 'number' && Number.isFinite(v) && types.includes('string')) return scalar(String(v));
  return { bad: `wants ${types.join(' or ')}, got ${describe(v)}` };
}

// A static attribute is a string (or true when bare); convert it to the
// prop's type before checking.
function fromStatic(v, schema) {
  const types = [].concat(schema.type);
  if (v === true) return types.includes('bool') || types.includes('json') ? true : '';
  if (types.includes('string')) return v;
  if (types.includes('bool')) return v === '' || v === 'true' ? true : v === 'false' ? false : v;
  if (types.includes('number') && v.trim() !== '' && Number.isFinite(Number(v))) return Number(v);
  if (types.some((t) => t === 'json' || t === 'array' || t === 'object')) { try { return JSON.parse(v); } catch { return v; } }
  return v;
}

const str = (v) => (v == null || v === nothing || v === false ? '' : typeof v === 'string' ? v : String(v));

// buildTree(value, env) → {root, handlers: Map<k, {type: fn}>}
// env: {caps, diag(level, code, message, where), unsupported(message, where), md: MarkdownCache}
export function buildTree(value, env) {
  const b = new Builder(env);
  const out = [];
  b.child(value, 'r', out, null);
  const root = out.length === 1 ? out[0] : { k: 'r', t: 'fragment', c: out };
  if (root.t === 'fragment' && out.length !== 1) b.uniq('r');
  return { root, handlers: b.handlers };
}

class Builder {
  constructor(env) {
    this.env = env;
    this.caps = env.caps;
    this.features = new Set(env.caps.features || []);
    this.keys = new Set();
    this.handlers = new Map();
  }

  uniq(k) {
    if (!this.keys.has(k)) { this.keys.add(k); return k; }
    let n = 2;
    while (this.keys.has(`${k}~${n}`)) n++;
    this.env.diag('warn', 'duplicate-key', `duplicate key ${JSON.stringify(k)} — renamed ${k}~${n} (keys must be unique among siblings)`, '');
    this.keys.add(`${k}~${n}`);
    return `${k}~${n}`;
  }

  child(value, P, out, cx) {
    if (value == null || value === nothing || value === false || value === true || value === '') return;
    if (isTemplate(value)) return this.template(value, P, out, cx);
    if (isRepeat(value)) return this.repeat(value, P, out, cx);
    if (typeof value === 'string' || typeof value === 'number' || typeof value === 'bigint') {
      out.push({ k: this.uniq(P), t: 'text', p: { text: String(value) } });
      return;
    }
    if (typeof value === 'object' && (Array.isArray(value) || typeof value[Symbol.iterator] === 'function')) {
      let i = 0;
      for (const v of value) this.child(v, `${P}.${i++}`, out, cx);
      return;
    }
    if (isLitTemplate(value)) this.env.diag('error', 'lit-template', "a lit template (html from 'lit') cannot render natively — use html from /vendor/xb-native.js", P);
    else this.env.diag('warn', 'bad-child', `cannot render ${describe(value)} as a child`, P);
  }

  repeat(r, P, out, cx) {
    let i = 0;
    for (const item of r.items ?? []) {
      const key = r.keyFn ? r.keyFn(item, i) : i;
      this.child(r.tplFn(item, i), `${P}:${key}`, out, cx);
      i++;
    }
  }

  template(t, P, out, cx) {
    const T = parse(t.strings, TEXT_PROPS);
    if (T.roots.length === 1) this.slot(T.roots[0], t.values, P, out, cx, T);
    else T.roots.forEach((s, i) => this.slot(s, t.values, `${P}.${i}`, out, cx, T));
  }

  slot(s, values, P, out, cx, T) {
    if (s.k === 'hole') this.child(values[s.i], P, out, cx);
    else if (s.k === 'text') out.push({ k: this.uniq(P), t: 'text', p: { text: s.s } });
    else this.element(s, values, P, out, cx, T);
  }

  element(s, values, P, out, cx, T) {
    const env = this.env;
    const tag = s.tag;
    const where = T.where ? `${T.where} ${s.at}` : s.at;
    const spec = own(PRIMS, tag) ? PRIMS[tag] : null;
    const capsRev = this.caps.prims ? this.caps.prims[tag] : undefined;
    if (!spec) {
      if (capsRev == null) {
        env.diag('error', 'unknown-tag', `<${tag}> is not a primitive of the vocabulary`, where);
        env.unsupported(`<${tag}> is not a primitive`, where);
      } else env.diag('info', 'unvalidated', `<${tag}> is unknown to this runtime but the app supports it — not validated`, where);
    } else if (capsRev == null) env.unsupported(`the app does not support <${tag}>`, where);

    const props = {};
    const events = [];
    let handlers = null;
    let key;
    const setProp = (name, v, isStatic) => {
      if (!spec) { const c = cloneJSON(v); if (c !== undefined) props[name] = c; return; }
      const schema = own(spec.props, name) ? spec.props[name] : null;
      if (!schema) { env.diag('warn', 'unknown-prop', `<${tag}> has no prop ${name} — dropped`, where); return; }
      if (schema.runtime) { env.diag('warn', 'runtime-prop', `<${tag}> ${name} is set by the runtime — dropped`, where); return; }
      const r = checkValue(isStatic ? fromStatic(v, schema) : v, schema,
        (msg) => env.diag('warn', 'bad-value', `<${tag}> ${name}: ${msg}`, where));
      if (r.bad) { env.diag('warn', schema.token ? 'bad-token' : 'bad-type', `<${tag}> ${name}: ${r.bad} — dropped`, where); return; }
      if (schema.since && capsRev != null && schema.since > capsRev) env.unsupported(`the app does not support <${tag}> ${name} (rev ${schema.since})`, where);
      if (schema.features && typeof r.v === 'string' && schema.features[r.v] && !this.features.has(schema.features[r.v])) {
        env.unsupported(`the app does not support <${tag}> ${name}="${r.v}" (${schema.features[r.v]})`, where);
      }
      props[name] = r.v;
    };

    for (const a of s.attrs) {
      if (a.kind === 'event') {
        const fn = values[a.i];
        if (typeof fn !== 'function') {
          if (fn != null && fn !== nothing && fn !== false) env.diag('warn', 'bad-handler', `<${tag}> @${a.name} wants a function, got ${describe(fn)}`, where);
          continue;
        }
        if (spec && !own(spec.events, a.name)) { env.diag('warn', 'unknown-event', `<${tag}> has no event ${a.name} — dropped`, where); continue; }
        if (!events.includes(a.name)) events.push(a.name);
        (handlers ||= {})[a.name] = fn;
        continue;
      }
      let v;
      if (a.kind === 'static') v = a.value;
      else if (a.kind === 'bool') { const x = values[a.i]; v = x === nothing ? false : !!x; }
      else if (a.parts.length === 1 && typeof a.parts[0] === 'object') {
        v = values[a.parts[0].i];
        if (v === nothing || v === undefined || v === null) continue;
      } else v = a.parts.map((p) => (typeof p === 'string' ? p : str(values[p.i]))).join('');
      if (a.name === 'key' && !(spec && own(spec.props, 'key'))) { key = v; continue; }
      setProp(a.name, v, a.kind === 'static');
    }
    if (s.textProp) {
      if (s.badKids) env.diag('warn', 'bad-children', `<${tag}> takes text only — elements inside it are dropped`, where);
      if (s.text) {
        const v = s.text.map((p) => {
          if (typeof p === 'string') return p;
          const x = values[p.i];
          if (isTemplate(x) || isRepeat(x)) { env.diag('warn', 'bad-children', `<${tag}> takes text only — a template inside it is dropped`, where); return ''; }
          return str(x);
        }).join('');
        setProp(s.textProp, v, false);
      }
    }

    const k = this.uniq(key !== undefined && key !== null && key !== nothing ? `${P}:${str(key)}` : P);
    const node = { k, t: tag };

    if (tag === 'markdown' || (tag === 'message' && props.markdown === true && typeof props.text === 'string')) {
      const src = tag === 'markdown' ? props.source ?? '' : props.text;
      delete props.source;
      props.tokens = env.md.tokens(k, src, props.streaming === true, { tables: this.features.has('markdown.tables') });
    }
    if (Object.keys(props).length) node.p = props;
    if (events.length) node.e = events;
    if (handlers) this.handlers.set(k, handlers);

    if (s.kids && s.kids.length) {
      const rule = spec ? spec.children : { any: true };
      if (rule.none) env.diag('warn', 'bad-children', `<${tag}> takes no children — dropped`, where);
      else if (tag === 'tab' && cx && cx.selected !== undefined && props.key !== cx.selected) node.c = []; // not materialized
      else {
        const kcx = rule.lazy === 'selected' && typeof props.selected === 'string' ? { selected: props.selected } : null;
        const c = [];
        s.kids.forEach((ks, i) => this.slot(ks, values, `${k}.${i}`, c, kcx, T));
        node.c = c;
        if (rule.only) {
          for (const ch of c) {
            if (!rule.only.includes(ch.t)) env.diag('warn', 'child-rule', `<${tag}> takes ${rule.only.join(', ')} — not <${ch.t}>`, where);
          }
        }
        if ((rule.min != null && c.length < rule.min) || (rule.max != null && c.length > rule.max)) {
          env.diag('warn', 'child-rule', `<${tag}> takes ${rule.min === rule.max ? `exactly ${rule.min}` : `${rule.min ?? 0}…${rule.max ?? '∞'}`} children, got ${c.length}`, where);
        }
      }
    }
    out.push(node);
  }
}
