// repl_bootstrap.js — runs once per goja VM, BEFORE any model code, and
// returns the inspect() the Go side keeps a handle to (repl.go newVM).
//
// Two jobs:
//   1. a prelude that caps the string builders which can allocate a gigabyte
//      in a single native call — those never reach the watchdog's sample
//      points, so they are the one runaway the sampler cannot catch;
//   2. a value formatter for tool results.
//
// The formatter lives in JS rather than Go because it must handle getters,
// Symbol.toStringTag and Proxies with real JS semantics, and because running
// inside the VM keeps it interruptible. It captures every intrinsic it needs
// into closure variables first, so later model code that overwrites
// Object.keys or Array.isArray cannot break (or hijack) our formatting.
(function () {
  'use strict';

  var O = Object, A = Array, S = String, N = Number, J = JSON;
  var gopd = O.getOwnPropertyDescriptor;
  var keys = O.keys;
  var isArr = A.isArray;
  var objToStr = O.prototype.toString;
  var strSlice = S.prototype.slice;
  var stringify = J.stringify;
  var mapGet = Map.prototype.forEach, setEach = Set.prototype.forEach;
  var push = A.prototype.push, join = A.prototype.join;

  // --- prelude: single-allocation bombs -----------------------------------
  // 'x'.repeat(1e9) is one sb.Grow(1e9) inside goja — there is no bytecode
  // loop for the watchdog to interrupt, and Go's OOM is fatal rather than
  // recoverable. Capping the obvious builders removes every accidental case;
  // a determined one (Array(1e6).join(x)) still gets through, which is why
  // repl_log carries the 'running' state as a crash-loop guard.
  var CAP = 4 * 1024 * 1024;
  function capped(name, fn, size) {
    return function () {
      if (size.apply(this, arguments) > CAP) {
        throw new RangeError(name + ': result would exceed the sandbox 4 MiB string cap');
      }
      return fn.apply(this, arguments);
    };
  }
  var rep = S.prototype.repeat;
  if (rep) S.prototype.repeat = capped('repeat', rep, function (n) { return this.length * N(n); });
  var ps = S.prototype.padStart;
  if (ps) S.prototype.padStart = capped('padStart', ps, function (n) { return N(n); });
  var pe = S.prototype.padEnd;
  if (pe) S.prototype.padEnd = capped('padEnd', pe, function (n) { return N(n); });

  // GC-timing observables would make a replayed session diverge from the
  // original in ways nothing else can: drop them.
  try { delete globalThis.WeakRef; } catch (e) {}
  try { delete globalThis.FinalizationRegistry; } catch (e) {}

  // --- formatter ----------------------------------------------------------
  var MAXDEPTH = 4, MAXARR = 100, MAXKEYS = 50, MAXSTR = 200;

  function quote(s) {
    var t = s.length > MAXSTR ? strSlice.call(s, 0, MAXSTR) : s;
    var out;
    try { out = stringify(t); } catch (e) { out = '"' + t + '"'; }
    return s.length > MAXSTR ? out + '…(+' + (s.length - MAXSTR) + ' chars)' : out;
  }

  function fnName(v) {
    var n = '';
    try { n = v.name; } catch (e) {}
    try {
      if (/^class[\s{]/.test(S(v))) return n ? '[class ' + n + ']' : '[class (anonymous)]';
    } catch (e) {}
    return n ? '[Function: ' + n + ']' : '[Function (anonymous)]';
  }

  function tagOf(v) {
    var t = objToStr.call(v);            // "[object Map]" → "Map"
    return strSlice.call(t, 8, t.length - 1);
  }

  function inspect(v, depth, seen) {
    var t = typeof v;
    if (v === null) return 'null';
    if (t === 'undefined') return 'undefined';
    if (t === 'number') return O.is(v, -0) ? '-0' : S(v);
    if (t === 'boolean') return S(v);
    if (t === 'bigint') return S(v) + 'n';
    if (t === 'symbol') return S(v);
    if (t === 'string') return quote(v);
    if (t === 'function') return fnName(v);

    if (seen.indexOf(v) !== -1) return '[Circular *1]';
    if (depth > MAXDEPTH) return '[…]';
    seen = seen.concat([v]);

    var tag = tagOf(v), parts = [], i, n;

    if (tag === 'Error' || v instanceof Error) {
      var msg = '';
      try { msg = (v.name || 'Error') + (v.message ? ': ' + v.message : ''); } catch (e) { msg = 'Error'; }
      var st = '';
      try { st = v.stack ? S(v.stack) : ''; } catch (e) {}
      if (st) {
        var lines = st.split('\n'), frames = [];
        for (i = 0; i < lines.length && frames.length < 6; i++) {
          var ln = lines[i];
          // Drop our own bootstrap frames — they are noise to the model.
          if (/^\s*at /.test(ln) && ln.indexOf('bootstrap.js') === -1) push.call(frames, ln);
        }
        if (frames.length) return msg + '\n' + join.call(frames, '\n');
      }
      return msg;
    }
    if (tag === 'Date') { try { return v.toISOString(); } catch (e) { return 'Invalid Date'; } }
    if (tag === 'RegExp') return S(v);
    if (tag === 'Promise') return 'Promise { … }';   // Go reads the real state

    if (isArr(v)) {
      n = v.length;
      for (i = 0; i < n && i < MAXARR; i++) {
        push.call(parts, i in v ? inspect(v[i], depth + 1, seen) : '<empty>');
      }
      if (n > MAXARR) push.call(parts, '… ' + (n - MAXARR) + ' more items');
      return '[ ' + join.call(parts, ', ') + ' ]';
    }
    if (tag === 'Map') {
      var mi = 0;
      mapGet.call(v, function (val, k) {
        if (mi < MAXKEYS) push.call(parts, inspect(k, depth + 1, seen) + ' => ' + inspect(val, depth + 1, seen));
        mi++;
      });
      if (mi > MAXKEYS) push.call(parts, '… ' + (mi - MAXKEYS) + ' more');
      return 'Map(' + mi + ') { ' + join.call(parts, ', ') + ' }';
    }
    if (tag === 'Set') {
      var si = 0;
      setEach.call(v, function (val) {
        if (si < MAXKEYS) push.call(parts, inspect(val, depth + 1, seen));
        si++;
      });
      if (si > MAXKEYS) push.call(parts, '… ' + (si - MAXKEYS) + ' more');
      return 'Set(' + si + ') { ' + join.call(parts, ', ') + ' }';
    }

    var ks;
    try { ks = keys(v); } catch (e) { return '[' + tag + ']'; }
    for (i = 0; i < ks.length && i < MAXKEYS; i++) {
      var k = ks[i], d;
      try { d = gopd(v, k); } catch (e) { d = null; }
      // A getter can hang, throw, or mutate — report it, never invoke it.
      var shown = d && !('value' in d) ? (d.get ? '[Getter]' : '[Setter]')
                                       : inspect(v[k], depth + 1, seen);
      push.call(parts, (/^[A-Za-z_$][A-Za-z0-9_$]*$/.test(k) ? k : quote(k)) + ': ' + shown);
    }
    if (ks.length > MAXKEYS) push.call(parts, '… ' + (ks.length - MAXKEYS) + ' more keys');
    var prefix = (tag !== 'Object' && tag !== 'Arguments') ? tag + ' ' : '';
    if (!parts.length) return prefix + '{}';
    return prefix + '{ ' + join.call(parts, ', ') + ' }';
  }

  return function (v) {
    try { return inspect(v, 0, []); } catch (e) {
      try { return '[uninspectable: ' + e + ']'; } catch (e2) { return '[uninspectable]'; }
    }
  };
})()
