package boot

import (
	"log/slog"
	"net/http"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/vm"
)

// stepVM sets up VM sandboxes (plans/vm-sandbox.md) under isolation: the
// manager terminals open VMs through, its cache GC, and the log line saying
// whether this host can run them (KVM or emulation, the assets) and whether
// the admin has turned them on. A host that can't just reports why — nothing
// else changes.
func (st *State) stepVM() error {
	if !st.Cfg.Isolate || st.Term == nil {
		return nil
	}
	m := &vm.Manager{Root: st.WS, Rootfs: st.rootfs, Bx: st.Term.BxPath}
	st.VM = m
	st.Term.VM = m
	st.Run.VM = m
	status := m.Status()
	m.GC()
	p := m.Policy()
	if status.Available && status.Emulated {
		slog.Info("VM sandboxes available, emulated", "why", status.Note, "terminals", p.Terminals, "backends", p.Backends, "memMiB", p.MemMiB, "vcpus", p.VCPUs)
	} else if status.Available {
		slog.Info("VM sandboxes available", "terminals", p.Terminals, "backends", p.Backends, "memMiB", p.MemMiB, "vcpus", p.VCPUs)
	} else {
		slog.Info("VM sandboxes unavailable", "reason", status.Reason)
	}
	return nil
}

// registerVMAPI mounts GET /vm (anyone signed in: whether VMs can start here
// and the workspace's switches) and PUT /vm/policy (workspace admins).
func (st *State) registerVMAPI(srv *server.Server) {
	brk := st.Broker
	srv.RegisterAPI("GET /vm", func(w http.ResponseWriter, r *http.Request) {
		out := map[string]any{"status": st.VM.Status(), "policy": st.VM.Policy()}
		if brk.IsAdmin(auth.PrincipalOf(r)) {
			out["used"] = st.VM.Used()
		}
		server.WriteJSON(w, http.StatusOK, out)
	})
	srv.RegisterAPI("PUT /vm/policy", func(w http.ResponseWriter, r *http.Request) {
		if !brk.IsAdmin(auth.PrincipalOf(r)) {
			http.Error(w, "admin only", http.StatusForbidden)
			return
		}
		if st.VM == nil {
			http.Error(w, "VM sandboxes need isolation (xbind --isolate)", http.StatusConflict)
			return
		}
		var p vm.Policy
		if err := server.DecodeJSON(r, &p); err != nil {
			http.Error(w, "bad policy: "+err.Error(), http.StatusBadRequest)
			return
		}
		if err := st.VM.SetPolicy(p); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		slog.Info("VM sandbox policy changed", "terminals", p.Terminals, "backends", p.Backends)
		server.WriteJSON(w, http.StatusOK, map[string]any{"status": st.VM.Status(), "policy": st.VM.Policy()})
	})
}
