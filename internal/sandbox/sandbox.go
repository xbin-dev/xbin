package sandbox

import (
	"errors"
	"os"
	"path"
	"sort"
	"strings"
)

// ErrUnsupported is returned by Launch on non-Linux platforms.
var ErrUnsupported = errors.New("sandbox: only supported on linux")

// GatewayIP is the virtual gateway address inside a relay netns. A relay may
// host-forward specific ports on this address to host services (e.g. xbind),
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

// NetClient is one per-client link a net-provider tile terminates.
type NetClient struct {
	Name string `json:"name"` // the client component (for xbind's bookkeeping)
	Addr string `json:"addr"` // provider-side link address, e.g. "10.42.0.1/30"
}

// Bind is one mount into the sandbox root.
//
// When Mask is set, Src is ignored and Dst is instead covered with an empty
// tmpfs, hiding whatever is beneath it (systemd's InaccessiblePaths): the
// terminal masks the workspace's secrets (.xbin), resource/vault state (data),
// and other users' homes this way, so the read-only workspace bind can't be
// used to read them. RO seals the cover (nothing may nest); a read-write cover
// lets a deeper bind nest on top (the own $HOME under a masked homes/).
type Bind struct {
	Src  string `json:"src"`            // host path (as seen before pivot_root); unused when Mask/Detach
	Dst  string `json:"dst"`            // absolute path inside the new root
	RO   bool   `json:"ro"`             // remount read-only after binding (seal, if Mask)
	Mask bool   `json:"mask,omitempty"` // cover Dst with an empty tmpfs instead of binding Src
	// Detach lazily unmounts every mount nested under Dst (Src ignored). Used
	// before a Mask to drop submounts the recursive workspace bind cloned in —
	// the workspace's gocryptfs resource (resenc) mounts — so a terminal's
	// `mount`/mountinfo can't enumerate other tiles' resource names even though
	// their contents are already masked. Safe because the sandbox root is
	// MS_REC|MS_PRIVATE: the detach applies to the sandbox clone only, never the
	// host's live mounts. Order it ahead of the same-Dst Mask (sortBinds keeps
	// caller order within a depth) so the submounts are still addressable.
	Detach bool `json:"detach,omitempty"`
}

// sortBinds orders binds ancestors-first (shallower Dst mounts earlier; stable
// within a depth, so equal-path binds keep caller order and the later one still
// shadows). This makes overlap nest correctly no matter how callers assemble
// the list: a broad read-only bind (e.g. an install prefix) added late can
// never mask an earlier read-write bind beneath it — the failure mode that made
// terminals' rw $HOME unreachable under a later ro /opt/xbin bind.
func sortBinds(binds []Bind) []Bind {
	out := append([]Bind(nil), binds...)
	sort.SliceStable(out, func(i, j int) bool {
		return bindDepth(out[i].Dst) < bindDepth(out[j].Dst)
	})
	return out
}

func bindDepth(dst string) int {
	return strings.Count(path.Clean("/"+strings.ReplaceAll(dst, "\\", "/")), "/")
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
	// "splice" → a TUN whose fd xbind splices to a provider tile (plans/
	// interfaces.md) instead of running the relay on it.
	Net string `json:"net,omitempty"`

	// NetAddr/NetGw override the egress TUN's address/gateway (empty = the relay
	// defaults 10.0.2.15/24 via 10.0.2.2). A client spliced to a provider tile
	// puts its point-to-point link addresses here.
	NetAddr string `json:"netAddr,omitempty"`
	NetGw   string `json:"netGw,omitempty"`
	// NetClients are a net-provider tile's per-client links: init creates one
	// extra TUN per entry (its /30 provider-side address, no default route) and
	// hands each fd to xbind, which splices it to that client's egress TUN. The
	// egress TUN fd is sent first, then these in order.
	NetClients []NetClient `json:"netClients,omitempty"`

	// HostNet skips the network namespace entirely — the process shares the host
	// network (unrestricted). For the owner plane (terminals), not components.
	HostNet bool `json:"hostNet,omitempty"`

	// MountGuard installs a seccomp filter (just before exec) that denies the
	// mount-teardown/move syscalls — umount2, move_mount, open_tree, and
	// mount(MS_MOVE) — so a process that is uid 0 in its user namespace can't
	// unmount or move away the empty-tmpfs masks that hide workspace secrets
	// (scope masks; docs/isolation.md). Keeps CAP_SYS_ADMIN otherwise. Set for
	// terminal sandboxes, whose read-only workspace mount carries those masks.
	MountGuard bool `json:"mountGuard,omitempty"`

	// ReadGuard, if non-nil, applies a Landlock ruleset (before exec) denying
	// reads of file *contents* under the workspace secret dirs even if their
	// mount masks are peeled — defense in depth for the terminal masks. Silent
	// no-op where Landlock is unavailable.
	ReadGuard *ReadGuardSpec `json:"readGuard,omitempty"`

	// Unprivileged (set for tile backends, not terminals) drops all capabilities
	// and installs a seccomp block-list of privileged/system-damaging syscalls
	// (mount, module load, kexec, reboot, ptrace, bpf, …) before exec — so a
	// buggy or wedged tile can't reach past its own process. Terminals need the
	// caps (apt, nested namespaces) so they keep them and rely on the narrower
	// mount/read guards instead.
	Unprivileged bool `json:"unprivileged,omitempty"`

	// Restricted (set for untrusted, non-admin *user* terminals) hardens a
	// terminal beyond the mount/read guards without breaking `apt`: init pins
	// the user namespace to zero nested user/mount namespaces (the ucount knobs
	// under /proc/sys/user, which block creation inside the kernel regardless of
	// clone/clone3/unshare — immune to clone3's unfilterable flags), then drops
	// CAP_SYS_ADMIN and CAP_SYS_RESOURCE (plus other dangerous caps) while KEEPING
	// the file/ownership caps dpkg needs, and installs a seccomp filter denying
	// the ns-creating syscalls as belt-and-suspenders. Net: a rogue shell can't
	// `unshare -Ur` into a fresh userns to regain CAP_SYS_ADMIN and mount over its
	// masks — yet `apt install` still works. (plans/DECISIONS.md D18; isolation.md.)
	Restricted bool `json:"restricted,omitempty"`

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

// ReadGuardSpec parameterizes the terminal read guard (Landlock). All paths are
// as seen inside the sandbox (== their host paths for the workspace).
type ReadGuardSpec struct {
	Root       string   `json:"root"`       // workspace root (e.g. /workspace)
	SecretDirs []string `json:"secretDirs"` // basenames under Root to deny reading (.xbin, data, homes)
	AllowUnder []string `json:"allowUnder"` // absolute paths kept readable (own $HOME under homes/)
}

// Protections reports which terminal-hardening mechanisms the running kernel
// supports, so the admin console can show whether tile terminals are actually
// guarded (docs/isolation.md). DetectProtections probes it.
type Protections struct {
	Seccomp     bool `json:"seccomp"`     // mount guard installable (seccomp filter mode)
	Landlock    bool `json:"landlock"`    // read guard installable
	LandlockABI int  `json:"landlockAbi"` // Landlock ABI version (0 = none)
}
