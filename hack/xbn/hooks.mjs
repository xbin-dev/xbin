// hack/xbn/hooks.mjs — module resolution for running native tile code in node:
// `/vendor/<name>` resolves the way xbind serves it (web/<name>, else
// web/vendor/<name>; internal/server/static.go handleVendor), so a tile's
// `import … from '/vendor/xb-native.js'` and the runtime's own absolute
// imports load from this checkout. Everything else resolves as usual.
//
//   import { installHooks } from './hack/xbn/hooks.mjs';
//   installHooks();                        // before importing web/xb-native.js
//   const xb = await import('./web/xb-native.js');
import { existsSync, statSync } from 'node:fs';
import * as nodeModule from 'node:module';

export const WEB = new URL('../../web/', import.meta.url);

export function resolveVendor(spec) {
  if (typeof spec !== 'string' || !spec.startsWith('/vendor/')) return null;
  const name = spec.slice('/vendor/'.length).split(/[?#]/)[0];
  if (!name || name.includes('..')) return null;
  for (const p of [name, `vendor/${name}`]) {
    const u = new URL(p, WEB);
    try { if (existsSync(u) && statSync(u).isFile()) return u.href; } catch { /* next */ }
  }
  return null;
}

// The synchronous hook (node ≥ 22.15 / 23.5); hooks-async.mjs is the same for
// older node's module.register.
export function resolve(spec, context, next) {
  const url = resolveVendor(spec);
  if (url) return { url, format: 'module', shortCircuit: true };
  return next(spec, context);
}

let installed = false;
export function installHooks() {
  if (installed) return;
  installed = true;
  if (typeof nodeModule.registerHooks === 'function') nodeModule.registerHooks({ resolve });
  else nodeModule.register(new URL('./hooks-async.mjs', import.meta.url));
}
