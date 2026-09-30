// partition_routes.go — the route table in a partitioned agent (API.md
// "Partitioned instances"). Unpartitioned, nothing here changes a route.
//
//   - The global instance also takes a person's calls from their own
//     partition (docs/partitions.md: a user partition calling its own global
//     instance): xbind attributes them to the person, with X-XBin-Partition
//     "user:<id>" and their role clamped to reader or writer — never the
//     tile itself — and guard() decides what they may do as that person
//     (agentRole).
//   - A person's partition serves its person's own conversations. What is
//     the tile's, not theirs, goes elsewhere (partitionRoute): a manager's
//     change of the tile-wide settings (config, classes, the halt switch, a
//     shared skill) is forwarded to the global instance, which checks them
//     as a manager and mirrors the result into conf; sharing a conversation,
//     and the channels and event triggers the global instance runs, answer
//     409 here.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// agentRole is every route's platform gate: the admin role (the tile itself —
// its frames and terminals — and its owner), as ever; in a partitioned
// agent's global instance also a person's call from their partition.
func agentRole(h http.HandlerFunc) http.Handler {
	admin := xbin.RoleFunc("admin", h)
	if !globalMode() {
		return admin
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if personFromPartition(r) {
			h(w, r)
			return
		}
		admin.ServeHTTP(w, r)
	})
}

// personFromPartition: at global, a call a person's partition (their frame,
// terminal or backend) made to it — attributed by xbind.
func personFromPartition(r *http.Request) bool {
	c := xbin.Caller(r)
	return c.From == xbin.Self() && c.User != "" && strings.HasPrefix(c.Partition, "user:") &&
		(c.Role == "reader" || c.Role == "writer")
}

// userRoute is what a person's partition does with a route.
type userRoute int

const (
	userLocal      userRoute = iota // served here
	userGlobal                      // a tile-wide setting: forwarded to the global instance
	userSkill                       // a skill: the person's own here, a shared one forwarded
	userNoShare                     // sharing: 409, the conversation is its person's alone here
	userNoChannels                  // channels and event triggers are global's: 409
)

var userRoutes = map[string]userRoute{
	"GET /config":               userGlobal, // the whole config, as managers edit it: conf holds it as viewers see it
	"PUT /config":               userGlobal,
	"PUT /classes":              userGlobal,
	"PUT /halt":                 userGlobal,
	"PUT /skills":               userSkill,
	"DELETE /skills/{name}":     userSkill,
	"POST /runs/{id}/members":   userNoShare,
	"POST /runs/{id}/links":     userNoShare,
	"POST /join":                userNoShare,
	"POST /channels/{id}/claim": userNoChannels,
	"POST /triggers":            userNoChannels,
}

// sharesInPartition: a change that would share a conversation (or an
// automation's runs) — a visibility other than private, a team role other
// than a private conversation's — which a person's partition refuses (409
// noShareWords) like the sharing routes.
func sharesInPartition(vis, role *string) bool {
	return userMode() && (vis != nil && *vis != visPrivate || role != nil && *role != roleViewer)
}

const (
	noShareWords    = "this conversation is in your own space, which only you can open: it can't be shared from here"
	noChannelsWords = "chat channels and event triggers are run by the agent's shared instance: they can't be set up from your own space"
)

// partitionRoute is h as a person's partition serves pattern.
func partitionRoute(pattern string, h http.HandlerFunc) http.HandlerFunc {
	if !userMode() {
		return h
	}
	switch userRoutes[pattern] {
	case userGlobal:
		return forwardSetting
	case userSkill:
		return func(w http.ResponseWriter, r *http.Request) { forwardSkill(w, r, h) }
	case userNoShare:
		return func(w http.ResponseWriter, _ *http.Request) { xbin.WriteError(w, http.StatusConflict, noShareWords) }
	case userNoChannels:
		return func(w http.ResponseWriter, _ *http.Request) { xbin.WriteError(w, http.StatusConflict, noChannelsWords) }
	}
	return h
}

// forwardSetting relays a manager's settings read or write to the global
// instance and, once it took a write, reads conf afresh (and applies the
// halt switch to this partition's live runs at once, rather than at their
// next step).
func forwardSetting(w http.ResponseWriter, r *http.Request) {
	body, ok := forwardBody(w, r)
	if !ok {
		return
	}
	res, ok := relay(w, r, body)
	if !ok || res.Status/100 != 2 || r.Method == http.MethodGet {
		return
	}
	if confIn != nil {
		confIn.invalidate()
	}
	if strings.HasSuffix(r.URL.Path, "/halt") && agent != nil {
		var on struct{ On bool }
		_ = json.Unmarshal(body, &on)
		if on.On {
			agent.haltStop()
		} else if agent.eng != nil {
			go agent.eng.recover()
		}
	}
}

