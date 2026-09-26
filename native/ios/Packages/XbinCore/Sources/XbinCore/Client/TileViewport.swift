import Foundation

// A tile page's viewport on a phone (plans/native.md §6.3: "pages that set a
// mobile viewport get it; desktop-first pages render at a comfortable width
// with zoom-to-fit available").
//
// xbin tiles are built for a card in the shell's canvas — an iframe whose
// size the card sets — so almost none has a `<meta name="viewport">`: in a
// frame it means nothing. Top-level in a WKWebView, WebKit then lays such a
// page out for a 980 px desktop and scales that to the screen: at about 0.4
// a tile's 12–13 px text is unreadable. Most tiles are fluid (a card can be
// any width), so the device's own width suits them; the few that need more
// (a wide table, a fixed-width layout) get exactly their width, fitted to
// the screen, with pinch zoom.
//
// The app's user script does this in the page's DOM, from its own content
// world — no HTML is rewritten on the way (the scheme handler passes every
// response through; the server keeps its one sanctioned transform):
//
//   - a page with a viewport meta of its own is never touched, not even for
//     a moment: the script waits for <body> (the head is parsed by then) and
//     does nothing when one is there — or when one appears later;
//   - otherwise it appends `width=device-width, initial-scale=1`;
//   - while the page loads and for WATCH ms after `load`, when its content is
//     wider than the layout (documentElement.scrollWidth > clientWidth) the
//     layout grows to the content's width, at most MAX px and STEPS times (an
//     element of 100vw plus a margin outgrows every width), which WebKit fits
//     to the screen; pinch zoom stays the page's (no user-scalable=no here).
//     It never grows once the person has zoomed in (the visual viewport is
//     narrower than the layout), and a rotation starts again from the
//     device's width;
//   - only HTML documents (an image or text file opened as a page keeps
//     WebKit's own fitting), only the main frame (WebTileController).
public enum TileViewport {
    /// The widest layout a desktop-first page grows to (px).
    public static let maxWidth = 1280
    /// How often a page's layout may grow.
    public static let maxSteps = 4
    /// How long after `load` late content (a fetched table) still widens it (ms).
    public static let watchMillis = 10_000
    /// The WKContentWorld the script runs in: its variables stay out of the
    /// page's reach (the meta element itself is the page's DOM).
    public static let contentWorld = "xbin-viewport"

    /// The user script, injected at document start into the main frame, in
    /// ``contentWorld``. A plain literal: no backslashes, no interpolation, so
    /// native/tools/viewport.test.mjs can lift it from this file verbatim.
    public static let userScript = """
    (() => {
      const d = document, MAX = 1280, STEPS = 4, WATCH = 10000;
      if (d.contentType !== 'text/html' && d.contentType !== 'application/xhtml+xml') return;
      let meta = null, placed = false, grown = 0, timer = 0, mo = null, until = 0, dev = 0;
      const theirs = () => Array.from(d.querySelectorAll('meta[name]'))
        .some((m) => m !== meta && m.name.toLowerCase() === 'viewport');
      const fit = (w) => Math.floor((dev / w) * 10000) / 10000;
      const setWidth = (w) => meta.setAttribute('content', w
        ? 'width=' + w + ', initial-scale=' + fit(w) + ', minimum-scale=' + fit(w)
        : 'width=device-width, initial-scale=1');
      const stop = () => { if (mo) mo.disconnect(); mo = null; };
      const check = () => {
        timer = 0;
        if (!meta) return;
        if (theirs()) { meta.remove(); meta = null; stop(); return; }
        const root = d.documentElement, have = root.clientWidth, vv = window.visualViewport;
        if (!have) return;
        if (!grown) dev = have;
        if (vv && vv.width < have - 2) return;
        const need = Math.ceil(Math.max(root.scrollWidth, d.body ? d.body.scrollWidth : 0));
        if (need > have + 1 && have < MAX && grown < STEPS) {
          grown++;
          setWidth(Math.min(need, MAX));
          later(100);
        }
      };
      const later = (ms) => { if (!timer) timer = setTimeout(check, ms); };
      const place = () => {
        if (placed) return;
        placed = true;
        if (theirs() || !d.documentElement) return;
        meta = d.createElement('meta');
        meta.name = 'viewport';
        setWidth(0);
        (d.head || d.documentElement).appendChild(meta);
        mo = new MutationObserver(() => { if (until && Date.now() > until) stop(); else later(250); });
        mo.observe(d.documentElement, { childList: true, subtree: true, attributes: true, characterData: true });
        d.addEventListener('load', () => { if (mo) later(100); }, true);
        matchMedia('(orientation: portrait)').addEventListener('change', () => {
          if (!meta) return;
          grown = 0;
          setWidth(0);
          later(150);
        });
        later(0);
      };
      const wait = new MutationObserver(() => { if (d.body) { wait.disconnect(); place(); } });
      wait.observe(d, { childList: true, subtree: true });
      d.addEventListener('DOMContentLoaded', () => { wait.disconnect(); place(); later(0); });
      addEventListener('load', () => {
        until = Date.now() + WATCH;
        setTimeout(stop, WATCH);
        later(0);
      });
    })();
    """
}
