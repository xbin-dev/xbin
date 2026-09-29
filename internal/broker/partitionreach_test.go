package broker

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/util"
)

// covers PD-04 PD-05 PD-13 PD-45 — the reach table of plans/partitions/03
// §B.2, cell by cell, with the consent policy off and on: a person's own
// partition for a partitioned resource, today's keys for a shared one
// (read-only for a user partition when "read") and for the global
// instance; another partitioned tile's user partition reaches the same
// person's namespace only if they can read the tile — and, with
// partitionConsent on, consented; an unpartitioned tile reaches today's
// (global's); view-as reaches nothing; a cron resource isn't partitioned.
func TestPartitionReachTable(t *testing.T) {
	w := partFx(t)
	b := w.b
	pk := func(user string) string { return partitionKeyFor(user, "uid-"+user) }
	type cell struct {
		name     string
		p        auth.Principal
		target   string
		pkey     string // "" = today's keys
		readOnly bool
		err      string // a refusal's text, when refused
	}
	off := []cell{
		{"own partitioned", aliceDocs, "res:apps/docs/docs", pk("alice"), false, ""},
		{"another person's own", carolDocs, "res:apps/docs/docs", pk("carol"), false, ""},
		{"own read", aliceDocs, "res:apps/docs/pub", "", true, ""},
		{"own shared", aliceDocs, "res:apps/docs/board", "", false, ""},
		{"global partitioned", docsGlobal, "res:apps/docs/docs", "", false, ""},
		{"global read", docsGlobal, "res:apps/docs/pub", "", false, ""},
		{"root token", auth.Principal{Owner: true}, "res:apps/docs/docs", "", false, ""},
		{"cross-scope, can read", aliceAgent, "res:apps/docs/docs", pk("alice"), false, ""},
		{"cross-scope, can't read", bobAgent, "res:apps/docs/docs", "", false, "bob can't read apps/docs"},
		{"cross-scope, unpartitioned caller", plainTile, "res:apps/docs/docs", "", false, ""},
		{"view-as", viewAsAlice, "res:apps/docs/docs", "", false, "view-as can't open it"},
		{"cron", aliceDocs, "res:apps/docs/beat", "", false, ""},
		{"own scope of the caller, unrelated", aliceAgent, "res:apps/agent/mem", pk("alice"), false, ""},
	}
	check := func(policy string, cells []cell) {
		t.Helper()
		for _, c := range cells {
			ra, found, err := b.reachRes(c.p, c.target)
			switch {
			case !found:
				t.Errorf("%s %s: %s not found", policy, c.name, c.target)
			case c.err != "":
				if err == nil || !strings.Contains(err.Error(), c.err) {
					t.Errorf("%s %s: err %v, want %q", policy, c.name, err, c.err)
				}
			case err != nil:
				t.Errorf("%s %s: %v", policy, c.name, err)
			case ra.pkey != c.pkey || ra.readOnly != c.readOnly:
				t.Errorf("%s %s: pkey %q readOnly %v, want %q %v", policy, c.name, ra.pkey, ra.readOnly, c.pkey, c.readOnly)
			case ra.dep != util.MainDeployment:
				t.Errorf("%s %s: deployment %q", policy, c.name, ra.dep)
			}
		}
	}
	check("off", off)

	// The policy on: the cross-scope edge needs alice's consent; nothing else
	// changes.
	w.setConsentPolicy(true)
	on := append([]cell(nil), off...)
	for i := range on {
		if on[i].name == "cross-scope, can read" {
			on[i].pkey, on[i].err = "", "alice hasn't let apps/agent use their apps/docs data"
		}
	}
	check("on", on)
	prev := partitionConsentSeam
	t.Cleanup(func() { partitionConsentSeam = prev })
	var asked []string
	partitionConsentSeam = func(b *Broker, user, from, to string) bool {
		asked = append(asked, user+" "+from+"→"+to)
		return user == "alice"
	}
	for i := range on {
		if on[i].name == "cross-scope, can read" {
			on[i].pkey, on[i].err = pk("alice"), ""
		}
	}
	check("on, consented", on)
	if len(asked) != 1 || asked[0] != "alice apps/agent→apps/docs" {
		t.Errorf("consent asked %q", asked)
	}
	w.setConsentPolicy(false)

	// A write to a "read" resource by a user partition is refused (403 with
	// the reason); a read passes, and the global instance writes.
	ra, _, _ := b.reachRes(aliceDocs, "res:apps/docs/pub")
	if err := b.allowAt(aliceDocs, ra, "writer"); err == nil || err.Error() != "res:apps/docs/pub is read-only for people's partitions" {
		t.Errorf("alice writes pub: %v", err)
	}
	if err := b.allowAt(aliceDocs, ra, "reader"); err != nil {
		t.Errorf("alice reads pub: %v", err)
	}
	ra, _, _ = b.reachRes(docsGlobal, "res:apps/docs/pub")
	if err := b.allowAt(docsGlobal, ra, "writer"); err != nil {
		t.Errorf("global writes pub: %v", err)
	}

	// The seams unset: a partitioned scope refuses rather than falls back to
	// today's keys; an unpartitioned tile's reach is untouched.
	addressedPartitionSeam = func(b *Broker, p auth.Principal, tile string) (string, error) {
		if c, ok := b.Reg.Component(tile); !ok || !partitionedNow(c) {
			return "", nil
		}
		return "", errPartitionUnwired
	}
	if _, _, err := b.reachRes(aliceDocs, "res:apps/docs/docs"); err == nil {
		t.Error("an unwired identity plane reached a partitioned scope")
	}
	if ra, _, err := b.reachRes(plainTile, "res:apps/docs/docs"); err != nil || ra.pkey != "" {
		t.Errorf("an unpartitioned caller, unwired: %+v %v", ra, err)
	}
}

