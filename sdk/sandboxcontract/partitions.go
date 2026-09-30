package sandboxcontract

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
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
		// a share makes a sandbox visible, and the person rules still apply:
		// a private one shared with a partition whose person is neither its
		// owner nor a member is refused, and listed nowhere — until they are
		// a member. So with one the consumer shares with its own partition.
		priv := a.Asserting("erin").Create(map[string]any{"name": "erin's", "start": false})
		a.Call("PATCH", "/sandboxes/"+priv.ID, map[string]any{"shares": []map[string]any{
			{"consumer": b.from, "partitionId": pid("bob"), "users": "*"},
			{"consumer": a.from, "partitionId": pid("dave"), "users": "*"}}}, http.StatusOK, nil)
		dave := a.InPartition("dave", pid("dave"))
		for _, p := range []Caller{bob, dave} {
			p.Refused("GET", "/sandboxes/"+priv.ID, nil, 403, "not-allowed")
			p.Refused("POST", "/sandboxes/"+priv.ID+"/run", map[string]any{"cmd": "true"}, 403, "not-allowed")
			if _, ok := p.List()[priv.ID]; ok {
				t.Fatalf("%s lists a private sandbox its person may not use", p.who())
			}
		}
		a.Call("PATCH", "/sandboxes/"+priv.ID, map[string]any{"members": []string{"bob", "dave"}}, http.StatusOK, nil)
		for _, p := range []Caller{bob, dave} {
			if got := p.Get(priv.ID); !got.Shared {
				t.Fatalf("%s's view of a private sandbox shared with it, its person a member: %+v", p.who(), got)
			}
		}
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
	{"sockets", func(t *testing.T, e *env) {
		// the sockets — a terminal, a tty exec's, a program's stdio (whose
		// attach takes over its stdin: a coding agent's) — keep the
		// partitions as every route does: a partition-homed sandbox's are
		// that partition's alone, refused before the upgrade; the partition's
		// backend is its person's; a team sandbox at the consumer's
		// non-personal identity is the partition's person's to use
		tty, stdio := e.has("tty"), e.has("stdio")
		if !tty && !stdio {
			t.Skip("neither tty nor stdio")
		}
		a := e.as("a")
		alice, bob := a.InPartition("alice", pid("alice")), a.InPartition("bob", pid("bob"))
		sb := alice.Create(map[string]any{"name": "alice's", "visibility": "team"})
		var paths []string
		var tx Exec // a tty exec
		var agent *pipe
		if tty {
			tx = alice.Exec(sb.ID, map[string]any{"cmd": `read l; echo "got:$l"`, "tty": true})
			paths = append(paths, ttyPath(sb.ID, url.Values{"cmd": {"true"}}), "/sandboxes/"+sb.ID+"/execs/"+tx.ID+"/tty")
		}
		if stdio {
			x := alice.Exec(sb.ID, map[string]any{"cmd": "cat", "stdin": true, "split": true})
			agent = openPipe(t, alice, sb.ID, x.ID, 0, 0) // alice's partition holds its stdin
			paths = append(paths, stdioPath(sb.ID, x.ID, 0, 0))
		}
		for _, path := range paths {
			// another partition (its backend, its page, its backend naming
			// its person), the consumer's global instance and that instance
			// naming alice: for them it doesn't exist
			for _, other := range []Caller{bob, bob.Verified("bob"), bob.Asserting("bob"), a, a.Global(), a.Asserting("alice"), a.Global().Asserting("alice")} {
				dialRefused(t, other, path, 404, "not-found")
			}
			// the partition naming another person
			dialRefused(t, alice.Asserting("mallory"), path, 403, "not-allowed")
		}
		if stdio { // none of the refused dials took its stdin
			agent.send("still alice's\n")
			agent.expect("still alice's\n")
			agent.op(map[string]any{"op": "eof"})
			agent.exited(code(0), "")
		}
		if tty { // its partition's backend, naming its person, and its page
			at := attach(t, alice.Asserting("alice"), "/sandboxes/"+sb.ID+"/execs/"+tx.ID+"/tty")
			at.send("x\r")
			at.expect("got:x")
			at.exited(0)
			sh := attach(t, alice.Verified("alice"), ttyPath(sb.ID, url.Values{"cmd": {"echo hers-$((6*7))"}}))
			sh.expect("hers-42")
			sh.exited(0)
		}
		// at the consumer's non-personal identity: another person's private
		// sandbox isn't the partition's, a team one is its person's to use
		carol := a.Asserting("carol")
		private := carol.Create(map[string]any{"name": "carol's", "visibility": "private"})
		team := a.Create(map[string]any{"name": "team", "visibility": "team"})
		if tty {
			dialRefused(t, alice, ttyPath(private.ID, url.Values{"cmd": {"true"}}), 404, "not-found")
			tm := attach(t, alice, ttyPath(team.ID, url.Values{"cmd": {"echo team-$((6*7))"}}))
			tm.expect("team-42")
			tm.exited(0)
		}
		if stdio {
			theirs := carol.Exec(private.ID, map[string]any{"cmd": "cat", "stdin": true})
			dialRefused(t, alice, stdioPath(private.ID, theirs.ID, 0, 0), 404, "not-found")
			carol.Call("DELETE", "/sandboxes/"+private.ID+"/execs/"+theirs.ID, nil, http.StatusNoContent, nil)
			x := alice.Exec(team.ID, map[string]any{"cmd": "cat", "stdin": true})
			p := openPipe(t, alice, team.ID, x.ID, 0, 0)
			p.send("team\n")
			p.expect("team\n")
			p.op(map[string]any{"op": "eof"})
			p.exited(code(0), "")
		}
	}},
	{"recreated", func(t *testing.T, e *env) {
		// a person deleted and made again has a new partition id: nothing of
		// what the old one's partition holds
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
		// but what the consumer's non-personal identity has follows its
		// person rules, by user id — as a page's call there always has: a
		// sandbox the old alice owned at global is the new alice's too (the
		// manager can't tell two people of one id apart there)
		glob := a.Asserting("alice").Create(map[string]any{"name": "alice's at global", "start": false})
		if got := again.Get(glob.ID); !got.Shared || got.Owner.User != "alice" {
			t.Fatalf("the new alice's partition's view of a global sandbox owned by alice: %+v", got)
		}
		var raw []byte
		old.Call("GET", "/sandboxes/"+priv.ID, nil, http.StatusOK, &raw)
		var v struct{ Owner map[string]any }
		if json.Unmarshal(raw, &v) != nil || v.Owner["partitionId"] != pid("alice-1") {
			t.Fatalf("the owner's partitionId: %s", raw)
		}
	}},
}
