// Package broker implements the runtime-plane authorization and shared
// resources of plans/auth.md: the grant table and policy (RBAC §3), private
// vaults (§4), and brokered resources — kv, blob, bus, cron, sqlite
// provisioning (§5). It installs itself into the server (API endpoints),
// proxy (policy), runner (per-component env, tier-2 spawn users), and events
// hub (bus filter).
package broker

import (
	"fmt"
	"net/http"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"github.com/magik6k/xbin/internal/auth"
	"github.com/magik6k/xbin/internal/builtins"
	"github.com/magik6k/xbin/internal/events"
	"github.com/magik6k/xbin/internal/registry"
	"github.com/magik6k/xbin/internal/resenc"
	"github.com/magik6k/xbin/internal/server"
	"github.com/magik6k/xbin/internal/users"
	"github.com/magik6k/xbin/internal/vault"
)

// CronPrincipal is the From identity of scheduler-invoked calls.
const CronPrincipal = "xbin/cron"

type Broker struct {
	Reg *registry.Registry
	Hub *events.Hub

	mu        sync.Mutex
	kv        *kvStore
	cron      *cronRunner
	uids      *uidAllocator         // nil = tier 1
	barrier   *vault.Barrier        // vault encryption-at-rest barrier
	resenc    *resenc.Manager       // per-resource gocryptfs mounts (filesystem/sqlite)
	tiles     *builtins.Set         // embedded optional tile catalog (nil = none)
	templates *builtins.TemplateSet // embedded builtin template catalog (nil = none)
	updater   *builtins.Updater     // builtin update tracking (nil = none)
	Users     *users.Store          // human users (nil = single-user/root-only)

	// OnStructureChange, if set, is called after the broker changes the
	// component tree (e.g. a tile import) so the host can reconcile deps/
	// symlinks and regenerate go.work without waiting for the watcher.
	OnStructureChange func()

	// OnGrantChange, if set, is called with a component whose spawn-materialized
	// access changed (a res:* resource / gpu:* grant, or a `net` interface
	// binding added/revoked) so the host can restart its backend to pick up the
	// new policy/env — those are captured at spawn, not per request.
	OnGrantChange func(component string)

	// StopBackend, if set, terminates a component's running backend now (used
	// when the owner disables/offloads it; plans/lifecycle.md). Wired to
	// runner.Stop by main.
	StopBackend func(component string)

	// ProxyHandler is the element proxy, used to call an archiver tile's API
	// internally (as the owner) for backup/restore (plans/lifecycle.md). Set by
	// main to avoid a broker→proxy import cycle.
	ProxyHandler http.Handler

	// Version is the xbind version, stamped into backup manifests.
	Version string

	// AllowInsecureVault permits storing secrets as plaintext at rest when no
	// encryption barrier is configured (dev / --insecure-vault only). When
	// false, vault writes without a barrier are refused rather than written
	// in the clear.
	AllowInsecureVault bool
}

// SetBuiltins installs the embedded builtin tile catalog (from main, which
// owns the embedded FS). Call before Register.
func (b *Broker) SetBuiltins(s *builtins.Set) { b.tiles = s }

// SetBuiltinTemplates installs the embedded builtin template catalog. Call
// before Register.
func (b *Broker) SetBuiltinTemplates(s *builtins.TemplateSet) { b.templates = s }

// SetUpdater installs the builtin update tracker (plans/builtin-updates.md).
// Call before Register.
func (b *Broker) SetUpdater(u *builtins.Updater) { b.updater = u }

func New(reg *registry.Registry, hub *events.Hub, scopeUIDs bool) (*Broker, error) {
	b := &Broker{Reg: reg, Hub: hub}
	var err error
	if b.kv, err = openKV(reg.Root); err != nil {
		return nil, err
	}
	if b.barrier, err = vault.Open(filepath.Join(reg.Root, "data", "vault")); err != nil {
		return nil, err
	}
	b.initResEnc()
	if scopeUIDs {
		if b.uids, err = newUIDAllocator(reg.Root); err != nil {
			return nil, err
		}
	}
	b.cron = newCronRunner(b)
	b.Provision()
	return b, nil
}

// UnsealOrInit brings the vault barrier online with a passphrase: initializes
// it on first use (migrating any legacy plaintext), or unseals an existing
// one. Called at boot from XBIN_VAULT_PASSPHRASE, and by the unseal API.
func (b *Broker) UnsealOrInit(passphrase string) error {
	if b.barrier.Initialized() {
		if err := b.barrier.Unseal(passphrase); err != nil {
			return err
		}
		b.MountEncrypted()
		return nil
	}
	if err := b.barrier.Init(passphrase); err != nil {
		return err
	}
	b.migrateVaults()
	b.MountEncrypted() // default-on: encrypt file-backed resources from now on
	return nil
}