// covers PD-44 G3 — a paused partitioned scope (its code dropped the key
// while it holds data: pending) and one whose mode record can't be read
// refuse with 409, never falling back to today's keys.
func TestPartitionReachPaused(t *testing.T) {
	w := partFx(t)
	if code, body := nsKV(t, w.b, "PUT", aliceDocs, "res:apps/docs/docs/k", "v"); code != 200 {
		t.Fatalf("alice's first write: %d %s", code, body)
	}
	w.write(map[string]string{"apps/docs/xbin.json": strings.Replace(partFxFiles["apps/docs/xbin.json"], `"partition":["user","global"],`, "", 1)})
	w.rescan()
	if st, _, _ := w.state("apps/docs"); st != registry.PartitionPending {
		t.Fatalf("apps/docs is %s, want pending (it holds a partition's data)", st)
	}
	if code, body := nsKV(t, w.b, "GET", docsGlobal, "res:apps/docs/docs/k", ""); code != 409 || !strings.Contains(body, "is paused") {
		t.Errorf("a paused scope: %d %s", code, body)
	}
}

// covers PD-05 PD-45 S8 — the kv plane end to end: each person's partition
// has its own copy of a partitioned resource, the global instance its own
// at today's keys; a "read" resource is written by global and read by
// people (a person's write is 403); a shared one is one copy. The first
// write records whose the namespace is.
func TestPartitionKVRoundTrip(t *testing.T) {
	w := partFx(t)
	b := w.b
	put := func(p auth.Principal, rest, v string, want int) {
		t.Helper()
		if code, body := nsKV(t, b, "PUT", p, rest, v); code != want {
			t.Fatalf("PUT %s as %s/%s: %d %s, want %d", rest, p.Component, p.UserID, code, body, want)
		}
	}
	get := func(p auth.Principal, rest string) string {
		t.Helper()
		code, body := nsKV(t, b, "GET", p, rest, "")
		if code == 404 {
			return "-"
		}
		if code != 200 {
			t.Fatalf("GET %s as %s/%s: %d %s", rest, p.Component, p.UserID, code, body)
		}
		return body
	}
	put(aliceDocs, "res:apps/docs/docs/k", "alice's", 200)
	put(docsGlobal, "res:apps/docs/docs/k", "global's", 200)
	if a, c, g := get(aliceDocs, "res:apps/docs/docs/k"), get(carolDocs, "res:apps/docs/docs/k"), get(docsGlobal, "res:apps/docs/docs/k"); a != "alice's" || c != "-" || g != "global's" {
		t.Errorf("docs: alice %q, carol %q, global %q", a, c, g)
	}
	put(aliceDocs, "res:apps/docs/pub/k", "x", 403)
	put(docsGlobal, "res:apps/docs/pub/k", "public", 200)
	if a := get(aliceDocs, "res:apps/docs/pub/k"); a != "public" {
		t.Errorf("pub read by alice: %q", a)
	}
	put(carolDocs, "res:apps/docs/board/k", "carol's note", 200)
	if a := get(aliceDocs, "res:apps/docs/board/k"); a != "carol's note" {
		t.Errorf("board read by alice: %q", a)
	}
	// alice's partition of apps/agent reaches alice's apps/docs data.
	put(aliceAgent, "res:apps/docs/docs/from-agent", "via agent", 200)
	if a := get(aliceDocs, "res:apps/docs/docs/from-agent"); a != "via agent" {
		t.Errorf("alice's agent wrote into %q", a)
	}
	// The namespace names its person.
	m, ok, err := b.readNS(partNS("apps/docs", util.MainDeployment, partitionKeyFor("alice", "uid-alice")))
	if err != nil || !ok || m.Partition == nil || m.Partition.User != "alice" || m.Partition.UID != "uid-alice" || m.Partition.Tile != "apps/docs" {
		t.Fatalf("alice's ns.json: %+v %v %v", m, ok, err)
	}
	// Today's kv.db holds global's and the shared keys only.
	if main, _ := os.ReadFile(filepath.Join(w.root, "data", "kv.db")); strings.Contains(string(main), "alice") {
		t.Error("data/kv.db holds a partition's key")
	}
	// A user partition's own bus is refused until the identity plane
	// delivers partitioned events; the global instance's is today's.
	if r := zeroDataCall(t, b.apiBusPublish, "POST", "", `{"resource":"res:apps/docs/bus","topic":"t"}`, aliceDocs); r.Code != 503 {
		t.Errorf("a partition's bus publish: %d %s", r.Code, r.Body)
	}
	if r := zeroDataCall(t, b.apiBusPublish, "POST", "", `{"resource":"res:apps/docs/bus","topic":"t"}`, docsGlobal); r.Code != 200 {
		t.Errorf("global's bus publish: %d %s", r.Code, r.Body)
	}
}

