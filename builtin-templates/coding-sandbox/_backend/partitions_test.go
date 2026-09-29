package main

// partitions_test.go — a partitioned consumer's user partitions, beyond the
// contract's suite (sdk/sandboxcontract, section user-partitions): partition
// headers that don't agree are refused, what the runtime is told, quotas
// summed per consumer tile, what operators see (S19), a restart.

import (
	"context"
	"io"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/sdk/sandboxcontract"
)

func TestPartitionHeaders(t *testing.T) {
	t.Parallel()
	tm := newTestManager(t, "")
	for _, h := range []map[string]string{
		{"X-XBin-Partition": "user:alice"},                                                     // a user partition without its id
		{"X-XBin-Partition-Id": "u-1"},                                                         // an id without its partition
		{"X-XBin-Partition": "global", "X-XBin-Partition-Id": "u-1"},                           // global has none
		{"X-XBin-Partition": "org:x", "X-XBin-Partition-Id": "o-1"},                            // a kind this manager doesn't know
		{"X-XBin-Partition": "user:", "X-XBin-Partition-Id": "u-1"},                            // no person
		{"X-XBin-Partition": "user:alice", "X-XBin-Partition-Id": "u 1"},                       // not an id
		{"X-XBin-Partition": "user:alice", "X-XBin-Partition-Id": "u-1", "X-XBin-User": "bob"}, // another person's page
		{"X-XBin-Partition": "user:alice", "X-XBin-Partition-Id": "u-1", "Sbx-User": "bob"},    // acting for another
	} {
		for _, path := range []string{"/sbx/hello", "/sbx/sandboxes"} {
			req, _ := http.NewRequest("GET", tm.srv.URL+path, nil)
			req.Header.Set("X-XBin-From", "apps/ph-a")
			for k, v := range h {
				req.Header.Set(k, v)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != 403 || !strings.Contains(string(b), `"not-allowed"`) {
				t.Errorf("GET %s with %v: %d %s, want 403 not-allowed", path, h, resp.StatusCode, b)
			}
		}
	}
	// the ones that agree
	a := tm.tg.As(t, "apps/ph-a")
	a.InPartition("alice", "u-1").Verified("alice").Asserting("alice").Call("GET", "/sandboxes", nil, 200, nil)
	a.Global().Call("GET", "/sandboxes", nil, 200, nil)
}

func TestPartitionRuntimeAndQuotas(t *testing.T) {
	t.Parallel()
	tm := newTestManager(t, "")
	tm.setConfig(t, func(c *Config) { c.Quotas = Quotas{Consumer: Quota{Sandboxes: 4}, Person: Quota{Sandboxes: 1}} })
	a := tm.tg.As(t, "apps/pq-a")
	alice, bob := a.InPartition("alice", "u-alice"), a.InPartition("bob", "u-bob")
	if l := hello(t, alice)["limits"].(map[string]any); l["sandboxes"] != float64(1) {
		t.Fatalf("hello's effective limits for a partition's person: %v", l)
	}
	sa := alice.Create(map[string]any{"name": "a1", "start": false})
	sg := a.Create(map[string]any{"name": "g", "start": false})
	// the runtime is told the consumer tile and the person, and the partition as a label
	for _, c := range []struct {
		id, user, part string
	}{{sa.ID, "alice", "u-alice"}, {sg.ID, "", ""}} {
		rec := tm.m.recCopy(c.id)
		in, err := tm.fb.Get(context.Background(), rec.Runtime)
		if err != nil {
			t.Fatal(err)
		}
		if in.For != "apps/pq-a" || in.ForUser != c.user || in.Labels["coding-sandbox/id"] != c.id || in.Labels["coding-sandbox/partition"] != c.part {
			t.Fatalf("the runtime's sandbox of %s: for %q, forUser %q, labels %v", c.id, in.For, in.ForUser, in.Labels)
		}
		if _, ok := in.Labels["coding-sandbox/partition"]; !ok && c.part != "" || ok && c.part == "" {
			t.Fatalf("the partition label of %s: %v", c.id, in.Labels)
		}
	}
	// a person's quota: alice's partition is alice's
	alice.Refused("POST", "/sandboxes", map[string]any{"name": "a2", "start": false}, 429, "limit")
	// the consumer's quota counts every partition of it, and global
	bob.Create(map[string]any{"name": "b1", "start": false})
	a.Create(map[string]any{"name": "g2", "start": false})
	r := a.InPartition("carol", "u-carol").Refused("POST", "/sandboxes", map[string]any{"name": "c1", "start": false}, 429, "limit")
	if !strings.Contains(r.Error, "apps/pq-a") {
		t.Fatalf("the consumer's quota refusal: %+v", r)
	}
	// the operators' usage: by consumer tile and by person
	if cs, ps := tm.m.usageBy(nil); len(cs) != 1 || cs["apps/pq-a"].Sandboxes != 4 || ps["alice"].Sandboxes != 1 || ps["bob"].Sandboxes != 1 {
		t.Fatalf("usage by consumer tile and person: %+v %+v", cs, ps)
	}
}

// Operators see every sandbox's metadata, as ever — but a user partition's
// sandbox's name and labels are its consumer's content: they see
// <consumer>/<partition id, 8> #<n> and no labels, unless it is shared with
// them.
func TestPartitionOperators(t *testing.T) {
	t.Parallel()
	tm := newTestManager(t, "")
	srv := tileServer(t, tm) // the tile is apps/cs
	a := tm.tg.As(t, "apps/agent")
	alice := a.InPartition("alice", "u-0123456789abcdef")
	priv := alice.Create(map[string]any{"name": "secret plans", "labels": map[string]string{"xbin.agent/conversation": "7"}, "start": false})
	second := alice.Create(map[string]any{"name": "more secrets", "start": false})
	shared := alice.Create(map[string]any{"name": "shared with olga", "visibility": "team", "labels": map[string]string{"k": "v"}, "start": false})
	alice.Call("PATCH", "/sandboxes/"+shared.ID, map[string]any{"shares": []map[string]any{{"consumer": "apps/cs", "users": []string{"olga"}}}}, 200, nil)
	glob := a.Create(map[string]any{"name": "global's", "labels": map[string]string{"k": "g"}, "start": false})
	olga := as{from: "apps/cs", role: "admin", user: "olga", level: "write"}
	owner := as{from: "owner", role: "admin"}
	state := func(who as) map[string]opView {
		t.Helper()
		var st struct{ Sandboxes []opView }
		call(t, srv, who, "GET", "/ops/state", nil, 200, &st)
		out := map[string]opView{}
		for _, v := range st.Sandboxes {
			out[v.ID] = v
		}
		return out
	}
	names := func(vs map[string]opView, ids ...string) []string {
		var out []string
		for _, id := range ids {
			out = append(out, vs[id].Name)
		}
		slices.Sort(out)
		return out
	}
	byOwner := state(owner)
	if got := names(byOwner, priv.ID, second.ID, shared.ID); !slices.Equal(got, []string{"apps/agent/u-012345 #1", "apps/agent/u-012345 #2", "apps/agent/u-012345 #3"}) {
		t.Fatalf("the operators' names of a user partition's sandboxes: %v", got)
	}
	for _, id := range []string{priv.ID, second.ID, shared.ID} {
		v := byOwner[id]
		if len(v.Labels) != 0 || v.Owner.PartitionID != "u-0123456789abcdef" || v.Owner.User != "alice" || v.Consumer != "apps/agent" || v.Shared {
			t.Fatalf("the operators' view of a user partition's sandbox: %+v", v)
		}
	}
	if v := byOwner[glob.ID]; v.Name != "global's" || v.Labels["k"] != "g" {
		t.Fatalf("the operators' view of the consumer's global sandbox: %+v", v)
	}
	// shared with olga: she sees it as she would as a consumer
	byOlga := state(olga)
	if v := byOlga[shared.ID]; v.Name != "shared with olga" || v.Labels["k"] != "v" {
		t.Fatalf("olga's view of a sandbox shared with her: %+v", v)
	}
	if v := byOlga[priv.ID]; v.Name != byOwner[priv.ID].Name || len(v.Labels) != 0 {
		t.Fatalf("olga's view of one not shared with her: %+v", v)
	}
	// a single sandbox's answer says the same
	var pv opView
	call(t, srv, owner, "PATCH", "/ops/sandboxes/"+priv.ID, map[string]any{"autoStopMin": 10}, 200, &pv)
	if pv.Name != byOwner[priv.ID].Name || len(pv.Labels) != 0 {
		t.Fatalf("an operator's PATCH answer: %+v", pv)
	}
	// the person, in her partition, sees her own
	if got := alice.Get(priv.ID); got.Name != "secret plans" || got.Labels["xbin.agent/conversation"] != "7" {
		t.Fatalf("alice's own view: %+v", got)
	}
}

func TestPartitionRestart(t *testing.T) {
	t.Parallel()
	db := filepath.Join(t.TempDir(), "db.sqlite")
	tm := newTestManager(t, db)
	a := tm.tg.As(t, "apps/pr-a")
	alice := a.InPartition("alice", "u-a1")
	sb := alice.Create(map[string]any{"name": "kept", "clientId": "k", "visibility": "team", "start": false})
	alice.Call("PATCH", "/sandboxes/"+sb.ID, map[string]any{"shares": []map[string]any{{"consumer": "apps/pr-b", "partitionId": "u-b1", "users": "*"}}}, 200, nil)
	tm.srv.Close()
	tm.m.Close()
	tm.fb.mu.Lock()
	fb := &fakeBackend{Root: tm.fb.Root, Grace: tm.fb.Grace, boxes: tm.fb.boxes}
	tm.fb.mu.Unlock()
	tm2 := serveManager(t, db, fb)
	alice2 := tm2.tg.As(t, "apps/pr-a").InPartition("alice", "u-a1")
	got := alice2.Get(sb.ID)
	if got.Owner.PartitionID != "u-a1" || got.Owner.Partition != "user:alice" || got.Owner.User != "alice" || got.Shared {
		t.Fatalf("after a restart: %+v", got.Owner)
	}
	var again sandboxcontract.Sandbox
	alice2.Call("POST", "/sandboxes", map[string]any{"name": "kept", "clientId": "k", "visibility": "team", "start": false}, 200, &again)
	if again.ID != sb.ID {
		t.Fatalf("a partition's create clientId across a restart: %s, want %s", again.ID, sb.ID)
	}
	tm2.tg.As(t, "apps/pr-b").InPartition("bob", "u-b1").Get(sb.ID)
	tm2.tg.As(t, "apps/pr-a").Refused("GET", "/sandboxes/"+sb.ID, nil, 404, "not-found")
}
