// model/ext.js — seams: named hooks a view calls at fixed points of its
// drawing, which feature modules fill without editing the view's hot files
// (D147 §8 U1). A view makes one registry (web-ext.js for
// the web, native/ext.js for the native view); a feature module registers
// its hooks when it is imported (harness-web.js, native/harness-all.js list
// them):
//
//   const ext = makeExt({ block: 'first', end: 'all', paint: 'each' });
//   ext.register({ block: (b) => (b.k === 'tool' && mine(b) ? tpl(b) : null) });
//   ext.block(b, ui) ?? builtIn(b)
//
// How a seam combines its hooks (in the order they registered):
//   first  the first answer that is not null/undefined/false wins; none → null
//   all    every such answer, as a list (a template draws a list); none → null
//   each   every hook is called; the seam answers null
// A hook that throws is logged and counts as no answer — one feature module
// never breaks the view that calls it. Pure: no DOM, no lit.
export function makeExt(kinds) {
  const hooks = {};
  for (const k of Object.keys(kinds)) hooks[k] = [];
  const call = (k, fn, args) => {
    try { return fn(...args); } catch (e) { console.error(`ext.${k}:`, e); return null; }
  };
  const answered = (x) => x != null && x !== false;
  const ext = {
    // register adds a module's hooks ({seam: fn, …}); an unknown seam is a
    // mistake, said at once. Returns the undo.
    register(mod) {
      const added = [];
      for (const [k, fn] of Object.entries(mod || {})) {
        if (!hooks[k]) throw new Error(`no seam "${k}" (${Object.keys(hooks).join(', ')})`);
        if (typeof fn !== 'function') throw new Error(`ext.${k}: a function`);
        hooks[k].push(fn);
        added.push([k, fn]);
      }
      return () => { for (const [k, fn] of added) hooks[k] = hooks[k].filter((f) => f !== fn); };
    },
    // has says whether any module hooks into seam k.
    has(k) { return !!(hooks[k] && hooks[k].length); },
  };
  for (const [k, how] of Object.entries(kinds)) {
    if (!['first', 'all', 'each'].includes(how)) throw new Error(`ext.${k}: first, all or each`);
    ext[k] = (...args) => {
      const fns = hooks[k];
      if (!fns.length) return null;
      if (how === 'each') { for (const fn of fns) call(k, fn, args); return null; }
      if (how === 'first') {
        for (const fn of fns) { const x = call(k, fn, args); if (answered(x)) return x; }
        return null;
      }
      const out = [];
      for (const fn of fns) { const x = call(k, fn, args); if (answered(x)) out.push(x); }
      return out.length ? out : null;
    };
  }
  return ext;
}
