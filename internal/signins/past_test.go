package signins

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// covers D245 (the sticky condition) — once someone else could change a
// tile's code, the tile asks for each person's trust even after it is
// "only theirs" again: the record, per tile identity, of who could — made
// when a second party is seen or the one leaves, never for a tile that was
// only ever one party's, never in a one-person workspace.
func TestPastRemembersWhoCouldChangeATile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "signins", "tiles.json")
	now := time.UnixMilli(1_800_000_000_000)
	p := NewPast(path, func() time.Time { return now })
	noFile := func(when string) {
		t.Helper()
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("%s: a record was written: %v", when, err)
		}
		if p.Recorded() != 0 {
			t.Fatalf("%s: %d tiles recorded", when, p.Recorded())
		}
	}
	others := func(ident, user string) []string { return p.Others(ident, user).Parties }

	// a one-person workspace: the workspace's own tiles, nobody with write
	// but admins — nothing is ever recorded
	for i := 0; i < 3; i++ {
		p.Observe("i-ws", "apps/welcome", "", nil)
		p.Observe("i-ws2", "apps/notes", "", nil)
	}
	noFile("a one-person workspace")
	// a tile its maker owns and never shared: nothing either
	p.Observe("i-hers", "apps/hers", "user:alice", nil)
	p.Observe("i-hers", "apps/hers", "user:alice", []string{"alice"}) // (herself, as an editor entry)
	noFile("a tile only ever one person's")
	if got := others("i-hers", "alice"); got != nil {
		t.Fatalf("others on her own tile: %v", got)
	}
	// no identity: nothing
	p.Observe("", "apps/x", "user:bob", []string{"mallory"})
	noFile("a tile without an identity")

	// mallory gets write on alice's tile: recorded — and while she can, she
	// is no "past" party (the question names her as an editor)
	p.Observe("i-hers", "apps/hers", "user:alice", []string{"mallory"})
	if p.Recorded() != 1 || others("i-hers", "alice") != nil {
		t.Fatalf("while mallory can change it: recorded %d, past %v", p.Recorded(), others("i-hers", "alice"))
	}
	// she loses it again: the tile is "only alice's" now — and remembers
	then := now
	now = now.Add(48 * time.Hour)
	p.Observe("i-hers", "apps/hers", "user:alice", nil)
	was := p.Others("i-hers", "alice")
	got, until := was.Parties, was.Until
	if !reflect.DeepEqual(got, []string{"mallory"}) || until != now.UnixMilli() {
		t.Fatalf("after mallory lost write: %v until %d (then %d)", got, until, then.UnixMilli())
	}
	// …for mallory herself the other party is alice's ownership, which is current: nobody past
	if got := others("i-hers", "mallory"); got != nil {
		t.Fatalf("mallory's view of her own past: %v", got)
	}
	// it survives a restart
	p = NewPast(path, func() time.Time { return now })
	if got := others("i-hers", "alice"); !reflect.DeepEqual(got, []string{"mallory"}) {
		t.Fatalf("after a restart: %v", got)
	}
	// she comes back: current again, not past; and leaves: past, with the later date
	p.Observe("i-hers", "apps/hers", "user:alice", []string{"mallory"})
	if got := others("i-hers", "alice"); got != nil {
		t.Fatalf("mallory back: %v", got)
	}
	now = now.Add(time.Hour)
	p.Observe("i-hers", "apps/hers", "user:alice", nil)
	if until := p.Others("i-hers", "alice").Until; until != now.UnixMilli() {
		t.Fatalf("until after she left again: %d", until)
	}

	// an owner before: bob's tile handed to carol — bob could change it
	p.Observe("i-crm", "apps/crm", "user:bob", nil) // (in memory: one party)
	p.Observe("i-crm", "apps/crm", "user:carol", nil)
	if got := others("i-crm", "carol"); !reflect.DeepEqual(got, []string{"bob"}) {
		t.Fatalf("after the transfer, for carol: %v", got)
	}
	// an org's tile handed to a person; a person's tile handed to the workspace
	p.Observe("i-org", "apps/org", "org:acme", nil)
	p.Observe("i-org", "apps/org", "user:dave", nil)
	if got := others("i-org", "dave"); !reflect.DeepEqual(got, []string{"org:acme"}) {
		t.Fatalf("an org's tile that became dave's: %v", got)
	}
	p.Observe("i-gift", "apps/gift", "user:erin", nil)
	p.Observe("i-gift", "apps/gift", "", nil) // now the workspace's: an admin's own — but erin could change it
	if got := others("i-gift", "root"); !reflect.DeepEqual(got, []string{"erin"}) {
		t.Fatalf("a person's tile that became the workspace's, for an admin: %v", got)
	}
	// a workspace tile a non-admin could edit for a while: the admins' exemption is gone
	p.Observe("i-ws", "apps/welcome", "", []string{"frank"})
	p.Observe("i-ws", "apps/welcome", "", nil)
	if got := others("i-ws", "root"); !reflect.DeepEqual(got, []string{"frank"}) {
		t.Fatalf("a workspace tile a non-admin could edit: %v", got)
	}
	// another identity at the same path knows nothing of it
	if got := others("i-hers-remade", "alice"); got != nil {
		t.Fatalf("a tile made again: %v", got)
	}

	// the file: xbind's own, 0600, keyed by identity, no tile content
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("the record's file: %v %v", fi, err)
	}
	b, _ := os.ReadFile(path)
	var f pastFile
	if err := json.Unmarshal(b, &f); err != nil || f.Tiles["i-hers"] == nil || f.Tiles["i-hers"].Tile != "apps/hers" {
		t.Fatalf("the record: %s %v", b, err)
	}
	// an unreadable record starts again rather than stopping anything
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	p = NewPast(path, nil)
	if p.Recorded() != 0 {
		t.Fatal("an unreadable record was read")
	}
}