// Barrier exposes the vault barrier (status/seal for the API layer).
func (b *Broker) Barrier() *vault.Barrier { return b.barrier }

// VaultInsecure reports whether secrets are being stored as plaintext at rest
// (no barrier configured) — used to warn operators.
func (b *Broker) VaultInsecure() bool { return !b.barrier.Initialized() }

// Register mounts the broker's /api/xbin/* endpoints.
func (b *Broker) Register(srv *server.Server) {
	srv.RegisterAPI("POST /create", b.apiCreate)
	srv.RegisterAPI("GET /grants", b.apiGrantsList)
	srv.RegisterAPI("POST /grants", b.apiGrantsAdd)
	srv.RegisterAPI("DELETE /grants", b.apiGrantsRevoke)
	srv.RegisterAPI("GET /bindings", b.apiBindingsList)
	srv.RegisterAPI("POST /bindings", b.apiBindingSet)
	srv.RegisterAPI("DELETE /bindings", b.apiBindingSet)
	srv.RegisterAPI("PUT /iface-instances", b.apiIfaceInstancesSet)
	srv.RegisterAPI("POST /lifecycle", b.apiLifecycleSet)
	srv.RegisterAPI("POST /backup", b.apiBackupNow)
	srv.RegisterAPI("GET /backups", b.apiBackupList)
	srv.RegisterAPI("POST /restore", b.apiRestore)
	srv.RegisterAPI("GET /backup-schedule", b.apiBackupScheduleList)
	srv.RegisterAPI("POST /backup-schedule", b.apiBackupScheduleSet)
	srv.RegisterAPI("DELETE /backup-schedule", b.apiBackupScheduleDelete)
	srv.RegisterAPI("GET /vault-status", b.apiVaultStatus)
	srv.RegisterAPI("POST /vault-unseal", b.apiVaultUnseal)
	srv.RegisterAPI("POST /vault-seal", b.apiVaultSeal)
	srv.RegisterAPI("POST /vault-rekey", b.apiVaultRekey)
	srv.RegisterAPI("GET /vault/{rest...}", b.apiVaultGet)
	srv.RegisterAPI("PUT /vault/{rest...}", b.apiVaultPut)
	srv.RegisterAPI("DELETE /vault/{rest...}", b.apiVaultDelete)
	srv.RegisterAPI("POST /bus/publish", b.apiBusPublish)
	srv.RegisterAPI("POST /clone", b.apiClone)
	srv.RegisterAPI("GET /kv/{rest...}", b.apiKVGet)
	srv.RegisterAPI("PUT /kv/{rest...}", b.apiKVPut)
	srv.RegisterAPI("DELETE /kv/{rest...}", b.apiKVDelete)
	srv.RegisterAPI("GET /blob/{rest...}", b.apiBlobGet)
	srv.RegisterAPI("PUT /blob/{rest...}", b.apiBlobPut)
	srv.RegisterAPI("DELETE /blob/{rest...}", b.apiBlobDelete)
	srv.RegisterAPI("GET /cron/jobs", b.apiCronList)
	srv.RegisterAPI("PUT /cron/jobs", b.apiCronPut)
	srv.RegisterAPI("DELETE /cron/jobs/{name}", b.apiCronDelete)
	b.registerAdmin(srv)
	b.registerTiles(srv)
	b.registerTemplates(srv)
	b.registerUsers(srv)
	b.registerPrefs(srv)
	srv.BusFilter = b.busFilter
	srv.IsAdmin = b.IsAdmin
	srv.Interfaces = b.HTTPInterfaces
}

// --- resource identity -------------------------------------------------

// resTarget is a parsed "res:<scope>/<name>" grant target.
type resTarget struct {
	Scope string // scope path; "" = workspace
	Name  string
}

func (rt resTarget) String() string {
	s := rt.Scope
	if s == "" {
		s = "workspace"
	}
	return "res:" + s + "/" + rt.Name
}

