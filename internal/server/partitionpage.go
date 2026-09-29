package server

// partitionpage.go — the in-frame "partition mode switch requested" page
// (plans/partitions/01 §2.4; PD-44, PD-50). While a tile's partition mode
// switch is pending, nothing of its primary runs, and a document of the tile
// is xbind's own page instead of the tile's: the D36 precedent
// (requestpage.go), not an HTML transform — no tile HTML is read or touched.
// It reaches every shell, old ones and the iOS app included, because it is
// what the tile's frame loads. The tile's other files (scripts, styles,
// images), and its HTML read as a file (a fetch, a bearer or another tile's
// code grant), are served as before. The primary's deployment URL
// (/c/<tile>+<primary>/, and a deployment origin's bare URL, which is
// rewritten to it) shows the page too; another deployment's isn't paused
// (PD-17) and never gets here.
//
// The page informs only: tile frames are sandboxed without same-origin, so
// it carries no script and no form (and says so in its own CSP). The
// deciding happens where a manager's session is: bx partition switch|keep,
// POST /api/xbin/partitions/mode, and the surfaces built on it. The tile's
// partitionNote — the tile's own words, sandbox-writable — is shown as text,
// escaped.

import (
	"net/http"
	"strings"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/registry"
)

// partitionPageCSP confines the page: its inline style alone, no script,
// an opaque origin.
const partitionPageCSP = "default-src 'none'; style-src 'unsafe-inline'; sandbox"

// servePartitionSwitchPage answers a document request for owner's files
// with the switch page while owner's partition mode switch is pending, and
// reports whether it did.
func (s *Server) servePartitionSwitchPage(w http.ResponseWriter, r *http.Request, owner, rel string) bool {
	if owner == "" || !partitionDocument(r, owner, rel) {
		return false
	}
	c, ok := s.Reg.Component(owner)
	if !ok {
		return false
	}
	st, from, req := c.PartitionState()
	if st != registry.PartitionPending || req == nil {
		return false
	}
	to := registry.SpecOf(req.Spec)
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	s.setDocCSP(w, r, partitionPageCSP)
	w.WriteHeader(http.StatusConflict)
	_, _ = w.Write([]byte(s.partitionSwitchPage(owner, from, to, c.Manifest.PartitionNote)))
	return true
}

// partitionDocument reports whether r loads one of owner's documents to
// show it: whatever the browser fetches as a document or a frame; without
// Fetch Metadata (an older browser, the app's scheme handler) a directory
// URL, an .html file or the native runtime document loaded under a
// person's login session or by the tile's own frame. Any other read — a
// fetch or a script's load of an .html file, a bearer's, another tile's
// code grant — gets the file as before.
func partitionDocument(r *http.Request, owner, rel string) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	switch r.Header.Get("Sec-Fetch-Dest") {
	case "document", "iframe", "frame", "embed", "object":
		return true
	case "":
	default:
		return false
	}
	if !strings.HasSuffix(r.URL.Path, "/") && !isHTMLName(rel) && !nativeRuntimeRequest(r) {
		return false
	}
	p := auth.PrincipalOf(r)
	return p.Component == "" && (p.Via == "session" || p.Via == "cookie") || p.Component == owner && p.Via == "frame"
}

// partitionSwitchPage is the page's HTML; every value it shows is escaped.
func (s *Server) partitionSwitchPage(tile string, from, to registry.PartitionSpec, note string) string {
	t := "<code>" + htmlEscape(tile) + "</code>"
	who := "a workspace admin"
	switch owner := s.policy().OwnerOf(tile); {
	case strings.HasPrefix(owner, "org:"):
		who = "an admin of <b>" + htmlEscape(owner) + "</b>, which owns it, or a workspace admin"
	case strings.HasPrefix(owner, "user:"):
		who = "its owner, <b>" + htmlEscape(owner) + "</b>, or a workspace admin"
	}
	where := "/xbin/partitions"
	if o := s.workspaceOrigin(); o != "" {
		where = o + where
	}
	// PD-44's words when the switch deletes everything; what it deletes when
	// "global" comes or goes (H1)
	deletes := "All data in this tile will be deleted for the switch to happen."
	if d := registry.SwitchDeletes(from, to); d != registry.SwitchDeletesAll {
		deletes = "Switching deletes " + d + "."
	}
	var b strings.Builder
	b.WriteString(partitionPageHead)
	b.WriteString(`<div class="card" role="alert"><h1>Partition mode switch requested</h1>`)
	b.WriteString(`<p>A partition mode switch is requested for ` + t + ` (<b>` + htmlEscape(from.String()) + `</b> → <b>` +
		htmlEscape(to.String()) + `</b>). <strong>` + htmlEscape(deletes) + `</strong></p>`)
	if n := strings.TrimSpace(note); n != "" {
		b.WriteString(`<p class="note">` + t + ` says: ` + htmlEscape(n) + `</p>`)
	}
	b.WriteString(`<p>Until a manager decides, ` + t + ` doesn't run.</p>`)
	b.WriteString(`<p class="who">Who decides: ` + who + ` — they switch (` + htmlEscape(registry.SwitchDeleting(from, to)) + `) or keep the current mode (nothing is deleted), at <code>` +
		htmlEscape(where) + `</code> or with <code>bx partition switch ` + htmlEscape(tile) + `</code> / <code>bx partition keep ` +
		htmlEscape(tile) + `</code>.</p></div>`)
	return b.String()
}

const partitionPageHead = `<!doctype html>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>partition mode switch requested</title>
<style>
  :root { color-scheme: light dark; --bg:#eef0f2; --card:#f6f7f8; --fg:#3b4046; --muted:#6a727b; --line:#cfd4da; --warn:#9a6700; }
  @media (prefers-color-scheme: dark) {
    :root { --bg:#0d1117; --card:#161b22; --fg:#c9d1d9; --muted:#8b949e; --line:#30363d; --warn:#d29922; }
  }
  * { box-sizing: border-box; }
  body { margin:0; min-height:100vh; display:flex; align-items:center; justify-content:center; padding:16px;
         background:var(--bg); color:var(--fg); font:15px/1.5 system-ui, sans-serif; }
  .card { width:min(480px, 100%); background:var(--card); border:1px solid var(--line); border-left:4px solid var(--warn);
          border-radius:8px; padding:24px; filter:grayscale(0.4); }
  h1 { font-size:17px; margin:0 0 8px; }
  p { margin:10px 0; overflow-wrap:anywhere; }
  strong { color:var(--warn); }
  .note { border-left:2px solid var(--line); padding-left:10px; white-space:pre-wrap; }
  .who { color:var(--muted); font-size:13px; }
  code { border:1px solid var(--line); border-radius:4px; padding:0 5px; }
</style>
`
