package sandbox

import (
	"errors"
	"os"
	"strings"
)

// ErrUnsupported is returned by Launch on non-Linux platforms.
var ErrUnsupported = errors.New("sandbox: only supported on linux")

// GatewayIP is the virtual gateway address inside a relay netns. A relay may
// host-forward specific ports on this address to host services (e.g. buxond),
// so a netns-isolated sandbox can reach the workspace controller without any
// host interface being visible (see relay.Config.Gateway/HostFwd).
const GatewayIP = "10.0.2.2"

// Handle carries the parent-side state of a launched sandbox: cleanup and, when
// egress is requested (Spec.Net == "relay"), the control socket over which the
// in-namespace init passes the TUN fd back to us. RecvTUN is Linux-only.
type Handle struct {
	cleanup func()
	ctrl    *os.File     // parent end of the fd-passing socketpair (nil = no relay)
	setup   func() error // range uid/gid mapping + release the init (nil = single-uid)
}

// NeedsRelay reports whether the init will hand back a TUN fd for an egress relay.
func (h *Handle) NeedsRelay() bool { return h != nil && h.ctrl != nil }

// SetupUserns must be called by the caller right after starting the sandbox
// process and before RecvTUN: in range-uid mode it writes the child's uid/gid
// maps (via newuidmap) and releases the init, which is blocked waiting for them.
// No-op (nil-safe) in single-uid mode or off Linux.
func (h *Handle) SetupUserns() error {
	if h == nil || h.setup == nil {
		return nil
	}
	return h.setup()
}

// Cleanup releases the spec temp file and the control socket.
func (h *Handle) Cleanup() {
	if h == nil {
		return
	}
	if h.cleanup != nil {
		h.cleanup()
	}
	if h.ctrl != nil {
		h.ctrl.Close()
	}
}

// HostResolver is the upstream DNS a relay forwards :53 queries to: the host's
// first nameserver (from /etc/resolv.conf), else a public default.
func HostResolver() string {
	if b, err := os.ReadFile("/etc/resolv.conf"); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if ns, ok := strings.CutPrefix(strings.TrimSpace(line), "nameserver "); ok {
				ns = strings.TrimSpace(ns)
				if !strings.Contains(ns, ":") {
					return ns + ":53"
				}
				return "[" + ns + "]:53"
			}
		}
	}
	return "1.1.1.1:53"
}

// Bind is one mount into the sandbox root.
type Bind struct {
	Src string `json:"src"` // host path (as seen before pivot_root)
	Dst string `json:"dst"` // absolute path inside the new root
	RO  bool   `json:"ro"`  // remount read-only after binding
}

// Spec describes one component sandbox. The parent (runner) fills it; the
// re-exec'd init consumes it to build the mount namespace and exec the backend.
type Spec struct {
	// Overlay rootfs: Lower is the base rootfs first, then any granted dep
	// dirs (all read-only). Upper/Work are the per-component writable layer; if
	// Upper is empty, init uses an internal tmpfs.
	Lower []string `json:"lower"`
	Upper string   `json:"upper,omitempty"`
	Work  string   `json:"work,omitempty"`

	Binds []Bind `json:"binds"` // component dir (ro), resources (rw), gateway socket, /dev nodes…

	Entry string   `json:"entry"` // absolute path inside the sandbox to exec
	Argv  []string `json:"argv"`  // full argv (Argv[0] is the program name)
	Env   []string `json:"env"`
	Cwd   string   `json:"cwd,omitempty"` // default "/"

	// Rootless single-uid mapping: container uid/gid 0 → these host ids.
	HostUID int `json:"hostUid"`
	HostGID int `json:"hostGid"`

	// Net selects the network namespace mode. "" / "none" → an empty netns
	// (loopback only) = default-deny egress; the gateway socket is bind-mounted
	// in regardless. "relay" → TUN + userspace egress relay under a policy.
	Net string `json:"net,omitempty"`

	// HostNet skips the network namespace entirely — the process shares the host
	// network (unrestricted). For the owner plane (terminals), not components.
	HostNet bool `json:"hostNet,omitempty"`

	// The following are filled by Launch (not the caller) to carry runtime wiring
	// to the re-exec'd init:

	// SyncFD, if non-zero, is an fd the init blocks on until the parent has
	// written its uid/gid maps (range mapping via newuidmap). 0 = maps are
	// already set (single-uid mode) and the init proceeds immediately.
	SyncFD int `json:"syncFd,omitempty"`
	// CtrlFD is the fd of the relay TUN fd-passing socket (0 = no relay).
	CtrlFD int `json:"ctrlFd,omitempty"`
	// FuseOverlay, when non-empty, is the path to a fuse-overlayfs binary the
	// init mounts the root with (supports redirect_dir/metacopy that unprivileged
	// kernel overlayfs forbids, so `apt install` etc. work). "" = kernel overlay.
	FuseOverlay string `json:"fuseOverlay,omitempty"`
}
