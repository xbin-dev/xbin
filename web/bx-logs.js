/**
 * <bx-logs component="apps/x"> — a read-only view of a tile backend's
 * stdout/stderr, streamed from GET /api/xbin/logs (the HTTP twin of
 * `bx logs -f`). Rendered into an xterm terminal (reused for ANSI colors +
 * fast scrollback), but with no input wired — it's a viewer, not a shell.
 *
 * Gated server-side exactly like the tile's terminal (admin, the tile
 * itself, or a terminal-level user), so it only appears where a shell would.
 *
 * Attributes:
 *   component — the tile path whose logs to stream (required)
 *
 * Shares the terminal theme/font-size prefs (bx-term-theme / bx-term-fontsize
 * in localStorage, and the live `bx-term-pref` event) so it looks like the
 * shells beside it. xterm loads lazily; renders into a shadow root with
 * xterm's stylesheet linked so it works anywhere.
 */

const scriptOnce = (src) =>
  new Promise((res, rej) => {
    const id = 'bxs-' + src.replace(/\W/g, '');
    if (document.getElementById(id)) { res(); return; }
    const s = document.createElement('script');
    s.id = id; s.src = src; s.onload = res; s.onerror = rej;
    document.head.appendChild(s);
  });

let xtermReady = null;
function loadXterm() {
  xtermReady ??= (async () => {
    await scriptOnce('/vendor/xterm.js');
    await scriptOnce('/vendor/addon-fit.js');
  })();
  return xtermReady;
}

// The theme prefs live in localStorage under the terminal's keys — a log
// viewer sitting next to shells should match them. Kept a tiny local copy of
// the resolver so bx-logs stays independent of bx-terminal's internals.
function savedFontSize() {
  const n = Number(localStorage.getItem('bx-term-fontsize'));
  return n >= 7 && n <= 28 ? n : 12.5;
}
function termBg() {
  return getComputedStyle(document.body).getPropertyValue('--bx-term-bg').trim() || '#262c36';
}

export class BxLogs extends HTMLElement {
  #term; #fit; #ro; #ac; #host; #closed = false; #onPref; #gen = 0;

  static get observedAttributes() { return ['component']; }

  connectedCallback() {
    this.style.height = this.style.height || '100%';
    if (!this.shadowRoot) {
      const root = this.attachShadow({ mode: 'open' });
      root.innerHTML =
        `<link rel="stylesheet" href="/vendor/xterm.css">` +
        `<style>
          :host{display:block; position:relative}
          .host{height:100%; background:var(--bx-term-bg, #262c36)}
          .badge{position:absolute; top:4px; right:10px; z-index:6;
            font:10px/1.6 system-ui,sans-serif; letter-spacing:.05em; text-transform:uppercase;
            color:#c7ccd4; background:rgba(140,148,161,.18); border-radius:5px;
            padding:0 7px; pointer-events:none; opacity:.8;}
        </style>` +
        `<div class="host"></div>` +
        `<span class="badge">read-only logs</span>`;
    }
    this.#host = this.shadowRoot.querySelector('.host');
    this.#start();
  }

  disconnectedCallback() {
    this.#closed = true;
    this.#ac?.abort();
    this.#ro?.disconnect();
    this.#term?.dispose();
    if (this.#onPref) window.removeEventListener('bx-term-pref', this.#onPref);
  }

  attributeChangedCallback(name, oldV, newV) {
    if (name === 'component' && oldV !== null && oldV !== newV && this.#term) {
      this.#term.clear();
      this.#stream();
    }
  }

  async #start() {
    await loadXterm();
    if (this.#closed) return;
    this.#term = new window.Terminal({
      fontSize: Math.max(7, Math.min(28, Math.round(savedFontSize()))),
      fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace',
      theme: { background: termBg() },
      scrollback: 8000,
      disableStdin: true,       // read-only: no cursor, no input
      cursorStyle: 'underline',
      cursorInactiveStyle: 'none',
      convertEol: true,         // backend lines are \n-terminated
    });
    this.#fit = new window.FitAddon.FitAddon();
    this.#term.loadAddon(this.#fit);
    this.#term.open(this.#host);
    this.#host.style.background = termBg();
    try { this.#fit.fit(); } catch { }
    this.#ro = new ResizeObserver(() => { try { this.#fit.fit(); } catch { } });
    this.#ro.observe(this);
    // Follow the shared terminal font-size preference (theme too).
    this.#onPref = (e) => {
      if (e.detail?.fontSize && this.#term) {
        this.#term.options.fontSize = Math.max(7, Math.min(28, Math.round(e.detail.fontSize)));
        try { this.#fit.fit(); } catch { }
      }
    };
    window.addEventListener('bx-term-pref', this.#onPref);
    this.#stream();
  }

  // #stream opens (or reopens) the follow request and pumps decoded text into
  // the terminal. A generation guard means a component switch or reconnect
  // supersedes the previous fetch so two streams never interleave.
  async #stream() {
    const comp = this.getAttribute('component');
    if (!comp || !this.#term) return;
    this.#ac?.abort();
    const ac = new AbortController();
    this.#ac = ac;
    const gen = ++this.#gen;
    const url = `/api/xbin/logs?component=${encodeURIComponent(comp)}&follow=1`;
    try {
      const r = await fetch(url, { signal: ac.signal });
      if (gen !== this.#gen) return;
      if (!r.ok) {
        let msg = r.status;
        try { msg = (await r.json()).error ?? msg; } catch { }
        this.#term.write(`\x1b[31m[logs unavailable: ${msg}]\x1b[0m\r\n`);
        return;
      }
      const reader = r.body.getReader();
      const dec = new TextDecoder();
      for (;;) {
        const { value, done } = await reader.read();
        if (done || gen !== this.#gen) break;
        this.#term.write(dec.decode(value, { stream: true }));
      }
      if (gen === this.#gen && !this.#closed) {
        this.#term.write('\r\n\x1b[90m[log stream ended — reconnecting…]\x1b[0m\r\n');
        setTimeout(() => { if (!this.#closed && gen === this.#gen) this.#stream(); }, 1500);
      }
    } catch (e) {
      if (ac.signal.aborted || gen !== this.#gen) return; // superseded / unmounted
      this.#term.write(`\r\n\x1b[90m[disconnected — retrying…]\x1b[0m\r\n`);
      setTimeout(() => { if (!this.#closed && gen === this.#gen) this.#stream(); }, 2000);
    }
  }
}

customElements.define('bx-logs', BxLogs);
