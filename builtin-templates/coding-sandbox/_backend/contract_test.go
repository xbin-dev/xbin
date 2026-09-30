package main

// contract_test.go — this manager, over the fake backend, against the
// sandbox-manager contract's conformance suite (sdk/sandboxcontract,
// docs/sandbox-manager.md).

import (
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/sdk/sandboxcontract"
)

// testManager is a manager over a fake backend, served for a test.
type testManager struct {
	m   *Manager
	fb  *fakeBackend
	st  *store
	srv *httptest.Server
	tg  sandboxcontract.Target
}

// newTestManager serves a manager over a fresh fake backend (its store in
// db, a fresh one when empty); tweak sets the backend's knobs.
func newTestManager(t *testing.T, db string, tweak ...func(*fakeBackend)) *testManager {
	t.Helper()
	fb := &fakeBackend{Root: t.TempDir(), Grace: 200 * time.Millisecond}
	for _, f := range tweak {
		f(fb)
	}
	return serveManager(t, db, fb)
}

// serveManager serves a manager over fb (its store in db, a fresh one when
// empty).
func serveManager(t *testing.T, db string, fb *fakeBackend) *testManager {
	t.Helper()
	if db == "" {
		db = filepath.Join(t.TempDir(), "db.sqlite")
	}
	st, err := openStore(db)
	if err != nil {
		t.Fatal(err)
	}
	if cfg, err := st.config(); err == nil && cfg.Layout.UID == 1000 {
		// a user no host runs the tests as: the fake places the layout (its
		// own user), and the manager must take what it placed
		cfg.Layout.UID, cfg.Layout.GID = 4242, 4242
		if err := st.putConfig(cfg); err != nil {
			t.Fatal(err)
		}
	}
	m, err := newManager(st, fb)
	if err != nil {
		t.Fatal(err)
	}
	m.Logf = func(string, ...any) {}
	m.resume()
	srv := httptest.NewServer(m.contractHandler())
	tm := &testManager{m: m, fb: fb, st: st, srv: srv}
	tm.tg = sandboxcontract.Target{URL: srv.URL, Grace: fb.grace(), Fresh: freshTarget}
	t.Cleanup(func() { srv.Close(); m.Close(); fb.Close(); _ = st.close() }) // before TempDir's own cleanup
	return tm
}

// freshTarget is a manager with the knobs a check asks for (Target.Fresh).
func freshTarget(t *testing.T, k sandboxcontract.Knobs) sandboxcontract.Target {
	t.Helper()
	return newTestManager(t, "", func(f *fakeBackend) { f.Ring, f.FileMax, f.Caps = k.OutputRing, k.FileMax, k.Caps }).tg
}

func TestContract(t *testing.T) {
	tg := freshTarget(t, sandboxcontract.Knobs{})
	tg.Strict = true // a reference manager: what the suite only warns about fails here
	tg.Caps = []string{"exec", "files", "tar", "stdio", "snapshots", "clone"}
	if fkHasPTY() {
		tg.Caps = append(tg.Caps, "tty")
	}
	sandboxcontract.Run(t, tg)
}
