// hack/xbn/hooks-async.mjs — hooks.mjs's resolve for node's module.register()
// (node before 22.15, which lacks module.registerHooks).
import { resolveVendor } from './hooks.mjs';

export async function resolve(spec, context, next) {
  const url = resolveVendor(spec);
  if (url) return { url, format: 'module', shortCircuit: true };
  return next(spec, context);
}
