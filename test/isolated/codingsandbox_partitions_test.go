//go:build linux && integration

package isolated

// codingsandbox_partitions_test.go — the sandbox-manager contract's
// user-partitions section through xbind (the partitioned-tiles plan's I1;
// B1's open end): the section's consumers are partitioned tiles, so the
// manager's parser meets the X-XBin-Partition and X-XBin-Partition-Id
// headers xbind itself derives from a person's page of a partitioned
// consumer — never ones a caller set, which xbind strips.
//
// The suite names partitions by synthetic ids (its pid) and asks for some
// callers xbind never makes; those checks are skipped, each saying which
// caller (csPartitionSkips), and testCSPartitionIDs makes their point with
// real people's real partition ids instead.

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/util"
	"github.com/xbin-dev/xbin/test/xbindtest"
)

// csPartitioned reports whether tile is one of the user-partitions
// section's consumers (apps/ct-user-partitions-<check>-<role>, the suite's
// naming), which makeConsumer makes partitioned.
func csPartitioned(tile string) bool { return strings.HasPrefix(tile, "apps/ct-user-partitions-") }

// errCSNeverMade is a caller of a partitioned consumer xbind never makes.
var errCSNeverMade = errors.New("xbind never makes this caller of a partitioned consumer")

// csPartitionPage is whose page of a partitioned consumer makes the caller
// (person verified, in partition part): "user:<p>" is p's page (their
// partition — the person verified as p, or none named); "global" and no
// partition are the owner's page (the consumer's global instance) with no
// person. A partitioned consumer's page is always its person's partition,
// so a verified person without their partition, or another person in
// one, is a caller xbind never makes.
func csPartitionPage(person, part string) (string, error) {
	switch p, isUser := strings.CutPrefix(part, "user:"); {
	case isUser && (person == "" || person == p):
		return p, nil
	case isUser:
		return "", errCSNeverMade
	case person != "":
		return "", errCSNeverMade
	}
	return "", nil
}

// csPartitionSkips are the target's skips: without verified people the
// checks that act as them, and in the user-partitions section the checks
// that need a caller xbind never makes, or a synthetic partition id.
func csPartitionSkips(people bool) map[string]string {
	skip := map[string]string{
		"user-partitions/apart": "it calls the consumer without a partition as a verified person (a partitioned consumer's page is always " +
			"its person's partition) and checks the suite's synthetic partition ids: testCSPartitionIDs makes its point through xbind",
		"user-partitions/shares": "it calls a partitioned consumer without a partition as a verified person and shares with synthetic " +
			"partition ids; the manager's handling of a share to a partition is its in-process run's (coding-sandbox's contract_test.go)",
		"user-partitions/person": "it has a person's partition verify another person, a caller xbind never makes " +
			"(the page's person is the partition's); the asserted mismatch is testCSPartitionIDs'",
		"user-partitions/recreated": "it needs a person deleted and made again mid-suite, while the parallel checks use them: " +
			"xbind's new partition id for a recreated person is TestPartitionsSmoke/person-recreated's",
	}
	if !people {
		skip["user-partitions"] = "no verified people: this xbind runs --no-auth, and user partitions are people's"
	}
	return skip
}

