package broker

import (
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/term"
	"github.com/xbin-dev/xbin/internal/users"
	"github.com/xbin-dev/xbin/internal/util"
)

// covers PD-10 PD-11 PD-17 PD-22 — a new session's partition (06 §1): a
// person on a partitioned tile acts in their own partition (an admin
// too), keyed by their partition id; the root token in global, or without
// the tile API when the tile has none; a session targeting a non-primary
// deployment reaches global but keeps its person's key; a person who can't
// read the tile is refused; a tile that isn't partitioned answers nothing;
// every session on a tile whose switch runs is refused (409's sentinel).
func TestTermPartition(t *testing.T) {
	w := partFx(t)
	b := w.b
	realPartitionIdentity()
	alice := auth.Principal{UserID: "alice", Via: "session"}
	ans, err := b.TermPartition(alice, "apps/docs", "")
	if err != nil {
		t.Fatal(err)
	}
	uid := b.storedPartitionUID("alice")
	if want := (term.Partition{Partitioned: true, Tile: "apps/docs", Part: "user:alice", Key: util.PartitionKey("alice", uid)}); uid == "" || ans != want {
		t.Errorf("alice on apps/docs: %+v, want %+v", ans, want)
	}
	if got := b.PersonPartitionKey("alice"); got != ans.Key {
		t.Errorf("PersonPartitionKey(alice) = %q, want %q", got, ans.Key)
	}
	if got := b.PersonPartitionKey("carol"); got != "" {
		t.Errorf("carol has no uid yet, but a partition id %q (PersonPartitionKey minted)", got)
	}
	term_ := auth.Principal{Component: "apps/docs", UserID: "alice", Via: "terminal"}
	if got, err := b.TermPartition(term_, "apps/docs", ""); err != nil || got != ans {
		t.Errorf("alice's shell token opening a session: %+v %v", got, err)
	}
	if got, err := b.TermPartition(alice, "apps/docs", "dev"); err != nil || got.Part != "global" || got.Key != ans.Key {
		t.Errorf("alice targeting a non-primary deployment: %+v %v", got, err)
	}
	owner := auth.Principal{Owner: true, Via: "cookie"}
	if got, err := b.TermPartition(owner, "apps/docs", ""); err != nil || got != (term.Partition{Partitioned: true, Tile: "apps/docs", Part: "global"}) {
		t.Errorf("the root token on apps/docs (with global): %+v %v", got, err)
	}
	got, err := b.TermPartition(owner, "apps/agent", "")
	if err != nil || !got.Partitioned || got.Part != "" || got.Key != "" || !strings.Contains(got.NoAPI, "sign in as a person") {
		t.Errorf("the root token on apps/agent (no global): %+v %v", got, err)
	}
	if _, err := b.TermPartition(auth.Principal{UserID: "bob", Via: "session"}, "apps/docs", ""); err == nil || !strings.Contains(err.Error(), "can't read") {
		t.Errorf("bob, who can't read apps/docs: %v", err)
	}
	if got, err := b.TermPartition(alice, "apps/plain", ""); err != nil || got != (term.Partition{}) {
		t.Errorf("an unpartitioned tile: %+v %v", got, err)
	}
	if tile, on := b.TermTilePartitioned("apps/docs"); !on || tile != "apps/docs" {
		t.Errorf("TermTilePartitioned(apps/docs) = %q %v", tile, on)
	}
	if _, on := b.TermTilePartitioned("apps/plain"); on {
		t.Error("apps/plain reads partitioned")
	}
	for _, tile := range []string{"apps/docs", "apps/plain"} {
		end, ok := b.beginSwitch(tile, "is paused: switching")
		if !ok {
			t.Fatal("beginSwitch")
		}
		if _, err := b.TermPartition(alice, tile, ""); !errors.Is(err, term.ErrPartitionSwitching) {
			t.Errorf("%s during its switch: %v", tile, err)
		}
		end()
	}
}

// fakeTerms records a switch's calls to the terminal manager.
type fakeTerms struct {
	mu    sync.Mutex
	calls []string
	state func() string // the recorded mode when called
	keys  []string
}

