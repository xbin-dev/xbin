package broker

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"

	"github.com/xbin-dev/xbin/internal/auth"
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

func (b *Broker) registerPrefs(srv *server.Server) {
	srv.RegisterAPI("GET /prefs", b.apiPrefsAll)
	srv.RegisterAPI("GET /prefs/{key}", b.apiPrefsGet)
	srv.RegisterAPI("PUT /prefs/{key}", b.apiPrefsPut)
	srv.RegisterAPI("DELETE /prefs/{key}", b.apiPrefsDelete)
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

func (b *Broker) prefsPath(p auth.Principal) string {
	user, comp := prefsKeys(p)
	return filepath.Join(b.Reg.Root, "data", "prefs", util.CompKey(user), util.CompKey(comp)+".json")
}

func (b *Broker) prefsRead(p auth.Principal) (map[string]json.RawMessage, error) {
	out := map[string]json.RawMessage{}
	bts, err := os.ReadFile(b.prefsPath(p))
	if os.IsNotExist(err) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	return out, json.Unmarshal(bts, &out)
}

func (b *Broker) prefsWrite(p auth.Principal, m map[string]json.RawMessage) error {
	path := b.prefsPath(p)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	bts, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, bts, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (b *Broker) apiPrefsAll(w http.ResponseWriter, r *http.Request) {
	m, err := b.prefsRead(auth.PrincipalOf(r))
	if err != nil {
		server.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	server.WriteJSON(w, http.StatusOK, m)
}

func (b *Broker) apiPrefsGet(w http.ResponseWriter, r *http.Request) {
	m, err := b.prefsRead(auth.PrincipalOf(r))
	if err != nil {
		server.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	v, ok := m[r.PathValue("key")]
	if !ok {
		server.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "no such pref"})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(v)
}

func (b *Broker) apiPrefsPut(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalOf(r)
	var raw json.RawMessage
	if err := decodeJSON(r, &raw); err != nil {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "body must be JSON"})
		return
	}
	m, err := b.prefsRead(p)
	if err == nil {
		m[r.PathValue("key")] = raw
		err = b.prefsWrite(p, m)
	}
	if err != nil {
		server.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	server.WriteJSON(w, http.StatusOK, map[string]string{"ok": "true"})
}

func (b *Broker) apiPrefsDelete(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalOf(r)
	m, err := b.prefsRead(p)
	if err == nil {
		delete(m, r.PathValue("key"))
		err = b.prefsWrite(p, m)
	}
	if err != nil {
		server.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	server.WriteJSON(w, http.StatusOK, map[string]string{"ok": "true"})
}
