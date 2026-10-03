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
 *   component  — the tile path whose logs to stream (required)
 *   deployment — optional: a non-primary tile deployment of it, whose own log
 *                this streams (GET /logs?deployment=; absent: the primary's).
 *                The server must echo it in the X-XBin-Deployment response
 *                header: an answer without that echo is the primary's log
 *                (an xbind that can't show a deployment's log ignored the
 *                parameter), so the view says so and shows none of it — it
 *                never presents the primary's log as the deployment's.
 *   partition  — optional, on a partitioned tile (docs/partitions.md
 *                §Operating people's partitions): '' (the default: what the
 *                viewer's credential reaches — their own partition), 'global'
 *                (the global instance's log) or 'user:<id>' (a log its person
 *                shares with an admin or a manager). The same echo rule: the
 *                answer must name the partition asked for (X-XBin-Partition).
 *                Another component or deployment clears it.
 *
 * The partition switcher: once an answer names a
 * partition — only a partitioned tile's does — the view asks GET
 * /partitions?tile= what else the viewer may read (the global instance's
 * log when theirs to read, and for an admin or a tile manager each person
 * who shares theirs) and offers it in the corner (logs-partition.js). An
 * unpartitioned tile, and an older xbind, never name one: the view there is
 * as it always was.
 *
 * Shares the terminal theme/font-size prefs (bx-term-theme / bx-term-fontsize
 * in localStorage, and the live `bx-term-pref` event) so it looks like the
 * shells beside it: the same palette (web/term-palettes.js — the workspace's,
 * from the --bx-term-* tokens, follows the person's appearance live, D184),
 * the --bx-mono face and the density's size until the person picks one.
 * xterm loads lazily; renders into a shadow root with xterm's stylesheet
 * linked so it works anywhere.
 */

import { scrollCssText } from '/vendor/bx-scroll.js';
import { logsQuery, echoOK, logChoices, badgeText, globalProbe } from '/vendor/logs-partition.js';
import { wireLinks } from '/vendor/term-links.js';
import { themeName, paletteFor } from '/vendor/term-palettes.js';
import { token, onAppearance } from '/vendor/bx-theme.js';

const LISTING_TTL = 30000; // how long the switcher trusts its listing on a new stream

// Same loader as bx-terminal: the tag is shared by id, so wait for ITS load
// rather than resolving because it already exists (the terminal and this
// view mount together when logs open first).
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

let xtermReady = null;
function loadXterm() {
  xtermReady ??= (async () => {
    await scriptOnce('/vendor/xterm.js');
    await scriptOnce('/vendor/addon-fit.js');
    await scriptOnce('/vendor/addon-web-links.js');
  })();
  return xtermReady;
}

// The theme prefs live in localStorage under the terminal's keys — a log
// viewer sitting next to shells should match them. Kept a tiny local copy of
// the reads so bx-logs stays independent of bx-terminal's internals. A
// sandboxed frame has no localStorage: reading it throws there — the defaults.
const stored = (k) => { try { return localStorage.getItem(k); } catch { return null; } };
// the size the person picked, or null: the density's --bx-term-size (sizeOf)
function savedFontSize() {
  const n = Number(stored('bx-term-fontsize') ?? NaN);
  return n >= 7 && n <= 28 ? n : null;
}
function sizeOf(el) {
  const n = parseFloat(token('--bx-term-size', el));
  return n >= 7 && n <= 28 ? n : 12;
}

export class BxLogs extends HTMLElement {
  #term; #fit; #ro; #ac; #host; #closed = false; #onPref; #onStorage; #offLook; #gen = 0;
  // the partition switcher: the default's answer (X-XBin-Partition), the
  // listing it was built from (per component, and when), whether the global
  // instance's log is the viewer's to read, and its entries
  #defaultPart = ''; #listing = null; #listingFor = ''; #listingAt = 0; #globalOK = true; #choices = [];
  #clearing = false; // the partition attribute is being cleared (another log)

  static get observedAttributes() { return ['component', 'deployment', 'partition']; }

  connectedCallback() {
    this.style.height = this.style.height || '100%';
    if (!this.shadowRoot) {
      const root = this.attachShadow({ mode: 'open' });
      root.innerHTML =
        `<link rel="stylesheet" href="/vendor/xterm.css">` +
        `<style>
          ${scrollCssText}
          :host{display:block; position:relative}
          /* the face xterm reads (#start): the --bx-mono token, Night's where a
             document has no theme.css */
          .host{height:100%; background:var(--bx-term-bg, #0B0C12);
            font-family:var(--bx-mono, "JetBrains Mono", ui-monospace, monospace)}
          /* as the terminal: 8px 12px around the screen (fit measures inside it) */
          .host .xterm{padding:8px 12px}
          .badge{position:absolute; top:6px; right:12px; z-index:6; box-sizing:border-box; height:20px; padding:0 6px;
            display:inline-flex; align-items:center; white-space:nowrap; pointer-events:none;
            font:var(--bx-font-micro, 600 11px/14px "Instrument Sans", system-ui, sans-serif); letter-spacing:var(--bx-tracking-micro, 0.06em);
            text-transform:uppercase; color:var(--bx-muted, #A3A6B6); background:var(--bx-panel, #1F2028);
            border:1px solid var(--bx-border, #33353F); border-radius:var(--bx-radius, 2px);}
          .corner{position:absolute; top:6px; right:12px; z-index:6; display:flex; gap:6px; align-items:center}
          .corner .badge{position:static}
          .part{box-sizing:border-box; height:24px; max-width:40ch; padding:0 6px; cursor:pointer;
            font:var(--bx-font-meta, 400 12px/16px "Instrument Sans", system-ui, sans-serif); color:var(--bx-text, #E9EAF0);
            background:var(--bx-panel, #1F2028); border:1px solid var(--bx-border-strong, #666A7E); border-radius:var(--bx-radius, 2px)}
          .part:focus-visible{outline:var(--bx-focus-outline, 3px solid #3DD6F5); outline-offset:var(--bx-focus-offset, 2px);
            box-shadow:var(--bx-focus-halo, 0 0 0 2px #0B0C12)}
          .part[hidden]{display:none}
          /* the switcher's strip over the log: the screen starts below it */
          .host.bar .xterm{padding-top:36px}
        </style>` +
        `<div class="host"></div>` +
        `<div class="corner"><select class="part" hidden aria-label="whose log" title="which partition's log"></select>` +
        `<span class="badge">read-only logs</span></div>`;
      root.querySelector('.part').addEventListener('change', (e) => this.setAttribute('partition', e.target.value));
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
    if (this.#onStorage) window.removeEventListener('storage', this.#onStorage);
    this.#offLook?.(); this.#offLook = null;
    this.#listingFor = ''; // opened again: what the switcher offers is asked again
  }

  // the palette in force: the one the terminals' settings menu picked, or the
  // workspace's from the --bx-term-* tokens at this element
  #applyTheme(name = stored('bx-term-theme')) {
    const theme = paletteFor(themeName(name), (t) => token(t, this));
    if (this.#term) this.#term.options.theme = theme;
    if (this.#host) this.#host.style.background = theme.background;
    return theme;
  }
  #applySize(n = savedFontSize() ?? sizeOf(this)) {
    if (!this.#term) return;
    const px = Math.max(7, Math.min(28, Math.round(n)));
    if (this.#term.options.fontSize !== px) this.#term.options.fontSize = px;
    try { this.#fit.fit(); } catch { }
  }

  attributeChangedCallback(name, oldV, newV) {
    if (oldV === newV || this.#clearing || !this.#term || (name === 'component' && oldV === null)) return;
    if (name !== 'partition') { // another log: learn it again, from its own default
      this.#defaultPart = ''; this.#choices = [];
      if (this.hasAttribute('partition')) { this.#clearing = true; this.removeAttribute('partition'); this.#clearing = false; }
    }
    this.#term.clear();
    this.#stream();
  }

  async #start() {
    await loadXterm();
    // xterm measures its scrollbar once, when it opens (the thin 6px bar,
    // D123): let the shadow root's xterm.css apply first, or it reads 0 and
    // assumes 15px (a link that already failed never fires again: capped)
    const css = this.shadowRoot.querySelector('link[rel="stylesheet"]');
    if (css && !css.sheet) await new Promise((r) => { css.addEventListener('load', r, { once: true }); css.addEventListener('error', r, { once: true }); setTimeout(r, 2000); });
    if (this.#closed) return;
    const fontSize = Math.max(7, Math.min(28, Math.round(savedFontSize() ?? sizeOf(this))));
    // the face (the --bx-mono token as .host resolves it), loaded before
    // xterm measures a cell — as the terminal does (bx-terminal.js)
    const fontFamily = getComputedStyle(this.#host).fontFamily;
    if (document.fonts?.load) {
      await Promise.race([document.fonts.load(`${fontSize}px ${fontFamily}`).catch(() => { }), new Promise((r) => setTimeout(r, 1500))]);
      if (this.#closed) return;
    }
    this.#term = new window.Terminal({
      fontSize, fontFamily,
      fontWeight: 400, fontWeightBold: 700, drawBoldTextInBrightColors: false, // bold is weight, never colour
      theme: this.#applyTheme(),
      scrollback: 8000,
      disableStdin: true,       // read-only: no cursor, no input
      cursorStyle: 'underline',
      cursorInactiveStyle: 'none',
      convertEol: true,         // backend lines are \n-terminated
    });
    this.#fit = new window.FitAddon.FitAddon();
    this.#term.loadAddon(this.#fit);
    // links as in a terminal (term-links.js; a log never writes the clipboard)
    wireLinks(this.#term, { focused: () => false });
    this.#term.open(this.#host);
    this.#applyTheme();
    try { this.#fit.fit(); } catch { }
    this.#ro = new ResizeObserver(() => { try { this.#fit.fit(); } catch { } });
    this.#ro.observe(this);
    // Follow the shared terminal preferences — the palette and the size —
    // from the terminals in this document, other tabs, and the person's
    // appearance (the workspace palette and the density's size).
    this.#onPref = (e) => {
      if (e.detail?.theme) this.#applyTheme(e.detail.theme);
      if (e.detail?.fontSize) this.#applySize(e.detail.fontSize);
    };
    window.addEventListener('bx-term-pref', this.#onPref);
    this.#onStorage = (e) => {
      if (e.key === 'bx-term-theme') this.#applyTheme();
      if (e.key === 'bx-term-fontsize') this.#applySize();
    };
    window.addEventListener('storage', this.#onStorage);
    this.#offLook = onAppearance(() => { this.#applyTheme(); this.#applySize(); });
    this.#stream();
  }

  // #stream opens (or reopens) the follow request and pumps decoded text into
  // the terminal. A generation guard means a component switch or reconnect
  // supersedes the previous fetch so two streams never interleave.
  async #stream() {
    const comp = this.getAttribute('component');
    if (!comp || !this.#term) return;
    const dep = this.getAttribute('deployment') || '';
    const part = dep ? '' : this.getAttribute('partition') || ''; // a deployment beyond the primary runs one instance
    this.#ac?.abort();
    const ac = new AbortController();
    this.#ac = ac;
    const gen = ++this.#gen;
    this.#badge(dep);
    const url = `/api/xbin/logs?component=${encodeURIComponent(comp)}${dep ? `&deployment=${encodeURIComponent(dep)}` : ''}${logsQuery(part)}&follow=1`;
    try {
      const r = await fetch(url, { signal: ac.signal });
      if (gen !== this.#gen) return;
      if (!dep) this.#learn(comp, part, r);
      if (part && r.ok && !echoOK(part, r.headers.get('X-XBin-Partition'))) {
        ac.abort();
        this.#term.write(`\x1b[33m[this xbind answered with another log than ${part}'s: it can't show that one]\x1b[0m\r\n`);
        return;
      }
      if (!r.ok) {
        let msg = r.status;
        try { msg = (await r.json()).error ?? msg; } catch { }
        this.#term.write(`\x1b[31m[logs unavailable: ${msg}]\x1b[0m\r\n`);
        return;
      }
      // the echo rule: a named deployment's log comes back named, or it is
      // the primary's (an older xbind ignores the parameter) — say so and
      // stop, without reconnecting to the same answer
      if (dep && r.headers.get('X-XBin-Deployment') !== dep) {
        ac.abort();
        this.#term.write(`\x1b[33m[this xbind answered with ${comp}'s primary log, not ${dep}'s: it can't show a deployment's log]\x1b[0m\r\n`);
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

  // the corner badge names a deployment's log, so it can't pass for the
  // primary's — and on a partitioned tile whose log it is
  #badge(dep) {
    const b = this.shadowRoot?.querySelector('.badge');
    if (b) b.textContent = badgeText(dep ? [] : this.#choices, dep);
  }

  // #learn: an answer that names a partition is a partitioned tile's (only
  // those send X-XBin-Partition); the default's names what the viewer's
  // credential reaches. Then — once per component — the listing says what
  // else they may read. An answer without it, on a view that never saw one,
  // is an unpartitioned tile's or an older xbind's: nothing is asked, unless
  // the default was refused (a person's partition that hasn't started yet
  // answers 404 without the header, and the global instance's log may still
  // be theirs to pick).
  // The listing is asked again when the panel opens anew and, on a new
  // stream (a pick), once it is older than LISTING_TTL: a share that starts
  // or ends shows then. The global instance's log keeps today's gate
  // (terminal access or an admin), which a person's listing doesn't say: a
  // tail=0 read of it answers (403: not offered).
  async #learn(comp, part, r) {
    const header = r.headers.get('X-XBin-Partition') || '';
    if (!part && header) this.#defaultPart = header;
    if (!header && !this.#choices.length && r.ok) return;
    if (this.#listingFor !== comp || Date.now() - this.#listingAt > LISTING_TTL) {
      this.#listingFor = comp;
      this.#listingAt = Date.now();
      this.#listing = null;
      this.#globalOK = true;
      try {
        const lr = await fetch(`/api/xbin/partitions?tile=${encodeURIComponent(comp)}`);
        this.#listing = lr.ok ? await lr.json() : null;
        if (globalProbe(this.#listing, this.#defaultPart)) {
          const pr = await fetch(`/api/xbin/logs?component=${encodeURIComponent(comp)}&xbin-partition=global&tail=0`);
          this.#globalOK = pr.status !== 401 && pr.status !== 403; // 404: no log yet, still theirs to pick
          pr.body?.cancel().catch(() => {});
        }
      } catch { this.#listing = null; }
    }
    if (this.getAttribute('component') !== comp) return; // another log meanwhile
    this.#choices = logChoices(this.#listing, this.#defaultPart, { globalOK: this.#globalOK });
    this.#drawChoices();
  }

  #drawChoices() {
    const sel = this.shadowRoot?.querySelector('.part');
    if (!sel) return;
    const want = this.getAttribute('partition') || '';
    sel.hidden = this.#choices.length < 2;
    // the switcher gets a strip of its own over the log (the badge alone overlays it, as before)
    this.#host?.classList.toggle('bar', !sel.hidden);
    try { this.#fit?.fit(); } catch { }
    sel.replaceChildren(...this.#choices.map((c) => Object.assign(document.createElement('option'), { value: c.value, textContent: c.label, title: c.title })));
    sel.value = this.#choices.some((c) => c.value === want) ? want : '';
    this.#badge(this.getAttribute('deployment') || '');
  }

  // testApi(): the UI harness's view (hack/ui-harness): the partition
  // switcher's entries and choice, the corner's words, and the text shown.
  testApi() {
    const el = this, t = this.#term;
    return {
      choices: this.#choices.map((c) => ({ ...c })),
      partition: el.getAttribute('partition') || '',
      defaultPartition: this.#defaultPart,
      switcher: !el.shadowRoot?.querySelector('.part')?.hidden,
      badge: el.shadowRoot?.querySelector('.badge')?.textContent || '',
      text: () => {
        const b = t?.buffer?.active;
        if (!b) return '';
        const lines = [];
        for (let i = 0; i < b.length; i++) lines.push(b.getLine(i)?.translateToString(true) ?? '');
        return lines.join('\n').trim();
      },
      pick: (v) => el.setAttribute('partition', v),
    };
  }
}

customElements.define('bx-logs', BxLogs);
