package tilesbx

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
)

// The principals the gates tell apart.
var (
	mgr      = auth.Principal{Component: "apps/mgr", Via: "instance"}   // a manager tile's backend
	mgrFrame = auth.Principal{Component: "apps/mgr", Via: "frame"}      // the same tile's page
	mgrTerm  = auth.Principal{Component: "apps/mgr", Via: "terminal"}   // a terminal on it
	cron     = auth.Principal{Component: "xbin/cron", Via: "cron"}      // a cron tick
	user     = auth.Principal{UserID: "bob", Via: "session"}            // a signed-in person
	noCap    = auth.Principal{Component: "apps/other", Via: "instance"} // a backend without the cap
	admin    = auth.Principal{Owner: true, Via: "bearer"}
)

type fakeCaps map[string]bool

func (f fakeCaps) SandboxesFor(tile string) bool { return f[tile] }

type fakeNet map[string][]EgressClass

func (f fakeNet) Classes(tile string) []EgressClass { return f[tile] }

// fakeMounts answers ResourceMount from a table; a missing entry is "not held".
type fakeMounts map[string]MountSource

func (f fakeMounts) ResourceMount(tile, res string) (MountSource, error) {
	if ms, ok := f[tile+" "+res]; ok {
		if ms.Kind != "filesystem" {
			return MountSource{}, errors.New(res + " is a " + ms.Kind + " resource: only filesystem resources mount")
		}
		return ms, nil
	}
	return MountSource{}, errors.New("the tile doesn't hold " + res)
}

type fakeModes struct{ accel, reason string }

func (f fakeModes) VM() (string, string) { return f.accel, f.reason }

type fakeTiles map[string]bool

func (f fakeTiles) Exists(tile string) bool { return f[tile] }

// testDeps: apps/mgr holds the cap, declares an internet class, holds a
// writer filesystem resource and a sqlite one; VM mode runs on KVM.
func testDeps() Deps {
	return Deps{
		Caps:  fakeCaps{"apps/mgr": true, "apps/mgr2": true},
		Admin: func(p auth.Principal) bool { return p.IsAdmin() },
		Net: fakeNet{"apps/mgr": {{Class: "class:internet", Slot: "internet", Ref: "internet", Reach: "internet",
			Rules: []string{"net:internet"}}}},
		Mounts: fakeMounts{
			"apps/mgr res:apps/mgr/work": {Src: "/x/work", Role: "writer", Kind: "filesystem", Encrypted: true, Ready: true},
			"apps/mgr res:apps/mgr/ro":   {Src: "/x/ro", Role: "reader", Kind: "filesystem", Encrypted: true, Ready: true},
			"apps/mgr res:apps/mgr/db":   {Src: "/x/db", Role: "writer", Kind: "sqlite"},
		},
		Modes: fakeModes{accel: "kvm"},
		Tiles: fakeTiles{"apps/mgr": true},
	}
}

type testEnv struct {
	t   *testing.T
	m   *Manager
	mux *http.ServeMux
}

// newEnv builds an isolated runtime over a fresh workspace, and the routes
// as internal/boot mounts them.
func newEnv(t *testing.T, mut ...func(*Options)) *testEnv {
	t.Helper()
	o := Options{Root: t.TempDir(), Isolated: true, UIDRange: true, Deps: testDeps(),
		Now: func() time.Time { return time.UnixMilli(1790000000000) }}
	for _, f := range mut {
		f(&o)
	}
	m := New(o)
	return &testEnv{t: t, m: m, mux: routes(m)}
}

// routes mirrors internal/boot/tilesandboxes.go (a manager's GET /sandboxes
// is handed to ServeList by the registry view's route).
func routes(m *Manager) *http.ServeMux {
	mux := http.NewServeMux()
	for pat, h := range routeTable(m) {
		mux.HandleFunc(pat, h)
	}
	return mux
}

