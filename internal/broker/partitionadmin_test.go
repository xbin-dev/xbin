package broker

// partitionadmin_test.go — what the admin console's runtime → partitions
// view reads (partitionadmin.go; plans/partitions/06 §12.3, PD-46).

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/util"
)

// adminTileFx is the ops fixture with apps/x holding xbin:admin: its frames
// are the admin console's.
func adminTileFx(t *testing.T) *opsFx {
	t.Helper()
	f := newOpsFx(t)
	if err := f.b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) {
		ws.Grants = append(ws.Grants, registry.Grant{From: "apps/x", Target: "xbin", Role: "admin"})
	}); err != nil {
		t.Fatal(err)
	}
	return f
}

// consoleFrame is apps/x's frame minted under uid's login (gen "s.…").
func consoleFrame(uid, gen string) auth.Principal {
	return auth.Principal{Component: "apps/x", UserID: uid, Via: "frame", Gen: gen}
}

// overviewRow is tile's row of a GET /partitions answer without a tile.
func overviewRow(out map[string]any, tile string) map[string]any {
	rows, _ := out["tiles"].([]any)
	for _, r := range rows {
		if row := r.(map[string]any); row["tile"] == tile {
			return row
		}
	}
	return nil
}

// covers 06§12.3 PD-46 — the listing without a tile answers the admin
// console (the admin tile's frame under a person's login) an admin's view —
// totals, orphans, isolation — like an admin's own session, never the
// driver's own rows, notices or credentials; the console driven by a person
// who isn't an admin (a member, a writer, the tile's manager), the same
// tile's frame a terminal minted, a view-as frame and a tile without xbin
// admin get the tile-level fields only, as before.
func TestPartitionsOverviewAdminConsole(t *testing.T) {
	f := adminTileFx(t)
	_, own := f.get(personP(t, f.partWS, "bob"), "")
	code, out := f.get(consoleFrame("bob", "s.bob"), "")
	row := overviewRow(out, "apps/pg")
	if code != 200 || row == nil || row["totals"] == nil || out["orphans"] == nil || out["isolated"] == nil || out["now"] == nil {
		t.Fatalf("the admin console's overview: %d %v", code, out)
	}
	if totals := row["totals"].(map[string]any); totals["people"] != float64(3) {
		t.Errorf("the console's totals %v", totals)
	}
	if ownRow := overviewRow(own, "apps/pg"); ownRow == nil || ownRow["totals"] == nil {
		t.Errorf("bob's own session's overview: %v", own)
	}
	if row["mine"] != nil || out["credentials"] != nil || out["notices"] != nil {
		t.Errorf("the console's overview carries its driver's own: mine %v, credentials %v, notices %v", row["mine"], out["credentials"], out["notices"])
	}
	viewAs := consoleFrame("alice", "s.v")
	viewAs.Impersonator = "bob"
	for name, p := range map[string]auth.Principal{
		"the console driven by alice (a member)":        consoleFrame("alice", "s.alice"),
		"the console driven by wendy (a writer)":        consoleFrame("wendy", "s.wendy"),
		"the console driven by mona (the tile manager)": consoleFrame("mona", "s.mona"),
		"a frame a terminal minted":                     consoleFrame("bob", "u.e.0"),
		"a view-as frame":                               viewAs,
		"a frame of a tile without xbin":                {Component: "apps/q", UserID: "bob", Via: "frame", Gen: "s.bob"},
		"the admin tile's instance token":               {Component: "apps/x", Via: "instance"},
		"a partitioned tile's own frame":                frameOf("apps/pg", "alice"),
	} {
		_, out := f.get(p, "")
		for _, r := range out["tiles"].([]any) {
			if row := r.(map[string]any); row["totals"] != nil || row["mine"] != nil {
				t.Errorf("%s: a tile row beyond the tile-level fields: %v", name, row)
			}
		}
		if out["orphans"] != nil || out["isolated"] != nil {
			t.Errorf("%s: the admins' fields: %v", name, out)
		}
	}
}

