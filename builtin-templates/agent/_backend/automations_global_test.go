package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// TestChannelRunsBothHomes (W4 flag 3): in a person's partition a channel's
// card counts its runs in both homes — the DMs handed here and the threads
// the person may see at global — and its run list reads both, merged newest
// first and paged by one cursor; marking it read marks both. Global not
// answering leaves this partition's own; the partition's own automations
// (a schedule) are its alone.
func TestChannelRunsBothHomes(t *testing.T) {
	setMode(t, modeUser, "alice")
	kv := newMemKV()
	putConf(kv, "", `{}`)
	ag, mux := chanFixture(t)
	partitionConf(t, ag, kv)
	_ = ag.db.addHandoffSchema()
	// alice's handed DMs of channel 3, here
	st := runStamp{Owner: "alice", Visibility: visPrivate, TeamRole: roleViewer, Origin: "channel", OriginID: 3}
	var mine []int64
	for i, ms := range []int64{9000, 4000} {
		id, err := ag.db.createRunStamped(fmt.Sprintf("dm %d", i), "{}", 0, statusIdle, st)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = ag.db.q.Exec(`UPDATE runs SET activity_ms=? WHERE id=?`, ms, id)
		mine = append(mine, id)
	}
	row := func(id, ms int64) string { return fmt.Sprintf(`{"id":%d,"activityMs":%d,"title":"t%d"}`, id, ms, id) }
	globalDown := false
	g := stubGlobalCalls(t, func(method, path string, _ []byte) (int, string) {
		switch {
		case globalDown:
			return 502, `{"error":"down"}`
		case method == "GET" && path == "/automations":
			return 200, `{"items":[{"kind":"channel","id":3,"name":"slack","access":"viewer","runs":3,"unread":1}]}`
		case method == "GET" && strings.HasPrefix(path, "/automations/channel/3/runs"):
			if strings.Contains(path, "cursor=") { // below 7000.12: the rest
				return 200, `{"items":[` + row(11, 2000) + `],"next":""}`
			}
			return 200, `{"items":[` + row(12, 7000) + `,` + row(11, 2000) + `],"next":"2000.11"}`
		}
		return 200, `{"ok":true}`
	})
	var list struct{ Items []AutomationItem }
	if w := callAs(t, mux, asAlice, "GET", "/automations", nil); w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &list) != nil ||
		len(list.Items) != 1 || list.Items[0].Runs != 5 {
		t.Fatalf("the channel's card counts both homes (2 here + 3 there): %d %s", w.Code, w.Body)
	}
	page := func(q string) runsPage {
		t.Helper()
		var p runsPage
		w := callAs(t, mux, asAlice, "GET", "/automations/channel/3/runs?limit=2"+q, nil)
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &p) != nil {
			t.Fatalf("runs: %d %s", w.Code, w.Body)
		}
		return p
	}
	ids := func(p runsPage) string {
		var out []string
		for _, it := range p.Items {
			out = append(out, fmt.Sprint(int64(it["id"].(float64))))
		}
		return strings.Join(out, ",")
	}
	p := page("")
	if ids(p) != fmt.Sprintf("%d,12", mine[0]) || p.Next != "7000.12" {
		t.Fatalf("page 1: %s next %q", ids(p), p.Next)
	}
	p = page("&cursor=" + p.Next)
	if ids(p) != fmt.Sprintf("%d,11", mine[1]) || p.Next != "" {
		t.Fatalf("page 2: %s next %q", ids(p), p.Next)
	}
	if !strings.Contains(strings.Join(g.got(), "\n"), "GET /automations/channel/3/runs?limit=2&cursor=7000.12") {
		t.Fatalf("global asked with the same cursor: %v", g.got())
	}
	if w := callAs(t, mux, asAlice, "POST", "/automations/channel/3/read", nil); w.Code != 200 ||
		!strings.Contains(strings.Join(g.got(), "\n"), "POST /automations/channel/3/read") {
		t.Fatalf("read: %d, global %v", w.Code, g.got())
	}
	// global doesn't answer: this partition's own
	globalDown = true
	noteGlobalWrite()
	if p := page(""); ids(p) != fmt.Sprintf("%d,%d", mine[0], mine[1]) {
		t.Fatalf("with global down: %s", ids(p))
	}
	// unpartitioned nothing is merged
	if globalItem("channel", 3) != true || globalItem("trigger", partitionIDBase+1) || globalItem("schedule", 3) {
		t.Fatal("globalItem")
	}
	setMode(t, modeLegacy, "")
	if globalItem("channel", 3) {
		t.Fatal("unpartitioned, a channel is the instance's own")
	}
}