// routeTable is every route's pattern and handler.
func routeTable(m *Manager) map[string]http.HandlerFunc {
	return map[string]http.HandlerFunc{
		"GET /sandboxes":                                 m.ServeList,
		"GET /sandboxes/runtime":                         m.ServeRuntime,
		"GET /sandboxes/policy":                          m.ServePolicy,
		"PUT /sandboxes/policy":                          m.ServeSetPolicy,
		"POST /sandboxes":                                m.ServeCreate,
		"GET /sandboxes/{name}":                          m.ServeGet,
		"PATCH /sandboxes/{name}":                        m.ServePatch,
		"DELETE /sandboxes/{name}":                       m.ServeDelete,
		"POST /sandboxes/{name}/start":                   m.ServeStart,
		"POST /sandboxes/{name}/stop":                    m.ServeStop,
		"POST /sandboxes/{name}/reset":                   m.ServeReset,
		"POST /sandboxes/{name}/rebase":                  m.ServeRebase,
		"POST /sandboxes/{name}/run":                     m.ServeRun,
		"GET /sandboxes/{name}/execs":                    m.ServeExecs,
		"POST /sandboxes/{name}/execs":                   m.ServeExecStart,
		"GET /sandboxes/{name}/execs/{id}":               m.ServeExec,
		"DELETE /sandboxes/{name}/execs/{id}":            m.ServeExecKill,
		"GET /sandboxes/{name}/execs/{id}/output":        m.ServeOutput,
		"POST /sandboxes/{name}/execs/{id}/stdin":        m.ServeStdin,
		"POST /sandboxes/{name}/execs/{id}/signal":       m.ServeSignal,
		"POST /sandboxes/{name}/execs/{id}/resize":       m.ServeResize,
		"GET /sandboxes/{name}/execs/{id}/tty":           m.ServeExecTTY,
		"GET /sandboxes/{name}/tty":                      m.ServeTTY,
		"GET /sandboxes/{name}/files/stat":               m.ServeStat,
		"GET /sandboxes/{name}/files/content":            m.ServeReadFile,
		"PUT /sandboxes/{name}/files/content":            m.ServeWriteFile,
		"GET /sandboxes/{name}/files/list":               m.ServeListDir,
		"POST /sandboxes/{name}/files/mkdir":             m.ServeMkdir,
		"POST /sandboxes/{name}/files/remove":            m.ServeRemove,
		"POST /sandboxes/{name}/files/move":              m.ServeMove,
		"GET /sandboxes/{name}/tar":                      m.ServeGetTar,
		"PUT /sandboxes/{name}/tar":                      m.ServePutTar,
		"POST /sandboxes/copy":                           m.ServeCopy,
		"GET /sandboxes/{name}/snapshots":                m.ServeSnapshots,
		"POST /sandboxes/{name}/snapshots":               m.ServeSnapshot,
		"POST /sandboxes/{name}/snapshots/{sid}/restore": m.ServeRestore,
		"DELETE /sandboxes/{name}/snapshots/{sid}":       m.ServeDeleteSnapshot,
	}
}

// do calls the runtime as p; body is JSON-encoded unless it is a string.
func (e *testEnv) do(p auth.Principal, method, path string, body any) *httptest.ResponseRecorder {
	e.t.Helper()
	var rd *bytes.Reader
	switch b := body.(type) {
	case nil:
		rd = bytes.NewReader(nil)
	case string:
		rd = bytes.NewReader([]byte(b))
	default:
		j, err := json.Marshal(b)
		if err != nil {
			e.t.Fatal(err)
		}
		rd = bytes.NewReader(j)
	}
	r := httptest.NewRequest(method, path, rd)
	r = r.WithContext(auth.WithPrincipal(r.Context(), p))
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, r)
	return w
}

// want asserts a status (and, for an error, the refusal).
func (e *testEnv) want(w *httptest.ResponseRecorder, status int, refusal string) {
	e.t.Helper()
	if w.Code != status {
		e.t.Fatalf("status %d, want %d: %s", w.Code, status, w.Body.String())
	}
	if refusal == "" {
		return
	}
	var eb errorBody
	if err := json.Unmarshal(w.Body.Bytes(), &eb); err != nil || eb.Refusal != refusal || eb.Error == "" {
		e.t.Fatalf("answer %s, want refusal %q", w.Body.String(), refusal)
	}
}

// info decodes a SandboxInfo answer.
func (e *testEnv) info(w *httptest.ResponseRecorder) Info {
	e.t.Helper()
	var in Info
	if err := json.Unmarshal(w.Body.Bytes(), &in); err != nil {
		e.t.Fatalf("%v: %s", err, w.Body.String())
	}
	return in
}

// create defines a sandbox as the manager, expecting 201.
func (e *testEnv) create(body map[string]any) Info {
	e.t.Helper()
	w := e.do(mgr, "POST", "/sandboxes", body)
	e.want(w, http.StatusCreated, "")
	return e.info(w)
}

func ns(name string) map[string]any { return map[string]any{"name": name, "mode": "namespace"} }

func contains(s, sub string) bool { return strings.Contains(s, sub) }
