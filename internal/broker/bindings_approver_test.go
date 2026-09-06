package broker

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/users"
)

// The matrix behind "an org admin could still bind host": who may wire a net
// slot on which tile, through the real POST /bindings handler, with the org's
// one network set = the whole internet (the reported setup). Server-side the
// gate held — the UIs were what misreported it — so this pins every cell:
//
//   - org admin on an org tile: inside the set (internet, org, none) ok;
//     host / an uncovered LAN 403 (D26 caller side: no allowance covers it);
//   - org admin on a PERSONAL tile (anyone's, including their own): 403 —
//     personal tiles are wired by workspace admins only (D54 keeps pre-D54
//     rules there; the org's sets do not govern them);
//   - the tile's owner / a member: 403 (agents and owners never self-bind);
//   - workspace admin on the org tile: host 400 "not covered" (D54 —
//     ws-admins widen the set instead), internet ok; on a personal tile host
//     ok (documented behaviour, no org ceiling applies).
func TestBindingApproverMatrix(t *testing.T) {
	b, st := netSetFixture(t, "")
	if err := st.UpsertNetSet("inet", users.NetSet{Rules: []string{"internet"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetOrgNetSets("sales", []string{"inet"}); err != nil {
		t.Fatal(err)
	}
	// carol's own personal tile — "a new tile created by that user".
	if err := st.SetOwner("apps/othervpn", "user:carol"); err != nil {
		t.Fatal(err)
	}
	carol := principalFor(t, st, "carol") // sales org admin, NOT a workspace admin
	bob := principalFor(t, st, "bob")     // sales member, owns apps/mine
	admin := auth.Principal{Owner: true}

	bind := func(p auth.Principal, comp, ref string) (int, string) {
		t.Helper()
		body := `{"component":"` + comp + `","slot":"net","provider":"` + ref + `"}`
		w := call(t, b.apiBindingSet, p, "POST", "/bindings", body, nil)
		return w.Code, w.Body.String()
	}
	unbind := func(comp string) {
		t.Helper()
		call(t, b.apiBindingSet, admin, "DELETE", "/bindings", `{"component":"`+comp+`","slot":"net"}`, nil)
	}
	expect := func(who string, p auth.Principal, comp string, want map[string]int) {
		t.Helper()
		for ref, code := range want {
			got, body := bind(p, comp, ref)
			if got != code {
				t.Errorf("%s binds %s on %s: want %d, got %d (%s)", who, ref, comp, code, got, strings.TrimSpace(body))
			}
			if got == 200 {
				unbind(comp)
			}
		}
	}

	// Org admin, org tile: the set is the allowance and the ceiling.
	expect("carol", carol, "apps/bot", map[string]int{
		"internet": 200, NetRefOrg: 200, NetRefNone: 200,
		"host": 403, "lan:10.0.0.0/8": 403, "apps/vpn": 200, // same-org provider needs no rule
	})
	// Org admin, personal tiles (a member's, and her own): never.
	for _, tile := range []string{"apps/mine", "apps/othervpn"} {
		expect("carol", carol, tile, map[string]int{"internet": 403, "host": 403, NetRefNone: 403})
	}
	// The owner himself: never (owners don't self-wire).
	expect("bob", bob, "apps/mine", map[string]int{"internet": 403, "host": 403})
	// Workspace admin: the org's set still caps org tiles; personal tiles
	// keep the pre-D54 rules.
	code, body := bind(admin, "apps/bot", "host")
	if code != 400 || !strings.Contains(body, "not covered") || !strings.Contains(body, "inet") {
		t.Fatalf("ws-admin host on an org tile: want 400 naming the set, got %d %s", code, body)
	}
	expect("admin", admin, "apps/bot", map[string]int{"internet": 200, "lan:10.0.0.0/8": 400})
	expect("admin", admin, "apps/mine", map[string]int{"host": 200, "internet": 200})

	// The list view tells every UI what to offer. approvable = the tiles this
	// caller may wire; blocked = a choice refused for everyone.
	type view struct {
		Approvable map[string]bool `json:"approvable"`
		Pending    []pendingBind   `json:"pending"`
	}
	list := func(p auth.Principal) view {
		t.Helper()
		w := call(t, b.apiBindingsList, p, "GET", "/bindings", "", nil)
		if w.Code != 200 {
			t.Fatalf("list: %d %s", w.Code, w.Body.String())
		}
		var v view
		if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	row := func(v view, comp string) *pendingBind {
		for i := range v.Pending {
			if v.Pending[i].Component == comp && v.Pending[i].Slot == "net" {
				return &v.Pending[i]
			}
		}
		return nil
	}
	option := func(pb *pendingBind, id string) bindOption {
		for _, o := range pb.Options {
			if o.ID == id {
				return o
			}
		}
		t.Fatalf("no option %s on %s", id, pb.Component)
		return bindOption{}
	}

	cv := list(carol)
	if !cv.Approvable["apps/bot"] || !cv.Approvable["apps/vpn"] {
		t.Fatalf("org admin must be approvable on her org's tiles: %v", cv.Approvable)
	}
	if cv.Approvable["apps/othervpn"] || cv.Approvable["apps/mine"] {
		t.Fatalf("org admin must not be approvable on personal tiles: %v", cv.Approvable)
	}
	bot := row(cv, "apps/bot")
	if bot == nil || !bot.Approvable || bot.Default != NetRefOrg {
		t.Fatalf("org tile pending row: %+v", bot)
	}
	if o := option(bot, "host"); !o.Blocked || !strings.Contains(o.Label, "not covered") {
		t.Fatalf("host must be blocked outside the set: %+v", o)
	}
	if o := option(bot, "internet"); o.Blocked {
		t.Fatalf("internet is inside the set: %+v", o)
	}
	if o := option(bot, "apps/othervpn"); !o.Blocked {
		t.Fatalf("a foreign provider needs a provider: rule: %+v", o)
	}
	if o := option(bot, "apps/vpn"); o.Blocked {
		t.Fatalf("same-org provider is covered: %+v", o)
	}
	// Her own personal tile is in her view (she can write it) but not hers to wire.
	if own := row(cv, "apps/othervpn"); own != nil && own.Approvable {
		t.Fatalf("personal tile must not be approvable by its owner: %+v", own)
	}

	bv := list(bob)
	if len(bv.Approvable) != 0 {
		t.Fatalf("a member approves nothing: %v", bv.Approvable)
	}
	if mine := row(bv, "apps/mine"); mine == nil || mine.Approvable {
		t.Fatalf("owner sees his slot listed, not approvable: %+v", mine)
	} else if o := option(mine, "host"); o.Blocked {
		t.Fatalf("no org sets on a personal tile — nothing is blocked: %+v", o)
	}

	av := list(admin)
	for _, tile := range []string{"apps/bot", "apps/mine", "apps/othervpn"} {
		if !av.Approvable[tile] {
			t.Fatalf("ws admin approves everything: %v", av.Approvable)
		}
	}
	if o := option(row(av, "apps/bot"), "host"); !o.Blocked {
		t.Fatal("blocked is about the tile, not the caller — host stays blocked for admins too")
	}
}
