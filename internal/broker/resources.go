package broker

import (
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	bolt "go.etcd.io/bbolt"

	"github.com/magik6k/buxon/internal/auth"
	"github.com/magik6k/buxon/internal/events"
	"github.com/magik6k/buxon/internal/registry"
	"github.com/magik6k/buxon/internal/server"
	"github.com/magik6k/buxon/internal/util"
)

// Resource provisioning + delivery (plans/auth.md §5, docs/resources.md).
//
//   sqlite — a file under data/resources/<scope-key>/<name>.sqlite; the path
//            is handed via env to same-scope components only (cross-scope db
//            sharing goes through service APIs, not shared files).
//   kv     — bbolt buckets behind /api/buxon/kv/…
//   blob   — a quota'd directory behind /api/buxon/blob/…
//   bus    — pub/sub topics on the events hub, /api/buxon/bus/publish
//   cron   — scheduled calls to the owning element's endpoints (cron.go)

// Provision creates on-disk state for declared resources. Called at start
// and after every rescan; idempotent.
func (b *Broker) Provision() {
	do := func(scope string, resources map[string]registry.Resource) {
		for name, res := range resources {
			dir := filepath.Join(b.Reg.Root, "data", "resources", util.ScopeKey(scope))
			switch res.Type {
			case "sqlite", "cron":
				if err := os.MkdirAll(dir, 0o755); err != nil {
					slog.Warn("provision", "err", err)
				}
			case "filesystem", "blob":
				if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
					slog.Warn("provision", "err", err)
				}
			case "kv", "bus":
				// kv lives in the shared bbolt db; bus is in-memory.
			default:
				slog.Warn("unknown resource type", "scope", scope, "name", name, "type", res.Type)
			}
			if b.uids != nil && scope != "" {
				b.uids.chownScopeData(dir, scope)
			}
		}
	}
	do("", b.Reg.Workspace().Resources)
	for scope, sm := range b.Reg.Scopes() {
		do(scope, sm.Resources)
	}
}

// EnvFor is installed into the runner: resource env for a component instance.
// Every granted resource yields BUXON_RES_<NAME>=<dsn>; sqlite additionally
// resolves to a direct file path when caller and resource share a scope.
func (b *Broker) EnvFor(c *registry.Component) []string {
	var env []string
	for _, u := range c.Manifest.Uses {
		rt, res, ok := b.parseRes(u.Target)
		if !ok || res == nil {
			continue
		}
		if _, granted := b.grantedRole(c.Path, rt.String()); !granted {
			continue
		}
		key := "BUXON_RES_" + envName(rt.Name)
		switch {
		case res.Type == "filesystem" && rt.Scope == c.Scope:
			// A rw directory the backend owns — BUXON_RES_<N> is the DIR path
			// (put a db, files, a cache… anything). buxond binds it rw.
			p := filepath.Join(b.Reg.Root, "data", "resources", util.ScopeKey(rt.Scope), rt.Name)
			env = append(env, key+"="+p)
		case res.Type == "sqlite" && rt.Scope == c.Scope:
			// Convenience over `filesystem`: BUXON_RES_<N> points at a .sqlite
			// FILE in that dir (the dir is still what's bound rw).
			p := filepath.Join(b.Reg.Root, "data", "resources", util.ScopeKey(rt.Scope), rt.Name+".sqlite")
			env = append(env, key+"="+p)
		case res.Type == "filesystem" || res.Type == "sqlite":
			// Cross-scope direct filesystem is deliberately not shared; the
			// owning scope should expose an API (docs/resources.md).
		default:
			env = append(env, key+"="+rt.String())
		}
	}
	// http interface slots → a URL the backend calls the bound provider at
	// (via the gateway); the binding also grants the call (plans/interfaces.md).
	for slot, iface := range b.HTTPInterfaces(c.Path) {
		env = append(env, "BUXON_IFACE_"+envName(slot)+"_URL=http://buxon"+iface["url"])
	}
	return env
}

func envName(s string) string {
	return strings.ToUpper(strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			return r
		}
		return '_'
	}, s))
}

// --- kv ------------------------------------------------------------------

type kvStore struct{ db *bolt.DB }

