package sandboxcontract

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"testing"
)

// --- user partitions of a partitioned consumer ------------------------------------------
//
// A partitioned consumer tile runs one instance per person (a user
// partition) plus, maybe, a global one. xbind tells a manager which with
// X-XBin-Partition ("user:<id>" or "global") and X-XBin-Partition-Id (a user
// partition's stable id). The consumer identity is (X-XBin-From, partition
// id), "" and global being the consumer's non-personal identity; a user
// partition's person is verified. Checked when hello's caps carry
// "partitions".

// pid is a user partition's id as xbind makes them: u-<32 hex>.
func pid(name string) string {
	s := sha256.Sum256([]byte("sandboxcontract\x00" + name))
	return "u-" + hex.EncodeToString(s[:16])
}

var userPartitionChecks = []check{
	{"apart", func(t *testing.T, e *env) {
		a := e.as("a")
		alice, bob := a.InPartition("alice", pid("alice")), a.InPartition("bob", pid("bob"))
		var h Hello
		alice.Call("GET", "/hello?protocol=1", nil, http.StatusOK, &h)
		sb := alice.Create(map[string]any{"name": "alice's", "visibility": "team"})
		if o := sb.Owner; o.Via != a.from || o.User != "alice" || o.Asserted || o.PartitionID != pid("alice") || o.Partition != "user:alice" || sb.Shared {
			t.Fatalf("a sandbox made in a user partition: owner %+v, shared %v", o, sb.Shared)
		}
		alice.Get(sb.ID)
		alice.Verified("alice").Get(sb.ID) // its page
		if _, ok := alice.List()[sb.ID]; !ok {
			t.Fatal("alice's partition doesn't list its sandbox")
		}
		// another partition of the same consumer, the consumer's global
		// instance, its people there: for them it doesn't exist
		for _, other := range []Caller{bob, bob.Verified("bob"), a, a.Global(), a.Verified("alice"), a.Asserting("alice")} {
			other.Refused("GET", "/sandboxes/"+sb.ID, nil, 404, "not-found")
			other.Refused("POST", "/sandboxes/"+sb.ID+"/run", map[string]any{"cmd": "true"}, 404, "not-found")
			other.Refused("PATCH", "/sandboxes/"+sb.ID, map[string]any{"name": "x"}, 404, "not-found")
			other.Refused("DELETE", "/sandboxes/"+sb.ID, nil, 404, "not-found")
			if _, ok := other.List()[sb.ID]; ok {
				t.Fatalf("%s lists alice's partition's sandbox", other.who())
			}
		}
		if out := alice.Sh(sb.ID, "echo mine"); out != "mine\n" {
			t.Fatalf("alice's partition runs: %q", out)
		}
		// clientIds are per partition
		body := map[string]any{"name": "one", "clientId": "c1", "start": false}
		x, y, z := alice.Create(body), bob.Create(body), a.Create(body)
		if x.ID == y.ID || x.ID == z.ID || y.ID == z.ID {
			t.Fatalf("one clientId in two partitions and global: %s %s %s", x.ID, y.ID, z.ID)
		}
		var again Sandbox
		alice.Call("POST", "/sandboxes", body, http.StatusOK, &again)
		if again.ID != x.ID {
			t.Fatalf("a repeated clientId in a partition made %s, want %s", again.ID, x.ID)
		}
	}},
	{"global", func(t *testing.T, e *env) {
		// "" and "global" are one consumer: a partitioned consumer's global
		// instance keeps what it made before it was partitioned
		a := e.as("a")
		plain := a.Create(map[string]any{"name": "plain", "start": false})
		glob := a.Global().Create(map[string]any{"name": "global", "start": false})
		if o := glob.Owner; o.Via != a.from || o.PartitionID != "" || o.Partition != "" || glob.Shared {
			t.Fatalf("made by the global instance: owner %+v, shared %v", o, glob.Shared)
		}
		if got := a.Global().Get(plain.ID); got.Shared {
			t.Fatal("the global instance sees its consumer's sandbox as shared")
		}
		a.Get(glob.ID)
		if l := a.Global().List(); len(l) != 2 || l[plain.ID].Shared || l[glob.ID].Shared {
			t.Fatalf("the global instance's list: %+v", l)
		}
		a.Global().Call("PATCH", "/sandboxes/"+plain.ID, map[string]any{"visibility": "team"}, http.StatusOK, nil)
		a.Call("DELETE", "/sandboxes/"+glob.ID, nil, http.StatusNoContent, nil)
	}},
	{"global-home", func(t *testing.T, e *env) {
		// a user partition sees what its person may use at its consumer's
		// non-personal identity — never the converse
		a := e.as("a")
		alice := a.InPartition("alice", pid("alice"))
		team := a.Create(map[string]any{"name": "team", "visibility": "team", "start": false})
		member := a.Asserting("carol").Create(map[string]any{"name": "member", "members": []string{"alice"}, "start": false})
		owned := a.Asserting("alice").Create(map[string]any{"name": "owned", "start": false})
		private := a.Asserting("carol").Create(map[string]any{"name": "carol's", "start": false})
		own := a.Create(map[string]any{"name": "the consumer's", "start": false})
		l := alice.List()
		for _, sb := range []Sandbox{team, member, owned} {
			if got := alice.Get(sb.ID); !got.Shared {
				t.Fatalf("alice's partition's view of %s: %+v", sb.Name, got)
			}
			if _, ok := l[sb.ID]; !ok {
				t.Fatalf("alice's partition doesn't list %s", sb.Name)
			}
		}
		for _, sb := range []Sandbox{private, own} { // nobody's but its consumer's, or another person's
			alice.Refused("GET", "/sandboxes/"+sb.ID, nil, 404, "not-found")
			alice.Refused("POST", "/sandboxes/"+sb.ID+"/run", map[string]any{"cmd": "true"}, 404, "not-found")
			if _, ok := l[sb.ID]; ok {
				t.Fatalf("alice's partition lists %s", sb.Name)
			}
		}
		if out := alice.Sh(team.ID, "echo team"); out != "team\n" {
			t.Fatalf("alice's partition runs in a team sandbox: %q", out)
		}
		// it stays its consumer's: a partition changes neither who may use it nor deletes it
		alice.Refused("PATCH", "/sandboxes/"+owned.ID, map[string]any{"visibility": "team"}, 403, "not-allowed")
		alice.Refused("PATCH", "/sandboxes/"+owned.ID, map[string]any{"shares": []map[string]any{}}, 403, "not-allowed")
		alice.Refused("DELETE", "/sandboxes/"+owned.ID, nil, 403, "not-allowed")
		// no longer a member: gone from her partition
		a.Call("PATCH", "/sandboxes/"+member.ID, map[string]any{"members": []string{}}, http.StatusOK, nil)
		alice.Refused("GET", "/sandboxes/"+member.ID, nil, 404, "not-found")
		// another consumer's partitions see none of it
		e.as("b").InPartition("alice", pid("alice")).Refused("GET", "/sandboxes/"+team.ID, nil, 404, "not-found")
		// shared with the consumer: its partitions see it where the share includes their person
		c := e.as("c")
		theirs := c.Create(map[string]any{"name": "c's", "visibility": "team", "start": false})
		c.Call("PATCH", "/sandboxes/"+theirs.ID, map[string]any{"shares": []map[string]any{{"consumer": a.from, "users": []string{"alice"}}}}, http.StatusOK, nil)
		if got := alice.Get(theirs.ID); !got.Shared {
			t.Fatalf("alice's partition's view of a sandbox shared with its consumer: %+v", got)
		}
		a.InPartition("bob", pid("bob")).Refused("GET", "/sandboxes/"+theirs.ID, nil, 404, "not-found")
	}},
	{"shares", func(t *testing.T, e *env) {
		a, b := e.as("a"), e.as("b")
		bob, carol := b.InPartition("bob", pid("bob")), b.InPartition("carol", pid("carol"))
		sb := a.Create(map[string]any{"name": "shared", "visibility": "team"})
		var p struct{ Shares []map[string]any }
		a.Call("PATCH", "/sandboxes/"+sb.ID, map[string]any{"shares": []map[string]any{{"consumer": b.from, "partitionId": pid("bob"), "users": "*"}}}, http.StatusOK, &p)
		if len(p.Shares) != 1 || p.Shares[0]["consumer"] != b.from || p.Shares[0]["partitionId"] != pid("bob") {
			t.Fatalf("a share to a partition, as the manager keeps it: %+v", p.Shares)
		}
		if got := bob.Get(sb.ID); !got.Shared {
			t.Fatalf("bob's partition's view of a sandbox shared with it: %+v", got)
		}
		if out := bob.Sh(sb.ID, "echo via-bob"); out != "via-bob\n" {
			t.Fatalf("bob's partition runs: %q", out)
		}
		for _, other := range []Caller{carol, b, b.Global(), b.Verified("bob")} { // the share names one partition
			other.Refused("GET", "/sandboxes/"+sb.ID, nil, 404, "not-found")
		}
		bob.Refused("PATCH", "/sandboxes/"+sb.ID, map[string]any{"shares": []map[string]any{}}, 403, "not-allowed")
		bob.Refused("DELETE", "/sandboxes/"+sb.ID, nil, 403, "not-allowed")
		// a partition shares its own, with another partition of its consumer
		alice := a.InPartition("alice", pid("alice"))
		mine := alice.Create(map[string]any{"name": "alice's", "visibility": "team", "start": false})
		alice.Call("PATCH", "/sandboxes/"+mine.ID, map[string]any{"shares": []map[string]any{{"consumer": a.from, "partitionId": pid("dave"), "users": []string{"dave"}}}}, http.StatusOK, nil)
		a.InPartition("dave", pid("dave")).Get(mine.ID)
		a.Refused("GET", "/sandboxes/"+mine.ID, nil, 404, "not-found")
		// unshared: gone again
		a.Call("PATCH", "/sandboxes/"+sb.ID, map[string]any{"shares": []map[string]any{}}, http.StatusOK, nil)
		bob.Refused("GET", "/sandboxes/"+sb.ID, nil, 404, "not-found")
	}},
	{"person", func(t *testing.T, e *env) {
		// a user partition's person is verified: a call naming another is refused
		a := e.as("a")
		alice := a.InPartition("alice", pid("alice"))
		alice.Asserting("mallory").Refused("POST", "/sandboxes", map[string]any{"name": "x"}, 403, "not-allowed")
		alice.Verified("mallory").Refused("POST", "/sandboxes", map[string]any{"name": "x"}, 403, "not-allowed")
		sb := alice.Asserting("alice").Create(map[string]any{"name": "hers", "start": false})
		if sb.Owner.User != "alice" || sb.Owner.Asserted {
			t.Fatalf("an owner named by the partition: %+v", sb.Owner)
		}
		alice.Asserting("mallory").Refused("GET", "/sandboxes/"+sb.ID, nil, 403, "not-allowed")
		alice.Asserting("mallory").Refused("GET", "/sandboxes", nil, 403, "not-allowed")
		alice.Verified("alice").Get(sb.ID)
	}},
	{"recreated", func(t *testing.T, e *env) {
		// a person deleted and made again has a new partition id: nothing of
		// the old one's
		a := e.as("a")
		old := a.InPartition("alice", pid("alice-1"))
		priv := old.Create(map[string]any{"name": "old", "start": false})
		team := old.Create(map[string]any{"name": "old team", "visibility": "team", "start": false})
		again := a.InPartition("alice", pid("alice-2"))
		for _, sb := range []Sandbox{priv, team} {
			again.Refused("GET", "/sandboxes/"+sb.ID, nil, 404, "not-found")
		}
		if l := again.List(); len(l) != 0 {
			t.Fatalf("the new alice's partition lists %d sandboxes", len(l))
		}
		var raw []byte
		old.Call("GET", "/sandboxes/"+priv.ID, nil, http.StatusOK, &raw)
		var v struct{ Owner map[string]any }
		if json.Unmarshal(raw, &v) != nil || v.Owner["partitionId"] != pid("alice-1") {
			t.Fatalf("the owner's partitionId: %s", raw)
		}
	}},
}
