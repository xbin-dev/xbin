package broker

import (
	"net/http"
	"strings"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/server"
)

// Component status & notifications — a small channel for a component to tell the
// workspace how it's doing: health, a self-clearing problem, or a one-shot
// user notification. The shell surfaces it as a breathing dot on the tile/folder
// and a tint on the screen tab (workspace-template/shell + AGENTS.md guidelines).
//
// A component reports its OWN status (element-self-scoped); the owner may report
// for any component (?component= / body.component). State is in-memory
// (component → last record), reset when the component's backend restarts
// (build-start) so a stale problem doesn't outlive the process that had it.
// Every change publishes a `status` event on the hub, delivered like the other
// non-bus events (reload/build) — the shell renders it only for tiles it shows;
// the GET list below is read-filtered by the caller.

type statusRec struct {
	Level   string `json:"level"`   // ok | info | warn | error
	Message string `json:"message"` // short human text
	TS      int64  `json:"ts"`      // unix seconds
}

var statusLevels = map[string]bool{"ok": true, "info": true, "warn": true, "error": true}

func (b *Broker) registerStatus(srv *server.Server) {
	b.statuses = map[string]statusRec{}
	srv.RegisterAPI("GET /tile-report", b.apiStatusList)
	srv.RegisterAPI("POST /tile-report", b.apiStatusSet)
	go b.watchStatusRestarts()
}

// GET /tile-report → {statuses:{<component>:{level,message,ts}}}. The caller sees
// only components they can read (admin: all).
func (b *Broker) apiStatusList(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalOf(r)
	admin := b.IsAdmin(p)
	out := map[string]statusRec{}
	b.statusMu.Lock()
	for comp, rec := range b.statuses {
		if admin || p.CanReadTile(comp) {
			out[comp] = rec
		}
	}
	b.statusMu.Unlock()
	server.WriteJSON(w, http.StatusOK, map[string]any{"statuses": out})
}

// POST /tile-report  {level, message?, transient?, component?} — a component
// reports its own status; the owner may target another via ?component= or
// body.component. level "ok" with an empty message CLEARS it (an "ok" WITH a
// message shows a healthy indicator); transient=true fires a one-shot
// notification (toast) without touching the stored status.
func (b *Broker) apiStatusSet(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalOf(r)
	var body struct {
		Level     string `json:"level"`
		Message   string `json:"message"`
		Transient bool   `json:"transient"`
		Component string `json:"component"`
	}
	if err := decodeJSON(r, &body); err != nil {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "need {level, message?, transient?}"})
		return
	}
	comp := p.Component
	if q := strings.Trim(r.URL.Query().Get("component"), "/"); q != "" {
		comp = q
	} else if body.Component != "" {
		comp = strings.Trim(body.Component, "/")
	}
	if comp == "" {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "no target component — an element reports its own status; the owner may pass ?component="})
		return
	}
	// An element reports only for itself; admin/owner may report for any.
	if !b.IsAdmin(p) && p.Component != comp {
		server.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "a component may only report its own status"})
		return
	}
	level := strings.ToLower(strings.TrimSpace(body.Level))
	if level == "" || level == "clear" {
		level = "ok"
	}
	if !statusLevels[level] {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "level must be one of ok|info|warn|error"})
		return
	}
	msg := strings.TrimSpace(body.Message)
	if len(msg) > 280 { // keep it a headline; detail belongs in the tile/logs
		msg = msg[:280]
	}
	rec := statusRec{Level: level, Message: msg, TS: time.Now().Unix()}

	if body.Transient {
		b.publishStatus(comp, rec, true)
		server.WriteJSON(w, http.StatusOK, map[string]string{"ok": "true"})
		return
	}
	b.statusMu.Lock()
	if level == "ok" && msg == "" {
		delete(b.statuses, comp) // clear
	} else {
		b.statuses[comp] = rec
	}
	b.statusMu.Unlock()
	b.publishStatus(comp, rec, false)
	server.WriteJSON(w, http.StatusOK, map[string]string{"ok": "true"})
}

func (b *Broker) publishStatus(comp string, rec statusRec, transient bool) {
	data := map[string]any{"level": rec.Level, "message": rec.Message, "ts": rec.TS}
	if transient {
		data["transient"] = true
	}
	b.Hub.Publish(events.Event{Type: "status", Component: comp, Data: data})
}

// watchStatusRestarts clears a component's stored status when its backend
// (re)starts, so a problem reported before a crash/restart doesn't linger — the
// fresh process re-asserts its own status. Runs for the broker's lifetime.
func (b *Broker) watchStatusRestarts() {
	ch, _ := b.Hub.Subscribe(nil)
	for e := range ch {
		if e.Type != "build-start" || e.Component == "" {
			continue
		}
		b.statusMu.Lock()
		_, had := b.statuses[e.Component]
		delete(b.statuses, e.Component)
		b.statusMu.Unlock()
		if had {
			b.publishStatus(e.Component, statusRec{Level: "ok", TS: time.Now().Unix()}, false)
		}
	}
}
