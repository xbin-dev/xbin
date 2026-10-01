//go:build linux && integration

package isolated

// partitions_smoke_test.go — the partitions smoke: an end-to-end check of
// partitioned tiles as built through wave 1, on a real `xbind --isolate`
// with owner auth on (people's partitions run only under isolation, PD-19).
// It was run as work pack "S1" of wave 2 — no pack of
// plans/partitions/95-work-packs.md but an early slice of its I1
// (Integration), and unrelated to isolation finding S1 of 90-decisions §G.
// Its records: plans/partitions/records/S1.md.
//
// It runs on a dev box only: it needs user namespaces, a base rootfs and
// gocryptfs, and CI builds no rootfs, so xbindtest.Require skips it there.
// The daemon fix it found is covered in CI by internal/broker's
// TestPartitionSwitchRemountsMain.
//
// A fixture tile, apps/pt, declares "partition": ["user", "global"]. Its Go
// backend (psSource, over the SDK; partitions_smoke_fx_test.go) echoes what
// xbind told it — XBIN_PARTITION, xbin.Partition(), xbin.Caller(r), its
// instance token — reads and writes a per-partition kv resource ("kv"), a
// shared kv resource ("board", "shared": true), a per-partition filesystem
// resource ("files") and a read-shared one ("pub", "shared": "read"),
// shows its mount table and relays calls through xbin.Client. apps/pt2
// (["user"]) and apps/pcall (unpartitioned) are granted writer on it.
// People alice, bob and dave (users) and carol and erin (workspace admins)
// use them beside the owner token. Each person stores values named after
// themselves ("alice-secret", key "alice-only"), and global after itself, so
// a leak shows by name. Each subtest names the guarantee it smokes (00 §3,
// 02/03's tests, 01 §2.4-§2.6); features later packs build (F5 bus
// subscriptions, cron, vault and notify; F7a terminals; F7b per-partition
// logs, tile status and the partitions event; F9 ?xbin-partition=global;
// F10 consent; F15 personal binds) are out of scope here, beside F9's
// answers on this fixture; wave 2's are smoked by TestPartitionsSmokeW2
// (partitions_smoke_w2_test.go).
//
//	set -a; eval "$(sed -n 's/^export \([A-Z_]*\) := \(.*\)$/\1=\2/p' .dev.mk | grep -v ^PATH)"; set +a
//	go test -tags=integration -count=1 -v -run '^TestPartitionsSmoke$' ./test/isolated/
//
// TestPartitionsSmokeReap (the idle stop: over ten minutes, no knob shortens
// it) runs only with XBIN_SMOKE_REAP=1; it runs in parallel with the main
// test. Run both with the Bash sandbox disabled on a dev box.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/util"
	"github.com/xbin-dev/xbin/test/xbindtest"
)

