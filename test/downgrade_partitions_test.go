//go:build integration

package test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/util"
)

// The downgrade of a workspace with a partitioned tile (plans/partitions/
// 10 §A.3, 03 §E; PD-06, PD-43; work pack I1), beside downgrade_test.go's:
// the new xbind runs apps/dg-part (["user", "global"]) for alice and bob —
// each with data of their own, alice with a cron job of her partition's —
// and apps/dg-req, whose code calls xbin.RequirePartition(). The previous
// release (XBIN_DOWNGRADE_BIN) then runs the workspace: one instance per
// tile at today's keys, so apps/dg-part serves global's data to everyone
// and nobody's partition data; alice's job never fires as the tile's; and
// apps/dg-req runs no backend at all (fail closed). It rewrites the users
// store (a user's change), dropping the uids it doesn't know. The new
// xbind again: the uids are re-adopted from the partitions' records, so
// alice's and bob's partitions — ids and data — are theirs as before, and
// apps/dg-req runs. (A sealed archive refused and a plaintext one restored
// by the previous release: sealed_backup_test.go.)

const (
	dgPart = "apps/dg-part"
	dgReq  = "apps/dg-req"
)

// covers PD-06 PD-43 10§A.3 03§E — a partitioned workspace under the
// previous release and back.
func TestDowngradePartitions(t *testing.T) {
	old := downgradeBinOrSkip(t)
	d := startIsolatedDaemon(t, isoOpts{Auth: true})
	e := &pnEnv{a: d.dl(), ws: d.WS, people: map[string]userClient{}}
	login := func() {
		t.Helper()
		e.a = d.dl()
		for _, p := range []string{"alice", "bob"} {
			e.people[p] = loginAs(t, e.a, p, "pw-"+p+"-4d1")
		}
	}
	for _, p := range []string{"alice", "bob"} {
		if c, b := e.a.do("POST", "/api/xbin/users", `{"id":"`+p+`","role":"user","tiles":{"apps/*":"read"},"password":"pw-`+p+`-4d1"}`); c != 200 {
			t.Fatalf("adding %s: %d %s", p, c, b)
		}
	}
	login()
	pnWrite(t, d.WS, dgPart, "part-v1", `["user","global"]`)
	req := strings.Replace(pnSource, "func main() {\n", "func main() {\n\txbin.RequirePartition()\n", 1)
	pnWrite(t, d.WS, dgReq, "req-v1", `["user","global"]`)
	writeIfChanged(t, filepath.Join(d.WS, "apps", "dg-req", "backend", "main.go"),
		strings.NewReplacer("__MARKER__", `"req-v1"`, "__FILE__", `"`+probeFile+`"`).Replace(req))
	e.waitState(t, dgPart, "partitioned")
	e.waitState(t, dgReq, "partitioned")
	waitProbeA(t, e.a, dgPart, "part-v1")
	waitProbeA(t, e.a, dgReq, "req-v1")

	// everyone's data, named after them; alice's job ticks her partition
	if c, b := e.a.do("PUT", "/api/"+dgPart+"/kv/k", "global-dg"); c != 200 {
		t.Fatalf("global's data: %d %s", c, b)
	}
	ids := map[string]string{}
	for _, p := range []string{"alice", "bob"} {
		fr := e.frame(t, dgPart, p)
		if c, b, _ := e.call(t, "PUT", "/api/"+dgPart+"/kv/k", p+"-dg", fr...); c != 200 {
			t.Fatalf("%s's data: %d %s", p, c, b)
		}
		var who struct{ Partition, PartitionID string }
		c, b, _ := e.call(t, "GET", "/api/"+dgPart+"/caller", "", fr...)
		if c != 200 || json.Unmarshal([]byte(b), &who) != nil || who.Partition != "user:"+p || who.PartitionID == "" {
			t.Fatalf("%s's partition: %d %s", p, c, b)
		}
		ids[p] = who.PartitionID
	}
	fr := e.frame(t, dgPart, "alice")
	if c, b, _ := e.call(t, "PUT", "/api/xbin/cron/jobs", `{"name":"mine","resource":"res:`+dgPart+`/cron","schedule":"@every 1m","path":"/alice-tick"}`,
		append(fr, "Content-Type", "application/json")...); c != 200 {
		t.Fatalf("alice's partition's cron job: %d %s", c, b)
	}
	uids := dgUIDs(t, d.WS)
	if uids["alice"] == "" || uids["bob"] == "" || util.PartitionKey("alice", uids["alice"]) != ids["alice"] {
		t.Fatalf("the users store's uids %v, partition ids %v", uids, ids)
	}

	// the previous release
	d.stop(t)
	d.Bin = old
	d.start(t)
	login()
	if c, b := e.a.do("GET", "/api/xbin/partitions", ""); c != 404 {
		t.Skipf("XBIN_DOWNGRADE_BIN answers GET /partitions (%d %.120s): it knows partitions, so this is no downgrade past them", c, b)
	}
	waitProbeA(t, e.a, dgPart, "part-v1")
	for name, hdrs := range map[string][]string{"the root token": e.root(), "alice's frame": e.frame(t, dgPart, "alice"), "bob's frame": e.frame(t, dgPart, "bob")} {
		c, b, _ := e.call(t, "GET", "/api/"+dgPart+"/kv/k", "", hdrs...)
		if c != 200 || b != "global-dg" {
			t.Errorf("under the previous release, %s reads %d %q, want global's data (today's keys)", name, c, b)
		}
		if c, b, _ := e.call(t, "GET", "/api/"+dgPart+"/env", "", hdrs...); strings.Contains(b, "XBIN_PARTITION") {
			t.Errorf("under the previous release, %s's instance knows a partition: %d %s", name, c, b)
		}
	}
	// apps/dg-req fails closed: its backend exits at start, so nothing
	// serves it, to anyone
	var code int
	var body string
	for end := time.Now().Add(90 * time.Second); time.Now().Before(end); time.Sleep(time.Second) {
		code, body = e.a.do("GET", "/api/"+dgReq+"/v", "")
		if code == 200 || code/100 == 5 && !strings.Contains(body, "building") {
			break
		}
	}
	t.Logf("apps/dg-req under the previous release: %d %s", code, firstN(body, 300))
	if code == 200 || strings.Contains(body, "req-v1") {
		t.Errorf("BUG: under the previous release %s (xbin.RequirePartition) serves: %d %s", dgReq, code, body)
	}
	// alice's job: a minute and some — it never fires as the tile's
	time.Sleep(75 * time.Second)
	c, b := e.a.do("GET", "/api/"+dgPart+"/seen", "")
	if c != 200 || strings.Contains(b, "/alice-tick") {
		t.Errorf("under the previous release alice's partition's job fired as the tile's: %d %s", c, b)
	}
	// a user's change: the previous release rewrites the users store
	if c, b := e.a.do("PATCH", "/api/xbin/users/bob", `{"name":"Bob B"}`); c != 200 {
		t.Fatalf("the previous release changes bob: %d %s", c, b)
	}
	rewritten := dgUIDs(t, d.WS)
	t.Logf("the users store's uids under the previous release: %v", rewritten)

	// the new binary again
	d.stop(t)
	d.Bin = xbindBin
	d.start(t)
	login()
	if rewritten["alice"] == "" {
		t.Log("the previous release dropped the uids: the new one re-adopts them from the partitions' records")
	}
	for _, p := range []string{"alice", "bob"} {
		fr := e.frame(t, dgPart, p)
		var who struct{ Partition, PartitionID string }
		var c int
		var b string
		waitFor(func() bool {
			c, b, _ = e.call(t, "GET", "/api/"+dgPart+"/caller", "", fr...)
			return c == 200
		}, 3*time.Minute)
		if c != 200 || json.Unmarshal([]byte(b), &who) != nil || who.Partition != "user:"+p || who.PartitionID != ids[p] {
			t.Errorf("%s's partition after the upgrade: %d %s, want id %s (re-adopted)", p, c, b, ids[p])
		}
		if c, b, _ := e.call(t, "GET", "/api/"+dgPart+"/kv/k", "", fr...); c != 200 || b != p+"-dg" {
			t.Errorf("%s's data after the upgrade: %d %q", p, c, b)
		}
	}
	if now := dgUIDs(t, d.WS); now["alice"] != uids["alice"] || now["bob"] != uids["bob"] {
		t.Errorf("the uids after the upgrade %v, before the downgrade %v", now, uids)
	}
	waitProbeA(t, e.a, dgReq, "req-v1")
}

// dgUIDs is every person's uid in the users store ("" = none).
func dgUIDs(t *testing.T, ws string) map[string]string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(ws, "data", "users.json"))
	if err != nil {
		t.Fatal(err)
	}
	var users struct {
		Users []struct{ ID, UID string }
	}
	if err := json.Unmarshal(b, &users); err != nil {
		t.Fatalf("users.json: %v", err)
	}
	out := map[string]string{}
	for _, u := range users.Users {
		out[u.ID] = u.UID
	}
	return out
}