// testCSPartitionIDs: a partitioned consumer's people each reach the
// manager in their own partition, with the id xbind derives
// (util.PartitionKey of the person and their uid): alice's sandbox is homed
// in her partition, and bob's partition, the consumer's global instance and
// her page carrying forged partition headers see what they should.
func testCSPartitionIDs(t *testing.T, e *csEnv) {
	if !e.people || e.d.IsRemote() {
		t.Skip("needs verified people and the workspace's users store (a local xbind with auth)")
	}
	const cons = "apps/ct-user-partitions-xbind-a"
	page := func(person string) xbindtest.Header { return xbindtest.FrameHeader(e.pageTok(t, cons, person)) }
	alice, bob, glob := page("alice"), page("bob"), page("")
	base := "/api/" + csTile + "/sbx/sandboxes"
	type sbx struct {
		ID     string
		Shared bool
		Owner  struct{ User, Via, PartitionID, Partition string }
	}
	create := func(name string, hdrs ...xbindtest.Header) sbx {
		t.Helper()
		var sb sbx
		e.d.Must(t, "POST", base, map[string]any{"name": name, "visibility": "team", "start": false}, 201, hdrs...).Decode(t, &sb)
		t.Cleanup(func() { e.d.Call(t, "DELETE", base+"/"+sb.ID, nil, hdrs...) })
		return sb
	}
	mine := create("alice's", alice)
	e.d.Must(t, "GET", base, nil, 200, bob) // bob's partition, used once: his uid is minted
	uids := csUIDs(t, e.d.WS)
	pid := util.PartitionKey("alice", uids["alice"])
	if uids["alice"] == "" || uids["bob"] == "" {
		t.Fatalf("the users store's uids after alice's and bob's partitions called: %v", uids)
	}
	if o := mine.Owner; o.User != "alice" || o.Via != cons || o.PartitionID != pid || o.Partition != "user:alice" || mine.Shared {
		t.Fatalf("a sandbox alice's partition made: owner %+v (want her partition id %s), shared %v", o, pid, mine.Shared)
	}
	forged := []xbindtest.Header{alice, xbindtest.H("X-XBin-Partition", "user:bob"), xbindtest.H("X-XBin-Partition-Id", util.PartitionKey("bob", uids["bob"])),
		xbindtest.H("X-XBin-User", "bob")}
	if f := create("forged", forged...); f.Owner.User != "alice" || f.Owner.PartitionID != pid {
		t.Errorf("BUG: alice's page with forged partition headers made a sandbox owned %+v", f.Owner)
	}
	listed := func(hdrs ...xbindtest.Header) bool {
		t.Helper()
		var l struct{ Sandboxes []sbx }
		e.d.Must(t, "GET", base, nil, 200, hdrs...).Decode(t, &l)
		for _, sb := range l.Sandboxes {
			if sb.ID == mine.ID {
				return true
			}
		}
		return false
	}
	if r := e.d.Call(t, "GET", base+"/"+mine.ID, nil, alice); r.Status != 200 || !listed(alice) {
		t.Errorf("alice's partition's own sandbox: %d %s", r.Status, r)
	}
	for name, hdrs := range map[string][]xbindtest.Header{
		"bob's partition":                      {bob},
		"the consumer's global instance":       {glob},
		"bob's partition with alice's headers": {bob, xbindtest.H("X-XBin-Partition", "user:alice"), xbindtest.H("X-XBin-Partition-Id", pid)},
	} {
		if r := e.d.Call(t, "GET", base+"/"+mine.ID, nil, hdrs...); r.Status != 404 || !strings.Contains(r.String(), "not-found") {
			t.Errorf("%s reads alice's partition's sandbox: %d %s", name, r.Status, r)
		}
		if listed(hdrs...) {
			t.Errorf("BUG: %s lists alice's partition's sandbox", name)
		}
	}
	// the consumer's global instance: no partition id
	if g := create("global's", glob); g.Owner.PartitionID != "" || g.Owner.Partition != "" || g.Owner.User != "" {
		t.Errorf("a sandbox the consumer's global instance made: owner %+v", g.Owner)
	}
	// a partition naming another person is refused, whatever it asks
	for _, c := range []struct {
		name, method, path string
		hdrs               []xbindtest.Header
		refusal            string
	}{
		{"alice's partition asserting mallory creates", "POST", base, []xbindtest.Header{alice, xbindtest.H("Sbx-User", "mallory")}, "acts for alice, not mallory"},
		{"bob's partition asserting alice reads hers", "GET", base + "/" + mine.ID, []xbindtest.Header{bob, xbindtest.H("Sbx-User", "alice")}, "acts for bob, not alice"},
		{"bob's partition asserting alice lists", "GET", base, []xbindtest.Header{bob, xbindtest.H("Sbx-User", "alice")}, "acts for bob, not alice"},
	} {
		var body any
		if c.method == "POST" {
			body = map[string]any{"name": "x", "start": false}
		}
		if r := e.d.Call(t, c.method, c.path, body, c.hdrs...); r.Status != 403 || !strings.Contains(r.String(), "not-allowed") ||
			!strings.Contains(r.String(), c.refusal) || strings.Contains(r.String(), mine.ID) {
			t.Errorf("%s: %d %s", c.name, r.Status, r)
		}
	}
}

// csUIDs is every person's uid in the workspace's users store.
func csUIDs(t *testing.T, ws string) map[string]string {
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
