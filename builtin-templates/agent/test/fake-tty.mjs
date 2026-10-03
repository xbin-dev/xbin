// fake-tty.mjs — a coding sandbox manager's terminal for the browser tests
// (test/terminal.mjs, test/harness-term.mjs): the terminal element and its
// pieces served at /vendor/ as xbind serves them (serveTerminal), and a
// fake of the manager's tty WebSocket in the page (FAKE_TTY).
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

const here = dirname(fileURLToPath(import.meta.url));
const web = process.env.BX_WEB || join(here, '..', '..', '..', 'web');

// serveTerminal(ctx): /vendor/bx-terminal.js and every module and asset it loads.
export async function serveTerminal(ctx) {
  const vendorFile = { 'bx-terminal.js': join(web, 'bx-terminal.js'), 'bx-scroll.js': join(web, 'bx-scroll.js'), 'term-predict.js': join(web, 'term-predict.js'),
    'term-src.js': join(web, 'term-src.js'), 'term-links.js': join(web, 'term-links.js'),
    'xterm.js': join(web, 'vendor', 'xterm.js'), 'addon-fit.js': join(web, 'vendor', 'addon-fit.js'),
    'addon-web-links.js': join(web, 'vendor', 'addon-web-links.js'), 'xterm.css': join(web, 'vendor', 'xterm.css') };
  for (const [name, file] of Object.entries(vendorFile)) {
    await ctx.route(`**/vendor/${name}`, (r) => r.fulfill({ contentType: name.endsWith('.css') ? 'text/css' : 'text/javascript', body: readFileSync(file, 'utf8') }));
  }
}

// FAKE_TTY runs in the page (ctx.addInitScript(FAKE_TTY), after STUB): the
// manager's terminal as a WebSocket the tile gets from xbin.ws(path) — the
// session frame, a prompt, echo, `echo X`, `exit`, resize and Escape
// recorded; a src with ?cmd= runs that command (it says so, and `exit` ends
// it); window.__tty.drop() cuts the socket. The page is bound to two
// managers (xbin.iface('sandboxes')): apps/coding-sandbox and apps/plain.
export function FAKE_TTY() {
  const T = window.__tty = { dials: [], resizes: [], keys: '', sockets: [], execs: 0 };
  const enc = new TextEncoder();
  const dec = new TextDecoder();
  class FakeWS {
    constructor(path) {
      this.url = path; this.readyState = 0; this.binaryType = 'blob'; this.line = '';
      T.dials.push(path);
      T.sockets.push(this);
      const m = /\/sbx\/sandboxes\/([^/]+)\/(?:execs\/([^/]+)\/)?tty/.exec(path);
      this.id = m && m[2] ? decodeURIComponent(m[2]) : 'e' + (++T.execs);
      // a sandbox named refuse-*: the handshake is refused (the page sees a close, never an open)
      if (m && m[1].startsWith('refuse')) { setTimeout(() => this.shut(1006), 20); return; }
      setTimeout(() => {
        this.readyState = 1;
        this.onopen && this.onopen({});
        this.text({ op: 'session', id: this.id, sandbox: m ? decodeURIComponent(m[1]) : '', echoAck: false });
        const cmd = new URLSearchParams(path.split('?')[1] || '').get('cmd');
        this.bin(m && m[2] ? '\r\n(reattached)\r\n$ ' : cmd ? `running: ${cmd}\r\nOpen https://example.invalid/login to sign in\r\n> ` : 'sandbox$ ');
      }, 20);
    }
    text(v) { this.onmessage && this.onmessage({ data: JSON.stringify(v) }); }
    bin(s) { this.onmessage && this.onmessage({ data: enc.encode(s).buffer }); }
    send(d) {
      if (typeof d === 'string') { const c = JSON.parse(d); if (c.op === 'resize') T.resizes.push([c.cols, c.rows, this.id]); return; }
      const s = dec.decode(d);
      T.keys += s;
      for (const ch of s) {
        if (ch === '\r') {
          const cmd = this.line; this.line = '';
          this.bin('\r\n');
          if (cmd === 'exit') { this.text({ op: 'exit', code: 0 }); this.shut(1000); return; }
          const e = /^echo (.*)$/.exec(cmd);
          this.bin((e ? e[1] + '\r\n' : '') + 'sandbox$ ');
        } else if (ch >= ' ') { this.line += ch; this.bin(ch); }
      }
    }
    shut(code) {
      if (this.readyState === 3) return;
      this.readyState = 3;
      setTimeout(() => this.onclose && this.onclose({ code }), 0);
    }
    close() { this.shut(1005); }
  }
  T.drop = () => T.sockets.at(-1).shut(1006);
  const install = () => {
    if (!window.xbin || window.xbin.__tty) return;
    window.xbin.__tty = true;
    window.xbin.ws = (path) => new FakeWS(path);
    window.xbin.iface = (slot) => (slot === 'sandboxes' ? { service: 'sandbox-manager', multi: true,
      endpoints: [{ provider: 'apps/coding-sandbox', url: '/api/apps/coding-sandbox' }, { provider: 'apps/plain', url: '/api/apps/plain' }] } : null);
  };
  install();
  document.addEventListener('DOMContentLoaded', install);
}
