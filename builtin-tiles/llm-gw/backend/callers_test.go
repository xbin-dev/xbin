package main

// callers_test.go — the per-caller counters of partitioned tiles and the
// fairness limit (callers.go), through the real handlers against a fake
// xbind (kv and vault, on the gateway socket the SDK dials) and a fake
// upstream.

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
	"time"
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
	dir, err := os.MkdirTemp("", "llmgw")
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
	cfg, _ := json.Marshal(gwConfig{Backends: map[string]backend{"fake": {BaseURL: fakeUp.URL}}, PartitionLimit: limit})
	fx.mu.Lock()
	fx.kv = map[string][]byte{"config": cfg}
	fx.mu.Unlock()
	statsMu.Lock()
	statsBy, active = nil, map[string]int{}
	statsMu.Unlock()
	callersMu.Lock()
	callerRows, gates, limitNow = nil, map[callerKey]*gate{}, 0
	callersMu.Unlock()
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

func TestCallerCounters(t *testing.T) {
	fresh(t, 0)
	ctx := context.Background()
	// a tile that isn't partitioned: per backend only, as before
	if w := proxy(ctx, chat, "m"); w.Code != 200 {
		t.Fatalf("apps/chat's call: %d %s", w.Code, w.Body)
	}
	out, _ := statsAs(t, owner)
	if _, has := out["callers"]; has || len(out) != 1 {
		t.Errorf("GET /stats with no partitioned caller: %v, want only backends", out)
	}
	if b := fx.get("callers"); b != nil {
		t.Errorf("a call from a tile that isn't partitioned wrote callers: %s", b)
	}
	for _, h := range []hdrs{alice, alice, bob, global, globalDev, aliceZ} {
		if w := proxy(ctx, h, "m"); w.Code != 200 {
			t.Fatalf("%v: %d %s", h, w.Code, w.Body)
		}
	}
	check := func(rows []callerStat) {
		t.Helper()
		if len(rows) != 5 {
			t.Errorf("rows: %+v, want 5", rows)
		}
		for _, c := range []struct {
			from, dep, pid, part string
			reqs                 int64
		}{
			{"apps/agent", "", "u-a", "user:alice", 2},
			{"apps/agent", "", "u-b", "user:bob", 1},
			{"apps/agent", "", "", "global", 1},
			{"apps/agent", "dev", "", "global", 1},
			{"apps/z", "", "u-za", "user:alice", 1},
		} {
			r := rowOf(rows, c.from, c.dep, c.pid)
			if r == nil || r.Partition != c.part || r.Reqs != c.reqs || r.TokIn != 12*c.reqs || r.TokOut != 34*c.reqs || r.Last == 0 || r.Active != 0 {
				t.Errorf("row %s#%s %s: %+v, want %d calls of %s", c.from, c.dep, c.pid, r, c.reqs, c.part)
			}
		}
	}
	out, rows := statsAs(t, owner)
	check(rows)
	var be map[string]stats
	_ = json.Unmarshal(out["backends"], &be)
	if be["fake"].Reqs != 7 {
		t.Errorf("the backend's counters: %+v, want 7 calls", be)
	}
	// persisted: a restart reads them back
	var persisted []callerRow
	if err := json.Unmarshal(fx.get("callers"), &persisted); err != nil || len(persisted) != 5 {
		t.Fatalf("kv callers: %v %s", err, fx.get("callers"))
	}
	callersMu.Lock()
	callerRows = nil
	callersMu.Unlock()
	_, rows = statsAs(t, owner)
	check(rows)
}

