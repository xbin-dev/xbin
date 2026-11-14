// hack/xbn/older-xbind.mjs — a runNative `setup` (hack/xbn/node.mjs) that
// plays an xbind older than a tile's native.js needs: the /vendor/ URLs
// matching data.missing (a RegExp's source) are not served — importing one
// fails, as a 404 does in the app's runtime.
//
//   runNative({ entry, data: { setup: '…/hack/xbn/older-xbind.mjs', missing: '^/vendor/chat/' } })
import { registerHooks } from 'node:module';

export default function olderXbind({ data }) {
  const missing = new RegExp(String(data.missing));
  // registered after the runner's own hook, so asked first
  registerHooks({
    resolve(spec, context, next) {
      if (typeof spec === 'string' && spec.startsWith('/vendor/') && missing.test(spec)) {
        throw Object.assign(new Error(`Cannot find module '${spec}': this xbind does not serve it (404)`), { code: 'ERR_MODULE_NOT_FOUND' });
      }
      return next(spec, context);
    },
  });
}
