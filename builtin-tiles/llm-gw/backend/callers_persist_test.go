package main

// callers_persist_test.go — the per-caller counters: counted per caller,
// persisted by the writer after the call (coalesced), read back at a
// start, kept through kv trouble, and a recreated person's row starting
// afresh (callers.go).

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// flush writes what the writer has due, as it would.
func flush(t *testing.T) {
	t.Helper()
	if !persistCallers() {
		t.Fatal("the counters couldn't be written")
	}
}

// restart forgets the rows in memory, as a new process would.
func restart() {
	callersMu.Lock()
	callerRows, callersLoaded, callersDirty, loadRetryAt = map[callerKey]*callerRow{}, false, false, 0
	callersMu.Unlock()
}

// setKV swaps the kv the rows live in (under the locks the writer and the
// loader read them under); the returned func puts the real ones back.
func setKV(load func() ([]byte, error), store func([]byte) error) func() {
	persistMu.Lock()
	callersMu.Lock()
	oldLoad, oldStore := loadCallersKV, storeCallersKV
	if load != nil {
		loadCallersKV = load
	}
	if store != nil {
		storeCallersKV = store
	}
	callersMu.Unlock()
	persistMu.Unlock()
	return func() {
		persistMu.Lock()
		callersMu.Lock()
		loadCallersKV, storeCallersKV = oldLoad, oldStore
		callersMu.Unlock()
		persistMu.Unlock()
	}
}

