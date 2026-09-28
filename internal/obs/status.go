package obs

import (
	"encoding/json"
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
// (build-start), or when a deploy swaps the generation its primary runs, so a
// stale problem doesn't outlive the process that had it.
// Every change publishes a `status` event on the hub, delivered like the other
// non-bus events (reload/build) — the shell renders it only for tiles it shows;
// the GET list below is read-filtered by the caller.

type statusRec struct {
	Level   string `json:"level"`   // ok | info | warn | error
	Message string `json:"message"` // short human text
	TS      int64  `json:"ts"`      // unix seconds

	// dep is the deployment that reported it (deploystatus.go): the bare
	// entry's is the primary's at the time, which a reassignment moves.
	dep string
}

var statusLevels = map[string]bool{"ok": true, "info": true, "warn": true, "error": true}

func (o *Plane) registerStatus(srv *server.Server) {
	srv.RegisterAPI("GET /tile-report", o.apiStatusList)
	srv.RegisterAPI("POST /tile-report", o.apiStatusSet)
	go o.watchStatusRestarts()
}

// GET /tile-report → {statuses:{<component>:{level,message,ts}}}. The caller sees
// only components they can read (admin: all).
func (o *Plane) apiStatusList(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalOf(r)
	admin := o.IsAdmin(p)
	out := map[string]statusRec{}
	o.statusMu.Lock()
	for comp, rec := range o.statuses {
		if isDepKey(comp) { // a non-primary deployment's: never listed (D127h)
			continue
		}
		if admin || p.CanReadTile(comp) {
			out[comp] = rec
		}
	}
	o.statusMu.Unlock()
	server.WriteJSON(w, http.StatusOK, map[string]any{"statuses": out})
}

// POST /tile-report  {level, message?, transient?, component?} — a component
// reports its own status; the owner may target another via ?component= or
// body.component. level "ok" with an empty message CLEARS it (an "ok" WITH a
// message shows a healthy indicator); transient=true fires a one-shot
// notification (toast) without touching the stored status.
func (o *Plane) apiStatusSet(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalOf(r)
	var body struct {
		Level     string `json:"level"`
		Message   string `json:"message"`
		Transient bool   `json:"transient"`
		Component string `json:"component"`
	}
	if err := server.DecodeJSON(r, &body); err != nil {
		server.WriteError(w, http.StatusBadRequest, "need {level, message?, transient?}")
		return
	}
	comp := p.Component
	if q := strings.Trim(r.URL.Query().Get("component"), "/"); q != "" {
		comp = q
	} else if body.Component != "" {
		comp = strings.Trim(body.Component, "/")
	}
	if comp == "" {
		server.WriteError(w, http.StatusBadRequest, "no target component — an element reports its own status; the owner may pass ?component=")
		return
	}
	// An element reports only for itself; admin/owner may report for any.
	if !o.IsAdmin(p) && p.Component != comp {
		server.WriteError(w, http.StatusForbidden, "a component may only report its own status")
		return
	}
	level := strings.ToLower(strings.TrimSpace(body.Level))
	if level == "" || level == "clear" {
		level = "ok"
	}
	if !statusLevels[level] {
		server.WriteError(w, http.StatusBadRequest, "level must be one of ok|info|warn|error")
		return
	}
	msg := strings.TrimSpace(body.Message)
	if len(msg) > 280 { // keep it a headline; detail belongs in the tile/logs
		msg = msg[:280]
	}
	rec := statusRec{Level: level, Message: msg, TS: time.Now().Unix()}
	// the deployment reporting: a non-primary one's status rides only the
	// deployments event (D127h)
	dep, code, err := o.reportDeployment(p, comp)
	if err != nil {
		server.WriteError(w, code, err.Error(), docsFor(code))
		return
	}
	if dep != o.primary(comp) {
		o.setDepStatus(comp, dep, rec, body.Transient)
		server.WriteOK(w)
		return
	}
	rec.dep = dep

	if body.Transient {
		o.publishStatus(comp, rec, true)
		server.WriteOK(w)
		return
	}
	o.statusMu.Lock()
	if level == "ok" && msg == "" {
		delete(o.statuses, comp) // clear
	} else {
		o.statuses[comp] = rec
	}
	o.statusMu.Unlock()
	o.publishStatus(comp, rec, false)
	server.WriteOK(w)
}

func (o *Plane) publishStatus(comp string, rec statusRec, transient bool) {
	data := map[string]any{"level": rec.Level, "message": rec.Message, "ts": rec.TS}
	if transient {
		data["transient"] = true
	}
	o.Hub.Publish(events.Event{Type: "status", Component: comp, Data: data})
}

// watchStatusRestarts clears a deployment's stored status when its backend
// (re)starts, so a problem reported before a crash/restart doesn't linger — the
// fresh process re-asserts its own status. The primary's restarts are today's
// build-start; a non-primary deployment's, the deployments event's op build,
// which clears only its own status (D127h). A deploy that puts a checkpoint on a
// deployment emits neither, so its status clears at that deploy's swap
// instead, never at its start: a failed deploy leaves the old generation
// serving with its status. A record change that moved the primary moves the
// statuses with it. Runs for the broker's lifetime.
func (o *Plane) watchStatusRestarts() {
	ch, _ := o.Hub.Subscribe(nil)
	swapped := map[string]string{} // depKey(tile, deployment) → the checkpoint whose swap cleared it, or clearedByBuild
	for e := range ch {
		if e.Component == "" {
			continue
		}
		var dep string // the deployment whose status clears
		switch f := statusFacts(e); {
		case e.Type == "build-start":
			dep = o.primary(e.Component)
			swapped[depKey(e.Component, dep)] = clearedByBuild
		case f.Op == "record":
			o.settlePrimary(e.Component)
			o.pruneStatuses(e.Component)
			continue
		case f.Op == "build" && f.Phase == "start" && f.Deployment != "":
			dep = f.Deployment
			swapped[depKey(e.Component, dep)] = clearedByBuild
		case deploySwap(e.Component, f, swapped):
			dep = f.Deployment
		default:
			continue
		}
		o.clearStatus(e.Component, dep)
	}
}

// clearedByBuild marks a tile whose status a build-start cleared: the swap
// that build leads to (a resume's) clears nothing more.
const clearedByBuild = "build-start"

// deployFacts is what the status watcher reads of a deployments event's
// data, of any Go type, through its JSON form.
type deployFacts struct {
	Op         string `json:"op"`
	Deployment string `json:"deployment"`
	Checkpoint string `json:"checkpoint"`
	Result     string `json:"result"`
	Phase      string `json:"phase"`
}

// statusFacts reads e's facts; none for any other type.
func statusFacts(e events.Event) (d deployFacts) {
	if e.Type != "deployments" {
		return d
	}
	if b, err := json.Marshal(e.Data); err == nil {
		_ = json.Unmarshal(b, &d)
	}
	return d
}

// deploySwap reports whether d announces the swap of a deploy onto a
// deployment of tile: op deploy in phase swap, running or ok. The swap
// clears once: the same swap's later events (its result, its reader form)
// find it in swapped, so a status the new generation reports after the swap
// stays; the next deploy's running phases start over.
func deploySwap(tile string, d deployFacts, swapped map[string]string) bool {
	if d.Op != "deploy" || d.Deployment == "" {
		return false
	}
	key := depKey(tile, d.Deployment)
	if d.Phase != "swap" || (d.Result != "running" && d.Result != "ok") {
		if d.Result == "running" {
			delete(swapped, key) // a deploy's turn, before its swap
		}
		return false
	}
	if c, seen := swapped[key]; seen && (c == d.Checkpoint || c == clearedByBuild) {
		return false
	}
	swapped[key] = d.Checkpoint
	return true
}
