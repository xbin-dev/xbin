package broker

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/delegation"
	"github.com/xbin-dev/xbin/internal/users"
	"github.com/xbin-dev/xbin/internal/util"
)

// accessWorld is changeSetWorld with owners and an org admin: alice owns
// apps/x, the org fin owns apps/pg (erin is its admin, carol a member),
// apps/pu is the workspace's; bob is a workspace admin.
func accessWorld(t *testing.T) *csWorld {
	t.Helper()
	w := changeSetWorld(t)
	if _, err := w.b.Users.Upsert(users.User{ID: "erin", Name: "Erin Ops", Role: users.RoleUser}, "password1"); err != nil {
		t.Fatal(err)
	}
	for _, u := range []users.User{{ID: "carol", Name: "Carol Fin", Role: users.RoleUser, Tiles: map[string]string{"apps/q": users.LevelRead, "apps/x": users.LevelRead}},
		{ID: "alice", Name: "Alice Smith", Role: users.RoleUser, Tiles: map[string]string{"apps/*": users.LevelRead, "users/*": users.LevelRead}}} {
		if _, err := w.b.Users.Upsert(u, ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := w.b.Users.UpsertOrg(users.Org{ID: "fin", Name: "Finance", Members: []users.Member{{ID: "carol", Level: users.LevelRead}, {ID: "erin", Level: users.LevelRead, Admin: true}}}); err != nil {
		t.Fatal(err)
	}
	for tile, owner := range map[string]string{"apps/x": "user:alice", "apps/pg": "org:fin", "users/alice/mcp": "user:alice"} {
		if err := w.b.Users.SetOwner(tile, owner); err != nil {
			t.Fatal(err)
		}
	}
	// carol and erin have agents too (apps/q stands in for the workspace agent)
	for _, u := range []string{"carol", "erin"} {
		if u == "erin" {
			_ = w.b.Users.SetUserTile("erin", "apps/q", users.LevelRead)
		}
		w.a.RegisterInstancePartition("d-"+u, "apps/q", "", util.UserPartition(u), "uid-"+u)
		h, err := w.store.NewAgentSession(u, "apps/q", nil, "local/test")
		if err != nil {
			t.Fatal(err)
		}
		csHandles.Store(u, h.ID)
	}
	return w
}

func accessOp(tile, user, level string) delegation.ChangeOp {
	return delegation.ChangeOp{Kind: ChangeAccessSet, Args: mustJSON(delegation.AccessArgs{Tile: tile, User: user, Level: level})}
}

// byHand runs PUT /access {tile, kind: user, id, level} — the tile's access
// dialog's call, the handler itself — as p, puts the entry back as it was,
// and answers the status.
func (w *csWorld) byHand(t *testing.T, p auth.Principal, tile, id, level string) int {
	t.Helper()
	var prev string
	var had bool
	if u, ok := w.b.Users.Get(id); ok {
		prev, had = u.Tiles[tile]
	}
	body, _ := json.Marshal(map[string]string{"tile": tile, "kind": "user", "id": id, "level": level})
	r := httptest.NewRequest(http.MethodPut, "/api/xbin/access", strings.NewReader(string(body))).WithContext(auth.WithPrincipal(context.Background(), p))
	rec := httptest.NewRecorder()
	w.b.apiAccessPut(rec, r)
	if rec.Code == http.StatusOK {
		if !had {
			prev = ""
		}
		if err := w.b.Users.SetUserTile(id, tile, prev); err != nil {
			t.Fatal(err)
		}
	}
	return rec.Code
}

// stripped is p with every admin right taken away: no workspace admin role,
// no org's adminship — the person with their own hands only.
func stripped(p auth.Principal) auth.Principal {
	u := *p.User
	u.Role = users.RoleUser
	return auth.Principal{UserID: p.UserID, User: &u, Via: "session"}
}

// covers D246 — an access.set line is personal exactly where the by-hand
// rule needs no admin right: over a random corpus of people (owner, member,
// org admin, workspace admin, a disabled account, the root token — and an
// admin viewing as someone, who describes nothing), tiles (a person's, an org's, the workspace's,
// partitioned ones — apps/pg, apps/pu —, one at agent level "off", one a
// disabled account owns), exact `none` exclusions in place, and targets
// (ids, display names, a disabled account, spellings the store keys apart:
// non-ASCII), a line is personal ⇔ PUT /access by that person with every
// admin right taken away passes the handler's gate; and an admin's line ⇔
// it passes with their full rights and not without them.
func TestAccessLinePersonalIsByHand(t *testing.T) {
	w := accessWorld(t)
	for _, u := range []users.User{{ID: "dana", Name: "Dana Off", Role: users.RoleUser, Disabled: true}, {ID: "sam", Name: "Sam", Role: users.RoleUser}} {
		if _, err := w.b.Users.Upsert(u, "password1"); err != nil {
			t.Fatal(err)
		}
	}
	w.write(map[string]string{"apps/off/xbin.json": `{"runtime":"go","agent":"off"}`, "apps/off/backend/main.go": "package main\n",
		"apps/d/xbin.json": `{"runtime":"go"}`, "apps/d/backend/main.go": "package main\n"})
	w.rescan()
	for tile, owner := range map[string]string{"apps/off": "user:alice", "apps/d": "user:dana"} {
		if err := w.b.Users.SetOwner(tile, owner); err != nil {
			t.Fatal(err)
		}
	}
	for _, e := range [][3]string{{"erin", "users/alice/mcp", "none"}, {"carol", "apps/off", "none"}, {"sam", "apps/pu", "none"}} {
		if err := w.b.Users.SetUserTile(e[0], e[1], e[2]); err != nil {
			t.Fatal(err)
		}
	}
	people := []string{"alice", "bob", "carol", "erin", "sam", "dana", "bob-as-alice", "root"}
	tiles := []string{"apps/x", "apps/pg", "apps/pu", "users/alice/mcp", "apps/off", "apps/d"}
	targets := []string{"alice", "bob", "carol", "erin", "zed", "Erin Ops", "Carol Fin", "dana", "Dana Off", "ſam", "ＢＯＢ", "SAM"}
	levels := []string{"none", "read", "write", "terminal", ""}
	principal := func(who string) (p, plain auth.Principal) {
		switch who {
		case "root":
			return auth.Principal{Owner: true, Via: "token"}, auth.Principal{Via: "token"}
		case "bob-as-alice":
			p = personP(t, w.partWS, "alice")
			p.Impersonator = "bob"
			return p, stripped(p)
		}
		p = personP(t, w.partWS, who)
		return p, stripped(p)
	}
	rng := rand.New(rand.NewSource(246))
	seen := map[string]int{}
	for range 1200 {
		who, tile, target, level := people[rng.Intn(len(people))], tiles[rng.Intn(len(tiles))], targets[rng.Intn(len(targets))], levels[rng.Intn(len(levels))]
		p, plain := principal(who)
		pv, err := w.b.PreviewPlanned(p, accessOp(tile, target, level), nil)
		seen["who:"+who]++
		if p.ViewAs() { // someone viewing as another person describes no change of theirs
			if err == nil {
				t.Errorf("a view-as session described %s → %s on %s: %+v", target, level, tile, pv)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s %s %s %s: %v", who, tile, target, level, err)
		}
		var call struct{ ID string }
		_ = json.Unmarshal(pv.Call.Body, &call)
		self := p.UserID != "" && (users.NormalizeID(call.ID) == users.NormalizeID(p.UserID) || nameKey(target) == nameKey(p.User.Name))
		plainGate := w.byHand(t, plain, tile, call.ID, level) != http.StatusForbidden
		fullGate := w.byHand(t, p, tile, call.ID, level) != http.StatusForbidden
		personal := !pv.Admin && pv.Blocked == ""
		disabled := p.User != nil && p.User.Disabled
		switch {
		case self && !p.IsAdmin():
			if pv.Blocked == "" || pv.Admin {
				t.Errorf("%s's own access on %s through their agent: blocked %q admin %v — a person who is no workspace admin never changes their own level through it", who, tile, pv.Blocked, pv.Admin)
			}
			seen["self-blocked"]++
		case self:
			if !pv.Admin || !hasWarning(pv, "YOUR OWN ACCESS") {
				t.Errorf("an admin's own access: admin %v warnings %v — never personal, always warned", pv.Admin, pv.Warnings)
			}
			seen["self-admin"]++
		case personal:
			if !plainGate || disabled {
				t.Errorf("%s: %s → %s on %s is personal, and the by-hand route refuses it without admin rights (or the account is disabled)", who, target, level, tile)
			}
			seen["personal"]++
		case pv.Forbidden:
			if fullGate {
				t.Errorf("%s: %s on %s says the person may not change it, and the by-hand route lets them", who, target, tile)
			}
			seen["forbidden"]++
		case pv.Admin && pv.Blocked == "":
			// (a disabled account's session never reaches the handler — the auth layer refuses it —
			// so its own hands are no right: its line is never personal)
			if plainGate && !disabled || !fullGate {
				t.Errorf("%s: %s on %s is an admin's line, and by hand: plain %v full %v", who, target, tile, plainGate, fullGate)
			}
			seen["admin"]++
		}
		if !pv.Forbidden && !(self && !p.IsAdmin()) && plainGate && !disabled && !personal && pv.Blocked == "" {
			t.Errorf("%s: %s → %s on %s passes the by-hand gate with no admin right and is called an admin's", who, target, level, tile)
		}
	}
	for _, k := range []string{"personal", "admin", "forbidden", "self-blocked", "self-admin"} {
		if seen[k] == 0 {
			t.Errorf("the corpus has no %s line: the property is checked on nothing (%v)", k, seen)
		}
	}
	for _, who := range people {
		if seen["who:"+who] == 0 {
			t.Errorf("the corpus has no line by %s (%v)", who, seen)
		}
	}
}

func hasWarning(pv delegation.ChangePreview, tag string) bool {
	for _, w := range pv.Warnings {
		if strings.HasPrefix(w, tag+":") {
			return true
		}
	}
	return false
}

// covers D246 — the line is xbind's, with the account resolved only for a
// person who sees accounts; terminal and a person's own level are warned;
// names that aren't names, the agent itself, a powerful tile and a tile a
// plan is still to make are no change at all.
func TestAccessLineWords(t *testing.T) {
	w := accessWorld(t)
	bob, alice := personP(t, w.partWS, "bob"), personP(t, w.partWS, "alice")
	pv, err := w.b.PreviewChange(bob, accessOp("apps/pu", "Carol Fin", "terminal"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(pv.Line, "carol — “Carol Fin”: no access → terminal by their own entry on apps/pu") || !pv.Admin || !hasWarning(pv, "TERMINAL") || pv.Account != "carol" {
		t.Errorf("an admin's line: %q admin %v warnings %v account %q", pv.Line, pv.Admin, pv.Warnings, pv.Account)
	}
	if pv.Inverse == nil || !strings.Contains(string(pv.Inverse.Args), `"level":""`) {
		t.Errorf("its undo removes the entry it adds: %+v", pv.Inverse)
	}
	// alice sees no accounts: her own tile's line names the account as typed and reads nothing of it
	for _, target := range []string{"carol", "zed"} {
		pv, _ := w.b.PreviewChange(alice, accessOp("apps/x", target, "read"))
		if pv.Admin || pv.Blocked != "" || pv.Before != nil || pv.Account != "" || !strings.Contains(pv.Line, "as typed") {
			t.Errorf("alice's personal line about %s: admin %v blocked %q before %s account %q line %q", target, pv.Admin, pv.Blocked, pv.Before, pv.Account, pv.Line)
		}
		if a, b := w.b.ResolveAccount(alice, target), (delegation.AccountMatch{State: "unseen"}); a != b {
			t.Errorf("alice resolves %s: %+v", target, a)
		}
	}
	// a name no account has, or two accounts share, can't be applied for an admin
	if _, err := w.b.Users.Upsert(users.User{ID: "dave2", Name: "Carol Fin", Role: users.RoleUser}, "password1"); err != nil {
		t.Fatal(err)
	}
	for target, want := range map[string]string{"zed": "no account is named", "Carol Fin": "more than one account"} {
		if pv, _ := w.b.PreviewChange(bob, accessOp("apps/pu", target, "read")); !strings.Contains(pv.Blocked, want) {
			t.Errorf("%s: blocked %q", target, pv.Blocked)
		}
	}
	for _, args := range []string{
		`{"tile":"apps/x","user":"carol","level":"admin"}`,
		`{"tile":"apps/x","user":"car‮ol","level":"read"}`,
		`{"tile":"apps/x","user":"","level":"read"}`,
		`{"tile":"apps x","user":"carol","level":"read"}`,
		`{"tile":"apps/q","user":"carol","level":"read"}`,
		`{"tile":"apps/x","user":"carol","level":"read","role":"admin"}`,
	} {
		if _, err := w.b.PreviewChange(bob, delegation.ChangeOp{Kind: ChangeAccessSet, Args: json.RawMessage(args)}); err == nil {
			t.Errorf("%s is a line", args)
		}
	}
	if _, err := w.b.PreviewPlanned(bob, accessOp("apps/new", "carol", "read"), []delegation.PlannedTile{{N: 1, Path: "apps/new"}}); err == nil {
		t.Error("a person's access on a tile the plan is still to make: a line that would wait")
	}
}

// covers D246 — the lookup's answer to a person who sees no accounts is the
// same for every name: N random names, some of them accounts, answer alike.
func TestLookupNoOracle(t *testing.T) {
	w := accessWorld(t)
	alice := personP(t, w.partWS, "alice")
	bob := personP(t, w.partWS, "bob")
	rng := rand.New(rand.NewSource(46))
	names := []string{"bob", "carol", "erin", "Erin Ops", "dave", "alice"}
	for range 40 {
		names = append(names, fmt.Sprintf("u%x", rng.Int63()))
	}
	want := delegation.AccountMatch{State: "unseen"}
	for _, n := range names {
		if got := w.b.ResolveAccount(alice, n); got != want {
			t.Errorf("alice looks up %q: %+v — exists and doesn't must answer alike", n, got)
		}
		got := w.b.ResolveAccount(bob, n)
		_, exists := w.b.Users.Get(n)
		if got.State == "unseen" || (got.State == "one") != (exists || n == "Erin Ops") {
			t.Errorf("an admin looks up %q: %+v", n, got)
		}
	}
	// exact only: no prefix, no substring, no case-folded id prefix
	for _, n := range []string{"car", "Erin", "erin ops x", "CAROL "} {
		got := w.b.ResolveAccount(bob, n)
		if got.State == "one" && !strings.EqualFold(strings.TrimSpace(n), got.ID) {
			t.Errorf("%q resolves to %+v: only an exact id or display name may", n, got)
		}
	}
}

// covers D246 — any line on a system component is warned (it starts
// unticked), and its level is read or none there, as the users store has
// it; the workspace agent itself is no line at all.
func TestAccessLineSystemComponent(t *testing.T) {
	b := sealedBrokerNoBarrier(t)
	for _, u := range []users.User{{ID: "bob", Role: users.RoleAdmin}, {ID: "dave", Name: "Dave", Role: users.RoleUser}} {
		if _, err := b.Users.Upsert(u, "password1"); err != nil {
			t.Fatal(err)
		}
	}
	u, _ := b.Users.Get("bob")
	acc, _ := b.Users.Access("bob")
	bob := auth.Principal{UserID: "bob", User: u, Access: acc, Via: "session"}
	pv, err := b.PreviewChange(bob, accessOp("xbin/fix", "dave", "none"))
	if err != nil || !hasWarning(pv, "SYSTEM COMPONENT") || !pv.Admin || pv.Blocked != "" {
		t.Fatalf("a system component's line: %v %+v", err, pv)
	}
	// it holds a capability: nobody is given it by a change set (as an org's share)
	if _, err := b.PreviewChange(bob, accessOp("xbin/fix", "dave", "read")); err == nil || !strings.Contains(err.Error(), "holds power") || !strings.Contains(err.Error(), "cap:open-links (xbin/fix)") {
		t.Errorf("read on a system component that holds a capability: %v", err)
	}
	if got := sealedEntryOK("xbin/fix", "write"); !strings.Contains(got, "read or none") {
		t.Errorf("write on a system component: %q", got)
	}
	defer auth.TestSealDelegate("xbin/fix")()
	if _, err := b.PreviewChange(bob, accessOp("xbin/fix", "dave", "none")); err == nil || !strings.Contains(err.Error(), "workspace agent") {
		t.Errorf("a person's access to a workspace agent through a change set: %v", err)
	}
}
