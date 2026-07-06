/**
 * <bx-terminal> — xterm.js wired to xbind's /ws/term PTY sessions.
 *
 * Attributes/properties:
 *   cwd      — component path to open the shell in (new session)
 *   net      — network scope for a new session: internet (default) | host | none
 *   session  — existing session id to reattach (set automatically after
 *              connect; survives element re-creation if you persist it)
 *
 * Events: 'bx-session' (detail: {id, net}) once the server assigns a session.
 * Wire protocol: docs/protocol.md §/ws/term.
 *
 * xterm.js ships as UMD, loaded lazily into the main document; bx-terminal
 * renders into light DOM so xterm's global stylesheet applies.
 */

const scriptOnce = (src) =>
  new Promise((res, rej) => {
    const id = 'bxs-' + src.replace(/\W/g, '');
    if (document.getElementById(id)) { res(); return; }
    const s = document.createElement('script');
    s.id = id; s.src = src; s.onload = res; s.onerror = rej;
    document.head.appendChild(s);
  });

function savedFontSize() {
  const n = Number(localStorage.getItem('bx-term-fontsize'));
  return n >= 7 && n <= 28 ? n : 12.5;
}

let xtermReady = null;
function loadXterm() {
  xtermReady ??= (async () => {
    await scriptOnce('/vendor/xterm.js');
    await scriptOnce('/vendor/addon-fit.js');
  })();
  return xtermReady;
}

const enc = new TextEncoder();

export class BxTerminal extends HTMLElement {
  #term; #fit; #ws; #ro; #closed = false; #retries = 0; #opened = false; #reattachFails = 0; #host;

  connectedCallback() {
    this.style.display = 'block';
    this.style.height = this.style.height || '100%';
    if (!this.shadowRoot) {
      // xterm's stylesheet is linked inside the shadow root so <bx-terminal>
      // works anywhere, including inside other elements' shadow DOM.
      const root = this.attachShadow({ mode: 'open' });
      root.innerHTML =
        `<link rel="stylesheet" href="/vendor/xterm.css">` +
        `<style>:host{display:block} .host{height:100%;background:var(--bx-term-bg, #262c36)}</style>` +
        `<div class="host"></div>`;
    }
    this.#host = this.shadowRoot.querySelector('.host');
    this.#start();
  }

  disconnectedCallback() {
    this.#closed = true;
    this.#ro?.disconnect();
    this.#ws?.close();
    this.#term?.dispose();
  }

