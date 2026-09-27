// backend.go — the seam between this manager and a substrate that runs
// sandboxes. The manager does everything the sandbox-manager contract
// (docs/sandbox-manager.md) asks beyond running a box: partitions and shares,
// owners and people, labels, clientIds, its own ids and versions, images,
// sizes, egress words, quotas. A Backend does only what is specific to the
// substrate, at the level of xbind's tile-sandbox runtime
// (docs/protocol.md §Tile sandboxes): sandboxes by NAME, their lifecycle,
// commands, files, trees, terminals and snapshots.
//
// The shapes are the Go SDK's (sdk/sandbox*.go) on purpose: the `xbin`
// backend is *xbin.Sandboxes itself (Fleet) and *xbin.Sandbox (Box), with no
// translation — backend_iface_test.go keeps that true. Another substrate (a
// cloud's instance API plus ssh, AGENTS.md) implements the same methods and
// answers refusals as *xbin.SandboxError with the contract's refusal enum.
//
// Backends register themselves in an init():
//
//	func init() { registerBackend("xbin", newXbinBackend) }
//
// and config.backend picks one ("xbin" by default). The `fake` backend
// (fake_*_test.go: host directories, TEST ONLY) exists in tests alone.
package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sort"
	"sync"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// Fleet is a substrate's sandboxes as a set: what it offers, and each
// sandbox's definition and lifecycle. *xbin.Sandboxes has exactly these
// methods.
type Fleet interface {
	// Runtime is what the substrate offers this manager now: modes, egress
	// classes (sandbox-net slots and their reach), capabilities, limits.
	Runtime(ctx context.Context) (*xbin.SandboxRuntime, error)
	List(ctx context.Context) ([]xbin.SandboxInfo, error)
	// Create defines a sandbox. spec.ClientID makes it repeat-safe; with
	// spec.From it is a clone (the clone capability). The answer's Defaults
	// are the layout that applied (a backend may place workdir and home).
	Create(ctx context.Context, spec xbin.SandboxSpec) (*xbin.SandboxInfo, error)
	Get(ctx context.Context, name string) (*xbin.SandboxInfo, error)
	Patch(ctx context.Context, name string, p xbin.SandboxPatch) (*xbin.SandboxInfo, error)
	Delete(ctx context.Context, name string) error
	// Start and Stop return once the transition is done or wait runs out.
	Start(ctx context.Context, name string, wait time.Duration) (*xbin.SandboxInfo, error)
	Stop(ctx context.Context, name string, wait time.Duration) (*xbin.SandboxInfo, error)
}

// Box is one sandbox of a Backend, by name. *xbin.Sandbox has exactly these
// methods. A stopped sandbox starts on a command or a file operation (the
// runtime's autoStart); the manager starts it first anyway, to count it
// against the quotas.
type Box interface {
	Run(ctx context.Context, r xbin.RunRequest) (*xbin.RunResult, error)
	Exec(ctx context.Context, r xbin.ExecRequest) (*xbin.ExecInfo, error)
	Execs(ctx context.Context) ([]xbin.ExecInfo, error)
	GetExec(ctx context.Context, id string) (*xbin.ExecInfo, error)
	Output(ctx context.Context, id string, q xbin.OutputQuery) (*xbin.OutputChunk, error)
	Stdin(ctx context.Context, id string, r io.Reader, eof bool) error
	Signal(ctx context.Context, id, sig string, group bool) error
	Resize(ctx context.Context, id string, rows, cols int) error
	Kill(ctx context.Context, id string) error

	// RelayTTY and RelayNewTTY serve the consumer's terminal WebSocket (r,
	// already checked by the manager) on the /ws/term wire: refusals come
	// before the upgrade, in the contract's error shape. o.SessionID and
	// o.SandboxID replace the ids in the session frame; o.ForUser is the
	// person attaching.
	RelayTTY(w http.ResponseWriter, r *http.Request, execID string, o xbin.TTYOptions)
	RelayNewTTY(w http.ResponseWriter, r *http.Request, o xbin.TTYStart)

	Stat(ctx context.Context, path string) (*xbin.FileStat, error)
	ReadFile(ctx context.Context, path string, off, n int64) (io.ReadCloser, *xbin.FileStat, error)
	WriteFile(ctx context.Context, path string, r io.Reader, o xbin.WriteOptions) (*xbin.FileStat, error)
	List(ctx context.Context, path string, limit int) (*xbin.FileList, error)
	Mkdir(ctx context.Context, path string, parents bool) error
	Remove(ctx context.Context, path string, recursive bool) error
	Move(ctx context.Context, from, to string, overwrite bool) error
	GetTar(ctx context.Context, path string, exclude []string) (io.ReadCloser, error)
	PutTar(ctx context.Context, path string, r io.Reader, mkdirs bool) error

	Snapshots(ctx context.Context) ([]xbin.Snapshot, error)
	Snapshot(ctx context.Context, name, clientID string) (*xbin.Snapshot, error)
	RestoreSnapshot(ctx context.Context, id string) (*xbin.SandboxInfo, error)
	DeleteSnapshot(ctx context.Context, id string) error
}

// Backend is a substrate: its fleet and each of its sandboxes.
type Backend interface {
	Fleet
	Sandbox(name string) Box
}

// BackendEnv is what a backend's constructor gets.
type BackendEnv struct {
	// Config is the backend's own settings (config.backendConfig), as the
	// operators stored them: a backend defines its shape.
	Config map[string]any
}

// --- the registry ------------------------------------------------------------------