func TestCallersVisibility(t *testing.T) {
	fresh(t, 0)
	for _, h := range []hdrs{alice, bob, global} {
		proxy(context.Background(), h, "m")
	}
	page := func(user, level string, extra ...string) hdrs {
		h := hdrs{"X-XBin-From": "apps/llm-gw", "X-XBin-Role": "admin", "X-XBin-User": user, "X-XBin-User-Level": level}
		for i := 0; i+1 < len(extra); i += 2 {
			h[extra[i]] = extra[i+1]
		}
		return h
	}
	for _, c := range []struct {
		name string
		h    hdrs
		want []string // partitions seen
	}{
		{"the owner token", owner, []string{"global", "user:alice", "user:bob"}},
		{"a manager of the tile (write)", page("carol", "write"), []string{"global", "user:alice", "user:bob"}},
		{"a manager of the tile (terminal)", page("carol", "terminal"), []string{"global", "user:alice", "user:bob"}},
		{"alice, who reads the tile", page("alice", "read"), []string{"user:alice"}},
		{"dave, who reads the tile and has no partition", page("dave", "read"), nil},
		{"an admin viewing as alice", page("alice", "read", "X-XBin-Viewed-By", "erin"), nil},
		{"an admin viewing as a manager", page("carol", "write", "X-XBin-Viewed-By", "erin"), nil},
	} {
		out, rows := statsAs(t, c.h)
		var got []string
		for _, r := range rows {
			got = append(got, r.Partition)
		}
		if strings.Join(got, ",") != strings.Join(c.want, ",") {
			t.Errorf("%s sees %v, want %v", c.name, got, c.want)
		}
		if _, has := out["callers"]; has != (len(c.want) > 0) {
			t.Errorf("%s: callers key present %v with %d rows", c.name, has, len(c.want))
		}
	}
}

