package main

// callers_view_test.go — who sees which rows of GET /stats' callers
// (callers_view.go; PD-46): per-person rows are the owner token's; a
// manager of the tile sees the global instances and the totals; a person
// their own rows; viewing as someone, nothing.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestCallersVisibility(t *testing.T) {
	fresh(t, 0)
	for _, h := range []hdrs{alice, bob, global} {
		proxy(context.Background(), h, "m")
	}
	page := func(user, level string, extra ...string) hdrs {
		h := hdrs{"X-XBin-From": "apps/llm-gw", "X-XBin-Role": "admin"}
		if user != "" {
			h["X-XBin-User"], h["X-XBin-User-Level"] = user, level
		}
		for i := 0; i+1 < len(extra); i += 2 {
			h[extra[i]] = extra[i+1]
		}
		return h
	}
	dash := hdrs{"X-XBin-From": "apps/dash", "X-XBin-Role": "admin"} // a tile granted admin here: no person behind it
	for _, c := range []struct {
		name    string
		h       hdrs
		want    []string // partitions seen
		lastOf  []string // rows with a last use
		totals  string   // "<partitions>/<reqs>" of apps/agent, "" = none
		manage  bool
		readIDs bool // may read people's partition ids
	}{
		{"the owner token", owner, []string{"global", "user:alice", "user:bob"}, []string{"global"}, "2/2", true, true},
		{"the page opened with the owner token", page("", ""), []string{"global", "user:alice", "user:bob"}, []string{"global"}, "2/2", true, true},
		{"a manager of the tile (write)", page("carol", "write"), []string{"global"}, []string{"global"}, "2/2", true, false},
		{"a manager of the tile (terminal; an admin reads so too)", page("carol", "terminal"), []string{"global"}, []string{"global"}, "2/2", true, false},
		{"alice, a manager", page("alice", "write"), []string{"global", "user:alice"}, []string{"global", "user:alice"}, "2/2", true, false},
		{"a tile granted admin here", dash, []string{"global"}, []string{"global"}, "2/2", true, false},
		{"alice, who reads the tile", page("alice", "read"), []string{"user:alice"}, []string{"user:alice"}, "", false, false},
		{"dave, who reads the tile and has no partition", page("dave", "read"), nil, nil, "", false, false},
		{"an admin viewing as alice", page("alice", "read", "X-XBin-Viewed-By", "erin"), nil, nil, "", false, false},
		{"an admin viewing as a manager", page("carol", "write", "X-XBin-Viewed-By", "erin"), nil, nil, "", false, false},
	} {
		out, rows := statsAs(t, c.h)
		var got, last []string
		for _, r := range rows {
			got = append(got, r.Partition)
			if r.Last != 0 {
				last = append(last, r.Partition)
			}
			if !c.readIDs && r.PartitionID != "" && r.Partition != "user:"+c.h["X-XBin-User"] {
				t.Errorf("%s reads someone else's partition id: %+v", c.name, r)
			}
		}
		if strings.Join(got, ",") != strings.Join(c.want, ",") || strings.Join(last, ",") != strings.Join(c.lastOf, ",") {
			t.Errorf("%s sees %v (a last use on %v), want %v (%v)", c.name, got, last, c.want, c.lastOf)
		}
		if _, has := out["callers"]; has != (len(c.want) > 0) {
			t.Errorf("%s: callers key present %v with %d rows", c.name, has, len(c.want))
		}
		var totals []callerTotal
		_ = json.Unmarshal(out["callerTotals"], &totals)
		tot := ""
		for _, x := range totals {
			if x.From == "apps/agent" && x.Deployment == "" {
				tot = fmt.Sprintf("%d/%d", x.Partitions, x.Reqs)
			}
		}
		if tot != c.totals || len(totals) > 1 {
			t.Errorf("%s: totals %s, want %q", c.name, out["callerTotals"], c.totals)
		}
		if strings.Contains(string(out["callerTotals"]), "user:") || strings.Contains(string(out["callerTotals"]), `"u-`) {
			t.Errorf("%s: the totals name a person: %s", c.name, out["callerTotals"])
		}
		if _, has := out["canManage"]; has != c.manage {
			t.Errorf("%s: canManage %v, want %v", c.name, has, c.manage)
		}
	}
	// without XBIN_COMPONENT, a call naming no one never reads as the tile
	selfPath = ""
	if _, rows := statsAs(t, hdrs{"X-XBin-Role": "admin"}); len(rows) != 1 || rows[0].Partition != "global" {
		t.Errorf("a call naming no one, without XBIN_COMPONENT: %+v", rows)
	}
}

// Someone else's live calls never show: not in a row, not as a row of its
// own before their first call ended.
func TestCallersNoLiveCountsOfOthers(t *testing.T) {
	fresh(t, 0)
	ctx := context.Background()
	done := make(chan struct{})
	go func() { proxy(ctx, alice, "slow"); close(done) }()
	<-arrived // alice's first call is in flight: no row of hers ended yet
	for _, h := range []hdrs{owner, {"X-XBin-From": "apps/llm-gw", "X-XBin-User": "carol", "X-XBin-User-Level": "write"}} {
		if out, rows := statsAs(t, h); len(rows) != 0 || out["callerTotals"] != nil {
			t.Errorf("%v sees alice's call in flight: %+v %s", h, rows, out["callerTotals"])
		}
	}
	_, rows := statsAs(t, hdrs{"X-XBin-From": "apps/llm-gw", "X-XBin-User": "alice", "X-XBin-User-Level": "read"})
	if len(rows) != 1 || rows[0].Active != 1 {
		t.Errorf("alice's own call in flight: %+v", rows)
	}
	holdMu.Lock()
	close(hold)
	holdMu.Unlock()
	<-done
}
