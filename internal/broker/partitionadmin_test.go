package broker

// partitionadmin_test.go — what the admin console's runtime → partitions
// view reads (partitionadmin.go; plans/partitions/06 §12.3, PD-46).

import (
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
// driver's own rows, notices or credentials; the same tile's frame a
// terminal minted, a view-as frame and a tile without xbin admin get the
// tile-level fields only, as before.
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
		"a frame a terminal minted":       consoleFrame("bob", "u.e.0"),
		"a view-as frame":                 viewAs,
		"a frame of a tile without xbin":  {Component: "apps/q", UserID: "bob", Via: "frame", Gen: "s.bob"},
		"the admin tile's instance token": {Component: "apps/x", Via: "instance"},
		"a partitioned tile's own frame":  frameOf("apps/pg", "alice"),
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
	// no history kept yet (a tile listed before any record) and no mail:
	// the fields are absent, never empty
	extra := map[string]any{}
	b.partitionAdminExtras("apps/nothing", extra)
	if len(extra) != 0 {
		t.Errorf("a tile without a record or mail: %v", extra)
	}
}

func containsAny(s string, subs ...string) bool {
	return slices.ContainsFunc(subs, func(x string) bool { return strings.Contains(s, x) })
}