// covers PD-04 PD-05 C7 — PartitionEnv: the global instance's is today's; a
// user partition's env is EnvFor's byte for byte, and its remap binds its
// own volumes at the canonical paths, a shared resource from today's volume
// (Shared, RO for "read"), and records whose the namespace is.
func TestPartitionEnv(t *testing.T) {
	w := partFx(t)
	b := w.b
	nsFakeVolumes(t, b)
	c, _ := b.Reg.Component("apps/docs")
	wantEnv, wantRemap := b.DeploymentEnv(c, util.MainDeployment)
	env, remap := b.PartitionEnv(c, "", "global")
	if strings.Join(env, "\n") != strings.Join(wantEnv, "\n") || remap != nil || wantRemap != nil {
		t.Errorf("global's env/remap: %v %v", env, remap)
	}
	env, remap = b.PartitionEnv(c, util.MainDeployment, "user:alice")
	if strings.Join(env, "\n") != strings.Join(b.EnvFor(c), "\n") {
		t.Errorf("alice's env differs from EnvFor's:\n%s", strings.Join(env, "\n"))
	}
	pkey := partitionKeyFor("alice", "uid-alice")
	own := func(name string) string {
		k, err := b.resKeysIn(resTarget{Scope: "apps/docs", Name: name}, util.MainDeployment, pkey)
		if err != nil {
			t.Fatal(err)
		}
		return b.resMount(k, false)
	}
	canon := func(name string) string { return b.fsResPath("apps/docs", name, false) }
	got, _ := json.Marshal(remap)
	for name, want := range map[string]struct {
		src          string
		shared, ro   bool
		expectInEnvs bool
	}{
		"files": {own("files"), false, false, true},
		"notes": {own("notes"), false, false, true},
		"team":  {canon("team"), true, false, true},
		"conf":  {canon("conf"), true, true, true},
	} {
		rb, ok := remap[canon(name)]
		if !ok || rb.Src != want.src || rb.Shared != want.shared || rb.RO != want.ro || rb.Omit {
			t.Errorf("%s: %+v (remap %s)", name, rb, got)
		}
	}
	if len(remap) != 4 {
		t.Errorf("remap has %d entries: %s", len(remap), got)
	}
	if m, ok, _ := b.readNS(partNS("apps/docs", util.MainDeployment, pkey)); !ok || m.Partition == nil || m.Partition.User != "alice" {
		t.Errorf("alice's namespace isn't recorded: %+v", m)
	}
	if reason := b.PartitionEncryptionHoldReason("apps/docs", "", "user:alice"); reason != "" {
		t.Errorf("alice's partition held: %s", reason)
	}
	if reason := b.PartitionEncryptionHoldReason("apps/docs", "", "org:x"); reason == "" {
		t.Error("an unknown partition kind isn't held")
	}
}

// covers PD-05 PD-48 — the blob plane: a person's blob lands in their own
// partition's volume, mounted on first use (after its namespace is
// recorded), and no other partition or the global instance sees it.
func TestPartitionBlob(t *testing.T) {
	w := partFx(t)
	b := w.b
	nsFakeVolumes(t, b)
	call := func(h func(http.ResponseWriter, *http.Request), method string, p auth.Principal, rest, body string) (int, string) {
		t.Helper()
		r := zeroDataCall(t, h, method, rest, body, p)
		return r.Code, r.Body.String()
	}
	if code, body := call(b.apiBlobPut, "PUT", aliceDocs, "res:apps/docs/box/a.txt", "alice's blob"); code != 200 {
		t.Fatalf("PUT: %d %s", code, body)
	}
	if code, body := call(b.apiBlobGet, "GET", aliceDocs, "res:apps/docs/box/a.txt", ""); code != 200 || body != "alice's blob" {
		t.Errorf("alice's GET: %d %s", code, body)
	}
	for _, p := range []auth.Principal{carolDocs, docsGlobal} {
		if code, _ := call(b.apiBlobGet, "GET", p, "res:apps/docs/box/a.txt", ""); code != 404 {
			t.Errorf("%s/%s reads alice's blob: %d", p.Component, p.UserID, code)
		}
	}
	pkey := partitionKeyFor("alice", "uid-alice")
	k, _ := b.resKeysIn(resTarget{Scope: "apps/docs", Name: "box"}, util.MainDeployment, pkey)
	if data, err := os.ReadFile(filepath.Join(b.resMount(k, false), "a.txt")); err != nil || string(data) != "alice's blob" {
		t.Errorf("alice's volume holds %q %v", data, err)
	}
	if m, ok, _ := b.readNS(partNS("apps/docs", util.MainDeployment, pkey)); !ok || m.Partition == nil || m.Partition.User != "alice" {
		t.Errorf("alice's namespace isn't recorded: %+v", m)
	}
}