var backends = struct {
	sync.Mutex
	m map[string]func(BackendEnv) (Backend, error)
}{m: map[string]func(BackendEnv) (Backend, error){}}

// registerBackend makes a backend available (call it from an init()).
func registerBackend(name string, make func(BackendEnv) (Backend, error)) {
	backends.Lock()
	defer backends.Unlock()
	backends.m[name] = make
}

// backendNames lists the registered backends.
func backendNames() []string {
	backends.Lock()
	defer backends.Unlock()
	out := make([]string, 0, len(backends.m))
	for n := range backends.m {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// openBackend builds the named backend; one that isn't registered (or fails
// to start) is a down backend that says why on every call.
func openBackend(name string, env BackendEnv) (Backend, error) {
	backends.Lock()
	mk := backends.m[name]
	backends.Unlock()
	if mk == nil {
		err := errors.New("no backend " + quote(name) + " in this build (registered: " + joinOr(backendNames(), "none") + ")")
		return downBackend{err}, err
	}
	b, err := mk(env)
	if err != nil {
		return downBackend{err}, err
	}
	return b, nil
}

// downBackend answers every call 503 unavailable with why.
type downBackend struct{ why error }

func (d downBackend) err() error {
	return &xbin.SandboxError{Status: http.StatusServiceUnavailable, Refusal: "unavailable",
		Message: "the sandbox substrate is unavailable: " + d.why.Error(), RetryAfter: 30 * time.Second}
}

func (d downBackend) Runtime(context.Context) (*xbin.SandboxRuntime, error) { return nil, d.err() }
func (d downBackend) List(context.Context) ([]xbin.SandboxInfo, error)      { return nil, d.err() }
func (d downBackend) Create(context.Context, xbin.SandboxSpec) (*xbin.SandboxInfo, error) {
	return nil, d.err()
}
func (d downBackend) Get(context.Context, string) (*xbin.SandboxInfo, error) { return nil, d.err() }
func (d downBackend) Patch(context.Context, string, xbin.SandboxPatch) (*xbin.SandboxInfo, error) {
	return nil, d.err()
}
func (d downBackend) Delete(context.Context, string) error { return d.err() }
func (d downBackend) Start(context.Context, string, time.Duration) (*xbin.SandboxInfo, error) {
	return nil, d.err()
}
func (d downBackend) Stop(context.Context, string, time.Duration) (*xbin.SandboxInfo, error) {
	return nil, d.err()
}

// Sandbox is never reached: every sandbox route needs a record, which needs
// a create, which a down backend refuses. It answers a box whose calls all
// fail, all the same.
func (d downBackend) Sandbox(string) Box { return downBox{d} }

type downBox struct{ d downBackend }

func (b downBox) Run(context.Context, xbin.RunRequest) (*xbin.RunResult, error) {
	return nil, b.d.err()
}
func (b downBox) Exec(context.Context, xbin.ExecRequest) (*xbin.ExecInfo, error) {
	return nil, b.d.err()
}
func (b downBox) Execs(context.Context) ([]xbin.ExecInfo, error)          { return nil, b.d.err() }
func (b downBox) GetExec(context.Context, string) (*xbin.ExecInfo, error) { return nil, b.d.err() }
func (b downBox) Output(context.Context, string, xbin.OutputQuery) (*xbin.OutputChunk, error) {
	return nil, b.d.err()
}
func (b downBox) Stdin(context.Context, string, io.Reader, bool) error  { return b.d.err() }
func (b downBox) Signal(context.Context, string, string, bool) error    { return b.d.err() }
func (b downBox) Resize(context.Context, string, int, int) error        { return b.d.err() }
func (b downBox) Kill(context.Context, string) error                    { return b.d.err() }
func (b downBox) Stat(context.Context, string) (*xbin.FileStat, error)  { return nil, b.d.err() }
func (b downBox) Mkdir(context.Context, string, bool) error             { return b.d.err() }
func (b downBox) Remove(context.Context, string, bool) error            { return b.d.err() }
func (b downBox) Move(context.Context, string, string, bool) error      { return b.d.err() }
func (b downBox) PutTar(context.Context, string, io.Reader, bool) error { return b.d.err() }
func (b downBox) Snapshots(context.Context) ([]xbin.Snapshot, error)    { return nil, b.d.err() }
func (b downBox) DeleteSnapshot(context.Context, string) error          { return b.d.err() }
func (b downBox) GetTar(context.Context, string, []string) (io.ReadCloser, error) {
	return nil, b.d.err()
}
func (b downBox) RelayTTY(w http.ResponseWriter, _ *http.Request, _ string, _ xbin.TTYOptions) {
	xbin.WriteSandboxError(w, b.d.err())
}
func (b downBox) RelayNewTTY(w http.ResponseWriter, _ *http.Request, _ xbin.TTYStart) {
	xbin.WriteSandboxError(w, b.d.err())
}
func (b downBox) ReadFile(context.Context, string, int64, int64) (io.ReadCloser, *xbin.FileStat, error) {
	return nil, nil, b.d.err()
}
func (b downBox) WriteFile(context.Context, string, io.Reader, xbin.WriteOptions) (*xbin.FileStat, error) {
	return nil, b.d.err()
}
func (b downBox) List(context.Context, string, int) (*xbin.FileList, error) { return nil, b.d.err() }
func (b downBox) Snapshot(context.Context, string, string) (*xbin.Snapshot, error) {
	return nil, b.d.err()
}
func (b downBox) RestoreSnapshot(context.Context, string) (*xbin.SandboxInfo, error) {
	return nil, b.d.err()
}