func openKV(root string) (*kvStore, error) {
	dir := filepath.Join(root, "data")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	db, err := bolt.Open(filepath.Join(dir, "kv.db"), 0o600, nil)
	if err != nil {
		return nil, err
	}
	return &kvStore{db: db}, nil
}

// kvAccess parses /kv/{res-target}/{key...} and authorizes.
// URL form: /api/buxon/kv/res:<scope>/<name>/<key…>
func (b *Broker) kvAccess(w http.ResponseWriter, r *http.Request, want string) (bucket, key string, ok bool) {
	rest := strings.Trim(r.PathValue("rest"), "/")
	if !strings.HasPrefix(rest, "res:") {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "kv paths are /api/buxon/kv/res:<scope>/<name>/<key>", "docs": "/docs/resources.md"})
		return "", "", false
	}
	// Find the declared resource by longest prefix.
	probe := rest
	for probe != "res:" {
		if rt, res, found := b.parseRes(probe); found && res != nil && res.Type == "kv" {
			key = strings.TrimPrefix(rest, probe)
			key = strings.TrimPrefix(key, "/")
			if err := b.allowRes(auth.PrincipalOf(r), rt.String(), want); err != nil {
				server.WriteJSON(w, http.StatusForbidden, map[string]string{"error": err.Error(), "docs": "/docs/auth.md"})
				return "", "", false
			}
			return rt.String(), key, true
		}
		i := strings.LastIndex(probe, "/")
		if i < 0 {
			break
		}
		probe = probe[:i]
	}
	server.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "no such kv resource", "docs": "/docs/resources.md"})
	return "", "", false
}

func (b *Broker) apiKVGet(w http.ResponseWriter, r *http.Request) {
	bucket, key, ok := b.kvAccess(w, r, "reader")
	if !ok {
		return
	}
	if key == "" { // list keys, optional ?prefix=
		prefix := []byte(r.URL.Query().Get("prefix"))
		var keys []string
		_ = b.kv.db.View(func(tx *bolt.Tx) error {
			bk := tx.Bucket([]byte(bucket))
			if bk == nil {
				return nil
			}
			c := bk.Cursor()
			for k, _ := c.Seek(prefix); k != nil && strings.HasPrefix(string(k), string(prefix)); k, _ = c.Next() {
				keys = append(keys, string(k))
			}
			return nil
		})
		sort.Strings(keys)
		server.WriteJSON(w, http.StatusOK, map[string]any{"keys": keys})
		return
	}
	var val []byte
	_ = b.kv.db.View(func(tx *bolt.Tx) error {
		if bk := tx.Bucket([]byte(bucket)); bk != nil {
			if v := bk.Get([]byte(key)); v != nil {
				val = append([]byte{}, v...)
			}
		}
		return nil
	})
	if val == nil {
		server.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "no such key"})
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	_, _ = w.Write(val)
}

func (b *Broker) apiKVPut(w http.ResponseWriter, r *http.Request) {
	bucket, key, ok := b.kvAccess(w, r, "writer")
	if !ok {
		return
	}
	if key == "" {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "missing key"})
		return
	}
	val, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	err = b.kv.db.Update(func(tx *bolt.Tx) error {
		bk, err := tx.CreateBucketIfNotExists([]byte(bucket))
		if err != nil {
			return err
		}
		return bk.Put([]byte(key), val)
	})
	if err != nil {
		server.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	server.WriteJSON(w, http.StatusOK, map[string]string{"ok": "true"})
}

func (b *Broker) apiKVDelete(w http.ResponseWriter, r *http.Request) {
	bucket, key, ok := b.kvAccess(w, r, "writer")
	if !ok {
		return
	}
	err := b.kv.db.Update(func(tx *bolt.Tx) error {
		if bk := tx.Bucket([]byte(bucket)); bk != nil {
			return bk.Delete([]byte(key))
		}
		return nil
	})
	if err != nil {
		server.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	server.WriteJSON(w, http.StatusOK, map[string]string{"ok": "true"})
}

// --- blob ------------------------------------------------------------------

func (b *Broker) blobAccess(w http.ResponseWriter, r *http.Request, want string) (dir, rel string, ok bool) {
	rest := strings.Trim(r.PathValue("rest"), "/")
	probe := rest
	for strings.HasPrefix(probe, "res:") {
		if rt, res, found := b.parseRes(probe); found && res != nil && res.Type == "blob" {
			rel = strings.TrimPrefix(strings.TrimPrefix(rest, probe), "/")
			if err := b.allowRes(auth.PrincipalOf(r), rt.String(), want); err != nil {
				server.WriteJSON(w, http.StatusForbidden, map[string]string{"error": err.Error(), "docs": "/docs/auth.md"})
				return "", "", false
			}
			base := filepath.Join(b.Reg.Root, "data", "resources", util.ScopeKey(rt.Scope), rt.Name)
			full, _, err := util.SafeJoin(base, rel)
			if err != nil {
				server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "bad path"})
				return "", "", false
			}
			return full, rel, true
		}
		i := strings.LastIndex(probe, "/")
		if i < 0 {
			break
		}
		probe = probe[:i]
	}
	server.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "no such blob resource", "docs": "/docs/resources.md"})
	return "", "", false
}