// waitFor polls cond for up to 5 s.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); !cond(); {
		if time.Now().After(end) {
			t.Fatalf("timed out: %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// inFlight is (active, waiting) of a caller.
func inFlight(k callerKey) (int, int) {
	callersMu.Lock()
	defer callersMu.Unlock()
	if g := gates[k]; g != nil {
		return g.n, len(g.waiters)
	}
	return 0, 0
}

func TestPartitionLimit(t *testing.T) {
	fresh(t, 1)
	ctx := context.Background()
	ak := callerKey{"apps/agent", "", "u-a"}
	done := make(chan *httptest.ResponseRecorder, 4)
	go func() { done <- proxy(ctx, alice, "slow") }()
	<-arrived // alice's first call is at the upstream, holding her one slot
	go func() { done <- proxy(ctx, alice, "second") }()
	waitFor(t, "alice's second call waits", func() bool { a, w := inFlight(ak); return a == 1 && w == 1 })
	_, rows := statsAs(t, owner)
	if r := rowOf(rows, "apps/agent", "", "u-a"); r == nil || r.Active != 1 || r.Waiting != 1 {
		t.Errorf("GET /stats while alice waits (no call of hers ended yet): %+v", r)
	}
	// others are never held: bob's partition, the global instance, a tile
	// that isn't partitioned, another tile's partition of alice
	for _, h := range []hdrs{bob, global, chat, aliceZ} {
		if w := proxy(ctx, h, "m"); w.Code != 200 {
			t.Errorf("%v while alice waits: %d", h, w.Code)
		}
	}
	// a waiting call whose caller goes away leaves, never reaching upstream
	cctx, cancel := context.WithCancel(ctx)
	go func() { done <- proxy(cctx, alice, "gone") }()
	waitFor(t, "alice's third call waits", func() bool { _, w := inFlight(ak); return w == 2 })
	cancel()
	if w := <-done; w.Body.Len() != 0 {
		t.Errorf("the call that went away was answered: %d %s", w.Code, w.Body)
	}
	if a, w := inFlight(ak); a != 1 || w != 1 {
		t.Errorf("after the third went away: active %d, waiting %d", a, w)
	}
	// raising the limit lets the waiting call through at once
	pw := httptest.NewRecorder()
	handlePutConfig(pw, request(ctx, "PUT", "/config", `{"partitionLimit": 2}`, hdrs{"X-XBin-From": "apps/llm-gw", "X-XBin-User": "carol", "X-XBin-User-Level": "write"}))
	if pw.Code != 200 || !strings.Contains(pw.Body.String(), `"partitionLimit":2`) {
		t.Fatalf("PUT /config partitionLimit 2: %d %s", pw.Code, pw.Body)
	}
	if w := <-done; w.Code != 200 {
		t.Errorf("alice's second call: %d %s", w.Code, w.Body)
	}
	holdMu.Lock()
	close(hold)
	holdMu.Unlock()
	if w := <-done; w.Code != 200 {
		t.Errorf("alice's slow call: %d %s", w.Code, w.Body)
	}
	seenMu.Lock()
	models := strings.Join(seen, ",")
	seenMu.Unlock()
	if strings.Contains(models, "gone") {
		t.Errorf("the call that went away reached the upstream: %s", models)
	}
	waitFor(t, "every slot free", func() bool {
		callersMu.Lock()
		defer callersMu.Unlock()
		return len(gates) == 0
	})
	_, rows = statsAs(t, owner)
	if r := rowOf(rows, "apps/agent", "", "u-a"); r == nil || r.Reqs != 2 || r.Active != 0 || r.Waiting != 0 {
		t.Errorf("alice's row at the end: %+v, want 2 calls", r)
	}
}

// The limit holds each person's partition to its own slots, in order.
func TestPartitionLimitOrder(t *testing.T) {
	fresh(t, 1)
	ctx := context.Background()
	ak := callerKey{"apps/agent", "", "u-a"}
	first := make(chan *httptest.ResponseRecorder, 1)
	go func() { first <- proxy(ctx, alice, "slow") }()
	<-arrived
	var order []string
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i, m := range []string{"a1", "a2", "a3"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			proxy(ctx, alice, m)
			mu.Lock()
			order = append(order, m)
			mu.Unlock()
		}()
		waitFor(t, m+" waits", func() bool { _, w := inFlight(ak); return w == i+1 })
	}
	holdMu.Lock()
	close(hold)
	holdMu.Unlock()
	<-first
	wg.Wait()
	if got := strings.Join(order, ","); got != "a1,a2,a3" {
		t.Errorf("the waiting calls ran as %s, want in order", got)
	}
}

func TestPutPartitionLimit(t *testing.T) {
	fresh(t, 0)
	put := func(level, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		handlePutConfig(w, request(context.Background(), "PUT", "/config", body, hdrs{"X-XBin-From": "apps/llm-gw", "X-XBin-User": "p", "X-XBin-User-Level": level}))
		return w
	}
	getCfg := func() map[string]any {
		w := httptest.NewRecorder()
		handleGetConfig(w, request(context.Background(), "GET", "/config", "", owner))
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return out
	}
	if _, has := getCfg()["partitionLimit"]; has {
		t.Errorf("GET /config names a limit that was never set")
	}
	if w := put("read", `{"partitionLimit": 2}`); w.Code != 403 {
		t.Errorf("a reader sets the limit: %d %s", w.Code, w.Body)
	}
	for _, bad := range []string{`-1`, `65`, `1.5`, `"2"`} {
		if w := put("write", `{"partitionLimit": `+bad+`}`); w.Code != 400 {
			t.Errorf("limit %s: %d %s", bad, w.Code, w.Body)
		}
	}
	if w := put("read", `{"aliases": {"best": "fake/x"}}`); w.Code != 200 || strings.Contains(w.Body.String(), "partitionLimit") {
		t.Errorf("aliases alone, as before: %d %s", w.Code, w.Body)
	}
	if w := put("write", `{"partitionLimit": 3}`); w.Code != 200 {
		t.Fatalf("a manager sets the limit: %d %s", w.Code, w.Body)
	}
	if got := getCfg(); got["partitionLimit"] != float64(3) || got["aliases"].(map[string]any)["best"] != "fake/x" {
		t.Errorf("GET /config after: %v", got)
	}
	if w := put("write", `{"partitionLimit": 0}`); w.Code != 200 {
		t.Fatalf("turning it off: %d %s", w.Code, w.Body)
	}
	if _, has := getCfg()["partitionLimit"]; has || strings.Contains(string(fx.get("config")), "partitionLimit") {
		t.Errorf("a limit turned off stays in the config: %s", fx.get("config"))
	}
}
