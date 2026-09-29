package broker

import (
	"cmp"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	bolt "go.etcd.io/bbolt"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/sandbox"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/util"
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
// and after every rescan; idempotent. It provisions main's namespaces, each
// from main's own code (declaredFrom): the registry's scopes while main is
// the primary, so a scope rooted by a tile whose primary is pinned
// provisions what the checkpoint's scope.json declares, read beneath its
// tree and checked before it gets here (D127n). A save therefore reaches only
// the namespace of the deployment that follows the work tree: main's here,
// and a deployment beyond main's through its declared set, which is read
// from its code when asked. A namespace beyond main has nothing to
// provision on disk: its kv file is made by its first write and its volumes
// on their first use (08-data §3.6, §3.7). A resource no longer declared is
// kept: nothing here removes data.
func (b *Broker) Provision() {
	do := func(scope string, resources map[string]registry.Resource) {
		sk, _ := scopeKeys(scope, util.MainDeployment) // main's keys: never an error
		dir := filepath.Join(b.Reg.Root, filepath.FromSlash(sk.Plain))
		for name, res := range resources {
			if _, err := b.resKeys(resTarget{Scope: scope, Name: name}, util.MainDeployment); err != nil {
				slog.Warn("provision: not provisioned", "err", err) // a refused name (NP-08-11)
				continue
			}
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
		res, err := b.declaredFrom(scope, sm, util.MainDeployment)
		if err != nil {
			slog.Warn("provision: main's declarations", "scope", scope, "err", err)
		}
		do(scope, res)
	}
	b.MountEncrypted() // mount any (new) encrypted file resources when unsealed
}

// EnvFor is installed into the runner: resource env for a component instance.
// Every granted resource yields XBIN_RES_<NAME>=<dsn>; sqlite additionally
// resolves to a direct file path when caller and resource share a scope.
//
// c may be a deployment view: its env is the deployment's (viewDeployment),
// whose own-scope uses resolve in its own declared set, so a resource only
// some deployments declare yields its variable only in those (D127n). Every
// value is the same in every deployment that declares the resource: the
// canonical path (main's mount) or the canonical id (D127j); DeploymentEnv
// adds the remap that binds a deployment's own volume at that path.
func (b *Broker) EnvFor(c *registry.Component) []string {
	dep := b.viewDeployment(c)
	var env []string
	for _, u := range c.Manifest.Uses {
		rt, res, ok := b.envTarget(c, dep, u.Target)
		if !ok {
			continue
		}
		if _, granted := b.grantedRole(c.Path, rt.String()); !granted {
			if _, granted = b.ownUse(c.Path, rt); !granted {
				continue
			}
		}
		key := "XBIN_RES_" + envName(rt.Name)
		switch {
		case res.Type == "filesystem" && rt.Scope == c.Scope:
			// A rw directory the backend owns — XBIN_RES_<N> is the DIR path
			// (put a db, files, a cache… anything). xbind binds it rw. When
			// encrypted this is the decrypted gocryptfs mount (resenc).
			if p := b.fsResPath(rt.Scope, rt.Name, false); p != "" {
				env = append(env, key+"="+p)
			}
		case res.Type == "sqlite" && rt.Scope == c.Scope:
			// Convenience over `filesystem`: XBIN_RES_<N> points at a .sqlite
			// FILE in that dir (the dir is still what's bound rw).
			if p := b.fsResPath(rt.Scope, rt.Name, true); p != "" {
				env = append(env, key+"="+p)
			}
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
	// Ingress wiring (plans/ingress.md). A terminator tile gets its forward
	// door; a bound stream interface gets the gateway address its provider's
	// port answers on; a lan-ingress leg gets the address it owns on the
	// provider's subnet; a provider gets its client map.
	if providesIngress(c) && b.IngressSocket != nil {
		env = append(env, fmt.Sprintf("XBIN_INGRESS_FORWARD_URL=http://%s:%d", sandbox.GatewayIP, ingressFwdPort))
	}
	for i, slot := range streamIfaceSlots(c) {
		if _, _, ok := b.streamIfaceTarget(c, slot); ok {
			env = append(env, fmt.Sprintf("XBIN_IFACE_%s_ADDR=%s:%d", envName(slot), sandbox.GatewayIP, streamIfacePortBase+i))
		}
	}
	for _, l := range b.NetLinksFor(c) {
		ip, _, _ := strings.Cut(l.Addr, "/")
		env = append(env, "XBIN_IFACE_"+envName(l.Slot)+"_IP="+ip)
	}
	if j := b.lanIngressEnvJSON(c); j != "" {
		env = append(env, "XBIN_LAN_INGRESS="+j)
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

// --- kv (its store: kvStore, deploydata.go) --------------------------------

// kvAccess parses /kv/{res-target}/{key...} and authorizes, answering the
// resource's keys (its bucket, file and label) in the data namespace the
// caller reaches (reachRes), past that namespace's hold and, for a writer,
// its write gate until release (nsEnter).
// URL form: /api/xbin/kv/res:<scope>/<name>/<key…>
func (b *Broker) kvAccess(w http.ResponseWriter, r *http.Request, want string) (k resKeys, key string, release func(), ok bool) {
	rest := strings.Trim(r.PathValue("rest"), "/")
	if !strings.HasPrefix(rest, "res:") {
		server.WriteError(w, http.StatusBadRequest, "kv paths are /api/xbin/kv/res:<scope>/<name>/<key>", "/docs/resources.md")
		return resKeys{}, "", nil, false
	}
	p := auth.PrincipalOf(r)
	// Find the declared resource by longest prefix.
	probe := rest
	for probe != "res:" {
		ra, found, err := b.reachRes(p, probe)
		if err != nil {
			writeNamespaceRefusal(w, err)
			return resKeys{}, "", nil, false
		}
		if found && ra.res.Type == "kv" {
			key = strings.TrimPrefix(rest, probe)
			key = strings.TrimPrefix(key, "/")
			if err := b.allowAt(p, ra, want); err != nil {
				server.WriteError(w, http.StatusForbidden, err.Error(), "/docs/auth.md")
				return resKeys{}, "", nil, false
			}
			k, err := b.reachKeys(ra)
			if err != nil {
				server.WriteError(w, http.StatusNotFound, err.Error(), "/docs/resources.md")
				return resKeys{}, "", nil, false
			}
			if !b.quotaOK(w, k.quotaKey(), want) {
				return resKeys{}, "", nil, false
			}
			release, ok := b.nsEnterReach(w, ra, want)
			return k, key, release, ok
		}
		i := strings.LastIndex(probe, "/")
		if i < 0 {
			break
		}
		probe = probe[:i]
	}
	server.WriteError(w, http.StatusNotFound, "no such kv resource", "/docs/resources.md")
	return resKeys{}, "", nil, false
}

func (b *Broker) apiKVGet(w http.ResponseWriter, r *http.Request) {
	k, key, release, ok := b.kvAccess(w, r, "reader")
	if !ok {
		return
	}
	defer release()
	db, err := b.kvDB(k, false) // nil: a namespace nothing wrote to reads as empty
	if err != nil {
		server.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if key == "" { // list keys, optional ?prefix=
		prefix := []byte(r.URL.Query().Get("prefix"))
		var keys []string
		_ = kvView(db, func(tx *bolt.Tx) error {
			bk := tx.Bucket([]byte(k.Bucket))
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
	_ = kvView(db, func(tx *bolt.Tx) error {
		if bk := tx.Bucket([]byte(k.Bucket)); bk != nil {
			if v := bk.Get([]byte(key)); v != nil {
				val = append([]byte{}, v...)
			}
		}
		return nil
	})
	if val == nil {
		server.WriteError(w, http.StatusNotFound, "no such key")
		return
	}
	plain, err := b.decodeKV(k.KVLabel, val)
	if err != nil {
		server.WriteError(w, http.StatusServiceUnavailable, "vault sealed — unseal to read encrypted resource data", "/docs/auth.md")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	_, _ = w.Write(plain)
}

func (b *Broker) apiKVPut(w http.ResponseWriter, r *http.Request) {
	k, key, release, ok := b.kvAccess(w, r, "writer")
	if !ok {
		return
	}
	defer release()
	if key == "" {
		server.WriteError(w, http.StatusBadRequest, "missing key")
		return
	}
	val, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		server.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	stored, err := b.encodeKV(k.KVLabel, val)
	if err != nil {
		server.WriteError(w, http.StatusServiceUnavailable, "vault sealed — unseal to write encrypted resource data", "/docs/auth.md")
		return
	}
	db, err := b.kvDB(k, true)
	if err != nil {
		server.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	err = db.Update(func(tx *bolt.Tx) error {
		bk, err := tx.CreateBucketIfNotExists([]byte(k.Bucket))
		if err != nil {
			return err
		}
		return bk.Put([]byte(key), stored)
	})
	if err != nil {
		server.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	server.WriteOK(w)
}

func (b *Broker) apiKVDelete(w http.ResponseWriter, r *http.Request) {
	k, key, release, ok := b.kvAccess(w, r, "writer")
	if !ok {
		return
	}
	defer release()
	db, err := b.kvDB(k, false)
	if err == nil && db != nil { // a namespace nothing wrote to has nothing to delete
		err = db.Update(func(tx *bolt.Tx) error {
			if bk := tx.Bucket([]byte(k.Bucket)); bk != nil {
				return bk.Delete([]byte(key))
			}
			return nil
		})
	}
	if err != nil {
		server.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	server.WriteOK(w)
}

// --- blob ------------------------------------------------------------------

// blobAccess parses /blob/{res-target}/{path...} and authorizes, answering
// the file's path in the decrypted volume of the data namespace the caller
// reaches (reachRes). A volume beyond main mounts on its first use; one
// nothing ever wrote answers "" for every request but a PUT, which reads as
// empty and creates nothing (08-data §3.6, §3.7). A held namespace answers
// 503 before anything mounts; a writer holds its write gate until release
// (nsEnter).
func (b *Broker) blobAccess(w http.ResponseWriter, r *http.Request, want string) (dir, rel string, release func(), ok bool) {
	rest := strings.Trim(r.PathValue("rest"), "/")
	p := auth.PrincipalOf(r)
	probe := rest
	for strings.HasPrefix(probe, "res:") {
		ra, found, err := b.reachRes(p, probe)
		if err != nil {
			writeNamespaceRefusal(w, err)
			return "", "", nil, false
		}
		if found && ra.res.Type == "blob" {
			rel = strings.TrimPrefix(strings.TrimPrefix(rest, probe), "/")
			if err := b.allowAt(p, ra, want); err != nil {
				server.WriteError(w, http.StatusForbidden, err.Error(), "/docs/auth.md")
				return "", "", nil, false
			}
			k, err := b.reachKeys(ra)
			if err != nil {
				server.WriteError(w, http.StatusNotFound, err.Error(), "/docs/resources.md")
				return "", "", nil, false
			}
			if !b.quotaOK(w, k.quotaKey(), want) || !b.nsAvailableID(w, ra.nsID()) {
				return "", "", nil, false
			}
			held := b.holdPartitionVolume(k) // before it mounts: never unmounted as idle under the request
			if k.NS != "" && b.encryptionReady() {
				if !b.resenc.Encrypted(k.DirKey, k.Name) && r.Method != http.MethodPut {
					held()
					return "", rel, func() {}, true // never written: reads as empty, creates nothing
				}
				if ra.who != nil && b.notePartitionNS(ra.nsID(), *ra.who) != nil {
					held()
					server.WriteError(w, http.StatusInternalServerError, "the partition's data namespace can't be recorded", "/docs/partitions.md")
					return "", "", nil, false
				}
				b.ensureVolume(k, ra.rt.Scope, ra.res.Type)
			}
			// blob is always an encrypted gocryptfs mount; refuse until it's up
			// (vault sealed / gocryptfs missing) so we never read or write plaintext
			// into the bare mountpoint.
			if !b.fsReady(k) {
				held()
				server.WriteError(w, http.StatusServiceUnavailable, "resource unavailable — vault sealed or encryption not ready", "/docs/auth.md")
				return "", "", nil, false
			}
			base := b.resMount(k, false) // decrypted gocryptfs mount
			full, _, err := util.SafeJoin(base, rel)
			if err != nil {
				held()
				server.WriteError(w, http.StatusBadRequest, "bad path")
				return "", "", nil, false
			}
			release, ok := b.nsEnterReach(w, ra, want)
			if !ok {
				held()
				return "", "", nil, false
			}
			return full, rel, func() { release(); held() }, true
		}
		i := strings.LastIndex(probe, "/")
		if i < 0 {
			break
		}
		probe = probe[:i]
	}
	server.WriteError(w, http.StatusNotFound, "no such blob resource", "/docs/resources.md")
	return "", "", nil, false
}

func (b *Broker) apiBlobGet(w http.ResponseWriter, r *http.Request) {
	full, rel, release, ok := b.blobAccess(w, r, "reader")
	if ok {
		defer release()
	}
	switch {
	case !ok:
		return
	case full == "" && rel == "": // a volume nothing wrote to: an empty listing
		server.WriteJSON(w, http.StatusOK, map[string]any{"entries": []string(nil)})
		return
	case full == "":
		server.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	fi, err := os.Stat(full)
	if err != nil {
		server.WriteError(w, http.StatusNotFound, "not found")
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
	full, rel, release, ok := b.blobAccess(w, r, "writer")
	if !ok {
		return
	}
	defer release()
	if rel == "" {
		server.WriteError(w, http.StatusBadRequest, "missing blob path")
		return
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		server.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	f, err := os.Create(full)
	if err != nil {
		server.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer f.Close()
	if _, err := io.Copy(f, io.LimitReader(r.Body, 256<<20)); err != nil {
		server.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	server.WriteOK(w)
}

func (b *Broker) apiBlobDelete(w http.ResponseWriter, r *http.Request) {
	full, rel, release, ok := b.blobAccess(w, r, "writer")
	if !ok {
		return
	}
	defer release()
	if rel == "" {
		server.WriteError(w, http.StatusBadRequest, "missing blob path")
		return
	}
	if full == "" { // a volume nothing wrote to has nothing to delete
		server.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	if err := os.Remove(full); err != nil {
		server.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	server.WriteOK(w)
}

// --- bus ---------------------------------------------------------------

func (b *Broker) apiBusPublish(w http.ResponseWriter, r *http.Request) {
	var msg struct {
		Resource string `json:"resource"`
		Topic    string `json:"topic"`
		Data     any    `json:"data"`
	}
	if err := server.DecodeJSON(r, &msg); err != nil || msg.Resource == "" || msg.Topic == "" {
		server.WriteError(w, http.StatusBadRequest, "need {resource, topic, data?}", "/docs/resources.md")
		return
	}
	p := auth.PrincipalOf(r)
	ra, found, err := b.reachRes(p, msg.Resource)
	switch {
	case err != nil:
		writeNamespaceRefusal(w, err)
		return
	case !found || ra.res.Type != "bus":
		server.WriteError(w, http.StatusNotFound, "no such bus resource", "/docs/resources.md")
		return
	}
	if err := b.allowAt(p, ra, "writer"); err != nil {
		server.WriteError(w, http.StatusForbidden, err.Error(), "/docs/auth.md")
		return
	}
	if !b.nsAvailableID(w, ra.nsID()) || b.publishPartitioned(w, ra, msg.Topic, msg.Data) { // a partitioned scope's own bus: stamped (partitionbus.go)
		return
	}
	// The event is in the namespace the publisher reaches: its own scope's
	// in its deployment's (08-data §4.3). One beyond main names it, so
	// delivery can match it; main's carries no field, as today.
	id, ev := ra.rt.String(), events.Event{Type: "bus", Topic: ra.rt.String() + "/" + msg.Topic, Data: msg.Data}
	counter := id
	if ra.dep != util.MainDeployment {
		ev.Deployment, counter = ra.dep, id+"\x00"+ra.dep
	}
	b.Hub.Publish(ev)
	b.countBusEvent(counter)
	b.bus.publishIn(id, ra.dep, msg.Topic, msg.Data)
	server.WriteOK(w)
}

// busFilter authorizes bus event delivery to a WS subscriber (installed as
// server.BusFilter; owner passes upstream of this). The event is in the
// data namespace e.Deployment names (main's without one), and reaches a
// tile principal only when that is the namespace it reaches for the
// resource (resNamespace) and it can read the bus there (08-data §4.3;
// 09-fabric §5.10). A frame is never an admin, so a primary's frontend
// never sees a publish of another namespace, even in an admin's browser.
// On a partitioned scope's own bus it reaches only a subscriber acting in
// the partition the event is stamped with (busPartitionReaches, 02 §9).
func (b *Broker) busFilter(p auth.Principal, e events.Event) bool {
	if p.Component == "" {
		return false
	}
	ns := cmp.Or(e.Deployment, util.MainDeployment)
	// e.Topic = "res:<scope-or-workspace>/<name>/<topic…>"
	probe := e.Topic
	for strings.HasPrefix(probe, "res:") {
		if rt, ok := b.resScope(probe); ok {
			set, _ := b.declaredIn(rt.Scope, ns)
			if res, ok := set[rt.Name]; ok && res.Type == "bus" {
				dep, own, err := b.resNamespace(p, rt.Scope)
				return err == nil && dep == ns &&
					b.allowAt(p, reach{rt: rt, res: res, dep: dep, own: own}, "reader") == nil &&
					b.busPartitionReaches(p, rt, res, own, e) // a partitioned scope's own bus (partitionbus.go)
			}
		}
		i := strings.LastIndex(probe, "/")
		if i < 0 {
			break
		}
		probe = probe[:i]
	}
	return false
}

// --- tile deployments: which namespace, which declarations ---------------
//
// A deployment beyond main has a data namespace of its own in its tile's
// scope (D127c), holding what its own code declares (D127n). A request by a
// tile's principal reaches its own scope's resources in the namespace of the
// deployment its credential addresses, and every other scope's, the
// workspace's included, in the scope primary's namespace, as an edge
// (08-data §4.1, §4.2). A tile without a record has only main: every answer
// here is then today's.

// maxNSResources caps the resources a data namespace beyond main declares:
// each file-backed one can mean a gocryptfs process (08-data §6.7). main
// keeps no cap (D119c).
const maxNSResources = 64

// reach is one resource as a request reaches it.
type reach struct {
	rt  resTarget
	res registry.Resource
	dep string // the deployment whose data namespace holds it ("main": today's keys)
	own bool   // the caller's own scope: its deployment's data, not an edge
	partReach
}

// reachRes resolves target for a request by p: the resource, the namespace
// holding it (resNamespace) and that namespace's declared set. found is false
// when the namespace declares no such name; the error is resNamespace's.
func (b *Broker) reachRes(p auth.Principal, target string) (ra reach, found bool, err error) {
	rt, ok := b.resScope(target)
	if !ok {
		return reach{}, false, nil
	}
	dep, own, err := b.resNamespace(p, rt.Scope)
	if err != nil {
		return reach{}, false, err
	}
	set, _ := b.declaredIn(rt.Scope, dep)
	res, ok := set[rt.Name]
	if !ok {
		return reach{}, false, nil
	}
	ra = reach{rt: rt, res: res, dep: dep, own: own}
	return ra, true, b.partitionReach(p, &ra) // a partitioned scope's (partitionreach.go)
}

// resNamespace is the deployment whose data namespace of scope a request by
// p reaches: for one of a tile's own principals, in its own scope, the
// deployment its credential addresses; otherwise the scope primary's,
// scopePrimary (08-data §4.1, §4.2). own reports the first case. The error
// is the addressed deployment's: gone (util.ErrNoDeployment) or refused.
func (b *Broker) resNamespace(p auth.Principal, scope string) (dep string, own bool, err error) {
	if scope != "" && p.Component != "" {
		if c, ok := b.Reg.Component(p.Component); ok && c.Scope == scope {
			dep, err := b.addressed(p, p.Component)
			return dep, true, err
		}
	}
	return b.scopePrimary(scope), false, nil
}

// scopePrimary is P(scope), the name of the scope primary's namespace: the
// scope root tile's primary; main for the workspace scope, which is never
// split, and for a scope no tile roots.
func (b *Broker) scopePrimary(scope string) string {
	if scope == "" {
		return util.MainDeployment
	}
	return b.primaryOf(scope)
}

// resScope splits a res: target as parseRes does, at its deepest declared
// scope, without asking whether the name is declared there: which
// namespace's declarations answer that is the caller's question.
func (b *Broker) resScope(target string) (resTarget, bool) {
	rest, ok := strings.CutPrefix(target, "res:")
	if !ok {
		return resTarget{}, false
	}
	if name, ok := strings.CutPrefix(rest, "workspace/"); ok {
		return resTarget{Name: name}, true
	}
	scopes := b.Reg.Scopes()
	for p := rest; p != "." && p != ""; {
		dir := path.Dir(p)
		if _, ok := scopes[dir]; ok && dir != "." {
			return resTarget{Scope: dir, Name: strings.TrimPrefix(rest, dir+"/")}, true
		}
		p = dir
	}
	return resTarget{}, false
}

// allowAt authorizes p on a resource it reaches at want. A resource the
// primary's code declares is authorized by allowRes, the tile's authority,
// exactly as today; one only the reached namespace's own code declares (its
// own scope, D127n) by the tile's same-scope use declaration (ownUse). Then
// the read clamp: a deployment beyond its tile's primary never writes any
// data but its own (08-data §7) (D127a).
func (b *Broker) allowAt(p auth.Principal, ra reach, want string) error {
	id := ra.rt.String()
	switch _, res, ok := b.parseRes(id); {
	case ok && res != nil:
		if err := b.allowRes(p, id, want); err != nil {
			return err
		}
	case p.IsAdmin():
	case p.Component == "":
		return fmt.Errorf("unauthenticated")
	default:
		if role, ok := b.ownUse(p.Component, ra.rt); !ok || !roleSatisfies(role, want, nil) {
			return fmt.Errorf("%s needs role %q on %s — declare it in \"uses\" and approve with bx grant", p.Component, want, ra.rt)
		}
	}
	return b.readClamp(p, ra, want)
}

// readClamp refuses a write by a non-primary deployment's principal to data
// that isn't its own: another scope's, or the workspace's, reached as an
// edge, which never gives it more than reader, whatever role the tile holds
// (08-data §7). Which edges it may read is the edge policy's, at
// resolveTarget; this refuses only what no policy value allows.
func (b *Broker) readClamp(p auth.Principal, ra reach, want string) error {
	if ra.readOnly && !roleSatisfies("reader", want, nil) {
		return errReadOnly(ra.rt) // a user partition on a "shared": "read" resource (04 §1)
	}
	if ra.own || p.Component == "" || roleSatisfies("reader", want, nil) {
		return nil
	}
	if _, ok := b.Reg.Component(p.Component); !ok {
		return nil // xbind's own cron and bus principals
	}
	dep, err := b.addressed(p, p.Component)
	switch {
	case err != nil:
		return err
	case b.isPrimary(p.Component, dep):
		return nil
	}
	return fmt.Errorf("%s+%s may not write %s: non-primary deployments reach other scopes read-only (edge policy \"read\")",
		p.Component, dep, ra.rt)
}

// ownUse is the role tile's own use declaration grants on rt when only the
// reached namespace's own code declares rt, so grantedRole, which resolves
// against the primary's code, can't see it: the same-scope auto-grant (ND5)
// under the policy ceiling. Nothing for a resource the primary's code
// declares, whose authority stays grantedRole's.
func (b *Broker) ownUse(tile string, rt resTarget) (string, bool) {
	c, ok := b.Reg.Component(tile)
	if !ok || rt.Scope == "" || rt.Scope != c.Scope || !b.ceilingAllows(tile, rt.String()) {
		return "", false
	}
	if _, res, ok := b.parseRes(rt.String()); ok && res != nil {
		return "", false
	}
	for _, u := range c.Manifest.Uses {
		if u.Target == rt.String() {
			return u.Role, true
		}
	}
	return "", false
}

// viewDeployment is the deployment a view describes: its Deployment, or the
// tile's primary for the registry's own component.
func (b *Broker) viewDeployment(c *registry.Component) string {
	return cmp.Or(c.Deployment, b.primaryOf(c.Path))
}

// envTarget resolves a uses target of c for deployment dep: its own scope in
// dep's declared set, any other scope and the workspace level in the scope
// primary's, which is the registry's (08-data §4.2).
func (b *Broker) envTarget(c *registry.Component, dep, target string) (resTarget, *registry.Resource, bool) {
	if rt, ok := b.resScope(target); ok && rt.Scope != "" && rt.Scope == c.Scope {
		set, _ := b.declaredIn(rt.Scope, dep)
		r, ok := set[rt.Name]
		return rt, &r, ok
	}
	rt, res, ok := b.parseRes(target)
	return rt, res, ok && res != nil
}
