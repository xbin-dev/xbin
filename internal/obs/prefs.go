package obs

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/fsutil"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/util"
)

// Per-user preferences (plans/multi-user.md follow-on): small, non-secret UI
// state scoped to (user × component). The shell persists its screen/tile
// layout here; any tile can keep its own per-user state the same way. Each
// caller reads/writes ONLY its own bucket — the store is keyed by the
// verified principal, so there is nothing to spoof and no cross-user or
// cross-tile access.
//
// Bucket = data/prefs/<user>/<component>.json, where user is the human's id
// (or "root" for the root token / single-user) and component is the calling
// tile ("root" for the shell / main page).
//
// A write is a read-modify-write of the whole bucket file, so writes to one
// bucket are serialised by a per-bucket lock (two clients saving different
// keys of the same bucket must not lose each other's update). Every
// successful write publishes a `prefs` event {component, data:{key, writer?}}
// seen only by that user's own clients (prefsChange.VisibleTo) — the shell
// reloads its layout when another client (the app) changed it.

func (o *Plane) registerPrefs(srv *server.Server) {
	srv.RegisterAPI("GET /prefs", o.apiPrefsAll)
	srv.RegisterAPI("GET /prefs/{key}", o.apiPrefsGet)
	srv.RegisterAPI("PUT /prefs/{key}", o.apiPrefsPut)
	srv.RegisterAPI("DELETE /prefs/{key}", o.apiPrefsDelete)
}

func prefsKeys(p auth.Principal) (user, comp string) {
	user = p.UserID
	if user == "" {
		user = "root" // root token / single-user
	}
	comp = p.Component
	if comp == "" {
		comp = "root" // the shell / main page
	}
	return
}

func (o *Plane) prefsPath(p auth.Principal) string {
	user, comp := prefsKeys(p)
	return filepath.Join(o.Root, "data", "prefs", util.CompKey(user), util.CompKey(comp)+".json")
}

func (o *Plane) prefsRead(p auth.Principal) (map[string]json.RawMessage, error) {
	out := map[string]json.RawMessage{}
	bts, err := os.ReadFile(o.prefsPath(p))
	if os.IsNotExist(err) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	return out, json.Unmarshal(bts, &out)
}

func (o *Plane) prefsWrite(p auth.Principal, m map[string]json.RawMessage) error {
	path := o.prefsPath(p)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	bts, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(path, bts, 0o644)
}

// prefsLock returns the lock guarding one bucket file.
func (o *Plane) prefsLock(p auth.Principal) *sync.Mutex {
	path := o.prefsPath(p)
	o.prefsMu.Lock()
	defer o.prefsMu.Unlock()
	if o.prefsLocks == nil {
		o.prefsLocks = map[string]*sync.Mutex{}
	}
	mu := o.prefsLocks[path]
	if mu == nil {
		mu = &sync.Mutex{}
		o.prefsLocks[path] = mu
	}
	return mu
}

// prefsUpdate applies fn to p's bucket under the bucket's lock and writes it.
func (o *Plane) prefsUpdate(p auth.Principal, fn func(map[string]json.RawMessage)) error {
	mu := o.prefsLock(p)
	mu.Lock()
	defer mu.Unlock()
	m, err := o.prefsRead(p)
	if err != nil {
		return err
	}
	fn(m)
	return o.prefsWrite(p, m)
}

// prefsChange is a `prefs` event's data: which key of the bucket named by
// the event's component changed, and the writer id the writing client sent
// (X-Prefs-Writer) so it can tell its own writes from another client's.
// user is the bucket's owner — never on the wire, only for VisibleTo.
type prefsChange struct {
	Key    string `json:"key"`
	Writer string `json:"writer,omitempty"`
	user   string
	comp   string
}

// VisibleTo scopes a `prefs` event to the bucket's owner: the user's own
// human sessions (browser, app) see every one of their buckets; an element
// principal (a tile's frame token, a terminal, a backend) only its own
// bucket — the same reach GET /prefs gives it. Never another user's, not
// even an admin's.
func (c prefsChange) VisibleTo(p auth.Principal) bool {
	user, comp := prefsKeys(p)
	if user != c.user {
		return false
	}
	return p.Component == "" || comp == c.comp
}

func (o *Plane) prefsChanged(r *http.Request, p auth.Principal, key string) {
	if o.Hub == nil {
		return
	}
	user, comp := prefsKeys(p)
	w := r.Header.Get("X-Prefs-Writer")
	if len(w) > 64 {
		w = w[:64]
	}
	o.Hub.Publish(events.Event{Type: "prefs", Component: comp, Data: prefsChange{Key: key, Writer: w, user: user, comp: comp}})
}

func (o *Plane) apiPrefsAll(w http.ResponseWriter, r *http.Request) {
	m, err := o.prefsRead(auth.PrincipalOf(r))
	if err != nil {
		server.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	server.WriteJSON(w, http.StatusOK, m)
}

func (o *Plane) apiPrefsGet(w http.ResponseWriter, r *http.Request) {
	m, err := o.prefsRead(auth.PrincipalOf(r))
	if err != nil {
		server.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	v, ok := m[r.PathValue("key")]
	if !ok {
		server.WriteError(w, http.StatusNotFound, "no such pref")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(v)
}

func (o *Plane) apiPrefsPut(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalOf(r)
	var raw json.RawMessage
	if err := server.DecodeJSON(r, &raw); err != nil {
		server.WriteError(w, http.StatusBadRequest, "body must be JSON")
		return
	}
	key := r.PathValue("key")
	if err := o.prefsUpdate(p, func(m map[string]json.RawMessage) { m[key] = raw }); err != nil {
		server.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	o.prefsChanged(r, p, key)
	server.WriteOK(w)
}

func (o *Plane) apiPrefsDelete(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalOf(r)
	key := r.PathValue("key")
	if err := o.prefsUpdate(p, func(m map[string]json.RawMessage) { delete(m, key) }); err != nil {
		server.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	o.prefsChanged(r, p, key)
	server.WriteOK(w)
}