// covers 06§12.3 04§3 I6 — for admins (the console included) the tile's
// listing carries its mode history, newest first, and the global inbox's
// counts (never an item); a person, a writer, a manager and tile code get
// none of them.
func TestPartitionsListAdminExtras(t *testing.T) {
	f := adminTileFx(t)
	b := f.b
	mailSendOK(t, b, partInst("apps/pg", "alice"), "global", "hush-topic", "alice to global")
	if err := b.barrier.Init("f12-pass"); err != nil {
		t.Fatal(err)
	}
	apk := f.pkeyOf("alice")
	if _, _, err := b.backupKeys().subkeyFor(partitionBackupSubject("apps/pg", util.MainDeployment, apk), "apps/pg"); err != nil {
		t.Fatal(err)
	}
	bob := personP(t, f.partWS, "bob")
	mustCode(t, call(t, b.apiPartitionReset, bob, "POST", "/", `{"tile":"apps/pg","partition":"user:alice","confirm":"apps/pg user:alice"}`, nil), 200, "the admin resets alice's")

	for name, p := range map[string]auth.Principal{"the admin": bob, "the admin console": consoleFrame("bob", "s.bob")} {
		code, out := f.get(p, "?tile=apps/pg")
		hist, _ := out["history"].([]any)
		if code != 200 || len(hist) < 2 {
			t.Fatalf("%s: %d history %v", name, code, out["history"])
		}
		newest, oldest := hist[0].(map[string]any), hist[len(hist)-1].(map[string]any)
		if newest["op"] != modeOpBackupErase || newest["partition"] != apk || oldest["op"] != modeOpAuto {
			t.Errorf("%s: history not newest first, or without the reset's erase: %v", name, hist)
		}
		gm, _ := out["globalMail"].(map[string]any)
		if gm["pending"] != float64(1) {
			t.Errorf("%s: the global inbox's counts %v", name, out["globalMail"])
		}
		if s := call(t, b.apiPartitionsList, p, "GET", "/partitions?tile=apps/pg", "", nil).Body.String(); containsAny(s, "hush-topic", "alice to global") {
			t.Errorf("BUG: %s's listing carries mail content: %s", name, s)
		}
	}
	for _, who := range []string{"alice", "wendy", "mona"} {
		_, out := f.get(personP(t, f.partWS, who), "?tile=apps/pg")
		if out["history"] != nil || out["globalMail"] != nil || out["lastWipe"] != nil {
			t.Errorf("%s sees the admins' fields: %v", who, out)
		}
	}
	if _, out := f.get(frameOf("apps/pg", "alice"), "?tile=apps/pg"); out["history"] != nil || out["globalMail"] != nil {
		t.Errorf("tile code sees the admins' fields: %v", out)
	}
	// the console driven by someone who isn't an admin reads the tile-level
	// fields only: no rows, no totals, no history, no inbox counts
	for _, who := range []string{"alice", "wendy", "mona"} {
		code, out := f.get(consoleFrame(who, "s."+who), "?tile=apps/pg")
		if code != 200 || out["tile"] != "apps/pg" {
			t.Fatalf("the console driven by %s: %d %v", who, code, out)
		}
		for _, k := range []string{"partitions", "totals", "history", "globalMail", "lastWipe", "orphans", "binds", "limits", "reviewedOnly", "notices"} {
			if out[k] != nil {
				t.Errorf("the console driven by %s (not an admin) reads %s: %v", who, k, out[k])
			}
		}
	}
	// no history kept yet (a tile listed before any record) and no mail:
	// the fields are absent, never empty
	extra := map[string]any{}
	b.partitionAdminExtras("apps/nothing", extra, b.mailCountsOf("apps/nothing"))
	if len(extra) != 0 {
		t.Errorf("a tile without a record or mail: %v", extra)
	}
}

