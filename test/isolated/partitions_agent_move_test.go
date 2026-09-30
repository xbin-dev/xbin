//go:build linux && integration

package isolated

// partitions_agent_move_test.go — the unshare-moves case of
// TestPartitionsAgent (work pack AF of the partitioned-tiles plan, the
// owner's ruling 90 §I10; the template's API.md "Partitioned instances" →
// "Shared conversations"): a shared conversation that stops being shared
// moves to its owner's own partition.
//
//   - alice's conversation shared with bob, at the global instance; bob
//     leaves it — nobody else is in it now — and it moves: the global
//     instance mails alice's partition, which reads it (as alice, over her
//     own-global calls), takes it in hidden, has global delete its copy,
//     then lists it;
//   - at no moment is it listed in both of alice's homes (polled
//     throughout); in the end it is in her own list (an id from 2^40) with
//     its transcript, gone from global's (404), its tombstone saying where
//     it went, and alice's stream of it got the event naming the new id;
//   - bob reads neither copy.

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/test/xbindtest"
)

func paUnshareMoves(t *testing.T, e *psEnv) {
	d := e.d
	call := func(person, method, path string, body any, global bool) xbindtest.Resp {
		t.Helper()
		p := "/api/" + paAgent + path
		if global {
			sep := "?"
			if strings.Contains(p, "?") {
				sep = "&"
			}
			p += sep + "xbin-partition=global"
		}
		return d.Call(t, method, p, body, e.fr(t, paAgent, person))
	}
	must := func(want int, person, method, path string, body any, global bool) xbindtest.Resp {
		t.Helper()
		r := call(person, method, path, body, global)
		if r.Status != want {
			t.Fatalf("%s: %s %s (global %v): %d %s (want %d)", person, method, path, global, r.Status, r, want)
		}
		return r
	}
	titled := func(person string, global bool, title string) []int64 {
		t.Helper()
		var out struct {
			Pinned, Items []struct {
				ID    int64
				Title string
			}
		}
		must(200, person, "GET", "/conversations?scope=mine", nil, global).Decode(t, &out)
		var ids []int64
		for _, it := range append(out.Pinned, out.Items...) {
			if it.Title == title {
				ids = append(ids, it.ID)
			}
		}
		return ids
	}

	var x struct{ ID int64 }
	must(200, "alice", "POST", "/ask", map[string]any{"text": "the moving plan",
		"share": map[string]any{"members": []map[string]string{{"user": "bob", "role": "participant"}}}}, true).Decode(t, &x)
	if x.ID <= 0 || x.ID >= pa2to40 {
		t.Fatalf("the shared conversation: %+v", x)
	}
	xbindtest.Eventually(t, 2*time.Minute, "the shared conversation answered", func() (bool, string) {
		var r paRun
		_ = json.Unmarshal(call("alice", "GET", fmt.Sprintf("/runs/%d", x.ID), nil, true).Body, &r)
		return r.Run.Status == "idle" && len(r.Messages) >= 3, fmt.Sprint(r.Run.Status, len(r.Messages))
	})
	const title = "moving plan (e2e)"
	must(200, "alice", "PATCH", fmt.Sprintf("/runs/%d", x.ID), map[string]string{"title": title}, true)
	stream := paFollow(t, d, e.fr(t, paAgent, "alice"), x.ID)
	xbindtest.Eventually(t, time.Minute, "alice's stream says hello", func() (bool, string) { return stream.count(0, "hello", nil) > 0, "" })

	// bob leaves: nobody else is in it — it moves to alice's own space
	must(200, "bob", "DELETE", fmt.Sprintf("/runs/%d/members/bob", x.ID), nil, true)
	var to int64
	polls, both := 0, 0
	xbindtest.Eventually(t, 3*time.Minute, "the conversation moved to alice's partition", func() (bool, string) {
		polls++
		atGlobal, own := titled("alice", true, title), titled("alice", false, title)
		if len(atGlobal) > 0 && len(own) > 0 {
			both++
		}
		var m struct {
			State string
			To    int64
		}
		_ = json.Unmarshal(call("alice", "GET", fmt.Sprintf("/moves/%d", x.ID), nil, true).Body, &m)
		to = m.To
		return m.State == "moved" && len(own) == 1 && own[0] == m.To && len(atGlobal) == 0,
			fmt.Sprintf("move %+v, global %v, own %v", m, atGlobal, own)
	})
	if both > 0 {
		t.Errorf("BUG: listed in both of alice's homes at %d of %d looks", both, polls)
	}
	if to < pa2to40 {
		t.Fatalf("moved to %d (want one of alice's own ids)", to)
	}
	var y paRun
	must(200, "alice", "GET", fmt.Sprintf("/runs/%d", to), nil, false).Decode(t, &y)
	if y.Run.Owner != "alice" || !strings.Contains(fmt.Sprint(y.Messages), "the moving plan") {
		t.Errorf("the moved conversation: %+v", y)
	}
	if st := call("alice", "GET", fmt.Sprintf("/runs/%d", x.ID), nil, true).Status; st != 404 {
		t.Errorf("the shared copy after the move: %d, want 404", st)
	}
	if st := call("bob", "GET", fmt.Sprintf("/runs/%d", to), nil, false).Status; st != 404 {
		t.Errorf("BUG: bob reads alice's moved conversation: %d", st)
	}
	xbindtest.Eventually(t, time.Minute, "alice's stream heard where it went", func() (bool, string) {
		n := stream.count(x.ID, "run", func(d json.RawMessage) bool { return strings.Contains(string(d), fmt.Sprintf(`"movedTo":%d`, to)) })
		return n > 0, stream.last(5)
	})
}
