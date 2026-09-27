package boot

import (
	"log/slog"
	"net/http"
	"sync"

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
	m.GC(st.pinnedBases()) // keep the images of bases a layer still pins
	p := m.Policy()
	if status.Available && status.Emulated {
		slog.Info("VM sandboxes available, emulated", "why", status.Note, "terminals", p.Terminals, "backends", p.Backends, "tiles", p.Tiles, "memMiB", p.MemMiB, "vcpus", p.VCPUs)
	} else if status.Available {
		slog.Info("VM sandboxes available", "terminals", p.Terminals, "backends", p.Backends, "tiles", p.Tiles, "memMiB", p.MemMiB, "vcpus", p.VCPUs)
	} else {
		slog.Info("VM sandboxes unavailable", "reason", status.Reason)
	}
	return nil
}

// registerVMAPI mounts GET /vm (anyone signed in: whether VMs can start here
// and the workspace's switches) and PUT /vm/policy (workspace admins).
func (st *State) registerVMAPI(srv *server.Server) {
	srv.RegisterAPI("GET /vm", st.getVM)
	srv.RegisterAPI("PUT /vm/policy", st.putVMPolicy)
}

func (st *State) getVM(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{"status": st.VM.Status(), "policy": st.VM.Policy()}
	if st.Broker.IsAdmin(auth.PrincipalOf(r)) {
		out["used"] = st.VM.Used()
		out["usedTiles"] = st.VM.UsedTiles()
	}
	server.WriteJSON(w, http.StatusOK, out)
}

// vmPolicyMu serializes PUT /vm/policy's read, merge and write.
var vmPolicyMu sync.Mutex

// putVMPolicy decodes the body onto the stored policy: a field the body
// leaves out keeps its value, so an admin console from before a field
// existed (tiles, tilesBudgetMiB, tilesEmulated) can't zero it (D120).
func (st *State) putVMPolicy(w http.ResponseWriter, r *http.Request) {
	if !st.Broker.IsAdmin(auth.PrincipalOf(r)) {
		http.Error(w, "admin only", http.StatusForbidden)
		return
	}
	if st.VM == nil {
		http.Error(w, "VM sandboxes need isolation (xbind --isolate)", http.StatusConflict)
		return
	}
	vmPolicyMu.Lock()
	defer vmPolicyMu.Unlock()
	p := st.VM.StoredPolicy()
	if err := server.DecodeJSON(r, &p); err != nil {
		http.Error(w, "bad policy: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := st.VM.SetPolicy(p); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	slog.Info("VM sandbox policy changed", "terminals", p.Terminals, "backends", p.Backends, "tiles", p.Tiles, "tilesEmulated", p.TilesEmulated)
	server.WriteJSON(w, http.StatusOK, map[string]any{"status": st.VM.Status(), "policy": st.VM.Policy(), "stored": p})
}
