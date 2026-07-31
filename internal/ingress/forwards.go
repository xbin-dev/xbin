package ingress

import (
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/xbin-dev/xbin/internal/util"
)

// A terminator TILE (a `provides {kind:"ingress"}` component, e.g. the
// Traefik builtin) hands decrypted HTTP back to xbind on a dedicated unix
// socket — its "last hop" door. The socket lives in the tmpfs run dir
// (0700, xbind-owned) and is reachable from the tile's netns only through
// an explicit relay gateway forward, so possession of the path alone means
// "this bound terminator" — per-socket attribution with no extra token.
type Forwards struct {
	Dir string // socket dir (the runner's run dir — tmpfs)
	// Handler builds the per-terminator entry (an HTTPHandler whose lookups
	// are scoped to source == that tile).
	Handler func(source string) http.Handler

	mu     sync.Mutex
	active map[string]*fwdSock
}

type fwdSock struct {
	path string
	srv  *http.Server
	err  string
}

// SocketPath is the deterministic forward-socket path for a terminator tile
// (used for relay host-forward wiring even before the socket exists).
func (f *Forwards) SocketPath(source string) string {
	return filepath.Join(f.Dir, "igw-"+util.CompKey(source)+".sock")
}

// Reconcile makes the live socket set match the terminator tiles.
func (f *Forwards) Reconcile(sources []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.active == nil {
		f.active = map[string]*fwdSock{}
	}
	want := map[string]bool{}
	for _, s := range sources {
		want[s] = true
	}
	for s, fs := range f.active {
		if !want[s] {
			if fs.srv != nil {
				_ = fs.srv.Close()
			}
			_ = os.Remove(fs.path)
			delete(f.active, s)
		}
	}
	for s := range want {
		if fs, ok := f.active[s]; ok && fs.err == "" {
			continue
		}
		f.active[s] = f.open(s)
	}
}

func (f *Forwards) open(source string) *fwdSock {
	p := f.SocketPath(source)
	_ = os.Remove(p)
	fs := &fwdSock{path: p}
	ln, err := net.Listen("unix", p)
	if err != nil {
		fs.err = err.Error()
		return fs
	}
	fs.srv = &http.Server{Handler: f.Handler(source)}
	go func() { _ = fs.srv.Serve(ln) }()
	return fs
}

// Close tears down every forward socket (daemon shutdown).
func (f *Forwards) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for s, fs := range f.active {
		if fs.srv != nil {
			_ = fs.srv.Close()
		}
		_ = os.Remove(fs.path)
		delete(f.active, s)
	}
}

// ForwardStatus is one terminator forward socket's state.
type ForwardStatus struct {
	Source string `json:"source"`
	Socket string `json:"socket"`
	Error  string `json:"error,omitempty"`
}

// Status snapshots the forward sockets, stably ordered.
func (f *Forwards) Status() []ForwardStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]ForwardStatus, 0, len(f.active))
	for s, fs := range f.active {
		out = append(out, ForwardStatus{Source: s, Socket: fs.path, Error: fs.err})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Source < out[j].Source })
	return out
}
