package boot

// isolation.go — --isolate's boot steps: the rootfs confined tool runs use
// (stepConfine), and the sandboxing of backends and terminals over it
// (stepIsolation), with the base-image pins both GC passes keep.

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/xbin-dev/xbin/internal/confine"
	"github.com/xbin-dev/xbin/internal/deps"
	"github.com/xbin-dev/xbin/internal/gpu"
	"github.com/xbin-dev/xbin/internal/layers"
	"github.com/xbin-dev/xbin/internal/sandbox"
)

// Isolation is orthogonal to --dev/--no-auth (which only change asset serving
// and logging): the sandbox network/fs model is different enough that dev
// should run against it too (`make dev`).
// stepConfine validates --isolate's rootfs and turns on confined tool runs:
// every tool xbind runs on tile data (git, go build) runs in a sandbox over
// that rootfs from here on — never as xbind (D78, internal/confine). Early,
// so the boot's own repo work on tiles is confined too.
func (st *State) stepConfine() error {
	cfg := st.Cfg
	if !cfg.Isolate {
		return nil
	}
	if cfg.Rootfs == "" {
		return fmt.Errorf("--isolate needs --rootfs <dir> (an unpacked base OCI rootfs; `make rootfs`)")
	}
	if !sandbox.Available() {
		return fmt.Errorf("--isolate: unprivileged user namespaces unavailable on this host")
	}
	abs, err := filepath.Abs(cfg.Rootfs)
	if err != nil || !dirExists(abs) {
		return fmt.Errorf("--isolate: rootfs %q not found", cfg.Rootfs)
	}
	st.rootfs = abs
	confine.Configure(abs)
	return nil
}

// pinnedBases is every base version a layer still pins (internal/layers:
// the terminal layers, the tile sandboxes and their snapshots, and the
// tile-sandbox definitions) — what both base-image GC passes keep: the
// preserved `<rootfs>-<version>` dirs (stepIsolation) and the VM images
// built from them (stepVM). nil when a pin couldn't be read (a stamp, a
// tree, or the definitions): the set may be short, so neither pass releases
// anything this boot.
func (st *State) pinnedBases() map[string]bool {
	pins, err := layers.Pinned(st.WS, st.sandboxBasePins)
	if err != nil {
		slog.Warn("base images: nothing released this boot — a layer's pin couldn't be read", "err", err)
		return nil
	}
	return pins
}

func (st *State) stepIsolation() error {
	cfg, run, tm, brk := st.Cfg, st.Run, st.Term, st.Broker
	if !cfg.Isolate {
		return nil
	}
	abs := st.rootfs // validated by stepConfine
	run.Rootfs = abs
	run.Isolate = true
	run.Egress = brk.EgressFor
	run.GPU = brk.GPUFor
	run.NetRoster = brk.NetProviderRoster
	run.NetTarget = brk.NetClientTarget
	run.NetHost = brk.NetHostShare
	run.NetCaps = brk.NetAdminFor         // cap:net-admin → keep net-admin caps
	run.ContainerCaps = brk.ContainersFor // cap:containers → keep userns caps for rootless podman
	if inv := gpu.Inventory(); len(inv) > 0 {
		slog.Info("NVIDIA GPUs available for gpu:* grants", "count", len(inv))
	}
	// Terminals share the base rootfs too (RT-4): the workspace is bound rw
	// (editing plane), plus the SDK source ro so `go build` resolves.
	tm.Isolate = true
	tm.Rootfs = abs
	// Never stack an existing terminal upper on a base image different from
	// the one it was built on (corrupts apt/dpkg state): a session's start
	// refuses a layer whose base is missing, or moves it to the current base
	// (base auto-update) — the boot only logs them (D175; it used to refuse
	// to start), reads the current base's version once, and has the remover
	// finish what a restart left of layers moved off their base.
	tm.CheckBaseImages()
	layers.GC(abs, st.pinnedBases()) // release preserved bases no layer pins anymore
	// Same locator as go.work generation (XBIN_SDK_PATH → /opt/xbin/sdk).
	// Never fall back to "": filepath.Abs("") is the daemon's cwd (the
	// install prefix in prod), and binding that read-only over the sandbox
	// shadowed the rw $HOME/component mounts beneath it.
	if p := deps.SDKPath(); p != "" {
		if sdk, err := filepath.Abs(p); err == nil && dirExists(sdk) {
			tm.ExtraBinds = append(tm.ExtraBinds, sandbox.Bind{Src: sdk, Dst: sdk, RO: true})
		}
	}
	slog.Info("per-component isolation enabled (tier 3)", "rootfs", abs)
	// Sandboxes need a delegated sub-uid/gid RANGE for apt/dpkg to chown
	// files to the system users their post-install scripts create. Without
	// it the sandbox falls back to single-uid mode (only container-root
	// mapped), where those chowns fail with EINVAL and heavier package
	// installs break midway (systemd, dbus, …) while simple ones still work.
	// Warn loudly — the failure is otherwise a cryptic dpkg error.
	rangeOK, reason := sandbox.IDMapStatus(os.Getuid(), os.Getgid())
	st.uidRange, st.uidRangeNote = rangeOK, reason
	if rangeOK {
		slog.Info("sandbox uid mapping: full sub-id range (apt/dpkg system-user installs work)")
	} else {
		slog.Warn("sandbox uid mapping: SINGLE-UID fallback — apt/dpkg installs that create system users (systemd, dbus, …) will fail with chown \"Invalid argument\"; delegate a sub-id range to this user and install the uidmap package (deploy/install.sh does both), then restart xbind",
			"reason", reason)
	}
	return nil
}
