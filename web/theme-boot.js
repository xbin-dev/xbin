/* theme-boot.js — the person's theme for xbind's own top-level pages that
   get no document injection (the partitions page, /xbin/partitions). A
   classic, synchronous script: in <head>, before the theme.css link, it
   copies the xbin_theme hint cookie (light|dark; absent = follow the
   system) into <meta name="xbin-theme">, so a page that opted in with
   <html data-bx-theme="auto"> opens in that theme from its first paint
   (D184; /vendor/bx-theme.js keeps the cookie). The cookie is a UI hint,
   never a credential: anything but light or dark is ignored. */
(function () {
  try {
    var m = /(?:^|;\s*)xbin_theme=(light|dark)(?:;|$)/.exec(document.cookie);
    if (!m || document.querySelector('head > meta[name="xbin-theme"]')) return;
    var meta = document.createElement('meta');
    meta.setAttribute('name', 'xbin-theme');
    meta.setAttribute('content', m[1]);
    document.head.prepend(meta);
  } catch (e) { /* no cookies here: follow the system */ }
})();
