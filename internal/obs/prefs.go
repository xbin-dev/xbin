package obs

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"

	"github.com/xbin-dev/xbin/internal/auth"
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
	m, err := o.prefsRead(p)
	if err == nil {
		m[r.PathValue("key")] = raw
		err = o.prefsWrite(p, m)
	}
	if err != nil {
		server.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	server.WriteOK(w)
}

func (o *Plane) apiPrefsDelete(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalOf(r)
	m, err := o.prefsRead(p)
	if err == nil {
		delete(m, r.PathValue("key"))
		err = o.prefsWrite(p, m)
	}
	if err != nil {
		server.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	server.WriteOK(w)
}
