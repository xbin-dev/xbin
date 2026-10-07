/* theme-boot.js — the theme before the first paint. A classic, synchronous
   script: in <head>, before the theme.css link.
   - A device override (D188): when this browser keeps one (localStorage
     xbin-theme-device = light|dark, the shell's settings menu → "This
     device"), the page shows it, whatever xbind injected: the injected
     <meta name="xbin-theme"> (the person's choice; absent = the system's)
     is set aside on <html data-bx-theme-person> and the meta holds the
     device's theme. The workspace's root page runs this, so the shell opens
     in the device's theme (/vendor/bx-theme.js syncDeviceTheme keeps it).
   - Otherwise, for xbind's own top-level pages that get no document
     injection (the partitions page, /xbin/partitions): it copies the
     xbin_theme hint cookie (light|dark; absent = follow the system) into
     the meta, so a page that opted in with <html data-bx-theme="auto">
     opens in that theme (D184; /vendor/bx-theme.js keeps the cookie).
   The cookie and the stored value are UI hints, never credentials:
   anything but light or dark is ignored. */
(function () {
  var sel = 'head > meta[name="xbin-theme"]';
  function put(theme) {
    var meta = document.querySelector(sel);
    if (!meta) {
      meta = document.createElement('meta');
      meta.setAttribute('name', 'xbin-theme');
      document.head.prepend(meta);
    }
    meta.setAttribute('content', theme);
  }
  try {
    var d = localStorage.getItem('xbin-theme-device');
    if (d === 'light' || d === 'dark') {
      var html = document.documentElement, was = document.querySelector(sel);
      if (!html.hasAttribute('data-bx-theme-person')) {
        var p = was && was.getAttribute('content');
        html.setAttribute('data-bx-theme-person', p === 'light' || p === 'dark' ? p : 'system');
      }
      put(d);
      return;
    }
  } catch (e) { /* no storage here: no override */ }
  try {
    var m = /(?:^|;\s*)xbin_theme=(light|dark)(?:;|$)/.exec(document.cookie);
    if (!m || document.querySelector(sel)) return;
    put(m[1]);
  } catch (e) { /* no cookies here: follow the system */ }
})();
