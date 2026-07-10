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

// Familiar terminal color schemes (xterm theme objects). "default" keeps xbin's
// dark-steel background (from the --bx-term-bg token) with xterm's own palette;
// the rest are the well-known ones. Shared across terminals via localStorage.
const TERM_THEMES = {
  'default': null,
  'dracula': { background: '#282a36', foreground: '#f8f8f2', cursor: '#f8f8f2', selectionBackground: '#44475a', black: '#21222c', red: '#ff5555', green: '#50fa7b', yellow: '#f1fa8c', blue: '#bd93f9', magenta: '#ff79c6', cyan: '#8be9fd', white: '#f8f8f2', brightBlack: '#6272a4', brightRed: '#ff6e6e', brightGreen: '#69ff94', brightYellow: '#ffffa5', brightBlue: '#d6acff', brightMagenta: '#ff92df', brightCyan: '#a4ffff', brightWhite: '#ffffff' },
  'nord': { background: '#2e3440', foreground: '#d8dee9', cursor: '#d8dee9', selectionBackground: '#434c5e', black: '#3b4252', red: '#bf616a', green: '#a3be8c', yellow: '#ebcb8b', blue: '#81a1c1', magenta: '#b48ead', cyan: '#88c0d0', white: '#e5e9f0', brightBlack: '#4c566a', brightRed: '#bf616a', brightGreen: '#a3be8c', brightYellow: '#ebcb8b', brightBlue: '#81a1c1', brightMagenta: '#b48ead', brightCyan: '#8fbcbb', brightWhite: '#eceff4' },
  'solarized-dark': { background: '#002b36', foreground: '#839496', cursor: '#93a1a1', selectionBackground: '#073642', black: '#073642', red: '#dc322f', green: '#859900', yellow: '#b58900', blue: '#268bd2', magenta: '#d33682', cyan: '#2aa198', white: '#eee8d5', brightBlack: '#586e75', brightRed: '#cb4b16', brightGreen: '#657b83', brightYellow: '#839496', brightBlue: '#657b83', brightMagenta: '#6c71c4', brightCyan: '#93a1a1', brightWhite: '#fdf6e3' },
  'solarized-light': { background: '#fdf6e3', foreground: '#657b83', cursor: '#586e75', selectionBackground: '#eee8d5', black: '#073642', red: '#dc322f', green: '#859900', yellow: '#b58900', blue: '#268bd2', magenta: '#d33682', cyan: '#2aa198', white: '#eee8d5', brightBlack: '#586e75', brightRed: '#cb4b16', brightGreen: '#657b83', brightYellow: '#839496', brightBlue: '#657b83', brightMagenta: '#6c71c4', brightCyan: '#93a1a1', brightWhite: '#fdf6e3' },
  'monokai': { background: '#272822', foreground: '#f8f8f2', cursor: '#f8f8f0', selectionBackground: '#49483e', black: '#272822', red: '#f92672', green: '#a6e22e', yellow: '#f4bf75', blue: '#66d9ef', magenta: '#ae81ff', cyan: '#a1efe4', white: '#f8f8f2', brightBlack: '#75715e', brightRed: '#f92672', brightGreen: '#a6e22e', brightYellow: '#f4bf75', brightBlue: '#66d9ef', brightMagenta: '#ae81ff', brightCyan: '#a1efe4', brightWhite: '#f9f8f5' },
  'gruvbox-dark': { background: '#282828', foreground: '#ebdbb2', cursor: '#ebdbb2', selectionBackground: '#504945', black: '#282828', red: '#cc241d', green: '#98971a', yellow: '#d79921', blue: '#458588', magenta: '#b16286', cyan: '#689d6a', white: '#a89984', brightBlack: '#928374', brightRed: '#fb4934', brightGreen: '#b8bb26', brightYellow: '#fabd2f', brightBlue: '#83a598', brightMagenta: '#d3869b', brightCyan: '#8ec07c', brightWhite: '#ebdbb2' },
  'one-dark': { background: '#282c34', foreground: '#abb2bf', cursor: '#528bff', selectionBackground: '#3e4451', black: '#282c34', red: '#e06c75', green: '#98c379', yellow: '#e5c07b', blue: '#61afef', magenta: '#c678dd', cyan: '#56b6c2', white: '#abb2bf', brightBlack: '#5c6370', brightRed: '#e06c75', brightGreen: '#98c379', brightYellow: '#e5c07b', brightBlue: '#61afef', brightMagenta: '#c678dd', brightCyan: '#56b6c2', brightWhite: '#ffffff' },
  'tango-dark': { background: '#2e3436', foreground: '#d3d7cf', cursor: '#d3d7cf', selectionBackground: '#555753', black: '#2e3436', red: '#cc0000', green: '#4e9a06', yellow: '#c4a000', blue: '#3465a4', magenta: '#75507b', cyan: '#06989a', white: '#d3d7cf', brightBlack: '#555753', brightRed: '#ef2929', brightGreen: '#8ae234', brightYellow: '#fce94f', brightBlue: '#729fcf', brightMagenta: '#ad7fa8', brightCyan: '#34e2e2', brightWhite: '#eeeeec' },
  'github-light': { background: '#ffffff', foreground: '#24292e', cursor: '#24292e', selectionBackground: '#c8e1ff', black: '#24292e', red: '#d73a49', green: '#28a745', yellow: '#dbab09', blue: '#0366d6', magenta: '#5a32a3', cyan: '#0598bc', white: '#6a737d', brightBlack: '#959da5', brightRed: '#cb2431', brightGreen: '#22863a', brightYellow: '#b08800', brightBlue: '#005cc5', brightMagenta: '#5a32a3', brightCyan: '#3192aa', brightWhite: '#d1d5da' },
};
const THEME_LABELS = {
  'default': 'Default (dark-steel)', 'dracula': 'Dracula', 'nord': 'Nord',
  'solarized-dark': 'Solarized Dark', 'solarized-light': 'Solarized Light',
  'monokai': 'Monokai', 'gruvbox-dark': 'Gruvbox Dark', 'one-dark': 'One Dark',
  'tango-dark': 'Tango Dark', 'github-light': 'GitHub Light',
};

