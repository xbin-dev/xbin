// kit.mjs — what a test page needs to run the real agent.js, which imports
// the frontend kit (/vendor/bx-kit.js): the kit itself, served from the xbin
// checkout this template lives in (or BX_KIT=<path> in an instance); the
// <meta name="xbin-sandbox"> marker, so the kit's api() goes through the
// page's stubbed xbin.fetch the way it does in a real tile frame; and a shim
// that gives the stubbed responses the text() the kit reads — the stubs speak
// json() and may be swapped mid-test, so the shim sits on the property.
import { readFileSync, existsSync } from 'node:fs';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { dirname, join } from 'node:path';
import * as nodeModule from 'node:module';

const here = dirname(fileURLToPath(import.meta.url));
const candidates = [process.env.BX_KIT, join(here, '..', '..', '..', 'web', 'bx-kit.js')].filter(Boolean);
export const KIT_PATH = candidates.find((p) => existsSync(p));
if (!KIT_PATH) {
  console.log('SKIP: /vendor/bx-kit.js not found next to the template (set BX_KIT=<path to bx-kit.js>)');
  process.exit(0);
}

// The drawn glyphs (<bx-icon>, D184): bx-icons.js beside the kit (in an
// instance: BX_ICONS=<path>); without it the page keeps its words, as on an
// xbind from before them.
export const ICONS_PATH = [process.env.BX_ICONS, join(dirname(KIT_PATH), 'bx-icons.js')].filter(Boolean).find((p) => existsSync(p));

// The chat kit (/vendor/chat/*.js: the markdown renderer chat-md.js stands
// on, and the revision boot.js asks for before it loads agent.js): xbind's
// web/chat beside the kit (in an instance: BX_CHAT=<that directory>).
export const CHAT_DIR = [process.env.BX_CHAT, join(dirname(KIT_PATH), 'chat')].filter(Boolean).find((p) => existsSync(join(p, 'rev.js')));
if (!CHAT_DIR) {
  console.log('SKIP: /vendor/chat/ not found beside the kit (set BX_CHAT=<xbin checkout>/web/chat)');
  process.exit(0);
}

// The same files for a test that loads a model in node (the words, without
// a page): the models import xbind's kit by its served URL
// (model/harness-heads.js: /vendor/chat/tools.js), so /vendor/<name>
// resolves to what the page is served — the chat kit's directory, else the
// file beside the kit, else under its vendor/. A static import is resolved
// before this module runs: load a model after it, dynamically —
//   const { needOf } = await import('../model/project-pins.js');
if (typeof nodeModule.registerHooks === 'function') {
  const web = dirname(KIT_PATH);
  nodeModule.registerHooks({
    resolve(spec, context, next) {
      if (typeof spec === 'string' && spec.startsWith('/vendor/')) {
        const name = spec.slice('/vendor/'.length).split(/[?#]/)[0];
        const hit = [name.startsWith('chat/') ? join(CHAT_DIR, name.slice('chat/'.length)) : '', join(web, name), join(web, 'vendor', name)]
          .find((f) => f && !name.includes('..') && existsSync(f));
        if (hit) return { url: pathToFileURL(hit).href, format: 'module', shortCircuit: true };
      }
      return next(spec, context);
    },
  });
}

// marked as most tests stub it — markdown passes through as written, and a
// streamed answer is its text: the renderer's own instance (Marked) and the
// shared one an older page configured.
export const MARKED_STUB = 'export class Marked{parse(s){return s}lexer(){throw new Error("marked is stubbed")}}' +
  'export const marked={parse:(s)=>s,use(){}};';

// serveKit(page | context): answer the kit's URL with the real kit, the chat
// kit's with its modules, and the glyphs'.
export const serveKit = async (target) => {
  await target.route('**/vendor/bx-kit.js', (r) =>
    r.fulfill({ contentType: 'text/javascript', body: readFileSync(KIT_PATH, 'utf8') }));
  await target.route('**/vendor/chat/*.js', (r) => {
    const f = join(CHAT_DIR, new URL(r.request().url()).pathname.replace(/^.*\/vendor\/chat\//, ''));
    return existsSync(f) ? r.fulfill({ contentType: 'text/javascript', body: readFileSync(f, 'utf8') }) : r.fulfill({ status: 404, body: '' });
  });
  if (ICONS_PATH) {
    await target.route('**/vendor/bx-icons.js', (r) =>
      r.fulfill({ contentType: 'text/javascript', body: readFileSync(ICONS_PATH, 'utf8') }));
  }
  // the code highlighter the chat kit asks for at its first diff: these pages have none (a module that
  // exports nothing — diffs are drawn plain); test/harness-cards.mjs serves xbind's own over this
  await target.route('**/vendor/bx-code.js', (r) => r.fulfill({ contentType: 'text/javascript', body: 'export {};' }));
};

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
