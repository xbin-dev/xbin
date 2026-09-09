package boot

import (
	"net/http"
	"os"
	goruntime "runtime"
	"strings"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/runner"
	"github.com/xbin-dev/xbin/internal/sandbox"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/term"
)

// registerRuntimeAPI mounts the endpoints that read across the runtime —
// runner, broker, ingress managers and the process itself — which no single
// package owns: /backends, /runtime, /ingress, /tile-status, /term-net.
func (st *State) registerRuntimeAPI(srv *server.Server) {
	run, brk, userStore := st.Run, st.Broker, st.Users
	srv.RegisterAPI("GET /backends", func(w http.ResponseWriter, r *http.Request) {
		if !brk.IsAdmin(auth.PrincipalOf(r)) {
			http.Error(w, "admin only", http.StatusForbidden)
			return
		}
		server.WriteJSON(w, http.StatusOK, run.Status())
	})
	// Full runtime visibility for the admin console: host + per-backend process,
	// namespaces, and egress/network activity (plans/isolation.md).
	srv.RegisterAPI("GET /runtime", func(w http.ResponseWriter, r *http.Request) {
		if !brk.IsAdmin(auth.PrincipalOf(r)) {
			http.Error(w, "admin only", http.StatusForbidden)
			return
		}
		var ms goruntime.MemStats
		goruntime.ReadMemStats(&ms)
		host := map[string]any{
			"version": st.Cfg.Version, "pid": os.Getpid(), "uid": os.Geteuid(),
			"kernel": kernelRelease(), "numCPU": goruntime.NumCPU(),
			"goroutines": goruntime.NumGoroutine(), "heapMB": float64(ms.HeapAlloc) / 1e6,
			"uptimeSec": int64(time.Since(st.Started).Seconds()),
			"isolate":   run.Isolate, "rootfs": run.Rootfs, "scopeUids": st.Cfg.ScopeUIDs && st.priv.Euid() == 0,
			"protections": sandbox.DetectProtections(), // terminal mount/read guard availability
		}
		// Live per-tile stats (cpu/mem/io series) with tile owners attached
		// so the console can group by org.
		stats := run.StatsSnapshot()
		if tiles, ok := stats["tiles"].([]runner.TileStats); ok {
			owners := userStore.Owners()
			for i := range tiles {
				tiles[i].Owner = owners[tiles[i].Path]
			}
		}
		bs := run.Inspect()
		// Annotate the runtime picture with each backend's effective network
		// (D54): the ref, mode, source and inert reason the console shows.
		for i := range bs {
			l := brk.NetLabel(bs[i].Path)
			bs[i].NetRef, bs[i].Net, bs[i].NetSource, bs[i].NetNote = l.Ref, l.Effective, l.Source, l.Note
		}
		server.WriteJSON(w, http.StatusOK, map[string]any{
			"host": host, "backends": bs, "resources": brk.ResourceUsage(),
			"stats": stats,
		})
	})
	// Ingress overview (plans/ingress.md): exposes + bindings + routes from
	// the broker, live listener/forward status from the managers. Admin-only —
	// the published surface and its failure modes are operator data.
	srv.RegisterAPI("GET /ingress", func(w http.ResponseWriter, r *http.Request) {
		if !brk.IsAdmin(auth.PrincipalOf(r)) {
			http.Error(w, "admin only", http.StatusForbidden)
			return
		}
		out := brk.IngressOverview()
		out["streams"] = st.streams.Status()
		out["forwards"] = st.forwards.Status()
		out["httpListener"] = map[string]any{
			"listen": st.Cfg.IngressListen, "tls": st.Cfg.IngressCert != "" && st.Cfg.IngressKey != "",
		}
		server.WriteJSON(w, http.StatusOK, out)
	})
	// Per-tile runtime status — readable from a tile terminal (self) or by an
	// admin for any tile. Read-only. Runtime metrics we already collect, scoped
	// to one component: backend process/cgroup/egress + disk usage/quota + its
	// alerts. `bx status` renders it.
	srv.RegisterAPI("GET /tile-status", func(w http.ResponseWriter, r *http.Request) {
		p := auth.PrincipalOf(r)
		comp := strings.Trim(r.URL.Query().Get("component"), "/")
		if comp == "" {
			comp = p.Component // default: the caller's own tile (terminal / element principal)
		}
		if comp == "" {
			http.Error(w, "specify ?component= (or call from a tile terminal)", http.StatusBadRequest)
			return
		}
		if !brk.IsAdmin(p) && p.Component != comp {
			http.Error(w, "you can only read your own tile's status", http.StatusForbidden)
			return
		}
		var be *runner.Backend
		for _, b := range run.Inspect() {
			if b.Path == comp {
				bb := b
				be = &bb
				break
			}
		}
		usage, quota, blocked := brk.TileDiskStatus(comp)
		server.WriteJSON(w, http.StatusOK, map[string]any{
			"component": comp,
			"backend":   be, // nil when no backend is running
			"disk":      map[string]any{"usageBytes": usage, "quotaBytes": quota, "blocked": blocked},
			"alerts":    brk.TileAlerts(comp),
			"net":       brk.NetLabel(comp), // the effective network + why (D54)
		})
	})
	// GET /term-net?tile= — the scopes a terminal on this tile may take for
	// the caller, so the picker offers only what the server will honour (D54).
	srv.RegisterAPI("GET /term-net", func(w http.ResponseWriter, r *http.Request) {
		p := auth.PrincipalOf(r)
		tile := strings.Trim(r.URL.Query().Get("tile"), "/")
		if tile == "" || !p.CanTerminalTile(tile) {
			server.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "no terminal access to this tile"})
			return
		}
		g := brk.TermNetFor(p, tile)
		scopes, def := term.ScopesFor(p, g)
		server.WriteJSON(w, http.StatusOK, map[string]any{
			"tile": tile, "scopes": scopes, "default": def, "label": g.OrgLabel, "org": g.OrgOK,
		})
	})
}
