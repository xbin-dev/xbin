package main

// fairness_test.go — the optional fairness limit (fairness.go): one
// person's partition's calls in flight, the rest waiting in order for at
// most limitWait, then 429 with a Retry-After; others never held.

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

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

var alicePage = hdrs{"X-XBin-From": "apps/llm-gw", "X-XBin-User": "alice", "X-XBin-User-Level": "read"}

func TestPartitionLimit(t *testing.T) {
	fresh(t, 1)
	ctx := context.Background()
	ak := callerKey{"apps/agent", "", "u-a"}
	done := make(chan *httptest.ResponseRecorder, 4)
	go func() { done <- proxy(ctx, alice, "slow") }()
	<-arrived // alice's first call is at the upstream, holding her one slot
	go func() { done <- proxy(ctx, alice, "second") }()
	waitFor(t, "alice's second call waits", func() bool { a, w := inFlight(ak); return a == 1 && w == 1 })
	_, rows := statsAs(t, alicePage)
	if r := rowOf(rows, "apps/agent", "", "u-a"); r == nil || r.Active != 1 || r.Waiting != 1 {
		t.Errorf("alice's own row while she waits (no call of hers ended yet): %+v", r)
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
	_, rows = statsAs(t, alicePage)
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

// A call that finds no slot within limitWait is answered 429 with a
// Retry-After — not held until the caller's own watchdog gives up on it.
func TestPartitionLimitBusy(t *testing.T) {
	fresh(t, 1)
	old := limitWait
	limitWait = 150 * time.Millisecond
	t.Cleanup(func() { limitWait = old })
	ctx := context.Background()
	ak := callerKey{"apps/agent", "", "u-a"}
	done := make(chan struct{})
	go func() { proxy(ctx, alice, "slow"); close(done) }()
	<-arrived
	start := time.Now()
	w := proxy(ctx, alice, "second")
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") == "" || !strings.Contains(w.Body.String(), "fairness limit") {
		t.Errorf("a call past the wait: %d %v %s, want 429 with a Retry-After", w.Code, w.Header(), w.Body)
	}
	if d := time.Since(start); d < limitWait || d > limitWait+2*time.Second {
		t.Errorf("answered after %v, want after the %v wait", d, limitWait)
	}
	if a, wt := inFlight(ak); a != 1 || wt != 0 {
		t.Errorf("after the 429: active %d, waiting %d; want alice's one call in flight", a, wt)
	}
	holdMu.Lock()
	close(hold)
	holdMu.Unlock()
	<-done
	_, rows := statsAs(t, alicePage)
	if r := rowOf(rows, "apps/agent", "", "u-a"); r == nil || r.Reqs != 1 {
		t.Errorf("alice's row: %+v, want the one call that ran (a 429 isn't a call)", r)
	}
	seenMu.Lock()
	defer seenMu.Unlock()
	if strings.Contains(strings.Join(seen, ","), "second") {
		t.Errorf("the refused call reached the upstream: %v", seen)
	}
}

// The agent's model client (builtin-templates/agent/_backend/llm_gateway.go)
// arms a 90 s stream watchdog before it has the response headers, and
// retries a 429. A held call must be answered well before that watchdog:
// through a real HTTP server, a client with a watchdog like the agent's
// (scaled down, the ratio kept) gets the 429, never its own timeout.
func TestPartitionLimitUnderTheAgentsWatchdog(t *testing.T) {
	if limitWait*3 > 90*time.Second {
		t.Fatalf("limitWait %v is too close to the agent's 90 s stream watchdog", limitWait)
	}
	fresh(t, 1)
	old := limitWait
	limitWait = 100 * time.Millisecond // 20 s : 90 s, scaled
	t.Cleanup(func() { limitWait = old })
	watchdog := 450 * time.Millisecond
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for k, v := range alice {
			r.Header.Set(k, v)
		}
		handleProxy(w, r)
	}))
	defer srv.Close()
	call := func(model string) (int, error) {
		ctx, cancel := context.WithTimeout(context.Background(), watchdog)
		defer cancel()
		req, _ := http.NewRequestWithContext(ctx, "POST", srv.URL+"/v1/chat/completions", strings.NewReader(`{"model":"`+model+`","stream":true}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "text/event-stream")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return 0, err
		}
		defer resp.Body.Close()
		_, _ = bufio.NewReader(resp.Body).ReadString('\n')
		return resp.StatusCode, nil
	}
	first := make(chan struct{})
	go func() { _, _ = call("slow"); close(first) }() // longer than the watchdog: its own watchdog ends it
	<-arrived
	code, err := call("second")
	if err != nil || code != http.StatusTooManyRequests {
		t.Errorf("the held call: %d %v, want a 429 before the watchdog", code, err)
	}
	holdMu.Lock()
	close(hold)
	holdMu.Unlock()
	<-first
	waitFor(t, "every slot free", func() bool {
		callersMu.Lock()
		defer callersMu.Unlock()
		return len(gates) == 0
	})
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
	// with a limit set, a manager's page offers its form even before any
	// partitioned tile called; a reader's doesn't
	if out, _ := statsAs(t, hdrs{"X-XBin-From": "apps/llm-gw", "X-XBin-User": "p", "X-XBin-User-Level": "write"}); out["canManage"] == nil {
		t.Errorf("a manager's GET /stats with a limit set: %v", out)
	}
	if out, _ := statsAs(t, alicePage); len(out) != 1 {
		t.Errorf("a reader's GET /stats with a limit set and no row of theirs: %v", out)
	}
	if w := put("write", `{"partitionLimit": 0}`); w.Code != 200 {
		t.Fatalf("turning it off: %d %s", w.Code, w.Body)
	}
	if _, has := getCfg()["partitionLimit"]; has || strings.Contains(string(fx.get("config")), "partitionLimit") {
		t.Errorf("a limit turned off stays in the config: %s", fx.get("config"))
	}
}
