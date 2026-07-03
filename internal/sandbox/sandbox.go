package sandbox

import (
	"errors"
	"os"
)

// ErrUnsupported is returned by Launch on non-Linux platforms.
var ErrUnsupported = errors.New("sandbox: only supported on linux")

// Handle carries the parent-side state of a launched sandbox: cleanup and, when
// egress is requested (Spec.Net == "relay"), the control socket over which the
// in-namespace init passes the TUN fd back to us. RecvTUN is Linux-only.
type Handle struct {
	cleanup func()
	ctrl    *os.File // parent end of the fd-passing socketpair (nil = no relay)
}

// NeedsRelay reports whether the init will hand back a TUN fd for an egress relay.
func (h *Handle) NeedsRelay() bool { return h != nil && h.ctrl != nil }

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
}
