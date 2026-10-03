package server

import (
	"encoding/json"
	"net/http"
	"strings"
)

// serveRequestAccessPage is the D36 signed-in-human 403 for /c/ tiles: names
// the tile and its owner (who to ask) and files an access request inline —
// the human mirror of the elements' pending-grant loop. Served only to
// session principals; elements and anonymous callers keep the bare 403.
// Styled as xbind's other pages (pagetheme.go, D184); it carries no request
// to read the theme cookie from, so /vendor/theme-boot.js reads it in the
// browser (the page is top-level on the workspace origin and runs script).
func (s *Server) serveRequestAccessPage(w http.ResponseWriter, tile string) {
	owner := s.policy().OwnerOf(tile)
	ownerLine := "a workspace admin manages this tile"
	switch {
	case strings.HasPrefix(owner, "org:"):
		ownerLine = "owned by <b>" + htmlEscape(owner) + "</b> — its org admins (or a workspace admin) can grant access"
	case strings.HasPrefix(owner, "user:"):
		ownerLine = "owned by <b>" + htmlEscape(owner) + "</b> — they (or a workspace admin) can grant access"
	}
	tileJSON, _ := json.Marshal(tile) // safe literal for the inline script
	page := strings.ReplaceAll(s.brandPage(requestAccessHTML, ""), "{{TILE_JSON}}", string(tileJSON))
	page = strings.ReplaceAll(page, "{{TILE}}", htmlEscape(tile))
	page = strings.ReplaceAll(page, "{{OWNER}}", ownerLine)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusForbidden)
	_, _ = w.Write([]byte(page))
}

// requestAccessHTML: theme-boot.js runs before the theme.css link, so the
// person's theme is there at first paint. The script shows one of three
// status lines (pagetheme.go's glyphs): a request already pending (info),
// filed (ok), or refused (error).
const requestAccessHTML = pageOpen + `<script src="/vendor/theme-boot.js"></script>
<link rel="stylesheet" href="/vendor/theme.css">
<title>no access — request it?</title>
<link rel="icon" href="{{ICON}}">
<style>` + pageCSS + `
.row{display:flex;gap:8px;margin-top:16px}
.row select{flex:1;min-width:0;width:auto}
.row button{flex:none;width:auto;margin:0}
</style></head><body class="bx">
<div class="plate">
  <div class="logo">{{LOGO}}</div>
  <div class="main">
  <h1>No access to <code>{{TILE}}</code></h1>
  <p>{{OWNER}}.</p>
  <div class="row">
    <select id="lvl" aria-label="Access level">
      <option value="read">read — view and use</option>
      <option value="write">write — edit and drive</option>
      <option value="terminal">terminal — a shell on it</option>
    </select>
    <button class="primary" id="req">Request access</button>
  </div>
  <div class="alert info" id="info" role="status" hidden>` + svgInfo + `<span></span></div>
  <div class="alert ok" id="ok" role="status" hidden>` + svgOK + `<span></span></div>
  <div class="alert error" id="err" role="alert" hidden>` + svgError + `<span></span></div>
  </div>
</div>
<script>
  const tile = {{TILE_JSON}};
  const show = (kind, t) => {
    for (const k of ['info', 'ok', 'err']) document.getElementById(k).hidden = k !== kind;
    document.querySelector('#' + kind + ' span').textContent = t;
  };
  fetch('/api/xbin/access-requests').then(r => r.ok ? r.json() : null).then(d => {
    if (d && (d.requests ?? []).some(q => q.mine && q.tile === tile))
      show('info', 'You already have a pending request for this tile — its manager has been able to see it since you filed it.');
  }).catch(() => {});
  document.getElementById('req').onclick = async () => {
    const level = document.getElementById('lvl').value;
    const r = await fetch('/api/xbin/access-requests', {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ tile, level }),
    });
    const d = await r.json().catch(() => ({}));
    if (r.ok) show('ok', 'Requested ' + level + ' — the tile\'s manager will see it in their organisations panel.');
    else show('err', d.error ?? ('request failed (' + r.status + ')'));
  };
</script>
</body></html>`