// parseRes resolves "res:apps/calendar/db" against declared scopes: the
// longest declared scope prefix wins, the remainder is the resource name.
// "res:workspace/<name>" addresses workspace-level resources.
func (b *Broker) parseRes(target string) (resTarget, *registry.Resource, bool) {
	rest, ok := strings.CutPrefix(target, "res:")
	if !ok {
		return resTarget{}, nil, false
	}
	if name, ok := strings.CutPrefix(rest, "workspace/"); ok {
		// Workspace-level resources are declared in the workspace xbin.json.
		if r, ok := b.Reg.Workspace().Resources[name]; ok {
			return resTarget{Scope: "", Name: name}, &r, true
		}
		return resTarget{Scope: "", Name: name}, nil, false
	}
	scopes := b.Reg.Scopes()
	p := rest
	for p != "." && p != "" {
		dir := path.Dir(p)
		if sm, ok := scopes[dir]; ok && dir != "." {
			name := strings.TrimPrefix(rest, dir+"/")
			if r, ok := sm.Resources[name]; ok {
				return resTarget{Scope: dir, Name: name}, &r, true
			}
			return resTarget{Scope: dir, Name: name}, nil, false
		}
		p = dir
	}
	return resTarget{}, nil, false
}

// --- policy (plans/auth.md §3) ------------------------------------------

// roleSatisfies: conventional ordering plus manifest-declared implications.
func roleSatisfies(have, want string, exp *registry.Expose) bool {
	norm := func(r string) string {
		switch r { // bus aliases
		case "subscriber":
			return "reader"
		case "publisher":
			return "writer"
		}
		return r
	}
	have, want = norm(have), norm(want)
	if have == want {
		return true
	}
	rank := map[string]int{"reader": 1, "writer": 2, "admin": 3}
	if rank[have] > 0 && rank[want] > 0 {
		return rank[have] >= rank[want]
	}
	if exp != nil {
		seen := map[string]bool{}
		var walk func(r string) bool
		walk = func(r string) bool {
			if r == want {
				return true
			}
			if seen[r] {
				return false
			}
			seen[r] = true
			for _, imp := range exp.Implies[r] {
				if walk(norm(imp)) {
					return true
				}
			}
			return false
		}
		return walk(have)
	}
	return false
}

// grantedRole returns the role `from` holds on `target` (component path or
// res:…), combining the explicit grant table with same-scope auto-grants
// from `uses` declarations (decision ND5).
func (b *Broker) grantedRole(from, target string) (string, bool) {
	ws := b.Reg.Workspace()
	best := ""
	for _, g := range ws.Grants {
		if g.From == from && g.Target == target {
			if best == "" || roleSatisfies(g.Role, best, nil) {
				best = g.Role
			}
		}
	}
	if best != "" {
		return best, true
	}
	// An http-interface binding to target is also the call grant (plans/interfaces.md).
	if role, ok := b.httpBindingRole(from, target); ok {
		return role, true
	}
	// Same-scope auto-grant: the use declaration itself is the grant.
	caller, ok := b.Reg.Component(from)
	if !ok {
		return "", false
	}
	for _, u := range caller.Manifest.Uses {
		if u.Target != target {
			continue
		}
		if b.sameScope(caller, target) {
			return u.Role, true
		}
	}
	return "", false
}

func (b *Broker) sameScope(caller *registry.Component, target string) bool {
	if rt, _, ok := b.parseRes(target); ok || strings.HasPrefix(target, "res:") {
		return ok && rt.Scope != "" && rt.Scope == caller.Scope
	}
	tc, ok := b.Reg.Component(target)
	return ok && tc.Scope != "" && tc.Scope == caller.Scope
}

// IsAdmin reports whether a principal may use workspace-administration
// endpoints: the owner, or an element granted the reserved target "xbin"
// at role admin (plans/admin-tile.md). Installed into the server as its
// IsAdmin hook so owner-only management endpoints become admin-capable.
func (b *Broker) IsAdmin(p auth.Principal) bool {
	if p.IsAdmin() { // root token or an admin-role user (plans/multi-user.md)
		return true
	}
	if p.Component == "" {
		return false
	}
	role, ok := b.grantedRole(p.Component, "xbin")
	return ok && roleSatisfies(role, "admin", nil)
}

// Policy is installed into the proxy: decides element→element API calls.
func (b *Broker) Policy(p auth.Principal, target *registry.Component) (string, bool) {
	if p.IsAdmin() {
		return "admin", true
	}
	if p.Component == target.Path {
		return "admin", true // element is admin of itself
	}
	if p.Component == CronPrincipal {
		return p.Role, true // role bound at job registration (cron.go)
	}
	role, ok := b.grantedRole(p.Component, target.Path)
	return role, ok
}

