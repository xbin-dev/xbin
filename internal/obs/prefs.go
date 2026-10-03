package obs

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"sync"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/prefsfile"
	"github.com/xbin-dev/xbin/internal/server"
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
// tile ("root" for the shell / main page); internal/prefsfile is the path
// rule's one home (the document injection reads the shell's bucket for the
// person's theme, D184). A tile deployment beyond main keeps its own
// buckets (deployprefs.go).
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
		user = prefsfile.Root // root token / single-user
	}
	comp = p.Component
	if comp == "" {
		comp = prefsfile.Root // the shell / main page
	}
	return
}

// prefsBucket is p's bucket file and, when p acts in a tile deployment
// beyond main, that deployment's name ("" for main's bucket and a person's).
func (o *Plane) prefsBucket(p auth.Principal) (path, dep string, err error) {
	user, comp := prefsKeys(p)
	if dep, err = o.prefsDeployment(p); err != nil {
		return "", "", err
	}
	if dep != "" {
		return filepath.Join(prefsfile.Dir(o.Root, user), prefsDeploymentFile(comp, dep)), dep, nil
	}
	return prefsfile.Path(o.Root, user, comp), "", nil
}

func (o *Plane) prefsRead(p auth.Principal) (map[string]json.RawMessage, error) {
	path, _, err := o.prefsBucket(p)
	if err != nil {
		return nil, err
	}
	return prefsfile.Read(path)
}

// prefsLock returns the lock guarding one bucket file.
func (o *Plane) prefsLock(path string) *sync.Mutex {
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

// prefsUpdate applies fn to p's bucket under the bucket's lock and writes
// it. The bucket is resolved once, so a deployment's bucket (deployprefs.go)
// is locked, read and written as one file even if the primary is reassigned
// meanwhile. dep is the bucket's deployment beyond main, as prefsBucket.
func (o *Plane) prefsUpdate(p auth.Principal, fn func(map[string]json.RawMessage)) (dep string, err error) {
	path, dep, err := o.prefsBucket(p)
	if err != nil {
		return "", err
	}
	mu := o.prefsLock(path)
	mu.Lock()
	defer mu.Unlock()
	m, err := prefsfile.Read(path)
	if err != nil {
		return "", err
	}
	fn(m)
	return dep, prefsfile.Write(path, m)
}

// prefsChange is a `prefs` event's data: which key of the bucket named by
// the event's component changed, and the writer id the writing client sent
// (X-Prefs-Writer) so it can tell its own writes from another client's.
// user is the bucket's owner, dep the bucket's tile deployment beyond main
// ("" for main's and a person's) and reach the deployment whose bucket a
// principal reads (prefsDeployment) — never on the wire, only for VisibleTo.
type prefsChange struct {
	Key    string `json:"key"`
	Writer string `json:"writer,omitempty"`
	user   string
	comp   string
	dep    string
	reach  func(auth.Principal) (string, error)
}

// VisibleTo scopes a `prefs` event to the bucket's owner: the user's own
// human sessions (browser, app) see every one of their buckets in main's
// namespace; an element principal (a tile's frame token, a terminal, a
// backend) only its own bucket — the same reach GET /prefs gives it, so a
// tile deployment's principals hear their deployment's bucket and not
// main's, and main's never another deployment's. A bucket beyond main
// reaches no human session: today's event types never speak of another
// deployment (docs/protocol.md §Tile deployments). Never another user's,
// not even an admin's.
func (c prefsChange) VisibleTo(p auth.Principal) bool {
	user, comp := prefsKeys(p)
	if user != c.user {
		return false
	}
	if p.Component == "" {
		return c.dep == ""
	}
	if comp != c.comp {
		return false
	}
	dep := ""
	if c.reach != nil {
		var err error
		if dep, err = c.reach(p); err != nil {
			return false
		}
	}
	return dep == c.dep
}

func (o *Plane) prefsChanged(r *http.Request, p auth.Principal, key, dep string) {
	if o.Hub == nil {
		return
	}
	user, comp := prefsKeys(p)
	w := r.Header.Get("X-Prefs-Writer")
	if len(w) > 64 {
		w = w[:64]
	}
	o.Hub.Publish(events.Event{Type: "prefs", Component: comp, Data: prefsChange{Key: key, Writer: w,
		user: user, comp: comp, dep: dep, reach: o.prefsDeployment}})
}

func (o *Plane) apiPrefsAll(w http.ResponseWriter, r *http.Request) {
	m, err := o.prefsRead(auth.PrincipalOf(r))
	if err != nil {
		writePrefsErr(w, err)
		return
	}
	server.WriteJSON(w, http.StatusOK, m)
}

func (o *Plane) apiPrefsGet(w http.ResponseWriter, r *http.Request) {
	m, err := o.prefsRead(auth.PrincipalOf(r))
	if err != nil {
		writePrefsErr(w, err)
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
	dep, err := o.prefsUpdate(p, func(m map[string]json.RawMessage) { m[key] = raw })
	if err != nil {
		writePrefsErr(w, err)
		return
	}
	o.prefsChanged(r, p, key, dep)
	server.WriteOK(w)
}

func (o *Plane) apiPrefsDelete(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalOf(r)
	key := r.PathValue("key")
	dep, err := o.prefsUpdate(p, func(m map[string]json.RawMessage) { delete(m, key) })
	if err != nil {
		writePrefsErr(w, err)
		return
	}
	o.prefsChanged(r, p, key, dep)
	server.WriteOK(w)
}