func TestPartitionsSmoke(t *testing.T) {
	t.Parallel()
	e := psSetup(t)
	d := e.d

	// the unpartitioned caller and the tile that holds data before it asks
	// to partition, both built before any partitioned tile exists
	psWrite(t, d, psCaller, "")
	psWrite(t, d, psPlain, "")

	t.Run("zero-state", func(t *testing.T) {
		// 10 §A.1: a workspace with no partitioned tile — no header, env,
		// meta, row field, uid or partition store
		for _, who := range []psWho{
			e.who(t, "/api/"+psCaller+"/who"),
			e.who(t, "/api/"+psCaller+"/who", e.fr(t, psCaller, "alice")),
			e.who(t, "/api/"+psCaller+"/who", e.as("carol")...),
		} {
			if who.Env != "" || who.Partition != "" || who.Caller.Partition != "" || who.Caller.PartitionID != "" {
				t.Errorf("an unpartitioned tile sees partition state: %+v", who)
			}
		}
		if row := e.row(t, psCaller); row != nil {
			t.Errorf("an unpartitioned tile's row has a partition entry: %+v", *row)
		}
		doc := d.Call(t, "GET", "/c/"+psCaller+"/", nil, e.as("alice")...)
		if doc.Status != 200 || strings.Contains(string(doc.Body), "xbin-partition") {
			t.Errorf("an unpartitioned tile's document: %d, meta present: %v", doc.Status, strings.Contains(string(doc.Body), "xbin-partition"))
		}
		for p := range psPeople {
			if u := e.uid(t, p); u != "" {
				t.Errorf("%s has a uid (%s) before any partition was used", p, u)
			}
		}
		for _, p := range []string{filepath.Join(d.WS, "data", "partitions"), filepath.Join(d.WS, "data", "resources-enc", ".partitions")} {
			if _, err := os.Stat(p); err == nil {
				t.Errorf("%s exists in a workspace without a partitioned tile", p)
			}
		}
	})

	// the unpartitioned tile's data, before it asks to partition (below)
	e.put(t, "/api/"+psPlain+"/kv/kv/secret", "plain-data")

	psWrite(t, d, psTile, `["user", "global"]`)
	psWrite(t, d, psPeer, `["user"]`)
	d.Grant(t, psCaller, psTile, "writer")
	d.Grant(t, psPeer, psTile, "writer")
	// the published host, bound before any instance starts: a binding
	// restarts every live instance of the tile, people's too, which would
	// race the boot comparisons below
	d.Must(t, "POST", "/api/xbin/bindings", map[string]string{"component": psTile, "slot": "web", "provider": "runtime", "host": psHost}, 200)

	t.Run("auto-mode", func(t *testing.T) {
		// 01 §2.3: a tile that holds no data takes the manifest's mode at once
		row := e.waitState(t, psTile, "partitioned")
		if !row.User || !row.Global || row.Request != nil {
			t.Errorf("the recorded mode: %+v", *row)
		}
		m, ok := e.modeRecord(t, psTile)
		if !ok || m.Mode == nil || !m.Mode.User || !m.Mode.Global || len(m.History) == 0 || m.History[0].Op != "auto" {
			t.Errorf("mode.json: %v %+v", ok, m)
		}
		if row := e.waitState(t, psPeer, "partitioned"); !row.User || row.Global || row.Request != nil {
			t.Errorf("%s's recorded mode: %+v", psPeer, *row)
		}
	})

	ids := map[string]string{}    // person → their partition id
	boots := map[string]string{}  // person → their instance's boot id
	tokens := map[string]string{} // person → their instance token
	t.Run("own-partition", func(t *testing.T) {
		// 02 §3/§6: a person's frame reaches their own partition, which
		// learns it from XBIN_PARTITION and the headers; an admin's session
		// reaches the admin's own
		for _, p := range []string{"alice", "bob", "carol"} {
			hdr := e.fr(t, psTile, p)
			w := e.who(t, "/api/"+psTile+"/who", hdr)
			part := "user:" + p
			if w.Env != part || w.Partition != part || w.User != p || w.Caller.Partition != part || w.Caller.User != p {
				t.Errorf("%s's frame reaches %+v, want %s", p, w, part)
			}
			if !psPkey.MatchString(w.Caller.PartitionID) {
				t.Errorf("%s's X-XBin-Partition-Id %q", p, w.Caller.PartitionID)
			}
			if uid := e.uid(t, p); uid == "" || util.PartitionKey(p, uid) != w.Caller.PartitionID {
				t.Errorf("%s's partition id %q isn't PartitionKey(%s, uid %q)", p, w.Caller.PartitionID, p, uid)
			}
			ids[p], boots[p], tokens[p] = w.Caller.PartitionID, w.Boot, w.Token
			t.Logf("%s: boot %s, id %s, caller %+v", p, w.Boot, w.Caller.PartitionID, w.Caller)

			doc := d.Call(t, "GET", "/c/"+psTile+"/", nil, e.as(p)...)
			if want := `<meta name="xbin-partition" content="` + part + `">`; doc.Status != 200 || !strings.Contains(string(doc.Body), want) {
				t.Errorf("%s's document: %d, want %s in %s", p, doc.Status, want, cut(doc.String(), 600))
			}
		}
		if w := e.who(t, "/api/"+psTile+"/who", e.as("carol")...); w.Env != "user:carol" || w.Boot != boots["carol"] {
			t.Errorf("carol's (admin) session reaches %+v, her frame boot %s", w, boots["carol"])
		}
		psExpect(t, "alice's own session on the tile's API (a user's session isn't its principal)",
			d.Call(t, "GET", "/api/"+psTile+"/who", nil, e.as("alice")...), nil, psWant{403, "user:alice is not granted access to " + psTile, false})
		if ids["alice"] == ids["bob"] || boots["alice"] == boots["bob"] || tokens["alice"] == tokens["bob"] || tokens["alice"] == "" {
			t.Errorf("alice and bob share an instance: ids %v boots %v", ids, boots)
		}
	})

	t.Run("data-apart", func(t *testing.T) {
		// 03 §B: each partition's kv and filesystem are its own; a shared
		// resource is one copy. Each person stores a key and a file named
		// after them, so a listing served from another partition shows.
		for _, p := range []string{"alice", "bob", "carol"} {
			fr := e.fr(t, psTile, p)
			e.put(t, "/api/"+psTile+"/kv/kv/secret", p+"-secret", fr)
			e.put(t, "/api/"+psTile+"/kv/kv/"+p+"-only", p+"-only", fr)
			e.put(t, "/api/"+psTile+"/fs/files/note", p+"-note", fr)
			e.put(t, "/api/"+psTile+"/fs/files/"+p+"-only", p+"-only", fr)
		}
		e.put(t, "/api/"+psTile+"/kv/kv/global-only", "global-only") // the owner token: global's
		e.put(t, "/api/"+psTile+"/fs/files/global-only", "global-only")
		e.put(t, "/api/"+psTile+"/kv/board/shared", "from-alice", e.fr(t, psTile, "alice"))
		for _, p := range []string{"alice", "bob", "carol"} {
			fr := e.fr(t, psTile, p)
			if v, st, body := e.value(t, "/api/"+psTile+"/kv/kv/secret", fr); v != p+"-secret" {
				t.Errorf("%s's kv: %d %s", p, st, body)
			}
			if v, st, body := e.value(t, "/api/"+psTile+"/fs/files/note", fr); v != p+"-note" {
				t.Errorf("%s's file: %d %s", p, st, body)
			}
			if v, st, body := e.value(t, "/api/"+psTile+"/kv/board/shared", fr); v != "from-alice" {
				t.Errorf("the shared board for %s: %d %s", p, st, body)
			}
			if got, want := e.names(t, "/api/"+psTile+"/keys/kv", fr), []string{p + "-only", "secret"}; !slices.Equal(got, want) {
				t.Errorf("%s's kv keys: %q, want %q", p, got, want)
			}
			if got, want := e.names(t, "/api/"+psTile+"/ls?res=files", fr), []string{p + "-only", "note"}; !slices.Equal(got, want) {
				t.Errorf("%s's files: %q, want %q", p, got, want)
			}
			if _, err := os.Stat(e.partDir(psTile, ids[p])); err != nil {
				t.Errorf("%s's partition namespace on disk: %v", p, err)
			}
		}
		if got, want := e.names(t, "/api/"+psTile+"/keys/kv"), []string{"global-only"}; !slices.Equal(got, want) {
			t.Errorf("global's kv keys: %q, want %q", got, want)
		}
		if got, want := e.names(t, "/api/"+psTile+"/ls?res=files"), []string{"global-only"}; !slices.Equal(got, want) {
			t.Errorf("global's files: %q, want %q", got, want)
		}
		// inside alice's sandbox (03 §Tests): her own partition's volume is
		// mounted — gocryptfs names its ciphertext directory, the partition
		// id in it — and no other partition's is
		mi, st, body := e.value(t, "/api/"+psTile+"/mountinfo", e.fr(t, psTile, "alice"))
		if st != 200 {
			t.Fatalf("alice's mount table: %d %s", st, body)
		}
		var parts []string
		for _, line := range strings.Split(mi, "\n") {
			if strings.Contains(line, ".partitions/") {
				parts = append(parts, line)
			}
		}
		t.Logf("alice's sandbox mounts of partition volumes: %q", parts)
		if len(parts) == 0 || !strings.Contains(strings.Join(parts, "\n"), ids["alice"]) {
			t.Errorf("alice's mount table shows no volume of her partition (%s), so it can't show bob's either: %s", ids["alice"], cut(mi, 2000))
		}
		for _, line := range parts {
			if !strings.Contains(line, ids["alice"]) {
				t.Errorf("BUG: alice's sandbox mounts another partition's volume: %s", line)
			}
		}
		for _, p := range []string{"bob", "carol"} {
			if strings.Contains(mi, ids[p]) {
				t.Errorf("BUG: alice's sandbox mount table names %s's partition (%s)", p, ids[p])
			}
		}
		// "shared": "read" — global writes, people's partitions only read
		e.put(t, "/api/"+psTile+"/fs/pub/readme", "from-global")
		for _, p := range []string{"alice", "bob"} {
			if v, st, body := e.value(t, "/api/"+psTile+"/fs/pub/readme", e.fr(t, psTile, p)); v != "from-global" {
				t.Errorf("the read-shared filesystem for %s: %d %s", p, st, body)
			}
		}
		psExpect(t, "alice's write to the read-shared filesystem",
			d.Call(t, "PUT", "/api/"+psTile+"/fs/pub/mine", "alice-pub", e.fr(t, psTile, "alice")), nil, psWant{502, "read-only file system", false})
		psExpect(t, "global's read of what alice's write would have made",
			d.Call(t, "GET", "/api/"+psTile+"/fs/pub/mine", nil), nil, psWant{404, `"not found"`, false})
	})

	t.Run("deployment", func(t *testing.T) {
		// PD-17, 03 §B.9: people's partitions live on the primary alone; a
		// non-primary deployment seeded from the primary's data gets global's
		// (today's keys), never a person's — and serves one shared instance
		e.put(t, "/api/"+psTile+"/kv/kv/secret", "global-before-dev")
		add := d.Call(t, "POST", "/api/xbin/deployments/add", map[string]any{"tile": psTile, "deployment": "dev", "data": "seed", "confirm": "copy-data"})
		if add.Status != 200 {
			t.Fatalf("adding dev, seeded: %d %s", add.Status, add)
		}
		t.Logf("adding dev: %s", cut(add.String(), 300))
		dev := "/api/" + psTile + "+dev"
		w := e.who(t, dev+"/who")
		t.Logf("the owner token on dev: %+v", w)
		if w.Env != "global" || w.Caller.Partition != "global" || w.Caller.PartitionID != "" {
			t.Errorf("dev's instance isn't the one shared instance: %+v", w)
		}
		if v, st, body := e.value(t, dev+"/kv/kv/secret"); v != "global-before-dev" {
			t.Errorf("dev's seeded kv: %d %s (want global's)", st, body)
		}
		forbid := psForbid("global", false)
		psExpect(t, "dev's files, a person's note", d.Call(t, "GET", dev+"/fs/files/note", nil), forbid, psWant{404, `"not found"`, false})
		if got, want := e.names(t, dev+"/ls?res=files"), []string{"global-only"}; !slices.Equal(got, want) {
			t.Errorf("dev's seeded files: %q, want global's %q", got, want)
		}
		if got, want := e.names(t, dev+"/keys/kv"), []string{"global-only", "secret"}; !slices.Equal(got, want) {
			t.Errorf("dev's seeded kv keys: %q, want global's %q", got, want)
		}
		if got, want := e.names(t, dev+"/ls?path="+filepath.Join(w.Files, "..")), []string{"files", "pub"}; !slices.Equal(got, want) {
			t.Errorf("dev's resource directory: %q, want %q", got, want)
		}
		// carol (admin, a tile writer) reaches dev's shared instance; bob (a
		// reader) gets no frame of it
		var ft struct{ Token string }
		d.Must(t, "GET", "/api/xbin/frame-token?component="+psTile+"&deployment=dev", nil, 200, e.as("carol")...).Decode(t, &ft)
		cw := e.who(t, dev+"/who", xbindtest.FrameHeader(ft.Token))
		if cw.Env != "global" || cw.Caller.PartitionID != "" || cw.Boot != w.Boot {
			t.Errorf("carol's frame of dev reaches %+v, want dev's one instance (boot %s)", cw, w.Boot)
		}
		psExpect(t, "carol's frame of dev", d.Call(t, "GET", dev+"/kv/kv/secret", nil, xbindtest.FrameHeader(ft.Token)), forbid, psOK("global-before-dev"))
		psExpect(t, "bob's (a reader) frame token of dev",
			d.Call(t, "GET", "/api/xbin/frame-token?component="+psTile+"&deployment=dev", nil, e.as("bob")...), nil, psWant{403, "", false})
		// alice's frame of the primary on dev's URL: never her partition there
		psExpect(t, "alice's primary frame on dev's URL", d.Call(t, "GET", dev+"/kv/kv/secret", nil, e.fr(t, psTile, "alice")),
			[]string{"alice-", "global-"}, psWant{403, "never reaches another deployment of its own tile", false})
		e.put(t, "/api/"+psTile+"/kv/kv/secret", "global-secret") // global's, as before
	})

	t.Run("bob-never-sees-alice", func(t *testing.T) {
		// G1: no route hands alice's partition data to bob. Every attempt
		// has its exact answer: bob's own value where it reaches bob's
		// partition (and never global's), global's where an unpartitioned
		// tile calls, the refusal where it's refused.
		bobFrame := e.fr(t, psTile, "bob")
		callerFrame := e.fr(t, psCaller, "bob")
		bobSess := e.as("bob")[0]
		bobTok := xbindtest.H("Authorization", "Bearer "+tokens["bob"])
		spoofs := []xbindtest.Header{xbindtest.H("X-XBin-Partition", "user:alice"), xbindtest.H("X-XBin-Partition-Id", ids["alice"]),
			xbindtest.H("X-XBin-User", "alice"), xbindtest.H("X-XBin-From", psTile), xbindtest.H("X-XBin-Deployment", "main")}
		with := func(h xbindtest.Header, more ...xbindtest.Header) []xbindtest.Header {
			return append([]xbindtest.Header{h}, more...)
		}
		type attempt struct {
			name string
			path string
			hdrs []xbindtest.Header
			own  bool     // it reaches bob's partition: global's data is as wrong as alice's
			want []psWant // the one answer (any of them, where several are listed)
		}
		ungranted := psWant{403, "user:bob is not granted access to " + psTile, false}
		otherDep := psWant{403, "never reaches another deployment of its own tile", false}
		kv, file, api := "/api/"+psTile+"/kv/kv/secret", "/api/"+psTile+"/fs/files/note", "/api/xbin/kv/res:"+psTile+"/kv/secret"
		keys, ls, apiList := "/api/"+psTile+"/keys/kv", "/api/"+psTile+"/ls?res=files", "/api/xbin/kv/res:"+psTile+"/kv/?prefix="
		atts := []attempt{
			{"frame", kv, with(bobFrame), true, []psWant{psOK("bob-secret")}},
			{"frame, file", file, with(bobFrame), true, []psWant{psOK("bob-note")}},
			{"frame, kv keys", keys, with(bobFrame), true, []psWant{psList("bob-only")}},
			{"frame, files listing", ls, with(bobFrame), true, []psWant{psList("bob-only")}},
			// F9 (05 §6): ?xbin-partition=global is an attributed call to
			// global (bob's frame is the tile's own credential), never
			// alice's partition; any other value is refused
			{"frame, ?xbin-partition=global", kv + "?xbin-partition=global", with(bobFrame), false, []psWant{psOK("global-secret")}},
			{"frame, ?xbin-partition=user:alice", file + "?xbin-partition=user:alice", with(bobFrame), true, []psWant{psBadPartitionParam}},
			{"frame, spoofed X-XBin-* headers", kv, with(bobFrame, spoofs...), true, []psWant{psOK("bob-secret")}},
			{"frame, spoofed headers, file", file, with(bobFrame, spoofs...), true, []psWant{psOK("bob-note")}},
			{"frame, spoofed headers, kv keys", keys, with(bobFrame, spoofs...), true, []psWant{psList("bob-only")}},
			{"session", kv, with(bobSess), true, []psWant{ungranted}},
			{"session, spoofed headers", kv, with(bobSess, spoofs...), true, []psWant{ungranted}},
			{"the primary's deployment URL", "/api/" + psTile + "+main/kv/kv/secret", with(bobFrame), true, []psWant{psOK("bob-secret")}},
			{"the primary's deployment URL, kv keys", "/api/" + psTile + "+main/keys/kv", with(bobFrame), true, []psWant{psList("bob-only")}},
			{"the primary's deployment URL, session", "/api/" + psTile + "+main/fs/files/note", with(bobSess), true, []psWant{ungranted}},
			{"the primary's deployment URL, spoofed headers", "/api/" + psTile + "+main/fs/files/note", with(bobFrame, spoofs...), true, []psWant{psOK("bob-note")}},
			{"dev's URL, frame", "/api/" + psTile + "+dev/kv/kv/secret", with(bobFrame), true, []psWant{otherDep}},
			{"dev's URL, frame, file", "/api/" + psTile + "+dev/fs/files/note", with(bobFrame), true, []psWant{otherDep}},
			{"dev's URL, session", "/api/" + psTile + "+dev/fs/files/note", with(bobSess), true, []psWant{{403, "deployment URLs need write access on " + psTile, false}}},
			{"kv API, frame", api, with(bobFrame), true, []psWant{psOK("bob-secret")}},
			{"kv API, frame, spoofed headers", api, with(bobFrame, spoofs...), true, []psWant{psOK("bob-secret")}},
			{"kv API list, frame", apiList, with(bobFrame), true, []psWant{psList("bob-only")}},
			{"kv API, session", api, with(bobSess), true, []psWant{{403, "unauthenticated", false}}},
			{"kv API, bob's instance token", api, with(bobTok), true, []psWant{psOK("bob-secret")}},
			{"kv API, bob's instance token, spoofed headers", api, with(bobTok, spoofs...), true, []psWant{psOK("bob-secret")}},
			{"kv API list, bob's instance token", apiList, with(bobTok), true, []psWant{psList("bob-only")}},
			{"bob's instance token on the tile's API", kv, with(bobTok), true, []psWant{psOK("bob-secret")}},
			{"bob's instance token on the tile's API, kv keys", keys, with(bobTok), true, []psWant{psList("bob-only")}},
			{"an unpartitioned tile's frame of bob's", kv, with(callerFrame), false, []psWant{psOK("global-secret")}},
			{"an unpartitioned tile's frame of bob's, kv keys", keys, with(callerFrame), false, []psWant{psList("global-only")}},
			{"the unpartitioned tile's backend, as bob's frame", "/api/" + psCaller + "/call?path=" + kv, with(callerFrame), false, []psWant{psOK("global-secret")}},
		}
		for _, a := range atts {
			r := d.Call(t, "GET", a.path, nil, a.hdrs...)
			psExpect(t, a.name, r, psForbid("bob", a.own), a.want...)
			t.Logf("%s: %d %s", a.name, r.Status, cut(r.String(), 160))
		}
		// his documents name his partition, the primary's deployment URL's
		// too; dev's is refused (he can't write the tile)
		for _, p := range []string{"/c/" + psTile + "/", "/c/" + psTile + "+main/", "/c/" + psTile + "+dev/"} {
			doc := d.Call(t, "GET", p, nil, bobSess)
			if strings.Contains(string(doc.Body), "user:alice") {
				t.Errorf("BUG: %s names alice's partition to bob", p)
			}
			mine := strings.Contains(string(doc.Body), `content="user:bob"`)
			if strings.Contains(p, "+dev") && (doc.Status != 403 || mine) || !strings.Contains(p, "+dev") && (doc.Status != 200 || !mine) {
				t.Errorf("%s for bob: %d, his meta: %v", p, doc.Status, mine)
			}
		}
	})

	t.Run("cross-tile", func(t *testing.T) {
		// 02 §4 rule 4 (PD-13): a partitioned tile's call to another it is
		// granted on reaches the same person's partition there, while that
		// person can read the target; rule 2: a self-call never leaves its
		// partition. The calls go out through the probe's xbin.Client, with
		// each partition instance's own token.
		call := func(tile, person, path string) xbindtest.Resp {
			return d.Call(t, "GET", "/api/"+tile+"/call?path="+path, nil, e.fr(t, tile, person))
		}
		kv := "/api/" + psTile + "/kv/kv/secret"
		for _, p := range []string{"alice", "bob"} {
			if w := e.who(t, "/api/"+psPeer+"/who", e.fr(t, psPeer, p)); w.Env != "user:"+p {
				t.Errorf("%s's frame of %s reaches %+v", p, psPeer, w)
			}
			psExpect(t, p+"'s "+psPeer+" → "+psTile, call(psPeer, p, kv), psForbid(p, true), psOK(p+"-secret"))
			psExpect(t, p+"'s "+psPeer+" → "+psTile+", kv keys", call(psPeer, p, "/api/"+psTile+"/keys/kv"), psForbid(p, true), psList(p+"-only"))
			if w, st := relayWho(t, call(psPeer, p, "/api/"+psTile+"/who")); st != 200 || w.Env != "user:"+p || w.Caller.From != psPeer ||
				w.Caller.Partition != "user:"+p || w.Caller.PartitionID != ids[p] {
				t.Errorf("%s's %s → %s reaches %d %+v, want their partition (%s)", p, psPeer, psTile, st, w, ids[p])
			}
			psExpect(t, p+"'s self-call", call(psTile, p, kv), psForbid(p, true), psOK(p+"-secret"))
			if w, st := relayWho(t, call(psTile, p, "/api/"+psTile+"/who")); st != 200 || w.Env != "user:"+p || w.Caller.From != psTile ||
				w.Caller.Partition != "user:"+p || w.Caller.PartitionID != ids[p] {
				t.Errorf("%s's self-call reaches %d %+v, want their own partition (%s)", p, st, w, ids[p])
			}
		}
		// bob loses read of apps/pt: his partition of apps/pt2 reaches none
		// of his apps/pt data; alice's is as it was
		d.Must(t, "PATCH", "/api/xbin/users/bob", map[string]any{"tiles": map[string]string{psPeer: "read", psCaller: "read", psPlain: "read"}}, 200)
		psExpect(t, "bob's "+psPeer+" → "+psTile+" once he can't read it", call(psPeer, "bob", kv), psForbid("bob", true),
			psWant{403, "bob can't read " + psTile, false})
		psExpect(t, "alice's "+psPeer+" → "+psTile+" meanwhile", call(psPeer, "alice", kv), psForbid("alice", true), psOK("alice-secret"))
		d.Must(t, "PATCH", "/api/xbin/users/bob", map[string]any{"tiles": map[string]string{"apps/*": "read"}}, 200)
		e.forget("bob")
		psExpect(t, "bob's "+psPeer+" → "+psTile+" once he reads it again", call(psPeer, "bob", kv), psForbid("bob", true), psOK("bob-secret"))
	})

	t.Run("global", func(t *testing.T) {
		// 02 §3, 05 §5: the root token, another (unpartitioned) tile and
		// ingress reach the global instance, at today's keys
		w := e.who(t, "/api/"+psTile+"/who")
		if w.Env != "global" || w.Partition != "global" || w.User != "" || w.Caller.Partition != "global" || w.Caller.PartitionID != "" {
			t.Errorf("the owner token reaches %+v, want global", w)
		}
		e.put(t, "/api/"+psTile+"/kv/kv/secret", "global-secret")
		if v, st, body := e.value(t, "/api/"+psTile+"/kv/kv/secret"); v != "global-secret" {
			t.Errorf("global's kv: %d %s", st, body)
		}
		if v, _, _ := e.value(t, "/api/"+psTile+"/kv/kv/secret", e.fr(t, psTile, "alice")); v != "alice-secret" {
			t.Errorf("alice's kv after global's write: %q", v)
		}
		// the unpartitioned tile's backend (its grant) and its frame
		var c struct {
			Status int
			Body   string
		}
		d.Must(t, "GET", "/api/"+psCaller+"/call?path=/api/"+psTile+"/who", nil, 200).Decode(t, &c)
		var cw psWho
		if c.Status != 200 || json.Unmarshal([]byte(c.Body), &cw) != nil || cw.Env != "global" || cw.Caller.From != psCaller || cw.Caller.Partition != "" {
			t.Errorf("the unpartitioned tile's backend reaches: %d %s", c.Status, c.Body)
		}
		fw := e.who(t, "/api/"+psTile+"/who", e.fr(t, psCaller, ""))
		if fw.Env != "global" || fw.Caller.From != psCaller || fw.Caller.Partition != "" || fw.Boot != w.Boot {
			t.Errorf("the unpartitioned tile's frame reaches %+v, want global (boot %s)", fw, w.Boot)
		}
		aw := e.who(t, "/api/"+psTile+"/who", e.fr(t, psCaller, "alice"))
		if aw.Env != "global" || aw.Caller.Partition != "" {
			t.Errorf("alice's frame of the unpartitioned tile reaches %+v, want global", aw)
		}
		d.Must(t, "GET", "/api/"+psCaller+"/call?path=/api/"+psTile+"/kv/kv/secret", nil, 200).Decode(t, &c)
		if !strings.Contains(c.Body, "global-secret") {
			t.Errorf("the unpartitioned tile's backend reads: %d %s", c.Status, c.Body)
		}
		// ingress: the published host (bound above) reaches global only
		var pub psWho
		xbindtest.Eventually(t, time.Minute, "ingress serves "+psHost, func() (bool, string) {
			req, _ := http.NewRequest("GET", "http://"+e.ingress+"/pub/who", nil)
			req.Host = psHost
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return false, err.Error()
			}
			defer resp.Body.Close()
			b, _ := io.ReadAll(resp.Body)
			return resp.StatusCode == 200 && json.Unmarshal(b, &pub) == nil, fmt.Sprint(resp.StatusCode, " ", string(b))
		})
		if pub.Env != "global" || pub.Caller.From != "ingress" || pub.Caller.Partition != "" || pub.Boot != w.Boot {
			t.Errorf("ingress reaches %+v, want global (boot %s)", pub, w.Boot)
		}
	})

	t.Run("admin", func(t *testing.T) {
		// G2: an admin reaches their own partition, never a person's; the
		// root token reaches global. Each answer is exact: carol's own
		// values, global's for the root token.
		type route struct {
			path         string
			carol, owner []psWant
		}
		routes := []route{
			{"/api/" + psTile + "/kv/kv/secret", []psWant{psOK("carol-secret")}, []psWant{psOK("global-secret")}},
			{"/api/" + psTile + "/kv/kv/secret?xbin-partition=user:alice", []psWant{psBadPartitionParam}, []psWant{psBadPartitionParam}},
			{"/api/" + psTile + "/fs/files/note", []psWant{psOK("carol-note")}, []psWant{{404, `"not found"`, false}}},
			{"/api/" + psTile + "/ls?res=files", []psWant{psList("carol-only")}, []psWant{psList("global-only")}},
			{"/api/" + psTile + "/keys/kv", []psWant{psList("carol-only")}, []psWant{psList("global-only")}},
			{"/api/xbin/kv/res:" + psTile + "/kv/secret", []psWant{psOK("carol-secret")}, []psWant{psOK("global-secret")}},
			{"/api/xbin/kv/res:" + psTile + "/kv/?prefix=", []psWant{psList("carol-only")}, []psWant{psList("global-only")}},
		}
		for _, rt := range routes {
			for name, hdrs := range map[string][]xbindtest.Header{"carol's session": e.as("carol"), "carol's frame": {e.fr(t, psTile, "carol")}} {
				r := d.Call(t, "GET", rt.path, nil, hdrs...)
				psExpect(t, name+", "+rt.path, r, psForbid("carol", true), rt.carol...)
			}
			r := d.Call(t, "GET", rt.path, nil)
			psExpect(t, "the owner token, "+rt.path, r, psForbid("global", false), rt.owner...)
		}
		// admins see who runs (metadata), never what it holds
		r := d.Must(t, "GET", "/api/xbin/sandboxes", nil, 200, e.as("carol")...)
		for _, f := range psForbid("carol", true) {
			if strings.Contains(string(r.Body), f) {
				t.Errorf("BUG: the sandbox list shows data (%q)", f)
			}
		}
		t.Logf("the sandbox list names alice's partition: %v", strings.Contains(string(r.Body), `"user:alice"`))
	})

	t.Run("logs", func(t *testing.T) {
		// 03 §A.4, 06 §5 (F7b): a person's instance logs to its partition's
		// own file; GET /logs of the tile answers each person — their session
		// and their frames, at any level — their own partition's log; the
		// global instance's is the root token's, and an admin's with
		// ?xbin-partition=global; an admin reads a person's only while that
		// person shares it; nobody's answer holds another's data
		own := filepath.Join(d.WS, ".xbin", "partition", util.TileKey(psTile), "main", ids["alice"], "backend.log")
		if b, err := os.ReadFile(own); err != nil || !strings.Contains(string(b), "alice-secret") {
			t.Errorf("alice's partition log doesn't hold what her instance logged (%s): %v %s", own, err, cut(string(b), 300))
		}
		logs := "/api/xbin/logs?component=" + psTile + "&tail=5000"
		for name, v := range map[string]struct {
			url  string
			hdrs []xbindtest.Header
		}{"the owner token": {logs, nil}, "carol's session, ?xbin-partition=global": {logs + "&xbin-partition=global", e.as("carol")}} {
			r := d.Call(t, "GET", v.url, nil, v.hdrs...)
			for _, f := range psForbid("global", false) {
				if strings.Contains(string(r.Body), f) {
					t.Errorf("BUG: %s's log of %s holds a person's data (%q): %s", name, psTile, f, cut(r.String(), 600))
				}
			}
			if r.Status != 200 || !strings.Contains(string(r.Body), "stored kv kv/global-only = global-only") {
				t.Errorf("%s's log of %s isn't global's: %d %s", name, psTile, r.Status, cut(r.String(), 600))
			}
		}
		for _, p := range []string{"alice", "bob"} {
			for name, hdrs := range map[string][]xbindtest.Header{p + "'s session": e.as(p), p + "'s frame": {e.fr(t, psTile, p)}} {
				r := d.Call(t, "GET", logs, nil, hdrs...)
				for _, f := range psForbid(p, false) {
					if strings.Contains(string(r.Body), f) {
						t.Errorf("BUG: %s, the log of %s holds %q: %s", name, psTile, f, cut(r.String(), 600))
					}
				}
				if r.Status != 200 || !strings.Contains(string(r.Body), p+"-secret") || r.Header.Get("X-XBin-Partition") != "user:"+p {
					t.Errorf("%s isn't %s's own partition log: %d %q %s", name, p, r.Status, r.Header.Get("X-XBin-Partition"), cut(r.String(), 300))
				}
			}
		}
		psExpect(t, "alice's frame, ?xbin-partition=global", d.Call(t, "GET", logs+"&xbin-partition=global", nil, e.fr(t, psTile, "alice")),
			psForbid("alice", false), psWant{403, "reads its own log", false})
		psExpect(t, "?partition=", d.Call(t, "GET", logs+"&partition=user:alice", nil, e.as("carol")...), psForbid("", false),
			psWant{400, "take no ?partition=", false})
		// carol, an admin, reads alice's log only while alice shares it
		shared := logs + "&user=alice"
		psExpect(t, "carol, alice's unshared log", d.Call(t, "GET", shared, nil, e.as("carol")...), psForbid("", false),
			psWant{403, "doesn't share", false})
		d.Must(t, "POST", "/api/xbin/partitions/share-log", map[string]any{"tile": psTile, "days": 1}, 200, e.as("alice")...)
		if r := d.Call(t, "GET", shared, nil, e.as("carol")...); r.Status != 200 || !strings.Contains(string(r.Body), "alice-secret") {
			t.Errorf("carol, alice's shared log: %d %s", r.Status, cut(r.String(), 300))
		}
		psExpect(t, "bob, alice's shared log", d.Call(t, "GET", shared, nil, e.as("bob")...), psForbid("bob", false),
			psWant{403, "reads it only while they share it", false})
		// carol follows it; alice stops sharing: the follow ends by itself (F7b fix)
		followed := make(chan string, 1)
		go func() {
			req, _ := http.NewRequest("GET", d.URL+shared+"&follow=1", nil)
			for _, h := range e.as("carol") {
				req.Header.Set(h.K, h.V)
			}
			resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
			if err != nil {
				followed <- "error: " + err.Error()
				return
			}
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			followed <- string(b)
		}()
		time.Sleep(time.Second)
		d.Must(t, "DELETE", "/api/xbin/partitions/share-log", map[string]any{"tile": psTile}, 200, e.as("alice")...)
		select {
		case body := <-followed:
			if !strings.Contains(body, "alice-secret") || !strings.Contains(body, "stream closed") {
				t.Errorf("carol's follow of alice's shared log: %s", cut(body, 400))
			}
		case <-time.After(20 * time.Second):
			t.Error("BUG: carol's follow of alice's log kept streaming after alice stopped sharing")
		}
		psExpect(t, "carol, after alice stopped sharing", d.Call(t, "GET", shared, nil, e.as("carol")...), psForbid("", false),
			psWant{403, "doesn't share", false})
		// tile-status: a person's partition's own credential reads its own
		var st struct {
			Partition string
			Backend   *struct{ State string }
		}
		d.Must(t, "GET", "/api/xbin/tile-status?component="+psTile, nil, 200, e.fr(t, psTile, "alice")).Decode(t, &st)
		if st.Partition != "user:alice" || st.Backend == nil {
			t.Errorf("alice's frame's tile-status: %+v", st)
		}
	})

	t.Run("ops", func(t *testing.T) {
		// 06 §6 (F7b): GET /partitions per audience — a person their own row,
		// an admin every person's metadata, tile code the tile's fields — and
		// a person's stop of their own instance
		var mine, admin, tile struct {
			Features   []string
			State      string
			Partitions []struct {
				User    string
				Running bool
			}
		}
		d.Must(t, "GET", "/api/xbin/partitions?tile="+psTile, nil, 200, e.as("alice")...).Decode(t, &mine)
		d.Must(t, "GET", "/api/xbin/partitions?tile="+psTile, nil, 200, e.as("carol")...).Decode(t, &admin)
		d.Must(t, "GET", "/api/xbin/partitions?tile="+psTile, nil, 200, e.fr(t, psTile, "alice")).Decode(t, &tile)
		if len(mine.Partitions) != 1 || mine.Partitions[0].User != "alice" || !slices.Contains(mine.Features, "partitions/1") {
			t.Errorf("alice's listing: %+v", mine)
		}
		if len(admin.Partitions) < 2 || tile.Partitions != nil || tile.State != "partitioned" {
			t.Errorf("carol's listing %+v, the tile's own %+v", admin, tile)
		}
		psConsole(t, d, e) // F12: the admin console's view (partitions_smoke_console_test.go)
		// bob stops his own (alice's boot is the caps case's witness: untouched)
		before := e.who(t, "/api/"+psTile+"/who", e.fr(t, psTile, "bob"))
		d.Must(t, "POST", "/api/xbin/partitions/stop", map[string]string{"tile": psTile, "partition": "user:bob"}, 200, e.as("bob")...)
		psExpect(t, "alice stops bob's", d.Call(t, "POST", "/api/xbin/partitions/stop", map[string]string{"tile": psTile, "partition": "user:bob"}, e.as("alice")...),
			nil, psWant{403, "tile manager's or an admin's", false})
		if after := e.who(t, "/api/"+psTile+"/who", e.fr(t, psTile, "bob")); after.Boot == before.Boot || after.Env != "user:bob" {
			t.Errorf("bob's instance after his stop: %+v (boot before %s)", after, before.Boot)
		}
		psExpect(t, "bob after his stop", d.Call(t, "GET", "/api/"+psTile+"/kv/kv/secret", nil, e.fr(t, psTile, "bob")), psForbid("bob", true), psOK("bob-secret"))
	})

	t.Run("view-as", func(t *testing.T) {
		// 02 §3/§10, G2, PD-08: an admin viewing the workspace as a person
		// (D64) reaches no partition of theirs — no frame token, no data.
		// alice is a user: her session is no API principal of the tile, so
		// its routes refuse her at the grant; erin is an admin, whose
		// session is one, so the partition gate itself refuses.
		e.put(t, "/api/"+psTile+"/kv/kv/secret", "erin-secret", e.fr(t, psTile, "erin"))
		e.put(t, "/api/"+psTile+"/fs/files/note", "erin-note", e.fr(t, psTile, "erin"))
		paths := []string{
			"/api/" + psTile + "/kv/kv/secret",
			"/api/" + psTile + "/fs/files/note",
			"/api/" + psTile + "/who",
			"/api/xbin/kv/res:" + psTile + "/kv/secret",
		}
		for _, v := range []struct{ person, tileGate string }{
			{"alice", "user:alice is not granted access to " + psTile},
			{"erin", psTile + " keeps erin's data private: view-as can't open it"},
		} {
			view := e.viewAs(t, "carol", v.person)
			pd08 := psTile + " keeps " + v.person + "'s data private: view-as can't open it"
			for _, p := range paths {
				st, body, _ := psRaw(t, "GET", d.URL+p, view)
				want := v.tileGate
				if strings.HasPrefix(p, "/api/xbin/") {
					want = pd08
				}
				if strings.Contains(body, v.person+"-") {
					t.Errorf("BUG: view-as %s reads their partition at %s: %d %s", v.person, p, st, body)
				} else if st != 403 || !strings.Contains(body, want) {
					t.Errorf("view-as %s, %s: %d %s, want 403 %q", v.person, p, st, cut(body, 300), want)
				}
			}
			st, body, _ := psRaw(t, "GET", d.URL+"/c/"+psTile+"/", view)
			if st != 200 || !strings.Contains(body, "the probe's page") {
				t.Errorf("view-as %s's document of %s: %d %s", v.person, psTile, st, cut(body, 300))
			}
			if strings.Contains(body, `name="xbin-frame-token" content="`) && !strings.Contains(body, `name="xbin-frame-token" content=""`) {
				t.Errorf("view-as %s's document of the partitioned tile carries a frame token", v.person)
			}
			if strings.Contains(body, `content="user:`+v.person+`"`) {
				t.Errorf("view-as %s's document names their partition", v.person)
			}
			// the renewal route still mints one (02 §10 keeps renewal as
			// is); it carries the impersonator, so the partition gate
			// refuses it everywhere
			st, body, _ = psRaw(t, "GET", d.URL+"/api/xbin/frame-token?component="+psTile, view)
			var ft struct{ Token string }
			if st != 200 || json.Unmarshal([]byte(body), &ft) != nil || ft.Token == "" {
				t.Errorf("view-as %s, GET /frame-token: %d %s (the notes expect a token that opens nothing)", v.person, st, cut(body, 200))
				continue
			}
			for _, p := range paths {
				r := d.Call(t, "GET", p, nil, xbindtest.FrameHeader(ft.Token))
				psExpect(t, "view-as "+v.person+"'s renewed frame token, "+p, r, []string{v.person + "-", "user:" + v.person + `"`}, psWant{403, pd08, false})
			}
		}
	})

	t.Run("caps", func(t *testing.T) {
		// 03 §A.5 (PD-18): past the tile's cap, with every partition in use,
		// a new person's start is 503; nothing of another person's is stopped
		running := 0
		for _, p := range []string{"alice", "bob", "carol", "erin"} {
			e.who(t, "/api/"+psTile+"/who", e.fr(t, psTile, p)) // in use: an interactive request now
			running++
		}
		d.Must(t, "POST", "/api/xbin/partitions/limits", map[string]any{"tile": psTile, "maxRunning": running}, 200)
		r := d.Call(t, "GET", "/api/"+psTile+"/who", nil, e.fr(t, psTile, "dave"))
		if r.Status != 503 || !strings.Contains(string(r.Body), "too many people's instances of "+psTile) {
			t.Errorf("dave past the cap: %d %s", r.Status, r)
		}
		if w := e.who(t, "/api/"+psTile+"/who", e.fr(t, psTile, "alice")); w.Boot != boots["alice"] {
			t.Errorf("alice's instance changed (boot %s → %s) under the cap", boots["alice"], w.Boot)
		}
		d.Must(t, "POST", "/api/xbin/partitions/limits", map[string]any{"tile": psTile, "maxRunning": 0}, 200)
		if w := e.who(t, "/api/"+psTile+"/who", e.fr(t, psTile, "dave")); w.Env != "user:dave" {
			t.Errorf("dave after the cap was cleared: %+v", w)
		}
	})

	t.Run("token-revocation", func(t *testing.T) {
		// 02 §2, S13: a user partition's instance token authenticates only
		// while its partition is covered (the person enabled, reading the
		// tile) and only for the instance's own generation
		kv := "/api/xbin/kv/res:" + psTile + "/kv/secret"
		cur := e.who(t, "/api/"+psTile+"/who", e.fr(t, psTile, "alice"))
		tokens["alice"], boots["alice"] = cur.Token, cur.Boot
		// a restart: new code restarts every instance of the tile, people's
		// too, and the earlier generation's token stops authenticating
		if err := d.WriteFiles(psTile, map[string]string{"backend/main.go": psSource + "\n// a new generation\n"}); err != nil {
			t.Fatal(err)
		}
		var next psWho
		xbindtest.Eventually(t, 4*time.Minute, "alice's instance restarts on the new code", func() (bool, string) {
			r := d.Call(t, "GET", "/api/"+psTile+"/who", nil, e.fr(t, psTile, "alice"))
			if r.Status != 200 || json.Unmarshal(r.Body, &next) != nil {
				return false, fmt.Sprint(r.Status, " ", r)
			}
			return next.Boot != cur.Boot, "still boot " + next.Boot
		})
		if next.Env != "user:alice" || next.Token == "" || next.Token == cur.Token {
			t.Errorf("alice's restarted instance: %+v (token before %s…)", next, cut(cur.Token, 8))
		}
		psExpect(t, "alice's earlier generation's token after the restart", d.Call(t, "GET", kv, nil, xbindtest.H("Authorization", "Bearer "+cur.Token)), nil, psWant{401, "", false})
		tokens["alice"], boots["alice"] = next.Token, next.Boot
		tok := xbindtest.H("Authorization", "Bearer "+tokens["alice"])
		if r := d.Call(t, "GET", kv, nil, tok); r.Status != 200 || string(r.Body) != "alice-secret" {
			t.Fatalf("alice's instance token before: %d %s", r.Status, r)
		}
		d.Must(t, "PATCH", "/api/xbin/users/alice", map[string]any{"disabled": true}, 200)
		if r := d.Call(t, "GET", kv, nil, tok); r.Status != 401 {
			t.Errorf("alice's instance token while she is disabled: %d %s (want 401)", r.Status, r)
		}
		if r := d.Call(t, "GET", "/api/"+psTile+"/kv/kv/secret", nil, e.fr(t, psTile, "alice")); r.Status == 200 {
			t.Errorf("alice's frame while she is disabled: %d %s", r.Status, r)
		}
		d.Must(t, "PATCH", "/api/xbin/users/alice", map[string]any{"disabled": false}, 200)
		r := d.Call(t, "GET", kv, nil, tok)
		t.Logf("alice's old instance token once she is enabled again: %d %s", r.Status, cut(r.String(), 120))
		if d.Call(t, "GET", "/api/xbin/whoami", nil, e.as("alice")...).Status != 200 {
			e.sess["alice"] = d.Login(t, "alice", psPassword("alice"))
		}
		e.forget("alice")
		w := e.who(t, "/api/"+psTile+"/who", e.fr(t, psTile, "alice"))
		t.Logf("alice's instance after she is enabled again: boot %s (before %s)", w.Boot, boots["alice"])
		tokens["alice"] = w.Token
		if v, st, body := e.value(t, "/api/"+psTile+"/kv/kv/secret", e.fr(t, psTile, "alice")); v != "alice-secret" {
			t.Errorf("alice's data after she is enabled again: %d %s", st, body)
		}
		// losing read of the tile uncovers her partition too
		tok = xbindtest.H("Authorization", "Bearer "+tokens["alice"])
		if r := d.Call(t, "GET", kv, nil, tok); r.Status != 200 {
			t.Fatalf("alice's current instance token: %d %s", r.Status, r)
		}
		d.Must(t, "PATCH", "/api/xbin/users/alice", map[string]any{"tiles": map[string]string{psCaller: "read", psPlain: "read"}}, 200)
		if r := d.Call(t, "GET", kv, nil, tok); r.Status != 401 {
			t.Errorf("alice's instance token once she can't read %s: %d %s (want 401)", psTile, r.Status, r)
		}
		d.Must(t, "PATCH", "/api/xbin/users/alice", map[string]any{"tiles": map[string]string{"apps/*": "read"}}, 200)
		e.forget("alice")
		tokens["alice"] = e.who(t, "/api/"+psTile+"/who", e.fr(t, psTile, "alice")).Token
	})

	t.Run("person-recreated", func(t *testing.T) {
		// C2/S17, 03 §E: a person deleted and made again under the same id
		// is a new person — a new uid, so a new partition id and a fresh,
		// empty partition; the old instance's token never authenticates
		// again. The one way a different person could inherit data.
		fr := e.fr(t, psTile, "dave")
		old := e.who(t, "/api/"+psTile+"/who", fr)
		e.put(t, "/api/"+psTile+"/kv/kv/secret", "dave-secret", fr)
		e.put(t, "/api/"+psTile+"/fs/files/note", "dave-note", fr)
		oldUID := e.uid(t, "dave")
		kv := "/api/xbin/kv/res:" + psTile + "/kv/secret"
		oldTok := xbindtest.H("Authorization", "Bearer "+old.Token)
		psExpect(t, "old dave's instance token", d.Call(t, "GET", kv, nil, oldTok), nil, psOK("dave-secret"))
		d.Must(t, "DELETE", "/api/xbin/users/dave", nil, 200)
		psExpect(t, "old dave's instance token once he is deleted", d.Call(t, "GET", kv, nil, oldTok), nil, psWant{401, "", false})
		// made again: his old partition's records predate the new record,
		// so nothing adopts their uid (adoptablePartitionUID, PD-43)
		e.addPerson(t, "dave", "user")
		e.forget("dave")
		w := e.who(t, "/api/"+psTile+"/who", e.fr(t, psTile, "dave"))
		newUID := e.uid(t, "dave")
		if newUID == "" || newUID == oldUID {
			t.Errorf("the new dave's uid %q, the old one's %q", newUID, oldUID)
		}
		if w.Env != "user:dave" || w.Caller.PartitionID == old.Caller.PartitionID || w.Caller.PartitionID != util.PartitionKey("dave", newUID) ||
			w.Token == old.Token || w.Boot == old.Boot {
			t.Errorf("the new dave reaches %+v, the old one's partition %s (boot %s)", w, old.Caller.PartitionID, old.Boot)
		}
		fr = e.fr(t, psTile, "dave")
		for _, p := range []string{"/api/" + psTile + "/kv/kv/secret", "/api/" + psTile + "/fs/files/note"} {
			psExpect(t, "the new dave, "+p, d.Call(t, "GET", p, nil, fr), []string{"dave-"}, psWant{404, `"not found"`, false})
		}
		for _, p := range []string{"/api/" + psTile + "/keys/kv", "/api/" + psTile + "/ls?res=files"} {
			if got := e.names(t, p, fr); len(got) != 0 {
				t.Errorf("BUG: the new dave's %s: %q, want nothing", p, got)
			}
		}
		psExpect(t, "old dave's instance token once dave is made again", d.Call(t, "GET", kv, nil, oldTok), nil, psWant{401, "", false})
	})

	t.Run("reset", func(t *testing.T) {
		// 06 §6 (F7b): a person's reset deletes their partition's data —
		// only theirs — after the typed confirmation; it starts empty again
		kv := "/api/" + psTile + "/kv/kv/secret"
		e.put(t, kv, "erin-secret", e.fr(t, psTile, "erin"))
		body := map[string]string{"tile": psTile, "partition": "user:erin"}
		psExpect(t, "erin's reset without the confirmation", d.Call(t, "POST", "/api/xbin/partitions/reset", body, e.as("erin")...),
			nil, psWant{409, psTile + " user:erin", false})
		psExpect(t, "bob resets erin's", d.Call(t, "POST", "/api/xbin/partitions/reset",
			map[string]string{"tile": psTile, "partition": "user:erin", "confirm": psTile + " user:erin"}, e.as("bob")...), nil, psWant{403, "an admin's act", false})
		d.Must(t, "POST", "/api/xbin/partitions/reset", map[string]string{"tile": psTile, "partition": "user:erin", "confirm": psTile + " user:erin"}, 200, e.as("erin")...)
		psExpect(t, "erin after her reset", d.Call(t, "GET", kv, nil, e.fr(t, psTile, "erin")), []string{"erin-"}, psWant{404, `"not found"`, false})
		psExpect(t, "bob after erin's reset", d.Call(t, "GET", kv, nil, e.fr(t, psTile, "bob")), psForbid("bob", true), psOK("bob-secret"))
	})

	t.Run("mode-keep", func(t *testing.T) {
		// 01 §2.4: a manifest change on a tile that holds data is a request;
		// nothing runs while it's pending; keep runs the recorded mode again
		d.WriteTile(t, psTile, map[string]string{"xbin.json": psManifest(psTile, "")})
		row := e.waitState(t, psTile, "pending")
		if !row.User || !row.Global || row.Request == nil || row.Request.User || row.Request.Global || row.Request.Declined {
			t.Errorf("the pending row: %+v %+v", *row, row.Request)
		}
		for _, who := range [][]xbindtest.Header{{e.fr(t, psTile, "alice")}, nil} {
			r := d.Call(t, "GET", "/api/"+psTile+"/who", nil, who...)
			var body struct {
				Error     string
				Partition struct{ State string }
			}
			_ = json.Unmarshal(r.Body, &body)
			if r.Status != 409 || body.Partition.State != "pending" || !strings.Contains(body.Error, "partition mode switch is requested") {
				t.Errorf("a call while pending: %d %s", r.Status, r)
			}
		}
		page := d.Call(t, "GET", "/c/"+psTile+"/", nil, append(e.as("alice"), xbindtest.H("Sec-Fetch-Dest", "iframe"))...)
		if page.Status != 409 || !strings.Contains(string(page.Body), "partition mode switch is requested") ||
			!strings.Contains(string(page.Body), "All data in this tile will be deleted") || !strings.Contains(string(page.Body), "&lt;b&gt;notes&lt;/b&gt;") ||
			strings.Contains(string(page.Body), "<b>notes") || strings.Contains(string(page.Body), "<script") {
			t.Errorf("the switch page: %d %s", page.Status, page)
		}
		if f := d.Call(t, "GET", "/c/"+psTile+"/", nil, e.as("alice")...); f.Status != 200 || !strings.Contains(string(f.Body), "the probe's page") {
			t.Errorf("a token's read of the paused tile's page: %d %s", f.Status, cut(f.String(), 200))
		}
		if a := d.Call(t, "GET", "/api/xbin/alerts", nil, e.as("alice")...); !strings.Contains(string(a.Body), "partition-switch") {
			t.Errorf("alice's alerts while pending: %d %s", a.Status, a)
		}
		keep := map[string]any{"tile": psTile, "act": "keep", "from": psBoth, "to": nil}
		if r := e.mode(t, "bob", keep); r.Status != 403 {
			t.Errorf("bob (a reader) keeps: %d %s", r.Status, r)
		}
		if r := e.d.Call(t, "POST", "/api/xbin/partitions/mode", keep, e.fr(t, psTile, "carol")); r.Status != 403 {
			t.Errorf("carol's frame of the tile keeps: %d %s", r.Status, r)
		}
		if r := e.mode(t, "carol", map[string]any{"tile": psTile, "act": "keep", "from": psUser, "to": nil}); r.Status != 409 {
			t.Errorf("a keep of another request: %d %s", r.Status, r)
		}
		if r := e.mode(t, "carol", keep); r.Status != 200 {
			t.Fatalf("carol (admin) keeps: %d %s", r.Status, r)
		}
		row = e.waitState(t, psTile, "partitioned")
		if row.Request == nil || !row.Request.Declined {
			t.Errorf("the kept row: %+v %+v", *row, row.Request)
		}
		w := e.who(t, "/api/"+psTile+"/who", e.fr(t, psTile, "alice"))
		if w.Env != "user:alice" {
			t.Errorf("alice after keep reaches %+v", w)
		}
		tokens["alice"] = w.Token
		if v, st, body := e.value(t, "/api/"+psTile+"/fs/files/note", e.fr(t, psTile, "alice")); v != "alice-note" {
			t.Errorf("alice's file after keep: %d %s", st, body)
		}
		if v, st, body := e.value(t, "/api/"+psTile+"/kv/kv/secret"); v != "global-secret" {
			t.Errorf("global's kv after keep: %d %s", st, body)
		}
	})

	t.Run("mode-switch", func(t *testing.T) {
		// 01 §2.5-§2.6: a switch wipes every namespace — people's and
		// global's — revokes people's tokens and records the new mode
		sw := func(extra map[string]any) map[string]any {
			m := map[string]any{"tile": psTile, "act": "switch", "from": psBoth, "to": nil}
			for k, v := range extra {
				m[k] = v
			}
			return m
		}
		var preview struct{ Wiped map[string]int64 }
		r := e.mode(t, "carol", sw(map[string]any{"dryRun": true}))
		if r.Status != 200 {
			t.Fatalf("the dry run: %d %s", r.Status, r)
		}
		r.Decode(t, &preview)
		t.Logf("the dry run: %s", cut(r.String(), 600))
		if preview.Wiped["partitions"] < 3 || preview.Wiped["namespaces"] < 3 {
			t.Errorf("the dry run's counts: %v", preview.Wiped)
		}
		for _, p := range []string{"alice", "bob"} {
			if _, err := os.Stat(e.partDir(psTile, ids[p])); err != nil {
				t.Errorf("the dry run removed %s's namespace: %v", p, err)
			}
		}
		if r := e.mode(t, "carol", sw(map[string]any{"confirm": "apps/other"})); r.Status != 400 {
			t.Errorf("a switch with the wrong path typed: %d %s", r.Status, r)
		}
		if r := e.mode(t, "bob", sw(map[string]any{"confirm": psTile})); r.Status != 403 {
			t.Errorf("bob (a reader) switches: %d %s", r.Status, r)
		}
		old := xbindtest.H("Authorization", "Bearer "+tokens["alice"])
		volumes := func() []string { // the tile's volumes' gocryptfs: main's and people's
			return append(e.gocryptfs("data/resources-enc/apps~pt/"), e.gocryptfs("data/resources-enc/.partitions/apps~pt/")...)
		}
		before := volumes()
		t.Logf("the tile's volumes before the switch: %v", before)
		for _, v := range []string{"data/resources-enc/apps~pt/files", "data/resources-enc/apps~pt/pub", ids["alice"], ids["bob"]} {
			if !slices.ContainsFunc(before, func(s string) bool { return strings.Contains(s, v) }) {
				t.Fatalf("no gocryptfs serves %s before the switch (the check below would pass vacuously): %v", v, before)
			}
		}
		r = e.mode(t, "carol", sw(map[string]any{"confirm": psTile}))
		if r.Status != 200 {
			t.Fatalf("the switch: %d %s", r.Status, r)
		}
		var left []string
		for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(250 * time.Millisecond) {
			left = nil
			for _, v := range volumes() {
				if slices.Contains(before, v) {
					left = append(left, v)
				}
			}
			if len(left) == 0 || time.Now().After(deadline) {
				break
			}
		}
		if len(left) > 0 {
			t.Errorf("the wiped volumes' gocryptfs still run after the switch: %v", left)
		}
		t.Logf("the switch: %s", cut(r.String(), 600))
		var done struct{ Wiped map[string]int64 }
		r.Decode(t, &done)
		if done.Wiped["partitions"] < 3 {
			t.Errorf("the switch's counts: %v", done.Wiped)
		}
		e.waitState(t, psTile, "")
		m, ok := e.modeRecord(t, psTile)
		if !ok || m.Mode != nil && (m.Mode.User || m.Mode.Global) || m.Request != nil || len(m.History) == 0 || m.History[len(m.History)-1].Op != "switch" {
			t.Errorf("mode.json after the switch: %v %+v", ok, m)
		}
		if r := d.Call(t, "GET", "/api/xbin/kv/res:"+psTile+"/kv/secret", nil, old); r.Status != 401 {
			t.Errorf("alice's old instance token after the switch: %d %s (want 401)", r.Status, r)
		}
		for _, p := range []string{"alice", "bob"} {
			if _, err := os.Stat(e.partDir(psTile, ids[p])); !os.IsNotExist(err) {
				t.Errorf("%s's partition namespace after the switch: %v", p, err)
			}
		}
		if ents, _ := os.ReadDir(filepath.Join(d.WS, "data", "resources-enc", ".partitions", strings.ReplaceAll(psTile, "/", "~"))); len(ents) > 0 {
			t.Errorf("people's partition data of %s left on disk: %v", psTile, ents)
		}
		e.forget("alice")
		w := e.who(t, "/api/"+psTile+"/who", e.fr(t, psTile, "alice"))
		if w.Env != "" || w.Caller.Partition != "" || w.Caller.PartitionID != "" {
			t.Errorf("alice after the switch reaches %+v, want the one unpartitioned instance", w)
		}
		for _, p := range []string{"/kv/kv/secret", "/fs/files/note", "/kv/board/shared"} {
			for _, hdrs := range [][]xbindtest.Header{{e.fr(t, psTile, "alice")}, nil} {
				if v, st, body := e.value(t, "/api/"+psTile+p, hdrs...); st != 404 {
					t.Errorf("%s after the switch: %d %q %s", p, st, v, body)
				}
			}
		}
		if got := e.names(t, "/api/"+psTile+"/ls?res=files"); len(got) != 0 {
			t.Errorf("the files after the switch: %q", got)
		}
		// every deployment's namespace goes too: dev's seeded copy of global's
		psExpect(t, "dev's kv after the switch", d.Call(t, "GET", "/api/"+psTile+"+dev/kv/kv/secret", nil), []string{"global-"},
			psWant{404, `"not found"`, false})
	})

	t.Run("mode-pending-unpartitioned", func(t *testing.T) {
		// 01 §2.4 the other way: an unpartitioned tile that holds data asks
		// for partitions; keep leaves it one instance on its data
		before := e.who(t, "/api/"+psPlain+"/who", e.fr(t, psPlain, "alice"))
		d.WriteTile(t, psPlain, map[string]string{"xbin.json": psManifest(psPlain, `["user", "global"]`)})
		e.waitState(t, psPlain, "pending")
		if r := d.Call(t, "GET", "/api/"+psPlain+"/who", nil, e.fr(t, psPlain, "alice")); r.Status != 409 {
			t.Errorf("alice's call while pending: %d %s", r.Status, r)
		}
		if r := e.mode(t, "carol", map[string]any{"tile": psPlain, "act": "keep", "from": nil, "to": psBoth}); r.Status != 200 {
			t.Fatalf("carol keeps: %d %s", r.Status, r)
		}
		w := e.who(t, "/api/"+psPlain+"/who", e.fr(t, psPlain, "alice"))
		if w.Env != "" || w.Caller.Partition != "" {
			t.Errorf("alice after keep reaches %+v, want the unpartitioned instance (before: boot %s)", w, before.Boot)
		}
		if v, st, body := e.value(t, "/api/"+psPlain+"/kv/kv/secret", e.fr(t, psPlain, "alice")); v != "plain-data" {
			t.Errorf("the kept tile's data: %d %s", st, body)
		}
	})
}