// clearHalt takes the brake off for a manager asking for work (actor.go
// resumeIfHalted): in the settings, as ever — or, in a person's partition,
// at the global instance, which keeps it for the whole tile. false: it is
// still on.
func (ag *Agent) clearHalt() bool {
	if !userMode() {
		_ = ag.db.putSetting("halt", "")
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := callGlobal(ctx, http.MethodPut, "/halt", []byte(`{"on":false}`), "application/json")
	if err != nil || res.Status/100 != 2 {
		logf("taking the halt off at the shared instance: %v (HTTP %d %s)", err, res.Status, clip(string(res.Body), 200))
		return false
	}
	if confIn != nil {
		confIn.invalidate()
	}
	return true
}

// forwardSkill: in a person's partition a skill is theirs (this partition's
// db) or everyone's (the global instance's, mirrored into conf) — never
// another person's:
//
//   - one of theirs is served here; saving it with owner "" publishes it —
//     it is saved for everyone at the global instance, then leaves this db;
//   - a new one saved with owner = the person stays here, theirs;
//   - any other save or delete — a shared skill, a new one without an
//     owner — goes to the global instance;
//   - an owner naming anyone else is refused (400).
func forwardSkill(w http.ResponseWriter, r *http.Request, local http.HandlerFunc) {
	name := r.PathValue("name")
	body, ok := forwardBody(w, r)
	if !ok {
		return
	}
	var b struct {
		Name  string
		Owner *string
	}
	_ = json.Unmarshal(body, &b)
	if name == "" {
		name = strings.TrimSpace(b.Name)
	}
	s, err := agent.db.localSkill(name)
	own := err == nil && s != nil
	switch {
	case b.Owner != nil && *b.Owner != "" && *b.Owner != runUser:
		xbin.WriteError(w, http.StatusBadRequest, "in your own space a skill is yours or everyone's: owner is \"\" or "+strconv.Quote(runUser))
	case own && r.Method == http.MethodPut && b.Owner != nil && *b.Owner == "":
		if res, ok := relay(w, r, body); ok && res.Status/100 == 2 { // published: everyone's now, at global
			_ = agent.db.deleteSkill(name)
			if confIn != nil {
				confIn.invalidate()
			}
		}
	case own || (b.Owner != nil && *b.Owner == runUser):
		r.Body = io.NopCloser(bytes.NewReader(body))
		local(w, r)
	default:
		if res, ok := relay(w, r, body); ok && res.Status/100 == 2 && confIn != nil {
			confIn.invalidate()
		}
	}
}

// localSkill is a skill in this db only (never conf's).
func (d *DB) localSkill(name string) (*Skill, error) {
	return scanSkill(d.q.QueryRow(`SELECT `+skillCols+` FROM skills WHERE name=?`, name).Scan)
}

func forwardBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if err != nil {
		xbin.WriteError(w, http.StatusBadRequest, "reading the body: "+err.Error())
		return nil, false
	}
	return body, true
}

// relay sends r (with body) to the global instance and writes its answer.
func relay(w http.ResponseWriter, r *http.Request, body []byte) (gwResp, bool) {
	path := r.URL.Path
	if r.URL.RawQuery != "" {
		path += "?" + r.URL.RawQuery
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	res, err := callGlobal(ctx, r.Method, path, body, r.Header.Get("Content-Type"))
	if err != nil {
		xbin.WriteError(w, http.StatusBadGateway, "the agent's shared instance didn't answer: "+err.Error())
		return res, false
	}
	if res.Type != "" {
		w.Header().Set("Content-Type", res.Type)
	}
	w.WriteHeader(res.Status)
	_, _ = w.Write(res.Body)
	return res, true
}

// handleHealth answers that this instance runs, and in which mode: the
// wake-up a person's partition sends the global instance (team.go), and a
// probe for anyone who can call the tile.
func handleHealth(w http.ResponseWriter, _ *http.Request) {
	out := map[string]any{"ok": true, "mode": runMode.String()}
	if t := teamStore.Load(); t != nil {
		out["team"] = teamVersion(t.sql)
	}
	xbin.WriteJSON(w, http.StatusOK, out)
}
