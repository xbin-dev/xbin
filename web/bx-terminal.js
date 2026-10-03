/**
 * <bx-terminal> — xterm.js wired to xbind's /ws/term PTY sessions, or to any
 * endpoint speaking the same terminal wire (`src`).
 *
 * Attributes/properties:
 *   src      — a WebSocket endpoint on the terminal wire (docs/protocol.md §The
 *              terminal wire) to connect to instead of xbind's /ws/term:
 *              typically a sandbox manager's …/sbx/sandboxes/{id}/tty?cwd=
 *              through the page's bound interface (docs/sandbox-manager.md).
 *              A path on this host is dialled with the page's credential —
 *              in a tile its frame token (xbin.ws), in chrome the session
 *              cookie. cwd/net/gpu/api/vm don't apply. A drop reconnects
 *              with backoff (a manager's route reattaches to the same
 *              session); a clean close or an exit frame ends it (bx-exit).
 *              Changing it starts over. Also a property. docs/elements.md.
 *   cwd      — component path to open the shell in (new session)
 *   net      — network scope for a new session: org | personal | internet | host | none;
 *              omit it for the tile's default (the org network on org-owned
 *              tiles with network sets, else internet — D54). The server may
 *              clamp the request; the attribute then mirrors what it granted.
 *   session  — existing session id to reattach (set automatically after
 *              connect; survives element re-creation if you persist it)
 *   vm       — "1": open the session in a VM sandbox (a Firecracker microVM,
 *              root in its own kernel; D89). Changing it
 *              restarts the session, like net/gpu/api.
 *   deployment — the tile deployment a new session calls (its target);
 *              omit it for the server's default (the primary, D127p).
 *              Changing it restarts the session; the server's echo is
 *              mirrored back without a restart, as net's is.
 *
 * Events: 'bx-session' (detail: {id, net, scopes:[{id,label,desc}], label,
 * netNote, vm, deployment, asked, api, partition?}) once the server assigns a
 * session — `scopes` is exactly what this user may pick on this tile, `netNote`
 * explains a clamp, `deployment` is the target the server echoed ('' for a
 * session that follows the primary) and `asked` the one this connection
 * requested, `api` whether it has the tile API (when the server says),
 * `partition` what it acts in on a partitioned tile (user:<id> | global).
 * Methods: note(text) writes a grey notice line (held while a full-screen
 * program owns the screen); end(text) ends the session with a red line.
 * Wire protocol: docs/protocol.md §/ws/term.
 *
 * xterm.js ships as UMD, loaded lazily into the main document; bx-terminal
 * renders into its shadow root with xterm's stylesheet linked inside it.
 *
 * Predictive echo (D70): typed characters are drawn at once as an overlay
 * (a layer over xterm's screen, positioned by its cell metrics) and
 * confirmed or removed when the server acks the input — mosh's algorithm, in
 * /vendor/term-predict.js; when a program hides the cursor (Ink apps such as
 * Claude Code, most TUIs) the engine learns where typed text lands from the
 * echo instead (D71). Per-browser mode in localStorage['bx-term-predict']:
 * auto (on when the RTT is over 100 ms), on, off. The settings menu shows the RTT.
 *
 * Colours and type (D184): the settings menu's palette is per browser
 * (localStorage['bx-term-theme'], web/term-palettes.js). "Workspace (follows
 * the theme)", every terminal's until the person picks another, is built from
 * the --bx-term-* tokens at this element and rebuilt when the person's
 * appearance changes (onAppearance, /vendor/bx-theme.js), so an open terminal
 * changes with the shell. The face is the --bx-mono token (JetBrains Mono),
 * loaded before xterm measures its cells; the size is the person's pick, else
 * the density's --bx-term-size. Bold is weight, never a brighter colour.
 */
import { Predictor, srttUpdate, SRTT_SHOW } from '/vendor/term-predict.js';
import { srcTarget, reattachSrc, canReattach, endedByClose, RETRIES, LIVED, backoff, exitWords } from '/vendor/term-src.js';
import { sandboxed } from '/vendor/bx-kit.js';
import { wireLinks, joinedLinksAt, rowOf } from '/vendor/term-links.js';
import { scrollCssText } from '/vendor/bx-scroll.js';
import { TERM_THEMES, THEME_LABELS, themeName, paletteFor } from '/vendor/term-palettes.js';
import { token, onAppearance } from '/vendor/bx-theme.js';
import { iconSvg } from '/vendor/bx-icons.js';

// Load a classic script once per document. Several elements (the terminal,
// the read-only logs view) share the tag by id, so a second caller must wait
// for the SAME tag to load — not resolve just because the tag exists.
const scriptOnce = (src) => {
  const id = 'bxs-' + src.replace(/\W/g, '');
  let s = document.getElementById(id);
  if (s?.dataset.loaded) return Promise.resolve();
  if (!s) {
    s = document.createElement('script');
    s.id = id; s.src = src;
    document.head.appendChild(s);
  }
  return new Promise((res, rej) => {
    s.addEventListener('load', () => { s.dataset.loaded = '1'; res(); }, { once: true });
    s.addEventListener('error', rej, { once: true });
  });
};

// A sandboxed tile frame (<bx-terminal src> in a tile) has no localStorage:
// reading it throws there — the defaults then.
const stored = (k) => { try { return localStorage.getItem(k); } catch { return null; } };

// The size the person picked (the menu's stepper, ctrl+scroll), or null:
// then the density's --bx-term-size (sizeOf).
function savedFontSize() {
  const n = Number(stored('bx-term-fontsize') ?? NaN);
  return n >= 7 && n <= 28 ? n : null;
}
// sizeOf(el): the --bx-term-size token at el (12 compact, 13 comfortable;
// 12 where a document has no theme.css).
function sizeOf(el) {
  const n = parseFloat(token('--bx-term-size', el));
  return n >= 7 && n <= 28 ? n : 12;
}

// The palette the person picked (web/term-palettes.js), shared across
// terminals via localStorage; 'default' follows the workspace's theme.
const savedTheme = () => themeName(stored('bx-term-theme'));