  // Switching network scope can't hot-reload (the netns/relay is fixed at
  // spawn), so a live change to `net` restarts the session: drop the current
  // session id and reconnect, which asks xbind for a fresh shell in the new
  // scope. (The caller is expected to have already ended the old session.)
  static get observedAttributes() { return ['net', 'gpu']; }
  attributeChangedCallback(name, oldV, newV) {
    if ((name !== 'net' && name !== 'gpu') || oldV === null || oldV === newV || !this.#term) return;
    this.#restart(name === 'gpu' ? `switching GPU → ${newV}…` : `switching network → ${newV}…`);
  }

  // restartFresh drops the current session and reconnects a brand-new one — used
  // after the persistent sandbox layer is reset out from under it.
  restartFresh() { if (this.#term) this.#restart('resetting sandbox…'); }

  #restart(msg) {
    this.removeAttribute('session');
    this.#retries = 0;
    if (msg) this.#term.write(`\r\n\x1b[90m[${msg}]\x1b[0m\r\n`);
    const old = this.#ws;
    this.#ws = null;
    if (old) { old.onclose = null; old.close(); }
    this.#connect();
  }

  async #start() {
    await loadXterm();
    if (this.#closed) return;
    this.#term = new window.Terminal({
      fontSize: savedFontSize(),
      fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace',
      // The terminal stays dark inside the light chrome: ANSI palettes
      // assume it, and it reads better in a code context.
      // Match the --bx-term-bg token (custom props pierce shadow DOM, but
      // xterm needs a concrete value at construction).
      theme: { background: getComputedStyle(this).getPropertyValue('--bx-term-bg').trim() || '#262c36' },
      scrollback: 4000,
    });
    this.#fit = new window.FitAddon.FitAddon();
    this.#term.loadAddon(this.#fit);
    this.#term.open(this.#host);
    this.#fit.fit();
    this.#ro = new ResizeObserver(() => { try { this.#fit.fit(); } catch { } });
    this.#ro.observe(this);
    // ctrl+scroll = font size (shared preference across all terminals).
    this.#host.addEventListener('wheel', (e) => {
      if (!e.ctrlKey) return;
      e.preventDefault();
      const cur = this.#term.options.fontSize;
      const next = Math.max(7, Math.min(28, cur + (e.deltaY < 0 ? 1 : -1)));
      if (next === cur) return;
      this.#term.options.fontSize = next;
      try { localStorage.setItem('bx-term-fontsize', String(next)); } catch { }
      try { this.#fit.fit(); } catch { }
    }, { passive: false });
    this.#term.onData((d) => {
      if (this.#ws?.readyState === WebSocket.OPEN) this.#ws.send(enc.encode(d));
    });
    this.#term.onResize(({ cols, rows }) => {
      if (this.#ws?.readyState === WebSocket.OPEN) {
        this.#ws.send(JSON.stringify({ op: 'resize', cols, rows }));
      }
    });
    this.#connect();
  }

  #connect() {
    this.#opened = false;
    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
    const q = this.getAttribute('session')
      ? `session=${encodeURIComponent(this.getAttribute('session'))}`
      : `cwd=${encodeURIComponent(this.getAttribute('cwd') || '')}` +
        `&net=${encodeURIComponent(this.getAttribute('net') || 'internet')}` +
        `&gpu=${encodeURIComponent(this.getAttribute('gpu') || 'none')}`;
    const ws = new WebSocket(`${proto}//${location.host}/ws/term?${q}`);
    ws.binaryType = 'arraybuffer';
    this.#ws = ws;

    ws.onopen = () => {
      this.#retries = 0;
      this.#opened = true;
      this.#reattachFails = 0;
      const { cols, rows } = this.#term;
      ws.send(JSON.stringify({ op: 'resize', cols, rows }));
      this.#term.focus();
    };
    ws.onmessage = (m) => {
      if (typeof m.data === 'string') {
        let ctl; try { ctl = JSON.parse(m.data); } catch { return; }
        if (ctl.op === 'session') {
          this.setAttribute('session', ctl.id);
          if (ctl.net) this.setAttribute('net', ctl.net);
          this.dispatchEvent(new CustomEvent('bx-session', { detail: { id: ctl.id, net: ctl.net }, bubbles: true }));
        } else if (ctl.op === 'exit') {
          this.#closed = true;
          this.#term.write('\r\n\x1b[90m[session ended]\x1b[0m\r\n');
        }
        return;
      }
      this.#term.write(new Uint8Array(m.data));
    };
    ws.onclose = () => {
      if (this.#closed) return;
      const reattaching = !!this.getAttribute('session');
      // A reattach whose handshake never succeeded almost always means the
      // session is gone (xbind 404s an unknown id — e.g. a stale id restored
      // from a previous page, or after a daemon restart). After a couple of
      // tries, start a fresh session instead of retrying a dead id forever.
      if (reattaching && !this.#opened && ++this.#reattachFails >= 2) {
        this.#restart('previous session gone — starting fresh…');
        return;
      }
      // Otherwise reattach by session id with backoff (xbind keeps the PTY).
      if (reattaching && this.#retries < 8) {
        const wait = Math.min(500 * 2 ** this.#retries++, 10000);
        this.#term.write(`\r\n\x1b[90m[reconnecting…]\x1b[0m\r\n`);
        setTimeout(() => { if (!this.#closed) this.#connect(); }, wait);
      } else {
        this.#term.write('\r\n\x1b[31m[disconnected]\x1b[0m\r\n');
      }
    };
  }
}

customElements.define('bx-terminal', BxTerminal);
