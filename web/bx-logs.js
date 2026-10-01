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
 * shells beside it. xterm loads lazily; renders into a shadow root with
 * xterm's stylesheet linked so it works anywhere.
 */

import { scrollCssText } from '/vendor/bx-scroll.js';
import { logsQuery, echoOK, logChoices, badgeText, globalProbe } from '/vendor/logs-partition.js';
import { wireLinks } from '/vendor/term-links.js';

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
          .host{height:100%; background:var(--bx-term-bg, #262c36)}
          .badge{position:absolute; top:4px; right:10px; z-index:6;
            font:10px/1.6 system-ui,sans-serif; letter-spacing:.05em; text-transform:uppercase;
            color:#c7ccd4; background:rgba(140,148,161,.18); border-radius:5px;
            padding:0 7px; pointer-events:none; opacity:.8;}
          .corner{position:absolute; top:4px; right:10px; z-index:6; display:flex; gap:6px; align-items:center}
          .corner .badge{position:static}
          .part{font:10.5px/1.4 system-ui,sans-serif; color:#c7ccd4; background:rgba(40,46,56,.92);
            border:1px solid rgba(140,148,161,.4); border-radius:5px; padding:0 4px; cursor:pointer}
          .part[hidden]{display:none}
          .host.bar{box-sizing:border-box; padding-top:24px}
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
    this.#listingFor = ''; // opened again: what the switcher offers is asked again
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
    // links as in a terminal (term-links.js; a log never writes the clipboard)
    wireLinks(this.#term, { focused: () => false });
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