// allowRes authorizes principal p on a resource target at want role.
func (b *Broker) allowRes(p auth.Principal, target string, want string) error {
	rt, res, ok := b.parseRes(target)
	if !ok || res == nil {
		return fmt.Errorf("unknown resource %s", target)
	}
	if p.IsAdmin() {
		return nil
	}
	if p.Component == "" {
		return fmt.Errorf("unauthenticated")
	}
	role, ok := b.grantedRole(p.Component, rt.String())
	if !ok || !roleSatisfies(role, want, nil) {
		return fmt.Errorf("%s needs role %q on %s — declare it in \"uses\" and approve with bx grant", p.Component, want, rt)
	}
	return nil
}

// Pending computes unsatisfied cross-scope `uses` declarations.
func (b *Broker) Pending() []registry.Grant {
	out := []registry.Grant{} // non-nil: JSON-encodes as [] not null (frontends do .length)
	for _, c := range b.Reg.Components() {
		// An offloaded component isn't running — its `uses` aren't live requests,
		// so don't surface them as pending (plans/lifecycle.md).
		if registry.IsOffloaded(b.Reg.LifecycleState(c.Path)) {
			continue
		}
		for _, u := range c.Manifest.Uses {
			if u.Target == "" || u.Role == "" {
				continue
			}
			if _, ok := b.grantedRole(c.Path, u.Target); ok {
				continue
			}
			out = append(out, registry.Grant{From: c.Path, Target: u.Target, Role: u.Role})
		}
	}
	return out
}

// --- grants API ---------------------------------------------------------

func (b *Broker) apiGrantsList(w http.ResponseWriter, r *http.Request) {
	if !b.IsAdmin(auth.PrincipalOf(r)) {
		server.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "admin only"})
		return
	}
	server.WriteJSON(w, http.StatusOK, map[string]any{
		"grants":  b.Reg.Workspace().Grants,
		"pending": b.Pending(),
	})
}

func (b *Broker) apiGrantsAdd(w http.ResponseWriter, r *http.Request) {
	if g, ok := b.grantMutation(w, r, func(ws *registry.WorkspaceManifest, g registry.Grant) {
		for _, e := range ws.Grants {
			if e == g {
				return
			}
		}
		ws.Grants = append(ws.Grants, g)
	}); ok {
		// Carry the affected caller so its already-loaded frame reloads and
		// retries against the new grant — no manual page refresh needed.
		b.Hub.Publish(events.Event{Type: "grants", Component: g.From})
		b.grantRestart(g)
	}
}

func (b *Broker) apiGrantsRevoke(w http.ResponseWriter, r *http.Request) {
	if g, ok := b.grantMutation(w, r, func(ws *registry.WorkspaceManifest, g registry.Grant) {
		out := ws.Grants[:0]
		for _, e := range ws.Grants {
			if e != g {
				out = append(out, e)
			}
		}
		ws.Grants = out
	}); ok {
		b.Hub.Publish(events.Event{Type: "grants", Component: g.From})
		b.grantRestart(g)
	}
}

// grantRestart restarts the caller's backend when the changed grant is one whose
// effect is materialized at spawn (a resource env var or GPU devices), so
// approving e.g. res:… or gpu:0 takes effect without a manual restart. (Egress
// is no longer a grant — it's a `net` interface binding, restarted via the
// bindings API; see apiBindingSet.)
func (b *Broker) grantRestart(g registry.Grant) {
	if b.OnGrantChange == nil {
		return
	}
	if strings.HasPrefix(g.Target, "res:") || strings.HasPrefix(g.Target, "gpu:") {
		b.OnGrantChange(g.From)
	}
}

func (b *Broker) grantMutation(w http.ResponseWriter, r *http.Request, apply func(*registry.WorkspaceManifest, registry.Grant)) (registry.Grant, bool) {
	if !b.IsAdmin(auth.PrincipalOf(r)) {
		server.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "admin only — grants are approved by the owner or an admin-capable tile"})
		return registry.Grant{}, false
	}
	var g registry.Grant
	if err := decodeJSON(r, &g); err != nil || g.From == "" || g.Target == "" || g.Role == "" {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "need {from, target, role}"})
		return registry.Grant{}, false
	}
	// Egress is no longer a grant — it's a `net` interface binding. Reject new
	// net:* grants loudly (rather than storing a silent no-op); DELETE still
	// works so a stale net:* grant from an older workspace can be cleaned up.
	if r.Method == http.MethodPost && strings.HasPrefix(g.Target, "net:") {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "network egress is not a grant — bind a `net` interface instead: `bx bind " + g.From + " net=internet` or POST /api/xbin/bindings (see plans/interfaces.md)"})
		return registry.Grant{}, false
	}
	if err := b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) { apply(ws, g) }); err != nil {
		server.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return registry.Grant{}, false
	}
	server.WriteJSON(w, http.StatusOK, map[string]string{"ok": "true"})
	return g, true
}
