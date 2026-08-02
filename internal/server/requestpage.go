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
func (s *Server) serveRequestAccessPage(w http.ResponseWriter, tile string) {
	owner := ""
	if s.OwnerOf != nil {
		owner = s.OwnerOf(tile)
	}
	ownerLine := "a workspace admin manages this tile"
	switch {
	case strings.HasPrefix(owner, "org:"):
		ownerLine = "owned by <b>" + htmlEscape(owner) + "</b> — its org admins (or a workspace admin) can grant access"
	case strings.HasPrefix(owner, "user:"):
		ownerLine = "owned by <b>" + htmlEscape(owner) + "</b> — they (or a workspace admin) can grant access"
	}
	tileJSON, _ := json.Marshal(tile) // safe literal for the inline script
	page := strings.ReplaceAll(requestAccessHTML, "{{TILE_JSON}}", string(tileJSON))
	page = strings.ReplaceAll(page, "{{TILE}}", htmlEscape(tile))
	page = strings.ReplaceAll(page, "{{OWNER}}", ownerLine)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusForbidden)
	_, _ = w.Write([]byte(page))
}

const requestAccessHTML = `<!doctype html>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>no access — request it?</title>
<style>
  :root { color-scheme: dark; }
  * { box-sizing: border-box; }
  body { margin:0; min-height:100vh; display:flex; align-items:center; justify-content:center;
         background:#0d1117; color:#e6edf3; font:15px/1.5 system-ui, sans-serif; }
  .card { width:min(420px, calc(100vw - 32px)); background:#161b22; border:1px solid #30363d;
          border-radius:8px; padding:28px; }
  h1 { font-size:17px; margin:0 0 6px; }
  code { background:#0d1117; border:1px solid #30363d; border-radius:4px; padding:1px 6px; }
  p { color:#9da7b3; margin:10px 0; }
  .row { display:flex; gap:8px; margin-top:14px; }
  select, button { font:inherit; border-radius:6px; border:1px solid #30363d; background:#0d1117;
                   color:#e6edf3; padding:8px 12px; }
  button.go { background:#238636; border-color:#2ea043; cursor:pointer; }
  .msg { margin-top:12px; font-size:13px; color:#7ee787; display:none; }
  .err { color:#f85149; }
</style>
<div class="card">
  <h1>No access to <code>{{TILE}}</code></h1>
  <p>{{OWNER}}.</p>
  <div class="row">
    <select id="lvl">
      <option value="read">read — view and use</option>
      <option value="write">write — edit and drive</option>
      <option value="terminal">terminal — a shell on it</option>
    </select>
    <button class="go" id="req">Request access</button>
  </div>
  <div class="msg" id="msg"></div>
</div>
<script>
  const tile = {{TILE_JSON}};
  const msg = document.getElementById('msg');
  const show = (t, err) => { msg.textContent = t; msg.style.display = 'block';
                             msg.classList.toggle('err', !!err); };
  fetch('/api/xbin/access-requests').then(r => r.ok ? r.json() : null).then(d => {
    if (d && (d.requests ?? []).some(q => q.mine && q.tile === tile))
      show('You already have a pending request for this tile — its manager has been able to see it since you filed it.');
  }).catch(() => {});
  document.getElementById('req').onclick = async () => {
    const level = document.getElementById('lvl').value;
    const r = await fetch('/api/xbin/access-requests', {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ tile, level }),
    });
    const d = await r.json().catch(() => ({}));
    if (r.ok) show('Requested ' + level + ' — the tile\'s manager will see it in their organisations panel.');
    else show(d.error ?? ('request failed (' + r.status + ')'), true);
  };
</script>
`
