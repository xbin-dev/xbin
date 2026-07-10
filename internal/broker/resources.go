package broker

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	bolt "go.etcd.io/bbolt"

	"github.com/magik6k/xbin/internal/auth"
	"github.com/magik6k/xbin/internal/events"
	"github.com/magik6k/xbin/internal/registry"
	"github.com/magik6k/xbin/internal/server"
	"github.com/magik6k/xbin/internal/util"
)

// Resource provisioning + delivery (plans/auth.md §5, docs/resources.md).
//
//   sqlite — a file under data/resources/<scope-key>/<name>.sqlite; the path
//            is handed via env to same-scope components only (cross-scope db
//            sharing goes through service APIs, not shared files).
//   kv     — bbolt buckets behind /api/xbin/kv/…
//   blob   — a quota'd directory behind /api/xbin/blob/…
//   bus    — pub/sub topics on the events hub, /api/xbin/bus/publish
//   cron   — scheduled calls to the owning element's endpoints (cron.go)

// Provision creates on-disk state for declared resources. Called at start
// and after every rescan; idempotent.
func (b *Broker) Provision() {
	do := func(scope string, resources map[string]registry.Resource) {
		scopeKey := util.ScopeKey(scope)
		dir := filepath.Join(b.Reg.Root, "data", "resources", scopeKey)
		for name, res := range resources {
			switch res.Type {
			case "cron":
				if err := os.MkdirAll(dir, 0o755); err != nil {
					slog.Warn("provision", "err", err)
				}
			case "filesystem", "sqlite", "blob":
				// Always encrypted: a per-resource gocryptfs mount stood up by
				// MountEncrypted (below) when the vault is unsealed. No plaintext
				// dir is ever created.
			case "kv", "bus":
				// kv lives in the shared bbolt db (values encrypted per bucket);
				// bus is in-memory.
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
	b.MountEncrypted() // mount any (new) encrypted file resources when unsealed
}

// EnvFor is installed into the runner: resource env for a component instance.
// Every granted resource yields XBIN_RES_<NAME>=<dsn>; sqlite additionally
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
		key := "XBIN_RES_" + envName(rt.Name)
		switch {
		case res.Type == "filesystem" && rt.Scope == c.Scope:
			// A rw directory the backend owns — XBIN_RES_<N> is the DIR path
			// (put a db, files, a cache… anything). xbind binds it rw. When
			// encrypted this is the decrypted gocryptfs mount (resenc).
			env = append(env, key+"="+b.fsResPath(rt.Scope, rt.Name, false))
		case res.Type == "sqlite" && rt.Scope == c.Scope:
			// Convenience over `filesystem`: XBIN_RES_<N> points at a .sqlite
			// FILE in that dir (the dir is still what's bound rw).
			env = append(env, key+"="+b.fsResPath(rt.Scope, rt.Name, true))
		case res.Type == "filesystem" || res.Type == "sqlite":
			// Cross-scope direct filesystem is deliberately not shared; the
			// owning scope should expose an API (docs/resources.md).
		default:
			env = append(env, key+"="+rt.String())
		}
	}
	// http interface slots → URLs the backend calls the bound provider(s) at
	// (via the gateway); the binding also grants the call (plans/interfaces.md).
	// Single slots: XBIN_IFACE_<slot>_URL (+_INSTANCE when bound to one).
	// multi:true slots: XBIN_IFACE_<slot> = JSON [{provider,instance?,url,service}].
	for slot, ri := range b.HTTPSlots(c.Path) {
		if ri.Def.Multi {
			list := make([]map[string]string, 0, len(ri.Endpoints))
			for _, e := range ri.Endpoints {
				m := map[string]string{"provider": e.Provider, "url": "http://xbin" + e.URL, "service": ri.Def.Service}
				if e.Instance != "" {
					m["instance"] = e.Instance
				}
				list = append(list, m)
			}
			j, _ := json.Marshal(list)
			env = append(env, "XBIN_IFACE_"+envName(slot)+"="+string(j))
			continue
		}
		if len(ri.Endpoints) > 0 {
			env = append(env, "XBIN_IFACE_"+envName(slot)+"_URL=http://xbin"+ri.Endpoints[0].URL)
			if inst := ri.Endpoints[0].Instance; inst != "" {
				env = append(env, "XBIN_IFACE_"+envName(slot)+"_INSTANCE="+inst)
			}
		}
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
// URL form: /api/xbin/kv/res:<scope>/<name>/<key…>
func (b *Broker) kvAccess(w http.ResponseWriter, r *http.Request, want string) (bucket, key string, ok bool) {
	rest := strings.Trim(r.PathValue("rest"), "/")
	if !strings.HasPrefix(rest, "res:") {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "kv paths are /api/xbin/kv/res:<scope>/<name>/<key>", "docs": "/docs/resources.md"})
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
			if !b.quotaOK(w, rt.Scope, want) {
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
	plain, err := b.decodeKV(bucket, val)
	if err != nil {
		server.WriteJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "vault sealed — unseal to read encrypted resource data", "docs": "/docs/auth.md"})
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	_, _ = w.Write(plain)
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
	stored, err := b.encodeKV(bucket, val)
	if err != nil {
		server.WriteJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "vault sealed — unseal to write encrypted resource data", "docs": "/docs/auth.md"})
		return
	}
	err = b.kv.db.Update(func(tx *bolt.Tx) error {
		bk, err := tx.CreateBucketIfNotExists([]byte(bucket))
		if err != nil {
			return err
		}
		return bk.Put([]byte(key), stored)
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
			if !b.quotaOK(w, rt.Scope, want) {
				return "", "", false
			}
			// blob is always an encrypted gocryptfs mount; refuse until it's up
			// (vault sealed / gocryptfs missing) so we never read or write plaintext
			// into the bare mountpoint.
			if !b.fsReady(util.ScopeKey(rt.Scope), rt.Name) {
				server.WriteJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "resource unavailable — vault sealed or encryption not ready", "docs": "/docs/auth.md"})
				return "", "", false
			}
			base := b.fsResPath(rt.Scope, rt.Name, false) // decrypted gocryptfs mount
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