// covers D245 — the sticky condition in the rule, and what an answer means
// in its basis: a tile that is only the person's now but that someone else
// could change before needs their trust; an answer given for less than a
// trust means now (before D245: the sign-in alone) is void and asked again;
// a card drawn with the old meaning is refused as stale.
func TestTrustCoversThePastAndWhatItMeans(t *testing.T) {
	w := newWorld(t)
	own := Source{Tile: "apps/hers", ID: "i-hers", Owner: "user:alice", OwnerKind: "you"}
	if own.NeedsTrust() {
		t.Fatal("her own tile, never shared, needs trust")
	}
	past := own
	past.Past, past.PastUntil = []string{"mallory"}, w.now.UnixMilli()
	if !past.NeedsTrust() {
		t.Fatal("a tile mallory could change before needs no trust")
	}
	if past.Basis() == own.Basis() {
		t.Fatal("who could change it before is not part of the basis")
	}
	if w.s.Trusts("alice", past) {
		t.Fatal("trusted before she answered")
	}
	// the card she answers names mallory; an answer for the card without her is stale
	if _, err := w.s.TrustTile("alice", past, "card", own.Basis()); !errors.Is(err, ErrTrustChanged) {
		t.Fatalf("an answer to the card as it was before the record: %v", err)
	}
	tr, err := w.s.TrustTile("alice", past, "card", past.Basis())
	if err != nil || tr.Means != TrustMeans || !reflect.DeepEqual(tr.Past, []string{"mallory"}) {
		t.Fatalf("her answer: %+v %v", tr, err)
	}
	if !w.s.Trusts("alice", past) {
		t.Fatal("her answer does not hold")
	}
	// mallory gains write again: someone new — void, as ever
	again := own
	again.Editors = []string{"mallory"}
	if w.s.Trusts("alice", again) {
		t.Fatal("mallory back with write, and the old answer covers it")
	}

	// a card drawn by an xbind that asked for less: its basis is another
	old := "tb_" + strings.TrimPrefix(past.Basis(), "tb2_")
	if _, err := w.s.TrustTile("alice", past, "card", old); !errors.Is(err, ErrTrustChanged) {
		t.Fatalf("an answer to a card of before D245: %v", err)
	}
	// an answer kept from before D245 (no meaning recorded): void, with the reason — asked again
	src := bobs("carol")
	if _, err := w.s.TrustTile("alice", src, "card", src.Basis()); err != nil {
		t.Fatal(err)
	}
	p := w.s.path("alice")
	b, _ := os.ReadFile(p)
	if !strings.Contains(string(b), `"means": 2`) {
		t.Fatalf("her file does not record what the answer means: %s", b)
	}
	stale := strings.Replace(string(b), `"means": 2,`, "", -1)
	if strings.Contains(stale, `"means"`) {
		t.Fatalf("could not make a pre-D245 file: %s", stale)
	}
	if err := os.WriteFile(p, []byte(stale), 0o600); err != nil {
		t.Fatal(err)
	}
	if w.s.Trusts("alice", src) {
		t.Fatal("an answer given before a trust meant the home too still holds")
	}
	for _, got := range w.s.Trusted("alice") {
		if got.Tile == src.Tile && (!got.Void() || got.VoidWhy != VoidMeans) {
			t.Fatalf("the old answer: %+v", got)
		}
	}
	// answered again, with the words as they are now: it holds
	if _, err := w.s.TrustTile("alice", src, "card", src.Basis()); err != nil || !w.s.Trusts("alice", src) {
		t.Fatalf("answered again: %v", err)
	}
}

