package main

// callers_test.go — the fixture of the per-caller counters' and the
// fairness limit's tests (callers_persist_test.go, callers_view_test.go,
// fairness_test.go): the real handlers against a fake xbind (kv and vault,
// on the gateway socket the SDK dials) and a fake upstream.

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// fakeXbind is the part of xbind this backend calls: kv and the vault.
type fakeXbind struct {
	mu sync.Mutex
	kv map[string][]byte // by key (the resource is "" in tests)
}

var (
	fx     = &fakeXbind{kv: map[string][]byte{}}
	fakeUp *httptest.Server
	// hold makes the upstream wait (model "slow") until it is closed;
	// arrived tells a test a slow call reached it.
	holdMu  sync.Mutex
	hold    = make(chan struct{})
	arrived = make(chan string, 16)
	seenMu  sync.Mutex
	seen    []string // the models the upstream answered
)

func TestMain(m *testing.M) {
	// A unix socket's path must fit in sun_path (~108 bytes): a long TMPDIR
	// wouldn't, so /tmp first.
	dir, err := os.MkdirTemp("/tmp", "lg")
	if err != nil {
		dir, err = os.MkdirTemp("", "lg")
	}
	if err != nil {
		panic(err)
	}
	sock := filepath.Join(dir, "gw.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		panic(err)
	}
	go http.Serve(ln, fx) //nolint:errcheck
	os.Setenv("XBIN_GATEWAY", sock)
	fakeUp = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Model string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Model == "slow" {
			holdMu.Lock()
			h := hold
			holdMu.Unlock()
			arrived <- body.Model
			<-h
		}
		seenMu.Lock()
		seen = append(seen, body.Model)
		seenMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[],"usage":{"prompt_tokens":12,"completion_tokens":34}}`)
	}))
	code := m.Run()
	fakeUp.Close()
	ln.Close()
	os.RemoveAll(dir)
	os.Exit(code)
}

func (f *fakeXbind) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case strings.HasPrefix(r.URL.Path, "/api/xbin/kv/"):
		key := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		switch r.Method {
		case "GET":
			b, ok := f.kv[key]
			if !ok {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write(b)
		case "PUT":
			b, _ := io.ReadAll(r.Body)
			f.kv[key] = b
		}
	case strings.HasSuffix(r.URL.Path, "/api-token-fake"):
		_, _ = io.WriteString(w, `{"value":"sk-fake"}`)
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeXbind) get(key string) []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.kv[key]
}

// fresh resets the fake xbind (config: one backend, "fake", at the fake
// upstream; limit the fairness limit) and the counters, as at a start.
func fresh(t *testing.T, limit int) {
	t.Helper()
	persistCallers() // a write still due from the test before goes to its kv
	cfg, _ := json.Marshal(gwConfig{Backends: map[string]backend{"fake": {BaseURL: fakeUp.URL}}, PartitionLimit: limit})
	statsMu.Lock()
	statsBy, active = nil, map[string]int{}
	statsMu.Unlock()
	fx.mu.Lock()
	fx.kv = map[string][]byte{"config": cfg}
	fx.mu.Unlock()
	callersMu.Lock()
	callerRows, gates, limitNow = map[callerKey]*callerRow{}, map[callerKey]*gate{}, 0
	callersLoaded, callersDirty, loadRetryAt = false, false, 0
	callersMu.Unlock()
	selfPath = "apps/llm-gw"
	holdMu.Lock()
	hold = make(chan struct{})
	holdMu.Unlock()
	seenMu.Lock()
	seen = nil
	seenMu.Unlock()
}

// hdrs are the X-XBin-* headers xbind sets on a call.
type hdrs map[string]string

var (
	chat      = hdrs{"X-XBin-From": "apps/chat", "X-XBin-Role": "writer"} // a tile that isn't partitioned
	global    = hdrs{"X-XBin-From": "apps/agent", "X-XBin-Role": "writer", "X-XBin-Partition": "global"}
	alice     = hdrs{"X-XBin-From": "apps/agent", "X-XBin-Role": "writer", "X-XBin-Partition": "user:alice", "X-XBin-Partition-Id": "u-a"}
	bob       = hdrs{"X-XBin-From": "apps/agent", "X-XBin-Role": "writer", "X-XBin-Partition": "user:bob", "X-XBin-Partition-Id": "u-b"}
	globalDev = hdrs{"X-XBin-From": "apps/agent", "X-XBin-Deployment": "dev", "X-XBin-Role": "writer", "X-XBin-Partition": "global"} // a non-primary deployment runs as global
	aliceZ    = hdrs{"X-XBin-From": "apps/z", "X-XBin-Role": "writer", "X-XBin-Partition": "user:alice", "X-XBin-Partition-Id": "u-za"}
)

func request(ctx context.Context, method, path, body string, h hdrs) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(ctx)
	r.Header.Set("Content-Type", "application/json")
	for k, v := range h {
		r.Header.Set(k, v)
	}
	return r
}

// proxy is one chat call through the gateway as h.
func proxy(ctx context.Context, h hdrs, model string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	handleProxy(w, request(ctx, "POST", "/v1/chat/completions", `{"model":"`+model+`","messages":[]}`, h))
	return w
}

// statsAs is GET /stats as the tile's page (or the owner, with no person).
func statsAs(t *testing.T, h hdrs) (map[string]json.RawMessage, []callerStat) {
	t.Helper()
	w := httptest.NewRecorder()
	handleStats(w, request(context.Background(), "GET", "/stats", "", h))
	var out map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("GET /stats: %d %s", w.Code, w.Body)
	}
	var rows []callerStat
	if c, ok := out["callers"]; ok {
		if err := json.Unmarshal(c, &rows); err != nil {
			t.Fatal(err)
		}
	}
	return out, rows
}

var owner = hdrs{"X-XBin-From": "owner", "X-XBin-Role": "admin"}

func rowOf(rows []callerStat, from, dep, pid string) *callerStat {
	for i := range rows {
		if r := rows[i]; r.From == from && r.Deployment == dep && r.PartitionID == pid {
			return &rows[i]
		}
	}
	return nil
}
