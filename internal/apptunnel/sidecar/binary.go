package sidecar

import (
	"errors"
	"os"
)

// The binary xbind runs: XBIN_IROH (a path) when set, else the gzip
// `make build` embedded (embed_iroh_*.go), else none — the "unavailable"
// state. The embedded one is extracted once per xbind to the best place
// that can exec it: a sealed memfd (nothing on disk, nothing anyone can
// change), else <RunDir>/iroh/ (0700), else <ws>/.xbin/iroh/ (Debian mounts
// /run noexec). Both directories are xbind's own; a sandbox never writes
// them. Status.Exec says which ("memfd" | "rundir" | "workspace" | "path").

// exe is the binary, open, ready to pass to the sandbox (Spec.Exe).
type exe struct {
	f   *os.File
	how string
}

func (x *exe) close() {
	if x != nil && x.f != nil {
		x.f.Close()
	}
}

// ErrUnavailable is Start's answer when this xbind has no sidecar: it was
// built without one (no -tags xbin_iroh) and XBIN_IROH is unset.
var ErrUnavailable = errors.New("app tunnel: this xbind has no iroh sidecar (built without it, and XBIN_IROH is unset)")

// binaryPath is the XBIN_IROH override, if any.
func (o Options) binaryPath() string {
	if o.Binary != "" {
		return o.Binary
	}
	return os.Getenv("XBIN_IROH")
}

func (o Options) available() bool { return o.binaryPath() != "" || len(embedded) > 0 }

// execKinds is the extraction order for the embedded binary.
var execKinds = []string{"memfd", "rundir", "workspace"}

// nextKind is the extraction kind after how ("" when none is left, or the
// binary wasn't extracted).
func nextKind(how string) string {
	for i, k := range execKinds {
		if k == how && i+1 < len(execKinds) {
			return execKinds[i+1]
		}
	}
	return ""
}
