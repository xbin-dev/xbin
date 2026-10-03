package server

// Workspace branding (D76): the title and icon an admin sets from the admin
// tile — GET/PUT /api/xbin/branding, held by internal/branding in
// data/branding.json — and how xbind's own pages pick them up (brandPage,
// brandLogo; pagetheme.go). The shell reads GET at boot and again on the
// `branding` hub event; the pages are server-rendered, so they read the
// store directly (it is a plain file, readable while the vault is still
// sealed).

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

// brandPage fills a page template's {{TITLE}}, {{ICON}} and {{LOGO}}: the
// workspace's brand when set, xbin's own otherwise. suffix is the page's
// own part of the title (" — sign in"). Only branding.ValidIcon's data URIs
// reach src/href; the title is HTML-escaped.
func (s *Server) brandPage(page, suffix string) string {
	b := s.brand()
	title, icon := "xbin"+suffix, defaultIconURI
	if b.Icon != "" {
		icon = b.Icon
	}
	if b.Title != "" {
		title = html.EscapeString(b.Title) + suffix
	}
	page = strings.ReplaceAll(page, "{{TITLE}}", title)
	page = strings.ReplaceAll(page, "{{ICON}}", icon)
	return strings.ReplaceAll(page, "{{LOGO}}", brandLogo(b, true))
}

// brandLogo is what a page's logo block holds: the workspace's icon (when
// withIcon) and title, or xbin's one-colour wordmark "xbin" (D184: the
// wordmark stands until the chosen mark is drawn). A page whose CSP loads
// no images, or that shouldn't carry the icon's bytes (up to 256 KiB),
// passes withIcon false and shows the title alone.
func brandLogo(b branding.Brand, withIcon bool) string {
	mark := ""
	if withIcon && b.Icon != "" {
		mark = `<img class="mark" src="` + b.Icon + `" alt="">`
	}
	if b.Title != "" {
		return mark + `<span class="name">` + html.EscapeString(b.Title) + `</span>`
	}
	return mark + `<span class="name">xbin</span>`
}

// pageTitle is a page's <title> word: the workspace's title, or "xbin".
func pageTitle(b branding.Brand) string {
	if b.Title != "" {
		return html.EscapeString(b.Title)
	}
	return "xbin"
}

// defaultIconURI is xbin's favicon as a data URI, as the sign-in pages
// always carried it (it changes when the chosen mark is drawn).
const defaultIconURI = "data:image/svg+xml;base64,PHN2ZyB4bWxucz0iaHR0cDovL3d3dy53My5vcmcvMjAwMC9zdmciIHZpZXdCb3g9IjAgMCA2NCA2NCIgcm9sZT0iaW1nIiBhcmlhLWxhYmVsPSJYL0JJTiI+CiAgPHBhdGggZD0iTTE4IDRINTZhNCA0IDAgMCAxIDQgNHYzOEw0NiA2MEg4YTQgNCAwIDAgMS00LTRWMTh6IiBmaWxsPSIjZjVhNjIzIi8+CiAgPHBhdGggZD0iTTIxIDIxIDQzIDQzTTQzIDIxIDIxIDQzIiBzdHJva2U9IiMyMzI3MmUiIHN0cm9rZS13aWR0aD0iOSIgc3Ryb2tlLWxpbmVjYXA9ImJ1dHQiLz4KICA8Y2lyY2xlIGN4PSI1MyIgY3k9IjExIiByPSIyLjYiIGZpbGw9IiMyMzI3MmUiIG9wYWNpdHk9Ii40Ii8+CiAgPGNpcmNsZSBjeD0iMTEiIGN5PSI1MyIgcj0iMi42IiBmaWxsPSIjMjMyNzJlIiBvcGFjaXR5PSIuNCIvPgo8L3N2Zz4K"