func TestCallerCounters(t *testing.T) {
	fresh(t, 0)
	ctx := context.Background()
	// a tile that isn't partitioned: per backend only, as before
	if w := proxy(ctx, chat, "m"); w.Code != 200 {
		t.Fatalf("apps/chat's call: %d %s", w.Code, w.Body)
	}
	flush(t)
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
	// the owner token: every row; a person's with its counters only
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
			if r == nil || r.Partition != c.part || r.Reqs != c.reqs || r.TokIn != 12*c.reqs || r.TokOut != 34*c.reqs || r.Active != 0 ||
				(r.Last == 0) != (c.pid != "") {
				t.Errorf("row %s#%s %s: %+v, want %d calls of %s (a last use only on a global instance's)", c.from, c.dep, c.pid, r, c.reqs, c.part)
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
	// persisted by the writer on its own: a restart reads them back
	waitFor(t, "the rows persisted", func() bool {
		var persisted []callerRow
		return json.Unmarshal(fx.get("callers"), &persisted) == nil && len(persisted) == 5
	})
	flush(t)
	var persisted []callerRow
	_ = json.Unmarshal(fx.get("callers"), &persisted)
	for _, r := range persisted {
		if r.Last == 0 {
			t.Errorf("a persisted row without its last use: %+v", r)
		}
	}
	restart()
	_, rows = statsAs(t, owner)
	check(rows)
}

// A person deleted and recreated under the same id (or whose partition was
// reset) comes with a new partition id: their row starts afresh, and the
// old one is dropped — never shown to the new person, never kept.
func TestCallersRecreatedPerson(t *testing.T) {
	fresh(t, 0)
	ctx := context.Background()
	proxy(ctx, alice, "m")
	proxy(ctx, alice, "m")
	flush(t)
	alice2 := hdrs{"X-XBin-From": "apps/agent", "X-XBin-Role": "writer", "X-XBin-Partition": "user:alice", "X-XBin-Partition-Id": "u-a2"}
	proxy(ctx, alice2, "m")
	_, rows := statsAs(t, hdrs{"X-XBin-From": "apps/llm-gw", "X-XBin-User": "alice", "X-XBin-User-Level": "read"})
	if len(rows) != 1 || rows[0].PartitionID != "u-a2" || rows[0].Reqs != 1 {
		t.Errorf("the new alice sees %+v, want her own row of one call", rows)
	}
	flush(t)
	if b := fx.get("callers"); strings.Contains(string(b), `"u-a"`) {
		t.Errorf("the old alice's row is kept: %s", b)
	}
	// the same when the old row is only in kv: the new alice's first call
	// ended while the persisted rows couldn't be read — also in the same
	// millisecond as the old alice's last (the clock pinned)
	fresh(t, 0)
	setNow := func(f func() int64) { callersMu.Lock(); nowMs = f; callersMu.Unlock() } // its readers hold callersMu
	realNow := nowMs
	t.Cleanup(func() { setNow(realNow) })
	pinned := realNow()
	setNow(func() int64 { return pinned })
	proxy(ctx, alice, "m")
	flush(t)
	restart()
	undo := setKV(func() ([]byte, error) { return nil, fmt.Errorf("kv get: 503") }, nil)
	proxy(ctx, alice2, "m")
	undo()
	callersMu.Lock()
	loadRetryAt = 0
	callersMu.Unlock()
	_, rows = statsAs(t, owner)
	if len(rows) != 1 || rows[0].PartitionID != "u-a2" {
		t.Errorf("after the load: %+v, want the new alice's row only", rows)
	}
}

// A kv error other than a missing key never starts the table over: counting
// goes on in memory, nothing is written until the persisted rows are read,
// and then they add up.
func TestCallersKVTrouble(t *testing.T) {
	fresh(t, 0)
	ctx := context.Background()
	proxy(ctx, alice, "m")
	proxy(ctx, bob, "m")
	flush(t)
	before := string(fx.get("callers"))
	restart()
	var mu sync.Mutex
	down := true
	undo := setKV(func() ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		if down {
			return nil, fmt.Errorf("kv get: 503 Service Unavailable")
		}
		return fx.get("callers"), nil
	}, nil)
	defer undo()
	proxy(ctx, alice, "m")
	if persistCallers() {
		t.Error("the rows were written although the persisted ones couldn't be read")
	}
	if got := string(fx.get("callers")); got != before {
		t.Errorf("kv changed while unreadable: %s, was %s", got, before)
	}
	mu.Lock()
	down = false
	mu.Unlock()
	callersMu.Lock()
	loadRetryAt = 0
	callersMu.Unlock()
	flush(t)
	restart()
	_, rows := statsAs(t, owner)
	if a, b := rowOf(rows, "apps/agent", "", "u-a"), rowOf(rows, "apps/agent", "", "u-b"); a == nil || a.Reqs != 2 || b == nil || b.Reqs != 1 {
		t.Errorf("after kv came back: alice %+v, bob %+v; want 2 and 1 calls", a, b)
	}
}

// A call never waits on kv: the writer persists after it, coalescing.
func TestCallersPersistAfterTheCall(t *testing.T) {
	fresh(t, 0)
	ctx := context.Background()
	proxy(ctx, alice, "m")
	flush(t)
	stuck := make(chan struct{})
	var mu sync.Mutex
	writes := 0
	undo := setKV(nil, func(b []byte) error {
		<-stuck
		mu.Lock()
		writes++
		mu.Unlock()
		fx.mu.Lock()
		fx.kv["callers"] = b
		fx.mu.Unlock()
		return nil
	})
	defer undo()
	start := time.Now()
	for i := 0; i < 20; i++ {
		if w := proxy(ctx, alice, "m"); w.Code != 200 {
			t.Fatalf("call %d: %d", i, w.Code)
		}
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("20 calls took %v while kv was stuck", d)
	}
	close(stuck)
	flush(t) // after the writer's write in progress: the latest snapshot
	mu.Lock()
	n := writes
	mu.Unlock()
	if n > 3 {
		t.Errorf("%d writes for 20 calls, want them coalesced", n)
	}
	var persisted []callerRow
	_ = json.Unmarshal(fx.get("callers"), &persisted)
	if len(persisted) != 1 || persisted[0].Reqs != 21 {
		t.Errorf("persisted: %+v, want alice's 21 calls", persisted)
	}
}