// The review's gaps in the record (D245): a former workspace admin, a tile
// whose identity changed, a full record, a workspace with one account.
func TestPastAdminsIdentityAndTheCap(t *testing.T) {
	now := time.UnixMilli(1_800_000_000_000)
	path := filepath.Join(t.TempDir(), "signins", "tiles.json")
	p := NewPast(path, func() time.Time { return now })

	// a workspace with one account that could ever change anything: an
	// admin's own tiles have no party (the broker passes no owner for an
	// admin's), whatever they own or hand on — nothing is written
	p.ObserveAdmins([]string{"root"})
	p.Observe("i-a", "apps/a", "", nil) // root's own tile…
	p.Observe("i-a", "apps/a", "", nil) // …handed to the workspace
	p.ObserveAdmins([]string{"root"})
	if _, err := os.Stat(path); !os.IsNotExist(err) || p.Recorded() != 0 {
		t.Fatalf("a one-account workspace recorded something: %v (%d)", err, p.Recorded())
	}

	// a second admin, then demoted: they could change EVERY tile — noted
	// once, workspace-wide, and named on every tile's question
	p.ObserveAdmins([]string{"root", "zed"})
	if p.Recorded() != 0 {
		t.Fatal("an admin being an admin was recorded")
	}
	now = now.Add(time.Hour)
	p.ObserveAdmins([]string{"root"})
	was := p.Others("i-a", "alice")
	if !reflect.DeepEqual(was.Parties, []string{"zed"}) || !reflect.DeepEqual(was.Admins, []string{"zed"}) || was.Until != now.UnixMilli() {
		t.Fatalf("after zed stopped being an admin, for alice on any tile: %+v", was)
	}
	if got := p.Others("i-never-seen", "bob"); !reflect.DeepEqual(got.Admins, []string{"zed"}) {
		t.Fatalf("…on a tile with no record of its own: %+v", got)
	}
	if got := p.Others("i-a", "zed"); len(got.Parties) != 0 {
		t.Fatalf("zed's own view names zed: %+v", got)
	}
	// it survives a restart; an admin again is in the trust base again
	p = NewPast(path, func() time.Time { return now })
	p.ObserveAdmins([]string{"root"})
	if got := p.Others("i-a", "alice"); !reflect.DeepEqual(got.Admins, []string{"zed"}) {
		t.Fatalf("after a restart: %+v", got)
	}
	p.ObserveAdmins([]string{"root", "zed"})
	if got := p.Others("i-a", "alice"); len(got.Parties) != 0 {
		t.Fatalf("zed is an admin again and still named: %+v", got)
	}
	p.ObserveAdmins([]string{"root"})

	// a tile's IDENTITY changes (a restore, another disk, made again): the
	// record of the tile at that path is this tile's — never "only yours" again
	p.Observe("i-crm", "apps/crm", "user:alice", []string{"mallory"})
	now = now.Add(time.Hour)
	p.Observe("i-crm", "apps/crm", "user:alice", nil) // mallory lost write
	if got := p.Others("i-crm", "alice").Parties; !reflect.DeepEqual(got, []string{"mallory", "zed"}) {
		t.Fatalf("before the identity changed: %v", got)
	}
	p.Observe("i-crm-RESTORED", "apps/crm", "user:alice", nil)
	if got := p.Others("i-crm-RESTORED", "alice").Parties; !reflect.DeepEqual(got, []string{"mallory", "zed"}) {
		t.Fatalf("the restored tile forgot who could change it: %v", got)
	}
	if got := p.Others("i-crm", "alice").Parties; !reflect.DeepEqual(got, []string{"mallory", "zed"}) {
		t.Fatalf("the old identity's record was pruned: %v", got)
	}
	// …someone who COULD when the old record was last written is carried as past
	p.Observe("i-wiki", "apps/wiki", "user:alice", []string{"eve"})
	p.Observe("i-wiki-2", "apps/wiki", "user:alice", nil)
	if got := p.Others("i-wiki-2", "alice").Parties; !reflect.DeepEqual(got, []string{"eve", "zed"}) {
		t.Fatalf("eve, who could change the tile that stood there: %v", got)
	}

	// the cap: a record that is full says "and others" — it still asks
	var many []string
	for i := 0; i < MaxPastParties+5; i++ {
		many = append(many, fmt.Sprintf("u%03d", i))
	}
	p.Observe("i-big", "apps/big", "", many)
	p.Observe("i-big", "apps/big", "", nil)
	big := p.Others("i-big", "alice")
	if !big.More || len(big.Parties) != MaxPastParties+1 { // (and zed)
		t.Fatalf("a full record: more %v, %d parties", big.More, len(big.Parties))
	}
	src := Source{Tile: "apps/big", ID: "i-big", Owner: "user:alice", OwnerKind: "you", PastMore: true}
	if !src.NeedsTrust() || src.Basis() == (Source{Tile: "apps/big", ID: "i-big", Owner: "user:alice", OwnerKind: "you"}).Basis() {
		t.Fatal("\"and others\" neither asks nor is part of the basis")
	}
}

