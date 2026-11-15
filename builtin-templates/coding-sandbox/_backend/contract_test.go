package main

// contract_test.go — this manager, over the fake backend, against the
// sandbox-manager contract's conformance suite (sdk/sandboxcontract,
// docs/sandbox-manager.md).

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"slices"
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
	return newTestManager(t, "", func(f *fakeBackend) {
		f.Ring, f.FileMax, f.Caps = k.OutputRing, k.FileMax, k.Caps
		if k.Caps == nil { // every capability: the substrate forwards the LLM connector too
			f.LLM = fakeLLMURL
		}
	}).tg
}

// fakeLLMURL is where the fake substrate says a sandbox created with
// net.llm reaches the LLM connector (xbind's runtime: its gateway).
const fakeLLMURL = "http://10.0.2.2:4180/v1"

func TestContract(t *testing.T) {
	tg := freshTarget(t, sandboxcontract.Knobs{})
	tg.Strict = true // a reference manager: what the suite only warns about fails here
	tg.Caps = []string{"exec", "files", "tar", "stdio", "snapshots", "clone", "partitions", "llm"}
	if fkHasPTY() {
		tg.Caps = append(tg.Caps, "tty")
	}
	sandboxcontract.Run(t, tg)
}

// TestContractNoLLM is the suite's llm section over a substrate that
// forwards no LLM connector (an xbind from before it): the manager offers
// no llm, and a sandbox asked for with one claims none.
func TestContractNoLLM(t *testing.T) {
	tg := newTestManager(t, "").tg
	tg.Fresh = nil
	tg.Skip = map[string]string{}
	for _, s := range []string{"hello", "sandboxes", "partitions", "user-partitions", "people", "lifecycle", "run", "execs", "tty", "stdio", "files", "tar", "snapshots", "ports", "caps"} {
		tg.Skip[s] = "TestContract runs it"
	}
	sandboxcontract.Run(t, tg)
}

// The llm capability through this manager: asked for and forwarded, the
// row names the address and the substrate's definition carries net.llm;
// not asked for, neither; a clone asks for itself.
func TestLLMCapability(t *testing.T) {
	tm := newTestManager(t, "", func(f *fakeBackend) { f.LLM = fakeLLMURL })
	a := tm.tg.As(t, "apps/agent")
	var h sandboxcontract.Hello
	a.Call("GET", "/hello?protocol=1", nil, 200, &h)
	if !slices.Contains(h.Caps, "llm") {
		t.Fatalf("hello's caps %v lack llm though the substrate forwards the connector", h.Caps)
	}
	sb := a.Create(map[string]any{"name": "coder", "llm": true})
	if sb.LLM == nil || sb.LLM.URL != fakeLLMURL {
		t.Fatalf("llm: %+v", sb.LLM)
	}
	recs, err := tm.st.records()
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(recs, func(r *record) bool { return r.ID == sb.ID })
	if i < 0 || !recs[i].LLM {
		t.Fatalf("the record of %s: %+v", sb.ID, recs)
	}
	info, err := tm.fb.Get(context.Background(), recs[i].Runtime)
	if err != nil || info.Net.LLM != fakeLLMURL {
		t.Fatalf("the substrate's definition: %+v %v", info, err)
	}
	if plain := a.Create(map[string]any{"name": "plain"}); plain.LLM != nil {
		t.Fatalf("a sandbox that didn't ask: %+v", plain.LLM)
	}
}
