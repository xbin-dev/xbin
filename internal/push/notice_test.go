package push

import (
	"testing"
	"time"
)

// covers PD-44 01§2.4 01§2.5 — xbind's own notices (a partition switch
// request to a manager, a wipe notice to a person) reach the person's
// devices that take tile notifications, sealed like any push, even from a
// tile the person muted; a device that takes agent pushes only doesn't get
// them.
func TestNotice(t *testing.T) {
	r := newRig(t, nil)
	r.register(alice, "phone", "handle-phone", "agent")
	r.register(alice, "ipad", "handle-ipad", "tile")
	if code, _, _ := r.call(alice, "PUT", "/push/prefs", map[string]any{"mutedTiles": []string{"apps/docs"}}); code != 200 {
		t.Fatal(code)
	}
	r.s.Notice("alice", "tile.partition-switch", "apps/docs is paused", "unpartitioned → user", "c/apps/docs/", "partition-switch:apps/docs")
	got := r.relay.waitPushes(1)
	time.Sleep(20 * time.Millisecond)
	if got = r.relay.pushes(); len(got) != 1 || got[0].Handle != "handle-ipad" {
		t.Fatalf("want the ipad alone: %+v", got)
	}
	want := Payload{V: 1, WS: r.s.Workspace(), Kind: "tile.partition-switch", Title: "apps/docs is paused",
		Body: "unpartitioned → user", Link: "c/apps/docs/", CollapseID: "xbin:partition-switch:apps/docs"}
	if p := r.open(got[0]); p != want {
		t.Fatalf("payload %+v\nwant    %+v", p, want)
	}
	r.s.Notice("", "tile.partition-deleted", "x", "", "", "") // nobody: nothing
	var nilSvc *Service
	nilSvc.Notice("alice", "tile.partition-deleted", "x", "", "", "") // no push plane: nothing
	time.Sleep(20 * time.Millisecond)
	if n := len(r.relay.pushes()); n != 1 {
		t.Errorf("%d pushes, want 1", n)
	}
}