// covers 06§12.3 PD-46 PD-54 — the routes that judge the credential itself
// judge the admin console by its driver: an admin driving it sets limits,
// lists every personal bind and removes one; a person who isn't an admin
// driving it does none of them (they act in their own session, as
// themselves); the admin tile's other credentials keep the tile's grant.
func TestPartitionsConsoleDriverJudged(t *testing.T) {
	w, st, _ := pbindWS(t)
	b := w.b
	if err := b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) {
		ws.Grants = append(ws.Grants, registry.Grant{From: "apps/plain", Target: "xbin", Role: "admin"})
	}); err != nil {
		t.Fatal(err)
	}
	console := func(uid string) auth.Principal {
		return auth.Principal{Component: "apps/plain", UserID: uid, Via: "frame", Gen: "s." + uid}
	}
	alice := principalFor(t, st, "alice")
	rec := pbindCall(t, w, alice, "POST", pbindBody("apps/agent", "mcp", "users/alice/mcp"))
	var out struct {
		Bind struct {
			ID string `json:"id"`
		} `json:"bind"`
	}
	made := &out.Bind
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &out) != nil || made.ID == "" {
		t.Fatalf("alice's bind: %d %s", rec.Code, rec.Body.String())
	}
	del := `{"id":"` + made.ID + `","user":"alice"}`
	for _, who := range []string{"alice", "carol"} {
		p := console(who)
		if rec := pbindCall(t, w, p, "GET", ""); rec.Code != 403 {
			t.Errorf("the console driven by %s lists the personal binds: %d %s", who, rec.Code, rec.Body.String())
		}
		if rec := pbindCall(t, w, p, "DELETE", del); rec.Code != 403 {
			t.Errorf("the console driven by %s removes alice's bind: %d %s", who, rec.Code, rec.Body.String())
		}
		if rec := call(t, b.apiPartitionLimits, p, "POST", "/partitions/limits", `{"tile":"apps/agent","maxRunning":9}`, nil); rec.Code != 403 {
			t.Errorf("the console driven by %s sets limits: %d %s", who, rec.Code, rec.Body.String())
		}
	}
	if a, _ := b.PartitionCaps("apps/agent"); a != 0 {
		t.Fatalf("a refused limit was saved: %d", a)
	}
	bob := console("bob")
	if rec := call(t, b.apiPartitionLimits, bob, "POST", "/partitions/limits", `{"tile":"apps/agent","maxRunning":9}`, nil); rec.Code != 200 {
		t.Errorf("the console driven by bob (an admin) sets limits: %d %s", rec.Code, rec.Body.String())
	}
	if rec := pbindCall(t, w, bob, "GET", ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), made.ID) {
		t.Errorf("the console driven by bob lists alice's bind: %d %s", rec.Code, rec.Body.String())
	}
	if rec := pbindCall(t, w, bob, "DELETE", del); rec.Code != 200 {
		t.Errorf("the console driven by bob removes alice's bind: %d %s", rec.Code, rec.Body.String())
	}
	// the admin tile's backend holds the grant itself, as before
	if rec := call(t, b.apiPartitionLimits, auth.Principal{Component: "apps/plain", Via: "instance"}, "POST", "/partitions/limits", `{"tile":"apps/agent","maxRunning":0}`, nil); rec.Code != 200 {
		t.Errorf("the admin tile's instance: %d %s", rec.Code, rec.Body.String())
	}
}

// covers 06§5 I5 — a tile manager who isn't an admin reads, in their own
// session, who shares their partition's log of the tile now (logShares):
// the logs panel offers each; nobody else gets the field — the sharer
// themselves, a writer, a member, an admin (who reads it on every row),
// the console, tile code — and it goes when the share ends.
func TestPartitionsListManagerLogShares(t *testing.T) {
	f := adminTileFx(t)
	b := f.b
	alice := personP(t, f.partWS, "alice")
	mona := personP(t, f.partWS, "mona")
	if _, out := f.get(mona, "?tile=apps/pg"); out["logShares"] != nil {
		t.Fatalf("a share before anyone shared: %v", out["logShares"])
	}
	mustCode(t, call(t, b.apiPartitionShareLog, alice, "POST", "/", `{"tile":"apps/pg","days":2}`, nil), 200, "alice shares her log")
	_, out := f.get(mona, "?tile=apps/pg")
	rows, _ := out["logShares"].([]any)
	if len(rows) != 1 || rows[0].(map[string]any)["user"] != "alice" || rows[0].(map[string]any)["until"] == nil {
		t.Fatalf("the manager's logShares: %v", out["logShares"])
	}
	if ps, _ := out["partitions"].([]any); len(ps) > 0 {
		t.Errorf("the manager reads people's rows: %v", ps)
	}
	for name, p := range map[string]auth.Principal{
		"alice (the sharer)":         alice,
		"wendy (a writer)":           personP(t, f.partWS, "wendy"),
		"zed (a member)":             personP(t, f.partWS, "zed"),
		"bob (an admin)":             personP(t, f.partWS, "bob"),
		"the console":                consoleFrame("bob", "s.bob"),
		"the console driven by mona": consoleFrame("mona", "s.mona"),
		"tile code":                  frameOf("apps/pg", "alice"),
	} {
		if _, out := f.get(p, "?tile=apps/pg"); out["logShares"] != nil {
			t.Errorf("%s reads logShares: %v", name, out["logShares"])
		}
	}
	mustCode(t, call(t, b.apiPartitionShareLog, alice, "DELETE", "/", `{"tile":"apps/pg"}`, nil), 200, "alice ends her share")
	if _, out := f.get(mona, "?tile=apps/pg"); out["logShares"] != nil {
		t.Errorf("an ended share still listed: %v", out["logShares"])
	}
}

func containsAny(s string, subs ...string) bool {
	return slices.ContainsFunc(subs, func(x string) bool { return strings.Contains(s, x) })
}
