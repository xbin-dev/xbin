package broker

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/users"
	"github.com/xbin-dev/xbin/internal/util"
)

// opsFx is partRouteWS with the people the operations need: wendy writes
// apps/pg, mona owns it (a tile manager), and alice and bob hold
// partitions of it; the runner's stops are recorded.
type opsFx struct {
	*partWS
	mu      sync.Mutex
	stops   []string // tile dep part
	stopOf  []string
	revoked []string
	pushes  []string
	running []PartitionInstance
}

func newOpsFx(t *testing.T) *opsFx {
	t.Helper()
	f := &opsFx{partWS: partRouteWS(t)}
	b := f.b
	for _, u := range []users.User{
		{ID: "wendy", Role: users.RoleUser, Tiles: map[string]string{"apps/pg": users.LevelWrite}},
		{ID: "mona", Role: users.RoleUser, Tiles: map[string]string{"apps/pg": users.LevelRead}},
		{ID: "zed", Role: users.RoleUser, Tiles: map[string]string{"apps/*": users.LevelRead}},
	} {
		if _, err := b.Users.Upsert(u, "password1"); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.Users.SetOwner("apps/pg", users.OwnerKindUser+":mona"); err != nil {
		t.Fatal(err)
	}
	b.SetPartitionInstanceStop(func(tile, dep, part string) {
		f.mu.Lock()
		f.stops = append(f.stops, tile+" "+dep+" "+part)
		f.mu.Unlock()
	})
	b.SetPartitionOps(func() []PartitionInstance {
		f.mu.Lock()
		defer f.mu.Unlock()
		return slices.Clone(f.running)
	}, func(id string) {
		f.mu.Lock()
		f.stopOf = append(f.stopOf, id)
		f.mu.Unlock()
	}, func(id string) int {
		f.mu.Lock()
		f.revoked = append(f.revoked, id)
		f.mu.Unlock()
		return 0
	})
	b.SetPartitionPush(func(user, kind, title, body, link, collapse string) {
		f.mu.Lock()
		f.pushes = append(f.pushes, user+" "+kind)
		f.mu.Unlock()
	})
	for _, id := range []string{"alice", "bob", "zed"} {
		f.hold(id, "apps/pg")
	}
	return f
}

// hold gives id a partition record of tile.
func (f *opsFx) hold(id, tile string) string {
	f.t.Helper()
	uid, err := f.b.mintPartitionUID(id)
	if err != nil {
		f.t.Fatal(err)
	}
	f.b.noteTermPartition(tile, id, uid)
	return util.PartitionKey(id, uid)
}

func (f *opsFx) get(p auth.Principal, query string) (int, map[string]any) {
	f.t.Helper()
	rec := call(f.t, f.b.apiPartitionsList, p, "GET", "/partitions"+query, "", nil)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func rowsOf(out map[string]any) []string {
	var users []string
	rows, _ := out["partitions"].([]any)
	for _, r := range rows {
		users = append(users, r.(map[string]any)["user"].(string))
	}
	return users
}

// covers PD-46 S19 06§6 — GET /partitions per audience: the features (the
// F5 word included), a person's own row with its ledger, a writer's and a
// manager's totals only, an admin's every metadata row and the orphans,
// tile code the tile-level fields only, a non-reader 404.
func TestPartitionsListVisibility(t *testing.T) {
	f := newOpsFx(t)
	code, out := f.get(personP(t, f.partWS, "alice"), "?tile=apps/pg")
	if code != 200 || !slices.Contains(anyStrings(out["features"]), GlobalAddressFeature) || out["state"] != "partitioned" {
		t.Fatalf("alice: %d %v", code, out)
	}
	if got := rowsOf(out); !slices.Equal(got, []string{"alice"}) || out["totals"] != nil || out["trust"] == nil || out["orphans"] != nil {
		t.Errorf("alice sees rows %v, totals %v, trust %v, orphans %v", got, out["totals"], out["trust"], out["orphans"])
	}
	for _, who := range []string{"wendy", "mona"} {
		_, out := f.get(personP(t, f.partWS, who), "?tile=apps/pg")
		totals, _ := out["totals"].(map[string]any)
		if got := rowsOf(out); len(got) != 0 || totals == nil || totals["people"] != float64(3) {
			t.Errorf("%s (writer/manager): rows %v, totals %v", who, got, out["totals"])
		}
	}
	_, out = f.get(personP(t, f.partWS, "bob"), "?tile=apps/pg")
	if got := rowsOf(out); !slices.Equal(got, []string{"alice", "bob", "zed"}) || out["orphans"] == nil {
		t.Errorf("the admin sees %v, orphans %v", got, out["orphans"])
	}
	for _, r := range out["partitions"].([]any) {
		if row := r.(map[string]any); row["ledger"] != nil {
			t.Errorf("an admin's row carries the person's ledger: %v", row)
		}
	}
	_, out = f.get(frameOf("apps/pg", "alice"), "?tile=apps/pg")
	if out["partitions"] != nil || out["totals"] != nil || out["state"] != "partitioned" || out["features"] == nil {
		t.Errorf("tile code sees %v", out)
	}
	if code, _ := f.get(personP(t, f.partWS, "carol"), "?tile=apps/pg"); code != 404 {
		t.Errorf("carol, who can't read apps/pg: %d", code)
	}
	_, out = f.get(personP(t, f.partWS, "alice"), "")
	found := false
	for _, r := range out["tiles"].([]any) {
		if row := r.(map[string]any); row["tile"] == "apps/pg" && row["mine"] != nil {
			found = true
		}
	}
	if !found || out["credentials"] == nil {
		t.Errorf("the overview for alice: %v", out)
	}
}

func anyStrings(v any) []string {
	var out []string
	for _, s := range v.([]any) {
		out = append(out, s.(string))
	}
	return out
}

// covers PD-26 11§3 06§6 — stop (the person's own, a manager's, an
// admin's; never another reader's); reset needs the typed confirmation,
// deletes the partition's namespace, record, log and share and erases its
// part: subkey, and an admin's tells the person; purge deletes orphans
// only.
func TestPartitionStopResetPurge(t *testing.T) {
	f := newOpsFx(t)
	b := f.b
	if err := b.barrier.Init("ops-pass"); err != nil {
		t.Fatal(err)
	}
	alice, bob, mona, zed := personP(t, f.partWS, "alice"), personP(t, f.partWS, "bob"), personP(t, f.partWS, "mona"), personP(t, f.partWS, "zed")
	body := `{"tile":"apps/pg","partition":"user:alice"}`
	mustCode(t, call(t, b.apiPartitionStop, zed, "POST", "/", body, nil), 403, "zed stops alice's")
	mustCode(t, call(t, b.apiPartitionStop, alice, "POST", "/", body, nil), 200, "alice stops her own")
	mustCode(t, call(t, b.apiPartitionStop, mona, "POST", "/", body, nil), 200, "the manager stops alice's")
	mustCode(t, call(t, b.apiPartitionStop, bob, "POST", "/", body, nil), 200, "the admin stops alice's")
	mustCode(t, call(t, b.apiPartitionStop, frameOf("apps/pg", "alice"), "POST", "/", body, nil), 403, "tile code")
	if len(f.stops) != 3 || f.stops[0] != "apps/pg main user:alice" {
		t.Errorf("stops %q", f.stops)
	}

	pkey := f.pkeyOf("alice")
	id := partNS("apps/pg", util.MainDeployment, pkey)
	if err := b.notePartitionNS(id, nsPartition{User: "alice", UID: b.storedPartitionUID("alice")}); err != nil {
		t.Fatal(err)
	}
	subject := partitionBackupSubject("apps/pg", util.MainDeployment, pkey)
	if _, _, err := b.backupKeys().subkeyFor(subject, "apps/pg"); err != nil {
		t.Fatal(err)
	}
	logDir, _ := b.partitionLogDir("apps/pg", util.MainDeployment, pkey)
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(logDir, "backend.log"), []byte("alice's secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustCode(t, call(t, b.apiPartitionShareLog, alice, "POST", "/", `{"tile":"apps/pg","days":3}`, nil), 200, "alice shares her log")
	recDir, _ := b.partitionRecordDir("apps/pg", util.MainDeployment, pkey)

	mustCode(t, call(t, b.apiPartitionReset, mona, "POST", "/", `{"tile":"apps/pg","partition":"user:alice","confirm":"apps/pg user:alice"}`, nil), 403, "a manager resets alice's")
	mustCode(t, call(t, b.apiPartitionReset, alice, "POST", "/", body, nil), 409, "no confirmation")
	if _, err := os.Stat(recDir); err != nil {
		t.Fatalf("an unconfirmed reset deleted: %v", err)
	}
	out := mustCode(t, call(t, b.apiPartitionReset, bob, "POST", "/", `{"tile":"apps/pg","partition":"user:alice","confirm":"apps/pg user:alice"}`, nil), 200, "the admin resets alice's")
	if d, _ := out["deleted"].(map[string]any); d["namespaces"] != float64(1) || d["subkeys"] != float64(1) {
		t.Errorf("deleted %v", out["deleted"])
	}
	for _, gone := range []string{recDir, logDir} {
		if _, err := os.Stat(gone); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s survived the reset: %v", gone, err)
		}
	}
	if _, ok, _ := b.readNS(id); ok {
		t.Error("alice's namespace survived the reset")
	}
	if keys, _ := b.backupKeys().list(); slices.ContainsFunc(keys, func(k backupSubkey) bool { return k.Subject == subject }) {
		t.Error("alice's part: subkey survived the reset")
	}
	if !slices.Contains(f.pushes, "alice tile.partition-reset") || len(b.noticesOf("alice", "apps/pg")) == 0 {
		t.Errorf("the admin's reset didn't tell alice: pushes %q", f.pushes)
	}

	// purge: a live partition is refused; zed deleted → orphaned → purged
	mustCode(t, call(t, b.apiPartitionPurge, bob, "POST", "/", `{"tile":"apps/pg","partition":"user:bob"}`, nil), 409, "purging a live partition")
	zedDir, _ := b.partitionRecordDir("apps/pg", util.MainDeployment, f.pkeyOf("zed"))
	uid := b.storedPartitionUID("zed")
	if _, err := b.Users.Delete("zed"); err != nil {
		t.Fatal(err)
	}
	b.PartitionPersonDeleted("zed", uid)
	if !slices.Contains(f.stopOf, "zed") || !slices.Contains(f.revoked, "zed") {
		t.Errorf("zed's instances weren't stopped and revoked: %q %q", f.stopOf, f.revoked)
	}
	mustCode(t, call(t, b.apiPartitionPurge, alice, "POST", "/", "", nil), 403, "a person purges")
	out = mustCode(t, call(t, b.apiPartitionPurge, bob, "POST", "/", "", nil), 200, "the admin purges")
	if p, _ := out["purged"].([]any); len(p) != 1 || p[0].(map[string]any)["user"] != "zed" {
		t.Errorf("purged %v", out["purged"])
	}
	if _, err := os.Stat(zedDir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("zed's orphaned partition survived the purge: %v", err)
	}
	if bobDir, _ := b.partitionRecordDir("apps/pg", util.MainDeployment, f.pkeyOf("bob")); !opsExists(bobDir) {
		t.Error("the purge took bob's live partition")
	}
}

func opsExists(p string) bool { _, err := os.Stat(p); return err == nil }

// covers PD-24 C15 S12 06§5 — which log GET /logs reads on a partitioned
// tile: the caller's own partition's (a person, their partition's
// instance), a shared one for an admin or manager only while shared, the
// global instance's on ?xbin-partition=global (never a partition's own
// credential), the root token's global; ?partition= 400; an unpartitioned
// tile untouched.
func TestPartitionLogChoice(t *testing.T) {
	f := newOpsFx(t)
	b := f.b
	alice, bob, mona := personP(t, f.partWS, "alice"), personP(t, f.partWS, "bob"), personP(t, f.partWS, "mona")
	ask := func(p auth.Principal, tile, q string) (string, util.Partition, bool, int) {
		t.Helper()
		v, _ := url.ParseQuery(q)
		rel, part, on, status, _ := b.PartitionLog(p, tile, v)
		return rel, part, on, status
	}
	own := ".xbin/partition/" + util.TileKey("apps/pg") + "/main/" + f.pkeyOf("alice") + "/backend.log"
	if _, _, on, _ := ask(alice, "apps/x", ""); on {
		t.Error("an unpartitioned tile's logs changed")
	}
	if rel, part, _, _ := ask(alice, "apps/pg", ""); rel != own || part != "user:alice" {
		t.Errorf("alice's session: %q %q", rel, part)
	}
	if rel, _, _, _ := ask(instanceOf("apps/pg", "user:alice"), "apps/pg", ""); rel != own {
		t.Errorf("alice's instance: %q", rel)
	}
	if _, _, _, st := ask(alice, "apps/pg", "partition=user:alice"); st != 400 {
		t.Errorf("?partition=: %d", st)
	}
	if rel, part, _, st := ask(alice, "apps/pg", "xbin-partition=global"); rel != "" || part != "global" || st != 0 {
		t.Errorf("alice asks global's: %q %q %d", rel, part, st)
	}
	if _, _, _, st := ask(instanceOf("apps/pg", "user:alice"), "apps/pg", "xbin-partition=global"); st != 403 {
		t.Errorf("a partition's instance asks global's: %d", st)
	}
	if rel, _, _, st := ask(auth.Principal{Owner: true}, "apps/pg", ""); rel != "" || st != 0 {
		t.Errorf("the root token: %q %d", rel, st)
	}
	for _, p := range []auth.Principal{bob, mona} {
		if _, _, _, st := ask(p, "apps/pg", "user=alice"); st != 403 {
			t.Errorf("%s reads alice's unshared log: %d", p.UserID, st)
		}
	}
	mustCode(t, call(t, b.apiPartitionShareLog, alice, "POST", "/", `{"tile":"apps/pg"}`, nil), 200, "share")
	for _, p := range []auth.Principal{bob, mona} {
		if rel, _, _, st := ask(p, "apps/pg", "user=alice"); rel != own || st != 0 {
			t.Errorf("%s reads alice's shared log: %q %d", p.UserID, rel, st)
		}
	}
	if _, _, _, st := ask(personP(t, f.partWS, "wendy"), "apps/pg", "user=alice"); st != 403 {
		t.Errorf("a writer reads alice's shared log: %d", st)
	}
	logShareNow = func() time.Time { return time.Now().Add(8 * 24 * time.Hour) }
	t.Cleanup(func() { logShareNow = time.Now })
	if _, _, _, st := ask(bob, "apps/pg", "user=alice"); st != 403 {
		t.Errorf("a share past its days: %d", st)
	}
	logShareNow = time.Now
	mustCode(t, call(t, b.apiPartitionShareLog, alice, "DELETE", "/", `{"tile":"apps/pg"}`, nil), 200, "unshare")
	if _, _, _, st := ask(bob, "apps/pg", "user=alice"); st != 403 {
		t.Errorf("after the share ended: %d", st)
	}
	mustCode(t, call(t, b.apiPartitionShareLog, alice, "POST", "/", `{"tile":"apps/pg","days":30}`, nil), 400, "30 days")
	mustCode(t, call(t, b.apiPartitionShareLog, instanceOf("apps/pg", "user:alice"), "POST", "/", `{"tile":"apps/pg"}`, nil), 403, "tile code shares")
}

// covers PD-20 PD-43 S17 06§9 — the users hooks: a deleted partition
// holder's instances stop, their partitions are orphaned and their home and
// history move to data/orphans; a person without partitions keeps neither
// moved; disabling a person stops their running instances, and only
// theirs.
func TestPartitionPeopleHooks(t *testing.T) {
	f := newOpsFx(t)
	b := f.b
	root := b.Reg.Root
	for _, d := range []string{"homes/zed", "data/agent-history/zed", "homes/carol"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	zuid := b.storedPartitionUID("zed")
	b.Users.Delete("zed")
	b.PartitionPersonDeleted("zed", zuid)
	orphan := filepath.Join(root, "data", "orphans", "zed-"+zuid[:8])
	if !opsExists(filepath.Join(orphan, "home")) || !opsExists(filepath.Join(orphan, "agent-history")) || opsExists(filepath.Join(root, "homes/zed")) {
		t.Errorf("zed's home and history weren't moved to %s", orphan)
	}
	rows := b.partitionPeople("apps/pg")
	if i := slices.IndexFunc(rows, func(r partitionRow) bool { return r.User == "zed" }); i < 0 || rows[i].State != partStateOrphaned {
		t.Errorf("zed's partition isn't orphaned: %+v", rows)
	}
	b.Users.Delete("carol")
	b.PartitionPersonDeleted("carol", "")
	if !opsExists(filepath.Join(root, "homes/carol")) {
		t.Error("a person who held no partitions had their home moved")
	}

	f.running = []PartitionInstance{{Tile: "apps/pg", Partition: "user:alice"}, {Tile: "apps/pg", Partition: "user:bob"}}
	b.PartitionPeopleChanged()
	if len(f.stops) != 0 {
		t.Errorf("live people's instances stopped: %q", f.stops)
	}
	a, _ := b.Users.Get("alice")
	a.Disabled = true
	if _, err := b.Users.Upsert(*a, ""); err != nil {
		t.Fatal(err)
	}
	b.PartitionPeopleChanged()
	if !slices.Equal(f.stops, []string{"apps/pg main user:alice"}) {
		t.Errorf("disabling alice stopped %q", f.stops)
	}
}

// covers PD-07 S6 06§9 — credentials an admin makes for a partition
// holder: policy off, told (push, notice) and effective; policy on, held:
// the link answers waiting, Allow activates, Refuse revokes, no answer
// activates 24 h after the notice (a fake clock); a held password leaves
// the old one working; a person without partitions is unaffected.
func TestPartitionHeldCredentials(t *testing.T) {
	f := newOpsFx(t)
	b := f.b
	b.InstallCredentialGate()
	t.Cleanup(func() { b.Users.SetInviteGate(nil) })
	bob, alice := personP(t, f.partWS, "bob"), personP(t, f.partWS, "alice")
	invite := func(id string) (string, map[string]any) {
		t.Helper()
		out := mustCode(t, call(t, b.apiUsersInvite, bob, "POST", "/", "", map[string]string{"id": id}), 200, "invite "+id)
		return out["invite"].(string), out
	}
	policy := func(on bool) {
		body := `{"schema":1,"partitionConsent":false,"credentialResetConfirm":` + map[bool]string{true: "true", false: "false"}[on] + `}`
		p := filepath.Join(f.root, "data", "workspace-policies.json")
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		later := time.Now().Add(time.Duration(len(body)) * time.Second)
		_ = os.Chtimes(p, later, later)
		if b.Policies().CredentialResetConfirm != on {
			t.Fatal("the policy didn't take")
		}
	}

	tok, out := invite("alice")
	if out["held"] != nil || !slices.Contains(f.pushes, "alice account.credential") || len(b.noticesOf("alice", "")) != 1 {
		t.Errorf("policy off: held %v, pushes %q", out["held"], f.pushes)
	}
	if _, err := b.Users.RedeemInvite(tok, "newpassword1"); err != nil {
		t.Fatalf("policy off, the link: %v", err)
	}

	policy(true)
	tok, out = invite("alice")
	if out["held"] != true {
		t.Fatalf("policy on: %v", out)
	}
	if _, err := b.Users.RedeemInvite(tok, "newpassword2"); !errors.Is(err, users.ErrInviteHeld) || !strings.Contains(err.Error(), "waiting for alice") {
		t.Fatalf("a held link redeemed: %v", err)
	}
	held := b.heldOf("alice")
	if len(held) != 1 || held[0].Kind != credInvite {
		t.Fatalf("held %+v", held)
	}
	mustCode(t, call(t, b.apiCredentialConfirm, frameOf("apps/pg", "alice"), "POST", "/", `{"id":"`+held[0].ID+`","allow":true}`, nil), 403, "tile code confirms")
	mustCode(t, call(t, b.apiCredentialConfirm, alice, "POST", "/", `{"id":"`+held[0].ID+`","allow":true}`, nil), 200, "alice allows")
	if _, err := b.Users.RedeemInvite(tok, "newpassword2"); err != nil {
		t.Fatalf("an allowed link: %v", err)
	}

	tok, _ = invite("alice")
	held = b.heldOf("alice")
	mustCode(t, call(t, b.apiCredentialConfirm, alice, "POST", "/", `{"id":"`+held[0].ID+`","allow":false}`, nil), 200, "alice refuses")
	if _, err := b.Users.RedeemInvite(tok, "newpassword3"); !errors.Is(err, users.ErrInvalidInvite) {
		t.Fatalf("a refused link: %v", err)
	}

	tok, _ = invite("alice")
	peopleNow = func() time.Time { return time.Now().Add(credentialHoldFor + time.Minute) }
	t.Cleanup(func() { peopleNow = time.Now })
	if _, err := b.Users.RedeemInvite(tok, "newpassword4"); err != nil {
		t.Fatalf("a link unanswered for 24 h: %v", err)
	}
	peopleNow = time.Now

	// a password: held, the old one works; 24 h later the new one does
	rec := call(t, func(w http.ResponseWriter, r *http.Request) { b.apiUsersUpdate(nil, w, r) }, bob, "PATCH", "/", `{"password":"heldpassword9"}`, map[string]string{"id": "alice"})
	if rec.Code != 200 || rec.Header().Get("X-XBin-Credential-Held") != "password" {
		t.Fatalf("a held password: %d %q %s", rec.Code, rec.Header().Get("X-XBin-Credential-Held"), rec.Body)
	}
	if _, ok := b.Users.Verify("alice", "heldpassword9"); ok {
		t.Error("a held password works")
	}
	if _, ok := b.Users.Verify("alice", "newpassword4"); !ok {
		t.Error("the old password stopped working while one is held")
	}
	b.lapseHeldCredentials(time.Now().Add(credentialHoldFor + time.Minute))
	if _, ok := b.Users.Verify("alice", "heldpassword9"); !ok {
		t.Error("a held password didn't take effect after 24 h")
	}

	// carol holds no partitions: unaffected
	tok, out = invite("carol")
	if out["held"] != nil {
		t.Errorf("carol's link held: %v", out)
	}
	if _, err := b.Users.RedeemInvite(tok, "carolpassword1"); err != nil {
		t.Errorf("carol's link: %v", err)
	}
}

// covers C16 06§10 — offloading a partitioned tile is refused (409),
// another tile's offload check is untouched.
func TestPartitionOffloadRefused(t *testing.T) {
	f := newOpsFx(t)
	err := f.b.partitionOffloadCheck("apps/pg")
	if err == nil || offloadStatus(err) != 409 {
		t.Errorf("apps/pg: %v", err)
	}
	if err := f.b.partitionOffloadCheck("apps/x"); err != nil {
		t.Errorf("apps/x: %v", err)
	}
}

// covers PD-23 06§4 — the trust warning: live reload on and a writer who
// isn't an admin on a partitioned tile, for admins and its readers;
// nothing once saves no longer reach it.
func TestPartitionTrustAlerts(t *testing.T) {
	f := newOpsFx(t)
	b := f.b
	got := b.partitionTrustAlerts(personP(t, f.partWS, "bob"), true)
	if !slices.ContainsFunc(got, func(a Alert) bool {
		return a.Tile == "apps/pg" && a.Kind == "partition-trust" && strings.Contains(a.Message, "wendy")
	}) {
		t.Errorf("no trust warning for apps/pg: %+v", got)
	}
	if got := b.partitionTrustAlerts(personP(t, f.partWS, "carol"), false); slices.ContainsFunc(got, func(a Alert) bool { return a.Tile == "apps/pg" }) {
		t.Error("carol, who can't read apps/pg, sees its warning")
	}
	b.SetPartitionLiveReload(func(string) bool { return false })
	if got := b.partitionTrustAlerts(personP(t, f.partWS, "bob"), true); len(got) != 0 {
		t.Errorf("live reload off: %+v", got)
	}
}

// covers 06§10 W1-wire — a removed tile's mode record goes once nothing of
// it is left, and stays while its people's partitions do.
func TestRemovedTileModeRecord(t *testing.T) {
	f := newOpsFx(t)
	b := f.b
	pkey := f.hold("alice", "apps/pu")
	if err := os.RemoveAll(filepath.Join(f.root, "apps", "pu")); err != nil {
		t.Fatal(err)
	}
	f.rescan()
	mode := filepath.Join(f.root, "data", "partitions", util.TileKey("apps/pu"), "mode.json")
	b.sweepRemovedModeRecords()
	if !opsExists(mode) {
		t.Fatal("the mode record went while alice's partition is left")
	}
	if _, err := b.dropOnePartition("apps/pu", util.MainDeployment, pkey, "test", ""); err != nil {
		t.Fatal(err)
	}
	b.sweepRemovedModeRecords()
	if opsExists(mode) {
		t.Error("the removed tile's mode record stayed with nothing left")
	}
}

// covers 06§6 — the partitions event's mode op reaches the tile's readers
// and its own credentials, a notice only its person's own sockets.
func TestPartitionEventAudiences(t *testing.T) {
	f := newOpsFx(t)
	mode := audienceEvent{tile: "apps/pg", readers: true}
	notice := audienceEvent{user: "alice"}
	alice, carol := personP(t, f.partWS, "alice"), personP(t, f.partWS, "carol")
	if !mode.VisibleTo(alice) || mode.VisibleTo(carol) || !mode.VisibleTo(frameOf("apps/pg", "carol")) || mode.VisibleTo(frameOf("apps/x", "alice")) {
		t.Error("the mode op's audience")
	}
	if !notice.VisibleTo(alice) || notice.VisibleTo(carol) || notice.VisibleTo(frameOf("apps/pg", "alice")) {
		t.Error("a notice's audience")
	}
	view := alice
	view.Impersonator = "bob"
	if notice.VisibleTo(view) {
		t.Error("view-as sees a notice")
	}
}