func (f *fakeTerms) StopTileSessions(tile string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "stop "+tile+" "+f.state())
	return nil
}

func (f *fakeTerms) WipePartitionTile(tile string, dry bool) (term.PartitionTileWipe, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if dry {
		f.calls = append(f.calls, "count "+tile)
	} else {
		f.calls = append(f.calls, "wipe "+tile+" "+f.state())
	}
	return term.PartitionTileWipe{Layers: len(f.keys), Keys: f.keys}, nil
}

func (f *fakeTerms) StopPartitionSessions(tile, pkey string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "stop "+tile+" "+pkey)
	return nil
}

func (f *fakeTerms) WipePartitionKey(tile, pkey string, dry bool) (term.PartitionTileWipe, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "wipe "+tile+" "+pkey)
	return term.PartitionTileWipe{Layers: 1, Keys: []string{pkey}}, nil
}

func (f *fakeTerms) take() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.calls
	f.calls = nil
	return out
}

// covers PD-22 PD-44 01§2.6 — a switch that deletes everything stops the
// tile's terminal and agent sessions and deletes its person layers and
// partition history, through the "person-terminals" wipe hook, while the
// old mode is still recorded; the dry run only counts; each person whose
// layer or history goes is told; removing "global" touches none of it.
func TestPartitionTermSwitch(t *testing.T) {
	w := partFx(t)
	b := w.b
	realPartitionIdentity()
	withSeam(t, &partitionIsolated, func() bool { return true })
	if !slices.ContainsFunc(wipeHooks, func(h wipeHook) bool { return h.name == "person-terminals" }) {
		t.Fatal("no person-terminals wipe hook")
	}
	if code, body := nsKV(t, b, "PUT", aliceDocs, "res:apps/docs/docs/k", "alice's"); code != 200 {
		t.Fatalf("alice's write: %d %s", code, body)
	}
	alice, _ := b.TermPartition(auth.Principal{UserID: "alice", Via: "session"}, "apps/docs", "")
	f := &fakeTerms{keys: []string{alice.Key, util.PartitionKey("gone", "0123")}, state: func() string {
		_, r, _ := w.state("apps/docs")
		return r.String()
	}}
	b.SetPartitionTerminals(f)
	t.Cleanup(func() { b.SetPartitionTerminals(nil) })
	act := func(body string) (int, string) {
		t.Helper()
		rec := call(t, b.apiPartitionMode, auth.Principal{Owner: true}, "POST", "/partitions/mode", body, nil)
		return rec.Code, rec.Body.String()
	}

	// removing global: people's partitions stay, so do their terminals
	w.write(map[string]string{"apps/docs/xbin.json": strings.Replace(partFxFiles["apps/docs/xbin.json"], `["user","global"]`, `["user"]`, 1)})
	w.rescan()
	if code, body := act(`{"tile":"apps/docs","act":"switch","from":{"user":true,"global":true},"to":{"user":true},"confirm":"apps/docs"}`); code != 200 {
		t.Fatalf("removing global: %d %s", code, body)
	}
	if got := f.take(); len(got) != 0 {
		t.Errorf("removing global touched the terminals: %q", got)
	}

	// dropping the partition: dry run counts, the switch stops then wipes
	w.write(map[string]string{"apps/docs/xbin.json": strings.Replace(partFxFiles["apps/docs/xbin.json"], `"partition":["user","global"],`, "", 1)})
	w.rescan()
	if st, _, _ := w.state("apps/docs"); st != registry.PartitionPending {
		t.Fatalf("apps/docs is %s, want pending", st)
	}
	if code, body := act(`{"tile":"apps/docs","act":"switch","from":{"user":true},"to":null,"dryRun":true}`); code != 200 {
		t.Fatalf("the dry run: %d %s", code, body)
	}
	if got := f.take(); !slices.Equal(got, []string{"count apps/docs"}) {
		t.Errorf("the dry run's calls: %q", got)
	}
	var told []string
	b.SetPartitionPush(func(user, kind, title, body, link, collapse string) {
		if kind == "tile.partition-deleted" {
			told = append(told, user)
		}
	})
	if code, body := act(`{"tile":"apps/docs","act":"switch","from":{"user":true},"to":null,"confirm":"apps/docs"}`); code != 200 {
		t.Fatalf("the switch: %d %s", code, body)
	}
	if got, want := f.take(), []string{"stop apps/docs user", "stop apps/docs user", "wipe apps/docs user"}; !slices.Equal(got, want) {
		t.Errorf("the switch's calls: %q, want %q (stopped, then wiped, under the old mode)", got, want)
	}
	if !slices.Contains(told, "alice") {
		t.Errorf("alice wasn't told her data went: %q", told)
	}
	if people := b.peopleOfPartitionKeys([]string{alice.Key, "u-nobody"}); !slices.Equal(people, []string{"alice"}) {
		t.Errorf("peopleOfPartitionKeys: %q", people)
	}
	// one partition's end (F7b's reset, purge, sweep): its sessions stop
	// first, then its layers and history go; a dry run only counts
	if got, err := b.wipePersonTerminalsOf("apps/docs", alice.Key, false); err != nil || got.Layers != 1 {
		t.Errorf("wipePersonTerminalsOf: %+v %v", got, err)
	}
	if got, want := f.take(), []string{"stop apps/docs " + alice.Key, "wipe apps/docs " + alice.Key}; !slices.Equal(got, want) {
		t.Errorf("one partition's end: %q, want %q", got, want)
	}
	if _, err := b.wipePersonTerminalsOf("", alice.Key, true); err != nil {
		t.Fatal(err)
	}
	if got := f.take(); !slices.Equal(got, []string{"wipe  " + alice.Key}) {
		t.Errorf("a dry sweep: %q", got)
	}
}