function savedTheme() {
  const t = localStorage.getItem('bx-term-theme');
  return t && t in TERM_THEMES ? t : 'default';
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
  #onPref; #onStorage; #gen = 0; // connection epoch: only the latest socket drives the term

  connectedCallback() {
    this.style.display = 'block';
    this.style.height = this.style.height || '100%';
    if (!this.shadowRoot) {
      // xterm's stylesheet is linked inside the shadow root so <bx-terminal>
      // works anywhere, including inside other elements' shadow DOM.
      const root = this.attachShadow({ mode: 'open' });
      root.innerHTML =
        `<link rel="stylesheet" href="/vendor/xterm.css">` +
        `<style>
          :host{display:block; position:relative}
          .host{height:100%;background:var(--bx-term-bg, #262c36)}
          .gear{position:absolute; top:4px; right:10px; z-index:6; width:22px; height:22px;
            border:0; border-radius:5px; padding:0; cursor:pointer; font-size:13px; line-height:22px;
            background:rgba(140,148,161,.18); color:#c7ccd4; opacity:0; transition:opacity .15s;}
          :host(:hover) .gear, .gear:focus, .gear.open{opacity:.85}
          .gear:hover{background:rgba(140,148,161,.34)}
          .tmenu{position:absolute; top:30px; right:10px; z-index:7; min-width:210px;
            background:var(--bx-panel,#23272e); color:var(--bx-text,#d7dce5);
            border:1px solid var(--bx-border,#39414d); border-radius:8px; padding:8px;
            box-shadow:0 10px 30px rgba(0,0,0,.5); font:12px/1.4 system-ui,sans-serif;}
          .tmenu[hidden]{display:none}
          .tmenu .hd{font-size:9.5px; letter-spacing:.08em; text-transform:uppercase;
            color:var(--bx-muted,#8794a1); font-weight:600; margin:0 2px 6px;}
          .tmenu .row{display:flex; align-items:center; justify-content:space-between; gap:8px; margin:5px 2px;}
          .tmenu select{flex:1; min-width:0; font:inherit; font-size:12px; padding:3px 6px;
            border:1px solid var(--bx-border,#39414d); border-radius:5px;
            background:var(--bx-bg,#1b1e24); color:var(--bx-text,#d7dce5);}
          .tmenu .fs{display:flex; align-items:center; gap:6px;}
          .tmenu .fs b{min-width:30px; text-align:center; font-variant-numeric:tabular-nums;}
          .tmenu .step{width:22px; height:22px; border:1px solid var(--bx-border,#39414d);
            border-radius:5px; background:var(--bx-bg,#1b1e24); color:var(--bx-text,#d7dce5);
            cursor:pointer; font:inherit; line-height:1;}
          .tmenu .step:hover{background:var(--bx-panel-2,#2b313a);}
        </style>` +
        `<div class="host"></div>` +
        `<button class="gear" title="terminal settings" aria-label="terminal settings">🔧</button>` +
        `<div class="tmenu" hidden>` +
          `<div class="hd">terminal</div>` +
          `<div class="row"><span>Theme</span><select class="theme"></select></div>` +
          `<div class="row"><span>Font size</span>` +
            `<span class="fs"><button class="step" data-d="-1" aria-label="smaller">−</button>` +
            `<b class="fsv"></b>` +
            `<button class="step" data-d="1" aria-label="larger">+</button></span></div>` +
        `</div>`;
    }
    this.#host = this.shadowRoot.querySelector('.host');
    this.#wireSettings();
    this.#start();
  }

  disconnectedCallback() {
    this.#closed = true;
    this.#ro?.disconnect();
    this.#ws?.close();
    this.#term?.dispose();
    if (this.#onPref) window.removeEventListener('bx-term-pref', this.#onPref);
    if (this.#onStorage) window.removeEventListener('storage', this.#onStorage);
  }

  // --- settings (theme + font size) ---------------------------------------

  // #themeObj resolves a theme name to an xterm theme object. "default" is the
  // --bx-term-bg background with xterm's own palette.
  #themeObj(name = savedTheme()) {
    return TERM_THEMES[name] ||
      { background: getComputedStyle(this).getPropertyValue('--bx-term-bg').trim() || '#262c36' };
  }

  #applyTheme(name) {
    const theme = this.#themeObj(name);
    if (this.#term) this.#term.options.theme = theme;
    if (this.#host && theme.background) this.#host.style.background = theme.background;
    const sel = this.shadowRoot?.querySelector('.theme');
    if (sel && sel.value !== name) sel.value = name;
  }

  #setFontSize(next) {
    next = Math.max(7, Math.min(28, next));
    if (!this.#term || next === this.#term.options.fontSize) return;
    this.#term.options.fontSize = next;
    try { localStorage.setItem('bx-term-fontsize', String(next)); } catch { }
    try { this.#fit.fit(); } catch { }
  }

  // #wireSettings builds the gear menu and syncs prefs across terminals: a
  // change here broadcasts (same document) and rides localStorage (other tabs).
  #wireSettings() {
    const root = this.shadowRoot;
    const gear = root.querySelector('.gear');
    const menu = root.querySelector('.tmenu');
    const sel = root.querySelector('.theme');
    const fsv = root.querySelector('.fsv');
    if (!gear || !menu || !sel) return;

    for (const key of Object.keys(TERM_THEMES)) {
      const o = document.createElement('option');
      o.value = key;
      o.textContent = THEME_LABELS[key] || key;
      sel.appendChild(o);
    }
    sel.value = savedTheme();
    const fontNow = () => (this.#term ? this.#term.options.fontSize : savedFontSize());
    const showFs = () => { fsv.textContent = String(fontNow()); };
    showFs();

    const onDoc = (e) => {
      if (!e.composedPath().includes(menu) && !e.composedPath().includes(gear)) close();
    };
    const close = () => {
      menu.hidden = true;
      gear.classList.remove('open');
      document.removeEventListener('pointerdown', onDoc, true);
    };
    gear.addEventListener('click', (e) => {
      e.stopPropagation();
      if (menu.hidden) {
        sel.value = savedTheme();
        showFs();
        menu.hidden = false;
        gear.classList.add('open');
        document.addEventListener('pointerdown', onDoc, true);
      } else {
        close();
      }
    });
    sel.addEventListener('change', () => {
      const name = sel.value;
      try { localStorage.setItem('bx-term-theme', name); } catch { }
      this.#applyTheme(name);
      window.dispatchEvent(new CustomEvent('bx-term-pref', { detail: { theme: name } }));
    });
    root.querySelectorAll('.step').forEach((b) => b.addEventListener('click', () => {
      this.#setFontSize(fontNow() + Number(b.dataset.d));
      showFs();
      window.dispatchEvent(new CustomEvent('bx-term-pref', { detail: { fontSize: this.#term?.options.fontSize } }));
    }));

    // Live-sync from another terminal in this document, or another tab.
    this.#onPref = (e) => {
      if (e.detail?.theme) this.#applyTheme(e.detail.theme);
      if (e.detail?.fontSize) this.#setFontSize(e.detail.fontSize);
      showFs();
    };
    window.addEventListener('bx-term-pref', this.#onPref);
    this.#onStorage = (e) => {
      if (e.key === 'bx-term-theme') this.#applyTheme(savedTheme());
      if (e.key === 'bx-term-fontsize') { this.#setFontSize(savedFontSize()); showFs(); }
    };
    window.addEventListener('storage', this.#onStorage);
  }

  // Switching network scope can't hot-reload (the netns/relay is fixed at
  // spawn), so a live change to `net` restarts the session: drop the current
  // session id and reconnect, which asks xbind for a fresh shell in the new
  // scope. (The caller is expected to have already ended the old session.)
  static get observedAttributes() { return ['net', 'gpu', 'api']; }
  attributeChangedCallback(name, oldV, newV) {
    if ((name !== 'net' && name !== 'gpu' && name !== 'api') || oldV === null || oldV === newV || !this.#term) return;
    const msg = name === 'gpu' ? `switching GPU → ${newV}…`
      : name === 'api' ? `${newV === '0' ? 'disabling' : 'enabling'} tile API…`
        : `switching network → ${newV}…`;
    this.#restart(msg);
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
      // Color scheme: the user's saved theme (settings menu), or the
      // --bx-term-bg token with xterm's default palette. See TERM_THEMES.
      theme: this.#themeObj(),
      scrollback: 4000,
    });
    this.#fit = new window.FitAddon.FitAddon();
    this.#term.loadAddon(this.#fit);
    // Ctrl+W is word-erase (WERASE, 0x17) in a shell, but the browser default
    // closes the tab — pre-empt that so the keystroke reaches the pty. Same
    // for Ctrl+Shift+W (close window). Returning true lets xterm still emit
    // the control byte; we only cancel the browser's default. Best-effort:
    // some browsers reserve these regardless, but Chromium/Firefox honor it
    // while the terminal has focus.
    this.#term.attachCustomKeyEventHandler((e) => {
      if (e.type === 'keydown' && e.ctrlKey && !e.altKey && !e.metaKey &&
          (e.key === 'w' || e.key === 'W')) {
        e.preventDefault();
      }
      return true;
    });
    this.#term.open(this.#host);
    this.#host.style.background = this.#themeObj().background || ''; // match themed bg
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
    // Claim a new epoch. Any earlier socket (e.g. one the server just killed on
    // a base-image reset) is now stale: its onclose must not schedule a
    // reconnect, and its late frames must not be written to the term — otherwise
    // two sockets end up on one session and every byte (incl. keystroke echo)
    // is doubled.
    const gen = ++this.#gen;
    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
    const q = this.getAttribute('session')
      ? `session=${encodeURIComponent(this.getAttribute('session'))}`
      : `cwd=${encodeURIComponent(this.getAttribute('cwd') || '')}` +
        `&net=${encodeURIComponent(this.getAttribute('net') || 'internet')}` +
        `&gpu=${encodeURIComponent(this.getAttribute('gpu') || 'none')}` +
        `&api=${this.getAttribute('api') === '0' ? '0' : '1'}`;
    const ws = new WebSocket(`${proto}//${location.host}/ws/term?${q}`);
    ws.binaryType = 'arraybuffer';
    this.#ws = ws;

    ws.onopen = () => {
      if (gen !== this.#gen) { ws.close(); return; } // superseded before it opened
      this.#retries = 0;
      this.#opened = true;
      this.#reattachFails = 0;
      const { cols, rows } = this.#term;
      ws.send(JSON.stringify({ op: 'resize', cols, rows }));
      this.#term.focus();
    };
    ws.onmessage = (m) => {
      if (gen !== this.#gen) { ws.close(); return; } // a stale socket must not drive the term
      if (typeof m.data === 'string') {
        let ctl; try { ctl = JSON.parse(m.data); } catch { return; }
        if (ctl.op === 'session') {
          this.setAttribute('session', ctl.id);
          if (ctl.net) this.setAttribute('net', ctl.net);
          this.dispatchEvent(new CustomEvent('bx-session', { detail: { id: ctl.id, net: ctl.net, baseOutdated: !!ctl.baseOutdated }, bubbles: true }));
        } else if (ctl.op === 'exit') {
          // Shell exited — the session is gone server-side. Let the host close
          // this terminal (its tab/window), like a real terminal emulator.
          this.#closed = true;
          this.dispatchEvent(new CustomEvent('bx-exit', { bubbles: true }));
        }
        return;
      }
      this.#term.write(new Uint8Array(m.data));
    };
    ws.onclose = () => {
      if (this.#closed || gen !== this.#gen) return; // stale socket (superseded) → don't reconnect
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
        setTimeout(() => { if (!this.#closed && gen === this.#gen) this.#connect(); }, wait);
      } else {
        this.#term.write('\r\n\x1b[31m[disconnected]\x1b[0m\r\n');
      }
    };
  }
}

customElements.define('bx-terminal', BxTerminal);
