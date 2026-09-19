// kit.mjs — what a test page needs to run the real agent.js, which imports
// the frontend kit (/vendor/bx-kit.js): the kit itself, served from the xbin
// checkout this template lives in (or BX_KIT=<path> in an instance); the
// <meta name="xbin-sandbox"> marker, so the kit's api() goes through the
// page's stubbed xbin.fetch the way it does in a real tile frame; and a shim
// that gives the stubbed responses the text() the kit reads — the stubs speak
// json() and may be swapped mid-test, so the shim sits on the property.
import { readFileSync, existsSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

const here = dirname(fileURLToPath(import.meta.url));
const candidates = [process.env.BX_KIT, join(here, '..', '..', '..', 'web', 'bx-kit.js')].filter(Boolean);
export const KIT_PATH = candidates.find((p) => existsSync(p));
if (!KIT_PATH) {
  console.log('SKIP: /vendor/bx-kit.js not found next to the template (set BX_KIT=<path to bx-kit.js>)');
  process.exit(0);
}

// serveKit(page | context): answer the kit's URL with the real kit.
export const serveKit = (target) => target.route('**/vendor/bx-kit.js', (r) =>
  r.fulfill({ contentType: 'text/javascript', body: readFileSync(KIT_PATH, 'utf8') }));

// A read hands back a wrapper bound to the stub installed AT THAT MOMENT, so
// a test that keeps `const orig = xbin.fetch` and later swaps in a function
// that delegates to orig gets the old stub, not itself.
const SHIM = '<script>(() => { let raw = window.xbin.fetch;' +
  ' const bind = (f) => async (u, o) => { const r = await f(u, o);' +
  ' if (r && !r.text) r.text = async () => (r.json ? JSON.stringify(await r.json()) : \'\'); return r; };' +
  ' Object.defineProperty(window.xbin, \'fetch\', { get: () => bind(raw), set: (f) => { raw = f; }, configurable: true }); })();</script>';

// tileHtml(html): index.html as a sandboxed tile frame sees itself.
export const tileHtml = (html) => html
  .replace('<head>', '<head><meta name="xbin-sandbox">')
  .replace('<script type="module"', `${SHIM}<script type="module"`);