// An answer covers who could change the tile before as its card named
// them. Someone who left after it was given stays covered (they were
// trusted while they could); a former admin recorded after it is not: the
// tile asks again, once.
func TestTrustCoversWhoItNamed(t *testing.T) {
	w := newWorld(t)
	src := bobs("carol")
	if _, err := w.s.TrustTile("alice", src, "card", src.Basis()); err != nil {
		t.Fatal(err)
	}
	// carol loses write: she is past now — and covered (the card named her)
	left := bobs()
	left.Past, left.PastUntil = []string{"carol"}, w.now.UnixMilli()
	if !w.s.Trusts("alice", bobs()) || !w.s.Trusts("alice", left) || !w.s.Trusts("alice", left) {
		t.Fatal("someone the answer named, who left since, voided it")
	}
	// zed stops being a workspace admin: every tile names him — this answer never did
	zed := left
	zed.Past, zed.PastAdmins = []string{"carol", "zed"}, []string{"zed"}
	if w.s.Trusts("alice", zed) {
		t.Fatal("an answer given before zed stopped being an admin covers him")
	}
	for _, tr := range w.s.Trusted("alice") {
		if tr.Tile == src.Tile && tr.VoidWhy != VoidPast {
			t.Fatalf("the answer: %+v", tr)
		}
	}
	// asked again, for the card that names him: it holds
	if _, err := w.s.TrustTile("alice", zed, "card", zed.Basis()); err != nil || !w.s.Trusts("alice", zed) {
		t.Fatalf("answered again: %v", err)
	}
	// a record that got full since: "and others" was not on the card
	more := zed
	more.PastMore = true
	if w.s.Trusts("alice", more) {
		t.Fatal("\"and others\" is covered by an answer that never said it")
	}
}

// The root token keeps trust answers of its own (the seam): under the key
// of its home, for the source as the workspace's own admin sees it.
func TestRootTokenTrust(t *testing.T) {
	w := newWorld(t)
	inner := func(user, tile string) (Source, bool) {
		if user != "" {
			t.Errorf("the inner source was asked for %q", user)
		}
		switch tile {
		case "apps/ws":
			return Source{Tile: tile, ID: "i-ws", OwnerKind: "workspace", NotOwn: true}, true // (as for nobody: not an admin)
		case "apps/crm":
			return bobs(), true
		}
		return Source{}, false
	}
	source := RootTokenSource(inner)
	ws, _ := source(RootTokenUser, "apps/ws")
	if ws.NeedsTrust() || !ws.Admin {
		t.Fatalf("the workspace's tile, for the root token: %+v", ws)
	}
	crm, ok := source(RootTokenUser, "apps/crm")
	if !ok || !crm.NeedsTrust() || w.s.Trusts(RootTokenUser, crm) {
		t.Fatalf("bob's tile, for the root token: %+v", crm)
	}
	if _, err := w.s.TrustTile(RootTokenUser, crm, "card", crm.Basis()); err != nil {
		t.Fatalf("the root token's answer: %v", err)
	}
	if !w.s.Trusts(RootTokenUser, crm) || len(w.s.Trusted(RootTokenUser)) != 1 || w.s.Trusts("alice", crm) {
		t.Fatal("the root token's answer does not hold, or is someone else's too")
	}
	// held to the tile as it stands, like a person's: mallory gains write
	if n := w.s.Revalidate(func(user, tile string) (Source, bool) {
		src, ok := source(user, tile)
		src.Editors = []string{"mallory"}
		return src, ok
	}); n != 1 || w.s.Trusts(RootTokenUser, crm) {
		t.Fatalf("revalidated %d of the root token's answers", n)
	}
	// it never has a saved sign-in
	if _, _, err := w.s.Save(RootTokenUser, "claude", "Work", "sk-ant-oat01-"+strings.Repeat("x", 40), false); err == nil {
		t.Fatal("a sign-in was saved for the root token")
	}
}