// TestPartitionsSmokeReap: a person's partition stops after 10 idle minutes
// (PD-18, 03 §A.6) — its instance token revoked — and starts again, on its
// data, at their next use. The idle time has no knob, so this waits for it:
// opt in with XBIN_SMOKE_REAP=1.
func TestPartitionsSmokeReap(t *testing.T) {
	if os.Getenv("XBIN_SMOKE_REAP") != "1" {
		t.Skip("the idle stop takes over ten minutes: XBIN_SMOKE_REAP=1 runs it")
	}
	t.Parallel()
	e := psSetup(t)
	d := e.d
	psWrite(t, d, psTile, `["user"]`)
	e.waitState(t, psTile, "partitioned")
	w := e.who(t, "/api/"+psTile+"/who", e.fr(t, psTile, "alice"))
	if w.Env != "user:alice" {
		t.Fatalf("alice reaches %+v", w)
	}
	e.put(t, "/api/"+psTile+"/kv/kv/secret", "alice-secret", e.fr(t, psTile, "alice"))
	e.put(t, "/api/"+psTile+"/fs/files/note", "alice-note", e.fr(t, psTile, "alice"))
	tok := xbindtest.H("Authorization", "Bearer "+w.Token)
	kv := "/api/xbin/kv/res:" + psTile + "/kv/secret"
	if r := d.Call(t, "GET", kv, nil, tok); r.Status != 200 {
		t.Fatalf("alice's instance token: %d %s", r.Status, r)
	}
	start := time.Now()
	for {
		r := d.Call(t, "GET", kv, nil, tok) // the kv API: no use of the backend
		if r.Status == 401 {
			break
		}
		if time.Since(start) > 14*time.Minute {
			t.Fatalf("alice's partition wasn't reaped within %s: her token answers %d %s", time.Since(start), r.Status, r)
		}
		time.Sleep(15 * time.Second)
	}
	took := time.Since(start)
	t.Logf("alice's partition was reaped (its token revoked) after %s idle", took.Round(time.Second))
	if took < 9*time.Minute {
		t.Errorf("reaped after %s, before the 10 idle minutes", took)
	}
	again := e.who(t, "/api/"+psTile+"/who", e.fr(t, psTile, "alice"))
	if again.Env != "user:alice" || again.Boot == w.Boot {
		t.Errorf("alice's next use: %+v (boot before %s)", again, w.Boot)
	}
	if v, st, body := e.value(t, "/api/"+psTile+"/fs/files/note", e.fr(t, psTile, "alice")); v != "alice-note" {
		t.Errorf("alice's file after the reap: %d %s", st, body)
	}
}