// covers PD-09 PD-11 S2 — one person's terminal can't reach another's
// partition, and an admin's terminal gets no silent path in (06 §Tests
// term/server): a terminal credential on a partitioned tile acts in its
// own person's partition only — what alice's terminal writes, carol's
// terminal and an admin's terminal on the same tile don't read; the admin's
// terminal is in the admin's own partition (PD-11).
func TestTermPartitionReach(t *testing.T) {
	w := partFx(t)
	b := w.b
	realPartitionIdentity()
	if _, err := b.Users.Upsert(users.User{ID: "root", Role: users.RoleAdmin}, "pw-root"); err != nil {
		t.Fatal(err)
	}
	termOf := func(id string) auth.Principal {
		return auth.Principal{Component: "apps/docs", UserID: id, Via: "terminal", Role: "writer"}
	}
	if code, body := nsKV(t, b, "PUT", termOf("alice"), "res:apps/docs/docs/secret", "alice's"); code != 200 {
		t.Fatalf("alice's terminal writes: %d %s", code, body)
	}
	if code, body := nsKV(t, b, "GET", termOf("alice"), "res:apps/docs/docs/secret", ""); code != 200 || !strings.Contains(body, "alice's") {
		t.Fatalf("alice's terminal reads back: %d %s", code, body)
	}
	for _, id := range []string{"carol", "root"} {
		if code, body := nsKV(t, b, "GET", termOf(id), "res:apps/docs/docs/secret", ""); code == 200 || strings.Contains(body, "alice's") {
			t.Errorf("%s's terminal reads alice's partition: %d %s", id, code, body)
		}
		part, err := b.addressedPartition(termOf(id), "apps/docs")
		if err != nil || part != util.UserPartition(id) {
			t.Errorf("%s's terminal acts in %q (%v), want their own", id, part, err)
		}
	}
	// the admin's session opens in the admin's own partition, never another's
	ans, err := b.TermPartition(auth.Principal{UserID: "root", Via: "session"}, "apps/docs", "")
	if err != nil || ans.Part != "user:root" || ans.Key != b.PersonPartitionKey("root") {
		t.Errorf("an admin's session: %+v %v", ans, err)
	}
}