// fontsLoaded(family, px): xterm measures its cells once (and again only when
// its font options change), so the face must be there first. Waits for the
// regular and bold faces, at most `wait` ms; a face that fails to load (or a
// document without it) resolves at once: the fallback face is measured then.
function fontsLoaded(family, px, wait = 1500) {
  const fs = document.fonts;
  if (!fs?.load || !family) return Promise.resolve();
  const both = Promise.all([fs.load(`${px}px ${family}`), fs.load(`700 ${px}px ${family}`)]).catch(() => { });
  return Promise.race([both, new Promise((r) => setTimeout(r, wait))]);
}

let xtermReady = null;
function loadXterm() {
  xtermReady ??= (async () => {
    await scriptOnce('/vendor/xterm.js');
    await scriptOnce('/vendor/addon-fit.js');
    await scriptOnce('/vendor/addon-web-links.js');
  })();
  return xtermReady;
}

const enc = new TextEncoder();
const PREDICT_MODES = ['auto', 'on', 'off'];
function savedPredict() {
  const v = stored('bx-term-predict');
  return PREDICT_MODES.includes(v) ? v : 'auto';
}

export class BxTerminal extends HTMLElement {
  #term; #fit; #ws; #ro; #closed = false; #retries = 0; #opened = false; #reattachFails = 0; #host; #ranInit = false;
  #serverNet = null; #notedSession = null; // effective scope per the server; the session we printed its notes for
  #serverDep = ''; #serverApi = null; #asked = ''; #restartMsg = null; // the echoed target and API, the target this connection asked for, a pending restart
  #onPref; #onStorage; #onAmbient; #gen = 0; // connection epoch: only the latest socket drives the term
  // predictive echo (D70): the engine, our count of input frames sent on this
  // socket, whether this xbind acks them, the smoothed RTT, the live
  // decoration markers, the last rendered overlay, and the harness's ack hold
  #pred = new Predictor(); #seq = 0; #echoAck = false; #srtt = null; #layer = null; #overlay = []; #hold = null;
  #pingTimer = null; #nullCell = null;
  #note = null; // a notice held while a full-screen program owns the alternate buffer (note())
  #openedAt = 0; #failed = 0; // src mode: when this socket opened; handshakes in a row that never opened
  // #baseFont is the user's chosen terminal font size; #ambient is the workspace
  // zoom applied by an ancestor (bx-shell). xterm's actual fontSize is their
  // product, and the host counter-zooms by 1/#ambient — so the terminal looks
  // the same size as if it were zoomed, but xterm sees net-zoom-1 and its mouse
  // math stays exact (its canvas cell metrics ignore CSS zoom; its pointer
  // coords don't — the mismatch drifts selection/right-click, worse the further
  // from the top-left). See _applySettings in bx-shell.
  #baseFont = savedFontSize() ?? 12; #ambient = 1;
  // the xterm theme in force (resolved once per change: #redraw reads it on
  // every write), and the subscriptions to the person's appearance and fonts
  #theme = null; #offLook = null; #onFonts = null;