func (b *Broker) apiBlobGet(w http.ResponseWriter, r *http.Request) {
	full, _, ok := b.blobAccess(w, r, "reader")
	if !ok {
		return
	}
	fi, err := os.Stat(full)
	if err != nil {
		server.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	if fi.IsDir() {
		entries, _ := os.ReadDir(full)
		var names []string
		for _, e := range entries {
			n := e.Name()
			if e.IsDir() {
				n += "/"
			}
			names = append(names, n)
		}
		server.WriteJSON(w, http.StatusOK, map[string]any{"entries": names})
		return
	}
	http.ServeFile(w, r, full)
}

func (b *Broker) apiBlobPut(w http.ResponseWriter, r *http.Request) {
	full, rel, ok := b.blobAccess(w, r, "writer")
	if !ok {
		return
	}
	if rel == "" {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "missing blob path"})
		return
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		server.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	f, err := os.Create(full)
	if err != nil {
		server.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	defer f.Close()
	if _, err := io.Copy(f, io.LimitReader(r.Body, 256<<20)); err != nil {
		server.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	server.WriteJSON(w, http.StatusOK, map[string]string{"ok": "true"})
}

func (b *Broker) apiBlobDelete(w http.ResponseWriter, r *http.Request) {
	full, rel, ok := b.blobAccess(w, r, "writer")
	if !ok {
		return
	}
	if rel == "" {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "missing blob path"})
		return
	}
	if err := os.Remove(full); err != nil {
		server.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	server.WriteJSON(w, http.StatusOK, map[string]string{"ok": "true"})
}

// --- bus ---------------------------------------------------------------

func (b *Broker) apiBusPublish(w http.ResponseWriter, r *http.Request) {
	var msg struct {
		Resource string `json:"resource"`
		Topic    string `json:"topic"`
		Data     any    `json:"data"`
	}
	if err := decodeJSON(r, &msg); err != nil || msg.Resource == "" || msg.Topic == "" {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "need {resource, topic, data?}", "docs": "/docs/resources.md"})
		return
	}
	rt, res, ok := b.parseRes(msg.Resource)
	if !ok || res == nil || res.Type != "bus" {
		server.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "no such bus resource", "docs": "/docs/resources.md"})
		return
	}
	if err := b.allowRes(auth.PrincipalOf(r), rt.String(), "writer"); err != nil {
		server.WriteJSON(w, http.StatusForbidden, map[string]string{"error": err.Error(), "docs": "/docs/auth.md"})
		return
	}
	b.Hub.Publish(events.Event{
		Type:  "bus",
		Topic: rt.String() + "/" + msg.Topic,
		Data:  msg.Data,
	})
	server.WriteJSON(w, http.StatusOK, map[string]string{"ok": "true"})
}

// busFilter authorizes bus event delivery to a WS subscriber (installed as
// server.BusFilter; owner passes upstream of this).
func (b *Broker) busFilter(p auth.Principal, e events.Event) bool {
	if p.Component == "" {
		return false
	}
	// e.Topic = "res:<scope-or-workspace>/<name>/<topic…>"
	probe := e.Topic
	for strings.HasPrefix(probe, "res:") {
		if rt, res, found := b.parseRes(probe); found && res != nil && res.Type == "bus" {
			return b.allowRes(p, rt.String(), "reader") == nil
		}
		i := strings.LastIndex(probe, "/")
		if i < 0 {
			break
		}
		probe = probe[:i]
	}
	return false
}
