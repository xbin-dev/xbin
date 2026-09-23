package server

// Workspace branding (D76): the title and icon an admin sets from the admin
// tile — GET/PUT /api/xbin/branding, held by internal/branding in
// data/branding.json — and how the sign-in / invite pages pick them up
// (brandPage). The shell reads GET at boot and again on the `branding` hub
// event; the pages are server-rendered, so they read the store directly
// (it is a plain file, readable while the vault is still sealed).

import (
	"encoding/json"
	"html"
	"net/http"
	"strings"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/branding"
	"github.com/xbin-dev/xbin/internal/events"
)

func (s *Server) registerBrandingAPI() {
	s.RegisterAPI("GET /branding", s.apiBrandingGet)
	s.RegisterAPI("PUT /branding", s.apiBrandingPut)
}

// brand is the current brand; xbin's own when there is no store or no file.
func (s *Server) brand() branding.Brand {
	if s.Brand == nil {
		return branding.Brand{}
	}
	b, _ := s.Brand.Load()
	return b
}

func brandView(b branding.Brand) map[string]any {
	return map[string]any{"title": b.Title, "icon": b.Icon, "hasIcon": b.Icon != ""}
}

// apiBrandingGet: any signed-in principal (the shell renders it).
func (s *Server) apiBrandingGet(w http.ResponseWriter, r *http.Request) {
	WriteJSON(w, http.StatusOK, brandView(s.brand()))
}

// apiBrandingPut: admin only. {title?, icon?} — each present key is a
// whole-value replace ("" clears) → the full view; every open shell is told
// (hub event `branding`). Audited like every admin write.
func (s *Server) apiBrandingPut(w http.ResponseWriter, r *http.Request) {
	// owner, an admin user, or an element holding xbin:admin — the admin tile
	// calls through its frame token (s.admin: the installed Policy)
	if !(s.admin(r) || auth.PrincipalOf(r).IsAdmin()) {
		apiErr(w, http.StatusForbidden, "admin only — needs the xbin:admin capability (docs/auth.md)")
		return
	}
	if s.Brand == nil {
		apiErr(w, http.StatusNotImplemented, "branding is not enabled")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 512<<10)
	var p branding.Patch
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		apiErr(w, http.StatusBadRequest, "need {title?, icon?} (JSON; the icon a data: URI of at most 256 KiB)")
		return
	}
	b, err := s.Brand.Apply(p)
	if err != nil {
		apiErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if s.Hub != nil {
		s.Hub.Publish(events.Event{Type: "branding"})
	}
	WriteJSON(w, http.StatusOK, brandView(b))
}

// brandPage fills a sign-in page template's {{TITLE}}, {{ICON}} and
// {{LOGO}}: the workspace's brand when set, xbin's own otherwise (the exact
// markup the pages always carried). suffix is the page's own part of the
// title (" — sign in"). Only branding.ValidIcon's data URIs reach src/href;
// the title is HTML-escaped.
func (s *Server) brandPage(page, suffix string) string {
	b := s.brand()
	title, icon, mark, word := "xbin"+suffix, defaultIconURI, defaultMarkSVG, "X/BIN"
	if b.Icon != "" {
		icon = b.Icon
		mark = `<img class="mark" src="` + b.Icon + `" alt="">`
	}
	if b.Title != "" {
		title = html.EscapeString(b.Title) + suffix
		word = html.EscapeString(b.Title)
	}
	page = strings.ReplaceAll(page, "{{TITLE}}", title)
	page = strings.ReplaceAll(page, "{{ICON}}", icon)
	return strings.ReplaceAll(page, "{{LOGO}}", mark+word)
}

// xbin's own mark, as the sign-in pages always carried it: the favicon as a
// data URI, and the inline logo (the wordmark "X/BIN" follows it).
const (
	defaultIconURI = "data:image/svg+xml;base64,PHN2ZyB4bWxucz0iaHR0cDovL3d3dy53My5vcmcvMjAwMC9zdmciIHZpZXdCb3g9IjAgMCA2NCA2NCIgcm9sZT0iaW1nIiBhcmlhLWxhYmVsPSJYL0JJTiI+CiAgPHBhdGggZD0iTTE4IDRINTZhNCA0IDAgMCAxIDQgNHYzOEw0NiA2MEg4YTQgNCAwIDAgMS00LTRWMTh6IiBmaWxsPSIjZjVhNjIzIi8+CiAgPHBhdGggZD0iTTIxIDIxIDQzIDQzTTQzIDIxIDIxIDQzIiBzdHJva2U9IiMyMzI3MmUiIHN0cm9rZS13aWR0aD0iOSIgc3Ryb2tlLWxpbmVjYXA9ImJ1dHQiLz4KICA8Y2lyY2xlIGN4PSI1MyIgY3k9IjExIiByPSIyLjYiIGZpbGw9IiMyMzI3MmUiIG9wYWNpdHk9Ii40Ii8+CiAgPGNpcmNsZSBjeD0iMTEiIGN5PSI1MyIgcj0iMi42IiBmaWxsPSIjMjMyNzJlIiBvcGFjaXR5PSIuNCIvPgo8L3N2Zz4K"
	defaultMarkSVG = `<svg viewBox="0 0 64 64" width="22" height="22" aria-hidden="true"><path d="M18 4H56a4 4 0 0 1 4 4v38L46 60H8a4 4 0 0 1-4-4V18z" fill="#f5a623"/><path d="M21 21 43 43M43 21 21 43" stroke="#23272e" stroke-width="9" stroke-linecap="butt"/></svg>`
)