  connectedCallback() {
    // NB: do NOT force `display: block` here. The host (bx-frame) hides inactive
    // tabs with an inline `display: none`, and an inline `display: block` set
    // here would override it — so on reload, when several restored terminals
    // connect at once, they'd ALL show (stacked in the column) until each tab is
    // clicked and re-rendered. `:host { display: block }` in the shadow CSS is
    // the standalone default and (being a shadow rule) never overrides the
    // host's inline display.
    this.style.height = this.style.height || '100%';
    if (!this.shadowRoot) {
      // xterm's stylesheet is linked inside the shadow root so <bx-terminal>
      // works anywhere, including inside other elements' shadow DOM.
      const root = this.attachShadow({ mode: 'open' });
      root.innerHTML =
        `<link rel="stylesheet" href="/vendor/xterm.css">` +
        `<style>
          ${scrollCssText}
          :host{display:block; position:relative}
          /* the face and size xterm reads (#start): the tokens, Night's where
             a document has no theme.css */
          .host{height:100%; background:var(--bx-term-bg, #0B0C12);
            font-family:var(--bx-mono, "JetBrains Mono", ui-monospace, monospace); font-size:var(--bx-term-size, 12px)}
          /* product-ui §7: 8px 12px around the screen (fit measures inside
             it); ligatures off: a program's -> and != are drawn as typed */
          .host .xterm{padding:8px 12px; font-variant-ligatures:none; font-feature-settings:"liga" 0, "calt" 0}
          button, select{font:inherit}
          :focus-visible{outline:var(--bx-focus-outline, 3px solid #3DD6F5); outline-offset:var(--bx-focus-offset, 2px);
            box-shadow:var(--bx-focus-halo, 0 0 0 2px #0B0C12)}
          .gear, .lag{position:absolute; top:6px; z-index:8; box-sizing:border-box; height:24px; padding:0;
            border:1px solid var(--bx-border, #33353F); border-radius:var(--bx-radius, 2px); cursor:pointer;
            background:var(--bx-panel, #1F2028); color:var(--bx-muted, #A3A6B6);}
          .gear{right:12px; width:24px; display:grid; place-items:center; opacity:0;
            transition:opacity var(--bx-dur-ui, 120ms) var(--bx-ease-out, cubic-bezier(0.16, 1, 0.3, 1));}
          :host(:hover) .gear, .gear:focus-visible, .gear.open{opacity:1}
          .gear:hover, .gear.open{color:var(--bx-text, #E9EAF0); background:var(--bx-hover, #2A2B34)}
          .gear svg, .lag svg{display:block; flex:none}
          .tmenu{position:absolute; top:34px; right:12px; z-index:9; min-width:232px; box-sizing:border-box;
            background:var(--bx-panel, #1F2028); color:var(--bx-text, #E9EAF0);
            border:1px solid var(--bx-border, #33353F); border-radius:var(--bx-radius, 2px); padding:8px var(--bx-pad, 12px);
            box-shadow:var(--bx-shadow-pop, 0 12px 32px rgba(0, 0, 0, 0.6)); font:var(--bx-font, 13px/18px "Instrument Sans", system-ui, sans-serif);}
          .tmenu[hidden]{display:none}
          .tmenu .hd{font:var(--bx-font-micro, 600 11px/14px "Instrument Sans", system-ui, sans-serif);
            letter-spacing:var(--bx-tracking-micro, 0.06em); text-transform:uppercase; color:var(--bx-muted, #A3A6B6); margin:0 0 6px;}
          .tmenu .row{display:flex; align-items:center; justify-content:space-between; gap:8px; margin:4px 0;}
          .tmenu select{flex:1; min-width:0; box-sizing:border-box; height:var(--bx-control-h, 28px); padding:0 6px;
            border:1px solid var(--bx-border-strong, #666A7E); border-radius:var(--bx-radius, 2px);
            background:var(--bx-panel, #1F2028); color:var(--bx-text, #E9EAF0);}
          .tmenu .fs{display:flex; align-items:center; gap:6px;}
          .tmenu .fs b{min-width:30px; text-align:center; font-weight:600; font-variant-numeric:tabular-nums;}
          .tmenu .step{width:var(--bx-control-h, 28px); height:var(--bx-control-h, 28px); padding:0;
            display:grid; place-items:center; border:1px solid var(--bx-border-strong, #666A7E);
            border-radius:var(--bx-radius, 2px); background:var(--bx-panel, #1F2028); color:var(--bx-text, #E9EAF0); cursor:pointer;}
          .tmenu .step:hover{background:var(--bx-hover, #2A2B34);}
          .tmenu .pstat{font:var(--bx-font-meta, 400 12px/16px "Instrument Sans", system-ui, sans-serif);
            font-variant-numeric:tabular-nums; color:var(--bx-muted, #A3A6B6); margin:2px 0 0;}
          /* the slow-link chip: predictive echo is drawing what you type */
          .lag{right:42px; display:flex; align-items:center; gap:4px; padding:0 7px; user-select:none;
            font:var(--bx-font-meta, 400 12px/16px "Instrument Sans", system-ui, sans-serif); font-variant-numeric:tabular-nums;}
          .lag:hover{color:var(--bx-text, #E9EAF0); background:var(--bx-hover, #2A2B34)}
          .lag[hidden]{display:none}
          /* the prediction overlay: a layer over xterm's screen, one span per run
             of predicted cells, placed by the renderer's cell metrics (works in
             the alternate buffer too, where xterm hides its own decorations) */
          .pov{position:absolute; left:0; top:0; right:0; bottom:0; z-index:7; pointer-events:none; overflow:hidden;}
          .pov[hidden]{display:none}
          .pov span{position:absolute; white-space:pre; overflow:hidden; font-kerning:none; box-sizing:border-box;}
          /* while a predicted cursor is drawn, the real one steps aside (xterm's
             own rules are (0,5,0) with !important; these outrank them) */
          :host(.pcur) .host .xterm .xterm-screen .xterm-rows .xterm-cursor.xterm-cursor-block{background-color:transparent !important; color:var(--bxp-fg) !important;}
          :host(.pcur) .host .xterm .xterm-screen .xterm-rows .xterm-cursor.xterm-cursor-outline{outline:none !important;}
          :host(.pcur) .host .xterm .xterm-screen .xterm-rows .xterm-cursor.xterm-cursor-bar{box-shadow:none !important;}
          :host(.pcur) .host .xterm .xterm-screen .xterm-rows .xterm-cursor.xterm-cursor-underline{border-bottom:0 !important; height:100% !important;}
        </style>` +
        `<div class="host"></div>` +
        `<button class="gear" title="terminal settings" aria-label="terminal settings" aria-haspopup="true">${iconSvg('settings')}</button>` +
        `<button class="lag" hidden title="slow link: typed text is shown before the server confirms it (predictive echo, terminal settings)">${iconSvg('bolt')}<span class="lagt"></span></button>` +
        `<div class="tmenu" hidden>` +
          `<div class="hd">terminal</div>` +
          `<div class="row"><span>Theme</span><select class="theme" aria-label="terminal theme"></select></div>` +
          `<div class="row"><span>Font size</span>` +
            `<span class="fs"><button class="step" data-d="-1" aria-label="smaller" title="smaller">${iconSvg('minus')}</button>` +
            `<b class="fsv"></b>` +
            `<button class="step" data-d="1" aria-label="larger" title="larger">${iconSvg('plus')}</button></span></div>` +
          `<div class="row"><span>Predictive echo</span><select class="predict" aria-label="predictive echo">` +
            `<option value="auto">auto · on when RTT &gt; ${SRTT_SHOW} ms</option>` +
            `<option value="on">on</option><option value="off">off</option></select></div>` +
          `<div class="pstat"></div>` +
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
    clearInterval(this.#pingTimer);
    this.#term?.dispose();
    if (this.#onPref) window.removeEventListener('bx-term-pref', this.#onPref);
    if (this.#onStorage) window.removeEventListener('storage', this.#onStorage);
    if (this.#onAmbient) window.removeEventListener('bx-ambient-zoom', this.#onAmbient);
    this.#offLook?.(); this.#offLook = null;
    if (this.#onFonts) document.fonts?.removeEventListener?.('loadingdone', this.#onFonts);
  }

  // --- settings (theme + font size) ---------------------------------------

  // #themeObj resolves a palette's name to an xterm theme object; 'default'
  // is the workspace's, read from the --bx-term-* tokens at this element.
  #themeObj(name = savedTheme()) {
    return paletteFor(name, (t) => token(t, this));
  }

  #applyTheme(name = savedTheme()) {
    name = themeName(name);
    const theme = this.#theme = this.#themeObj(name);
    if (this.#term) this.#term.options.theme = theme;
    if (this.#host) this.#host.style.background = theme.background;
    const sel = this.shadowRoot?.querySelector('.theme');
    if (sel && sel.value !== name) sel.value = name;
    this.#redraw();
  }

  // #followLook: the person's appearance changed (the shell's theme or
  // density, the system's light/dark): the workspace palette and the
  // density's size are read again; a palette or a size the person picked stays.
  #followLook() {
    if (savedTheme() === 'default') this.#applyTheme('default');
    if (savedFontSize() === null) {
      const n = sizeOf(this);
      if (n !== this.#baseFont) { this.#baseFont = n; this.#applyFont(); }
      const fsv = this.shadowRoot?.querySelector('.fsv');
      if (fsv) fsv.textContent = String(this.#baseFont);
    }
  }

  // #remeasure: a face that loaded after xterm measured its cells (a slow
  // link past fontsLoaded's wait): xterm measures again only when a font
  // option changes, so the family is set again (a leading space is the same
  // CSS family list), then the grid refits.
  #remeasure() {
    const t = this.#term;
    if (!t) return;
    const f = t.options.fontFamily;
    t.options.fontFamily = f.startsWith(' ') ? f.trimStart() : ` ${f}`;
    try { this.#fit.fit(); } catch { }
    this.#redraw();
  }

  // The user's font size is the BASE; xterm renders at base × ambient zoom (the
  // host counter-zooms so the net is 1). Changing either re-applies the product.
  #setFontSize(next) {
    next = Math.max(7, Math.min(28, next));
    if (next === this.#baseFont) return;
    this.#baseFont = next;
    try { localStorage.setItem('bx-term-fontsize', String(next)); } catch { }
    this.#applyFont();
  }

  #applyFont() {
    if (!this.#term) return;
    const eff = Math.max(7, Math.min(44, Math.round(this.#baseFont * this.#ambient)));
    if (this.#term.options.fontSize !== eff) this.#term.options.fontSize = eff;
    try { this.#fit.fit(); } catch { }
    this.#redraw(); // decorations are sized when made
  }

  // #applyAmbient counters an ancestor's CSS zoom so xterm renders at net-zoom-1
  // (exact mouse math) while staying the same visual size, via a larger font.
  #applyAmbient(z) {
    z = Number(z) > 0 ? Number(z) : 1;
    this.#ambient = z;
    this.style.zoom = Math.abs(z - 1) < 0.01 ? '' : String(1 / z);
    this.#applyFont();
  }

  // #syncAmbient re-detects the ancestor zoom and re-applies only if it changed
  // (so it's safe to call from the ResizeObserver — applying zoom resizes us,
  // which would otherwise loop).
  #syncAmbient() {
    const z = this.#detectAmbient();
    if (Math.abs(z - this.#ambient) > 0.005) this.#applyAmbient(z);
  }

  // #detectAmbient computes the cumulative CSS `zoom` an ANCESTOR applies to
  // this terminal, crossing shadow-DOM boundaries and excluding our own
  // counter-zoom. Self-contained on purpose: the terminal ships with xbind but
  // the shell that zooms it is workspace-owned, so relying on the shell to
  // announce its zoom would leave the fix half-applied on an un-updated
  // workspace. (Verified against Chromium; see scratch zoom tests.)
  #detectAmbient() {
    let z = 1, n = this;
    for (;;) {
      let parent = n.assignedSlot || n.parentNode;
      if (parent && parent.nodeType === 11) parent = parent.host; // ShadowRoot → host
      if (!parent || parent.nodeType !== 1) break;                // reached the document
      n = parent;
      const zz = parseFloat(getComputedStyle(n).zoom);
      if (zz > 0) z *= zz;
    }
    return z > 0 ? z : 1;
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
    const fontNow = () => this.#baseFont; // the user's size, not the zoom-scaled effective one
    const showFs = () => { fsv.textContent = String(fontNow()); };
    showFs();
    const psel = root.querySelector('.predict');
    psel.value = savedPredict();
    this.#pred.setMode(psel.value);
    psel.addEventListener('change', () => this.#setPredict(psel.value));
    root.querySelector('.lag').addEventListener('click', (e) => { e.stopPropagation(); gear.click(); });

    const onDoc = (e) => {
      if (!e.composedPath().includes(menu) && !e.composedPath().includes(gear)) close();
    };
    const close = () => {
      menu.hidden = true;
      gear.classList.remove('open');
      gear.setAttribute('aria-expanded', 'false');
      document.removeEventListener('pointerdown', onDoc, true);
    };
    gear.addEventListener('click', (e) => {
      e.stopPropagation();
      if (menu.hidden) {
        sel.value = savedTheme();
        showFs();
        psel.value = this.#pred.mode;
        this.#status();
        menu.hidden = false;
        gear.classList.add('open');
        gear.setAttribute('aria-expanded', 'true');
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
      window.dispatchEvent(new CustomEvent('bx-term-pref', { detail: { fontSize: this.#baseFont } }));
    }));

    // Live-sync from another terminal in this document, or another tab.
    this.#onPref = (e) => {
      if (e.detail?.theme) this.#applyTheme(e.detail.theme);
      if (e.detail?.fontSize) this.#setFontSize(e.detail.fontSize);
      if (e.detail?.predict) this.#setPredict(e.detail.predict, false);
      showFs();
    };
    window.addEventListener('bx-term-pref', this.#onPref);
    this.#onStorage = (e) => {
      if (e.key === 'bx-term-theme') this.#applyTheme(savedTheme());
      if (e.key === 'bx-term-fontsize') {
        const n = savedFontSize();
        if (n === null) this.#followLook(); else this.#setFontSize(n);
        showFs();
      }
      if (e.key === 'bx-term-predict') this.#setPredict(savedPredict(), false);
    };
    window.addEventListener('storage', this.#onStorage);
    // the person's appearance (D184): the workspace palette and the density's size follow it
    this.#offLook?.();
    this.#offLook = onAppearance(() => this.#followLook());
  }

  // --- predictive echo (D70) ----------------------------------------------

  #setPredict(mode, broadcast = true) {
    if (!PREDICT_MODES.includes(mode)) return;
    if (broadcast) {
      try { localStorage.setItem('bx-term-predict', mode); } catch { }
      window.dispatchEvent(new CustomEvent('bx-term-pref', { detail: { predict: mode } }));
    }
    this.#pred.setMode(mode);
    const psel = this.shadowRoot?.querySelector('.predict');
    if (psel && psel.value !== mode) psel.value = mode;
    this.#status();
    this.#redraw();
  }

  #rttText() { return this.#srtt == null ? 'RTT —' : `RTT ${Math.round(this.#srtt)} ms`; }
  #status() {
    const el = this.shadowRoot?.querySelector('.pstat');
    if (!el) return;
    el.textContent = !this.#echoAck ? (this.#ws ? (this.getAttribute('src') ? 'not offered by this terminal' : 'not supported by this xbind') : 'connecting…')
      : `${this.#rttText()}${this.#pred.shown() ? ' · predicting' : ''}`;
  }

  // The engine's view of the screen: xterm's active buffer (the alternate one
  // too — full-screen programs), screen rows.
  #fb() {
    const t = this.#term, b = t?.buffer.active;
    if (!b) return null;
    const nc = this.#nullCell ??= b.getNullCell();
    const cell = (r, c) => b.getLine(b.baseY + r)?.getCell(c, nc);
    return {
      rows: t.rows, cols: t.cols, cursor: { row: b.cursorY, col: b.cursorX },
      charAt: (r, c) => cell(r, c)?.getChars() ?? '',
      widthAt: (r, c) => cell(r, c)?.getWidth() ?? 1,
      lineAt: (r) => b.getLine(b.baseY + r)?.translateToString(false) ?? '',
    };
  }

  // #redraw validates the predictions against the screen and rebuilds the
  // overlay: one span per run of predicted cells (opaque, in the terminal's
  // colours, underlined when the link is slow) and one for the predicted
  // cursor, placed by the DOM renderer's cell metrics (.xterm-rows is exactly
  // cols × rows cells). Hidden while the user has scrolled back.
  #redraw() {
    const term = this.#term;
    if (!term?.element) return;
    const fb = this.#fb();
    if (fb) this.#pred.cull(fb, performance.now());
    const r = fb ? this.#pred.render(fb) : { cells: [], cursor: null };
    this.#overlay = r.cells;
    const scr = term.element.querySelector('.xterm-screen'), rows = scr?.querySelector('.xterm-rows');
    if (!scr || !rows) return;
    let layer = this.#layer;
    if (!layer || layer.parentNode !== scr) { layer = this.#layer = document.createElement('div'); layer.className = 'pov'; scr.appendChild(layer); }
    const b = term.buffer.active;
    layer.hidden = b.viewportY !== b.baseY;
    const cs = getComputedStyle(rows);
    const cw = parseFloat(cs.width) / term.cols, chh = parseFloat(cs.height) / term.rows;
    const theme = this.#theme ??= this.#themeObj();
    const bg = theme.background, fg = theme.foreground || cs.color;
    this.style.setProperty('--bxp-fg', fg);
    const spans = [];
    const put = (row, col, text, style) => {
      const el = document.createElement('span');
      Object.assign(el.style, { left: `${col * cw}px`, top: `${row * chh}px`, width: `${text.length * cw}px`, height: `${chh}px`, lineHeight: `${chh}px`,
        fontFamily: cs.fontFamily, fontSize: cs.fontSize, letterSpacing: cs.letterSpacing, ...style });
      el.textContent = text;
      spans.push(el);
    };
    for (const c of r.cells) put(c.row, c.col, c.text, { background: bg, color: fg, textDecoration: c.underline ? 'underline' : 'none' });
    if (r.cursor) {
      const run = r.cells.find((c) => c.row === r.cursor.row && c.col <= r.cursor.col && r.cursor.col < c.col + c.text.length);
      const ch = run ? run.text[r.cursor.col - run.col] : (fb.charAt(r.cursor.row, r.cursor.col) || ' ');
      put(r.cursor.row, r.cursor.col, ch, { background: theme.cursor || fg, color: theme.cursorAccent || bg });
    }
    layer.replaceChildren(...spans);
    this.classList.toggle('pcur', !!r.cursor);
    const lag = this.shadowRoot.querySelector('.lag');
    if (lag) {
      lag.hidden = !(this.#pred.shown() && (this.#pred.srttTrigger || this.#pred.glitchTrigger > 0));
      const t = lag.querySelector('.lagt'), words = this.#rttText().slice(4);
      if (t && t.textContent !== words) t.textContent = words;
    }
  }

  // An ack is applied through xterm's write queue: term.write is asynchronous,
  // and the ack must be judged against a screen that includes every output
  // frame that preceded it on the wire.
  #onAck(n) {
    if (this.#hold) { this.#hold.n = n; return; }
    this.#applyAck(n);
  }
  #applyAck(n) {
    const gen = this.#gen;
    this.#term.write('', () => {
      if (gen !== this.#gen) return; // a newer socket renumbered everything
      this.#pred.setLateAck(n);
      this.#redraw();
    });
  }

  #ping() {
    if (this.#ws?.readyState === WebSocket.OPEN && this.#echoAck) this.#ws.send(JSON.stringify({ op: 'ping', t: performance.now() }));
  }
  #onPong(t) {
    const r = performance.now() - Number(t);
    if (!(r >= 0 && r < 60000)) return;
    this.#srtt = srttUpdate(this.#srtt, r);
    this.#pred.setSrtt(this.#srtt);
    this.#status();
    this.#redraw();
  }

  // Switching network scope can't hot-reload (the netns/relay is fixed at
  // spawn), so a live change to `net` restarts the session: drop the current
  // session id and reconnect, which asks xbind for a fresh shell in the new
  // scope. (The caller is expected to have already ended the old session.)
  static get observedAttributes() { return ['net', 'gpu', 'api', 'vm', 'deployment', 'src']; }
  attributeChangedCallback(name, oldV, newV) {
    // another src is another terminal: start over there (after an exit too)
    if (name === 'src') {
      if (oldV === newV || !this.#term || !this.isConnected) return;
      this.#closed = false;
      this.#restart(newV ? 'connecting…' : '');
      return;
    }
    if (!this.#term) return;
    // An absent target is the primary: its appearing or going is a change.
    if (name === 'deployment' ? (oldV || '') === (newV || '') : oldV === null || oldV === newV) return;
    // The server reports the EFFECTIVE scope, target and API in its session
    // frame (it may clamp what was asked — D54 — or fall back — D127p);
    // mirroring that into the attribute must not respawn the shell we just got.
    if (name === 'net' && newV === this.#serverNet) return;
    if (name === 'deployment' && (newV || '') === this.#serverDep) return;
    if (name === 'api' && newV === this.#serverApi) return;
    const msg = name === 'gpu' ? `switching GPU → ${newV}…`
      : name === 'vm' ? (newV === '1' ? 'starting a VM…' : 'leaving the VM…')
      : name === 'api' ? `${newV === '0' ? 'disabling' : 'enabling'} tile API…`
      : name === 'deployment' ? `switching the target → ${newV || 'the primary'}…`
        : `switching network → ${newV === 'org' || newV === 'personal' ? newV + ' network' : newV}…`;
    // One render may change several (the target and the API, the VM and the
    // network): one restart, named by the last.
    const pending = this.#restartMsg !== null;
    this.#restartMsg = msg;
    if (!pending) queueMicrotask(() => { const m = this.#restartMsg; this.#restartMsg = null; if (!this.#closed) this.#restart(m); });
  }

  // src: the terminal-wire endpoint this terminal dials instead of /ws/term
  // (docs/elements.md); '' = xbind's /ws/term.
  get src() { return this.getAttribute('src') || ''; }
  set src(v) { if (v) this.setAttribute('src', String(v)); else this.removeAttribute('src'); }

  // restartFresh drops the current session and reconnects a brand-new one — used
  // after the persistent sandbox layer is reset out from under it.
  restartFresh() { if (this.#term) this.#restart('resetting sandbox…'); }

  #restart(msg) {
    this.removeAttribute('session');
    this.#retries = 0; this.#failed = 0; // a fresh start: its own retries (another src after one gave up)
    if (msg) this.#term.write(`\r\n\x1b[90m[${msg}]\x1b[0m\r\n`);
    const old = this.#ws;
    this.#ws = null;
    if (old) { old.onclose = null; old.close(); }
    this.#connect();
  }

  async #start() {
    await loadXterm();
    // xterm measures its scrollbar once, when it opens (the thin 6px bar,
    // D123): let the shadow root's xterm.css apply first, or it reads 0 and
    // assumes 15px (a link that already failed never fires again: capped)
    const css = this.shadowRoot.querySelector('link[rel="stylesheet"]');
    if (css && !css.sheet) await new Promise((r) => { css.addEventListener('load', r, { once: true }); css.addEventListener('error', r, { once: true }); setTimeout(r, 2000); });
    if (this.#closed) return;
    this.#baseFont = savedFontSize() ?? sizeOf(this);
    this.#ambient = this.#detectAmbient();
    const fontSize = Math.max(7, Math.min(44, Math.round(this.#baseFont * this.#ambient)));
    // the face: the --bx-mono token as .host resolves it (its fallback where
    // a document has no theme.css), loaded before xterm measures a cell
    const fontFamily = getComputedStyle(this.#host).fontFamily;
    await fontsLoaded(fontFamily, fontSize);
    if (this.#closed) return;
    this.#theme = this.#themeObj();
    this.#term = new window.Terminal({
      fontSize, fontFamily,
      // bold is weight (700), never a brighter colour (D184, product-ui §7)
      fontWeight: 400, fontWeightBold: 700, drawBoldTextInBrightColors: false,
      // the palette the person picked in the settings menu, or the
      // workspace's from the --bx-term-* tokens (web/term-palettes.js)
      theme: this.#theme,
      scrollback: 4000,
    });
    this.#fit = new window.FitAddon.FitAddon();
    this.#term.loadAddon(this.#fit);
    // URLs a CLI prints (an agent's sign-in link, a dev-server address) open
    // in one click, whole however it drew them: OSC 8, rows it broke, OSC 52
    // copy (term-links.js, D178).
    wireLinks(this.#term, { focused: () => this.shadowRoot?.activeElement === this.#term?.textarea });
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
    this.#host.style.background = this.#theme.background; // the padding around the screen, in the palette's bg
    this.#applyAmbient(this.#ambient); // counter the ancestor zoom + fit
    // the terminal's face arriving after the wait above: measure the cells again
    const face = fontFamily.split(',')[0].replace(/["']/g, '').trim();
    this.#onFonts = (e) => {
      if (!this.#closed && [...(e.fontfaces || [])].some((f) => f.family.replace(/["']/g, '').trim() === face)) this.#remeasure();
    };
    document.fonts?.addEventListener?.('loadingdone', this.#onFonts);
    // Follow the workspace font-size setting: it zooms the whole shell, but the
    // terminal counters that and re-scales via its font so xterm's mouse math
    // stays exact (see #applyAmbient). The ResizeObserver re-detects on any
    // layout change (a zoom change reflows us), so this works even on a
    // workspace whose shell predates the bx-ambient-zoom event; the event is
    // just a faster, jitter-free trigger when the shell does emit it.
    this.#onAmbient = () => this.#syncAmbient();
    window.addEventListener('bx-ambient-zoom', this.#onAmbient);
    this.#ro = new ResizeObserver(() => { this.#syncAmbient(); try { this.#fit.fit(); } catch { } });
    this.#ro.observe(this);
    // ctrl+scroll = font size (shared preference across all terminals).
    this.#host.addEventListener('wheel', (e) => {
      if (!e.ctrlKey) return;
      e.preventDefault();
      const next = this.#baseFont + (e.deltaY < 0 ? 1 : -1);
      if (next === this.#baseFont) return;
      this.#setFontSize(next);
      window.dispatchEvent(new CustomEvent('bx-term-pref', { detail: { fontSize: this.#baseFont } }));
    }, { passive: false });
    this.#term.onData((d) => {
      if (this.#ws?.readyState !== WebSocket.OPEN) return;
      // predict first (the prediction expires with the frame about to be sent), then send
      const fb = this.#echoAck ? this.#fb() : null;
      if (fb) this.#pred.newUserData(d, fb, performance.now());
      this.#seq++;
      this.#ws.send(enc.encode(d));
      this.#pred.setLocalFrameSent(this.#seq);
      if (fb) this.#redraw();
    });
    this.#term.onResize(({ cols, rows }) => {
      if (this.#ws?.readyState === WebSocket.OPEN) {
        this.#ws.send(JSON.stringify({ op: 'resize', cols, rows }));
      }
      this.#redraw();
    });
    this.#term.onWriteParsed(() => this.#redraw()); // the screen changed: judge and redraw the overlay
    this.#term.onScroll(() => this.#redraw());      // scrolled back: the overlay hides
    this.#term.buffer.onBufferChange(() => { this.#pred.reset(); this.#redraw(); this.#flushNote(); });
    // A program hiding the cursor (DECTCEM off) switches the engine to anchor
    // mode; showing it, or a full reset, switches back. Only the flag flips
    // here — the onWriteParsed redraw follows the same parse.
    const dectcem = (hidden) => (ps) => { if (ps.some((p) => p === 25)) this.#pred.setCursorHidden(hidden); return false; };
    this.#term.parser.registerCsiHandler({ prefix: '?', final: 'l' }, dectcem(true));
    this.#term.parser.registerCsiHandler({ prefix: '?', final: 'h' }, dectcem(false));
    this.#term.parser.registerEscHandler({ final: 'c' }, () => { this.#pred.setCursorHidden(false); return false; });
    this.#connect();
    this.#flushNote();
  }

  // note(text): a grey line from the terminal window, framed like the net
  // note — the tile's live reload state changed (frame-deploy.js). Written at
  // once on the normal buffer; while a full-screen program (vim, less, a TUI)
  // holds the alternate buffer only the latest note is kept, and written when
  // it exits, so no screen is ever corrupted. Control characters are dropped.
  note(text) {
    this.#note = `\r\n\x1b[90m[${String(text).replace(/[\x00-\x1f\x7f]/g, '')}]\x1b[0m\r\n`;
    this.#flushNote();
  }
  #flushNote() {
    if (!this.#note || !this.#term || this.#term.buffer.active.type !== 'normal') return;
    this.#term.write(this.#note);
    this.#note = null;
  }

  // end(text): the terminal window ends this session — the target it asked
  // for came back without the server's echo (frame-deploy.js): a red line,
  // the socket closed, the session ended server-side, no reconnect.
  end(text) {
    this.#closed = true;
    this.#term?.write(`\r\n\x1b[31m[${String(text).replace(/[\x00-\x1f\x7f]/g, '')}]\x1b[0m\r\n`);
    const id = this.getAttribute('session'), ws = this.#ws;
    this.#ws = null;
    if (ws) { ws.onclose = null; ws.close(); }
    if (id) fetch(`/ws/term?session=${encodeURIComponent(id)}`, { method: 'DELETE' }).catch(() => { });
  }

  #connect() {
    this.#opened = false;
    // Claim a new epoch. Any earlier socket (e.g. one the server just killed on
    // a base-image reset) is now stale: its onclose must not schedule a
    // reconnect, and its late frames must not be written to the term — otherwise
    // two sockets end up on one session and every byte (incl. keystroke echo)
    // is doubled.
    const gen = ++this.#gen;
    // input frames are numbered per socket (the server counts what it receives)
    this.#seq = 0; this.#echoAck = false; this.#pred.reset(); this.#pred.setLocalFrameSent(0); this.#pred.setLateAck(0);
    clearInterval(this.#pingTimer); this.#pingTimer = null;
    this.#status();
    const src = this.getAttribute('src');
    this.#asked = ''; // a src terminal names no target; #dialTerm sets it
    const ws = src ? this.#dialSrc(src) : this.#dialTerm();
    if (!ws) return;
    ws.binaryType = 'arraybuffer';
    this.#ws = ws;

    ws.onopen = () => {
      if (gen !== this.#gen) { ws.close(); return; } // superseded before it opened
      if (!src) this.#retries = 0; // src: only once it stayed open (#closedSrc)
      this.#opened = true;
      this.#openedAt = performance.now();
      this.#failed = 0;
      this.#reattachFails = 0;
      const { cols, rows } = this.#term;
      ws.send(JSON.stringify({ op: 'resize', cols, rows }));
      this.#term.focus();
    };
    ws.onmessage = (m) => this.#onFrame(m, ws, gen, src);
    ws.onclose = (ev) => {
      if (this.#closed || gen !== this.#gen) return; // stale socket (superseded) → don't reconnect
      if (src) { this.#closedSrc(ev, gen); return; }
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

  // #dialTerm opens xbind's /ws/term: a reattach by session id, or a new
  // session with the scope attributes.
  #dialTerm() {
    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
    this.#asked = this.getAttribute('session') ? '' : this.getAttribute('deployment') || ''; // a reattach asks for nothing
    const q = this.getAttribute('session')
      ? `session=${encodeURIComponent(this.getAttribute('session'))}`
      : `cwd=${encodeURIComponent(this.getAttribute('cwd') || '')}` +
        // net is sent only when chosen; absent = the server's default for this
        // tile (the org network on org-owned tiles, D54).
        (this.getAttribute('net') ? `&net=${encodeURIComponent(this.getAttribute('net'))}` : '') +
        `&gpu=${encodeURIComponent(this.getAttribute('gpu') || 'none')}` +
        // the target is sent only when chosen; absent = the server's default (D127p)
        (this.#asked ? `&deployment=${encodeURIComponent(this.#asked)}` : '') +
        `&api=${this.getAttribute('api') === '0' ? '0' : '1'}` +
        (this.getAttribute('vm') === '1' ? '&vm=1' : '');
    return new WebSocket(`${proto}//${location.host}/ws/term?${q}`);
  }

  // #dialSrc opens src (or, with a session to go back to, where it
  // reattaches): a path on this host with the page's credential — the frame
  // token in a sandboxed tile (xbin.ws), the session cookie in chrome — any
  // other ws(s) URL as it is.
  #dialSrc(src) {
    const t = srcTarget(reattachSrc(src, this.getAttribute('session')), document.baseURI);
    try {
      if (!t) throw new Error(`not a terminal address: ${src}`);
      if (t.url) return new WebSocket(t.url);
      const x = globalThis.xbin;
      if (sandboxed() && x && x.ws) return x.ws(t.path);
      return new WebSocket(`${location.protocol === 'https:' ? 'wss:' : 'ws:'}//${location.host}${t.path}`);
    } catch (e) {
      this.#term.write(`\r\n\x1b[31m[${e.message}]\x1b[0m\r\n`);
      return null;
    }
  }

  // #closedSrc: a src socket closed without an exit frame. A clean close ends
  // the terminal; a drop reconnects with backoff — to the same session where
  // src names its sessions — starting over only once a socket stayed open
  // LIVED ms. A handshake that never opened (refused: the page can't see
  // why, or unreachable) is retried once; a reattach that fails twice starts
  // a fresh session instead.
  #closedSrc(ev, gen) {
    const src = this.getAttribute('src');
    if (this.#opened && endedByClose(ev.code)) { this.#ended('connection closed'); return; }
    if (this.#opened && performance.now() - this.#openedAt >= LIVED) this.#retries = 0;
    const reattaching = !!this.getAttribute('session') && canReattach(src);
    if (!this.#opened) {
      if (reattaching && ++this.#reattachFails >= 2) { this.#restart('previous session gone — starting fresh…'); return; }
      if (!reattaching && ++this.#failed >= 2) {
        this.#term.write('\r\n\x1b[31m[could not open the terminal — refused, or out of reach]\x1b[0m\r\n');
        return;
      }
    }
    if (this.#retries >= RETRIES) { this.#term.write('\r\n\x1b[31m[disconnected]\x1b[0m\r\n'); return; }
    const wait = backoff(this.#retries++);
    if (this.#opened) this.#term.write(`\r\n\x1b[90m[reconnecting…]\x1b[0m\r\n`);
    setTimeout(() => { if (!this.#closed && gen === this.#gen) this.#connect(); }, wait);
  }

  // #ended: the session is over (an exit frame, a clean close) — say so, and
  // let the host decide (bx-exit).
  #ended(words, detail = {}) {
    this.#closed = true;
    clearInterval(this.#pingTimer);
    if (this.getAttribute('src')) this.#term.write(`\r\n\x1b[90m[${words}]\x1b[0m\r\n`);
    this.dispatchEvent(new CustomEvent('bx-exit', { bubbles: true, detail }));
  }

  // #onFrame handles one message from the server (either wire flavour).
  #onFrame(m, ws, gen, src) {
    if (gen !== this.#gen) { ws.close(); return; } // a stale socket must not drive the term
    if (typeof m.data === 'string') {
      let ctl; try { ctl = JSON.parse(m.data); } catch { return; }
      if (ctl.op === 'session') {
        this.setAttribute('session', ctl.id);
        // a one-shot command to run on first connect (e.g. a sign-in), typed
        // into the pty so its output (a clickable URL) is right there
        if (!this.#ranInit) {
          const run = this.getAttribute('run');
          // binary, as typed input is: a text frame is control JSON, and
          // anything else in one is dropped (internal/termwire)
          if (run) { this.#ranInit = true; try { this.#ws?.send(enc.encode(run + '\n')); } catch { } }
        }
        if (ctl.net) { this.#serverNet = ctl.net; this.setAttribute('net', ctl.net); }
        this.#serverDep = ctl.deployment || '';
        this.#serverApi = typeof ctl.api === 'boolean' ? (ctl.api ? '1' : '0') : null;
        // A clamp note ("host networking is admin-only — using the org
        // network"), the session's own target note ("this terminal calls
        // apps/crm+dev") and a partitioned tile's partition note are worth
        // one gray line each, once per session; the pickers show the rest.
        if ((ctl.netNote || ctl.targetNote || ctl.partitionNote) && ctl.id !== this.#notedSession) {
          this.#notedSession = ctl.id;
          for (const n of [ctl.netNote, ctl.targetNote, ctl.partitionNote]) if (n) this.#term.write(`\r\n\x1b[90m[${n}]\x1b[0m\r\n`);
        }
        this.dispatchEvent(new CustomEvent('bx-session', {
          detail: { id: ctl.id, net: ctl.net, scopes: ctl.scopes, label: ctl.label, netNote: ctl.netNote, baseOutdated: !!ctl.baseOutdated, vm: !!ctl.vm,
            deployment: this.#serverDep, asked: this.#asked, ...(this.#serverApi ? { api: ctl.api } : {}), ...(ctl.partition ? { partition: ctl.partition } : {}) },
          bubbles: true }));
        // this xbind acks input and answers pings: measure the link, keep measuring
        this.#echoAck = !!ctl.echoAck;
        this.#status();
        if (this.#echoAck) {
          this.#ping();
          this.#pingTimer = setInterval(() => { if (document.visibilityState === 'visible') this.#ping(); }, 5000);
        }
      } else if (ctl.op === 'ack') {
        if (typeof ctl.n === 'number') this.#onAck(ctl.n);
      } else if (ctl.op === 'pong') {
        this.#onPong(ctl.t);
      } else if (ctl.op === 'exit') {
        // Shell exited — the session is gone server-side. Let the host close
        // this terminal (its tab/window), like a real terminal emulator; a
        // src terminal says how it ended (its host may keep it open).
        if (src) { this.#ended(exitWords(ctl), { code: ctl.code ?? null, signal: ctl.signal || '' }); return; }
        this.#closed = true;
        this.dispatchEvent(new CustomEvent('bx-exit', { bubbles: true }));
      }
      return;
    }
    this.#term.write(new Uint8Array(m.data));
  }

  // testApi: stable names for the UI harness (hack/ui-harness), which may not
  // touch private state. Reads and writes existing state; nothing here is
  // used by the element itself.
  testApi() {
    const t = this;
    return {
      get rtt() { return t.#srtt; },
      get echoAck() { return t.#echoAck; },
      get mode() { return t.#pred.mode; },
      setPredict: (m) => t.#setPredict(m),
      get predicting() { return t.#pred.shown(); },
      get pending() { return t.#pred.pending(); },
      get overlay() { return t.#overlay; },
      get cursorHidden() { return t.#pred.cursorHidden; },
      get anchor() { return t.#pred.anchor; },
      get cursor() { const b = t.#term?.buffer.active; return b ? { row: b.cursorY, col: b.cursorX } : null; },
      screenLine: (row) => { const b = t.#term?.buffer.active; return b?.getLine(b.baseY + row)?.translateToString(true) ?? ''; },
      // the whole buffer (scrollback too) as text, a line per logical line
      // (a row the terminal wrapped joins the one before); the grid; the socket's state
      text: () => {
        const b = t.#term?.buffer.active, out = [];
        for (let i = 0; b && i < b.length; i++) {
          const l = b.getLine(i);
          if (l?.isWrapped && out.length) out[out.length - 1] += l.translateToString(true);
          else out.push(l?.translateToString(true) ?? '');
        }
        return out.join('\n');
      },
      get size() { return t.#term ? { cols: t.#term.cols, rows: t.#term.rows } : null; },
      get open() { return t.#ws?.readyState === WebSocket.OPEN; },
      // hold acks (the newest is applied on release): a local PTY echoes
      // within a millisecond, so this is how a pass sees a prediction pending
      holdAcks(on) {
        if (on) { t.#hold ??= { n: null }; return; }
        const n = t.#hold?.n; t.#hold = null;
        if (n != null) t.#applyAck(n);
      },
      ping: () => t.#ping(),
      // links (term-links.js): the joined URLs on screen row `row`, a cell's
      // centre in client pixels (a pass hovers and clicks it), the OSC 8 handler
      links: (row) => { const b = t.#term?.buffer.active; return b ? joinedLinksAt((j) => rowOf(t.#term, j), t.#term.cols, b.baseY + row) : []; },
      cellPoint: (row, col) => { const r = t.#term?.element?.querySelector('.xterm-screen')?.getBoundingClientRect(); return r ? { x: r.left + (col + 0.5) * r.width / t.#term.cols, y: r.top + (row + 0.5) * r.height / t.#term.rows } : null; },
      get linkHandler() { return !!t.#term?.options.linkHandler; },
    };
  }
}

customElements.define('bx-terminal', BxTerminal);
