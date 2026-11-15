package server

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/util"
)

// Admin-approved workspace chrome (D118, docs/auth.md §Who is calling).
// Chrome documents run unsandboxed on the workspace origin with the session
// cookie, acting as whoever opens them. A tile's own xbin.json is writable
// from its terminals and coding agents, so its `chrome: true` is a request:
// trustedChrome honours it only for shipped chrome and for the paths a
// workspace admin approved here (kept in users.json, users/chrome.go).

func (s *Server) registerChromeAPI() {
	s.RegisterAPI("GET /chrome", s.apiChromeGet)
	s.RegisterAPI("PUT /chrome", s.apiChromePut)
}

// chromeRow is one line of GET /chrome.
type chromeRow struct {
	Path      string `json:"path"`
	Requested bool   `json:"requested"`         // its xbin.json says chrome: true
	Approved  bool   `json:"approved"`          // a workspace admin approved it
	Shipped   bool   `json:"shipped,omitempty"` // built-in chrome: honoured without approval
	Chrome    bool   `json:"chrome"`            // effective: runs unsandboxed
	Missing   bool   `json:"missing,omitempty"` // approved, but no component there now
}

func (s *Server) chromeRow(path string) chromeRow {
	row := chromeRow{Path: path, Approved: s.chromeApproved(path), Shipped: shippedChrome[path]}
	c, ok := s.Reg.Component(path)
	if !ok {
		row.Missing = row.Approved
		return row
	}
	row.Requested = c.Manifest.Chrome
	row.Chrome = s.trustedChrome(path, c)
	return row
}

// chromeRows: every component whose manifest asks for chrome, and every
// approved path, sorted. The implicit chrome (root, shell) is not listed.
func (s *Server) chromeRows() []chromeRow {
	paths := map[string]bool{}
	for _, c := range s.Reg.Components() {
		if c.Manifest.Chrome && !isChrome(c.Path) {
			paths[c.Path] = true
		}
	}
	if s.Auth != nil && s.Auth.Users != nil {
		for _, p := range s.Auth.Users.ChromeTiles() {
			paths[p] = true
		}
	}
	out := make([]chromeRow, 0, len(paths))
	for p := range paths {
		out = append(out, s.chromeRow(p))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func (s *Server) chromeAdmin(w http.ResponseWriter, r *http.Request) bool {
	if !(s.admin(r) || auth.PrincipalOf(r).IsAdmin()) {
		apiErr(w, http.StatusForbidden, "admin only — approving workspace chrome is a workspace-admin act (docs/auth.md)")
		return false
	}
	return true
}

// apiChromeGet — GET /chrome: admin → {tiles: [chromeRow]}.
func (s *Server) apiChromeGet(w http.ResponseWriter, r *http.Request) {
	if !s.chromeAdmin(w, r) {
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"tiles": s.chromeRows()})
}

// apiChromePut — PUT /chrome {path, approved}: admin. Approving needs a
// component at the path; withdrawing works on any listed path (a removed
// tile's approval is a leftover). Publishes `grants` for the tile, so open
// frames re-read its facts and re-create the iframe with the new sandbox.
func (s *Server) apiChromePut(w http.ResponseWriter, r *http.Request) {
	if !s.chromeAdmin(w, r) {
		return
	}
	if s.Auth == nil || s.Auth.Users == nil {
		apiErr(w, http.StatusNotImplemented, "this workspace has no user store to keep the approval in")
		return
	}
	var body struct {
		Path     string `json:"path"`
		Approved *bool  `json:"approved"`
	}
	if err := DecodeJSON(r, &body); err != nil || body.Approved == nil {
		apiErr(w, http.StatusBadRequest, "need {path, approved: bool}")
		return
	}
	path := strings.Trim(body.Path, "/")
	switch {
	case !util.ComponentPathOK(path):
		apiErr(w, http.StatusBadRequest, "bad tile path")
		return
	case isChrome(path):
		apiErr(w, http.StatusBadRequest, path+" is the workspace chrome itself — there is nothing to approve")
		return
	}
	if *body.Approved && auth.HoldsDelegation(path) {
		apiErr(w, http.StatusConflict, path+" acts for people (it holds delegation): its page is never trusted chrome — it would approve its own agent's requests (docs/auth.md §Delegation)")
		return
	}
	if *body.Approved {
		if _, ok := s.Reg.Component(path); !ok {
			apiErr(w, http.StatusNotFound, "no such component")
			return
		}
	}
	if err := s.Auth.Users.SetChromeApproved(path, *body.Approved); err != nil {
		apiErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if s.Hub != nil {
		s.Hub.Publish(events.Event{Type: "grants", Component: path})
	}
	WriteJSON(w, http.StatusOK, s.chromeRow(path))
}

// DelegationChromeConflict is why this workspace must not start ("nil": it
// may): a component is both a delegation holder and trusted chrome — the
// implicit chrome (root, shell), shipped chrome, or a path an admin
// approved whose manifest asks for chrome.
//
// The two can't be one component. "The person's own session" — the only
// thing that approves a ticket, a standing approval, a change set, a
// tile's agent level, and that reads the agent's log — is, in code, a
// request that carries the person's session and no component
// (delegateActorOf's own, the broker's personOwnSession). Trusted chrome is
// code that makes exactly such requests: its documents run unsandboxed on
// the workspace origin with the session cookie. A holder's page renders
// what a model wrote; as chrome, one injected script would approve its own
// agent's requests. So a holder's page holds a frame token and nothing
// else: trustedChrome answers false for a holder whatever is approved,
// PUT /chrome refuses to approve one (409), and boot calls this once the
// holder lookup is installed (auth.SetDelegateHolders) and refuses to
// start on a conflict.
func (s *Server) DelegationChromeConflict() error {
	paths := []string{"root", "shell"}
	for p := range shippedChrome {
		paths = append(paths, p)
	}
	if s.Auth != nil && s.Auth.Users != nil {
		paths = append(paths, s.Auth.Users.ChromeTiles()...)
	}
	sort.Strings(paths)
	for _, p := range paths {
		if auth.HoldsDelegation(p) {
			return fmt.Errorf("%s is a delegation holder (it acts for people) and trusted chrome (its page runs with the person's session): a component may not be both — a holder's page would approve its own agent's requests. Withdraw the chrome approval (PUT /api/xbin/chrome), or don't seal the component as a holder", p)
		}
	}
	return nil
}
