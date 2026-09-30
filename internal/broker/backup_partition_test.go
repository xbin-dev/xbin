package broker

// People's partitions in backups (plans/partitions/11-backup-encryption.md
// §2-§4 §Tests; PD-25, PD-26, PD-56; S15): each person's partition gets a
// sealed archive of its own under its own backup key; the tile's main and
// data archives hold none of it; a partition archive restores only into
// its person's partition by the rules of 11 §4; erasing its key makes it
// unrestorable, and every erase lands in the tile's history. Built on
// partRouteWS (the real identity plane: people's uids minted by the users
// store) with a vault barrier and memArchiver.

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/backup"
	"github.com/xbin-dev/xbin/internal/users"
	"github.com/xbin-dev/xbin/internal/util"
)

const (
	pbTile   = "apps/pk"
	pbMarker = "PARTITION-MARKER-4b1d"
)

// partBk is partRouteWS with apps/pk — people's partitions and global, a
// per-person kv (docs), a shared kv (board), a cron and a bus — an archiver
// bound for every tile, and a vault barrier: every archive is sealed.
type partBk struct {
	*partWS
	arch *memArchiver
}

func newPartBk(t *testing.T) *partBk {
	t.Helper()
	w := partRouteWS(t)
	ws, err := os.ReadFile(filepath.Join(w.root, "xbin.json"))
	if err != nil {
		t.Fatal(err)
	}
	uses := `"uses":[{"target":"res:apps/pk/docs","role":"writer"},{"target":"res:apps/pk/board","role":"writer"},` +
		`{"target":"res:apps/pk/beat","role":"writer"},{"target":"res:apps/pk/feed","role":"writer"}]`
	w.write(map[string]string{
		"xbin.json":          strings.Replace(string(ws), `{"schema":1,`, `{"schema":1,"bindings":{"*":{"@archive":{"ref":"apps/archiver"}}},`, 1),
		"apps/pk/scope.json": `{"resources":{"docs":{"type":"kv"},"board":{"type":"kv","shared":true},"beat":{"type":"cron"},"feed":{"type":"bus"}}}`,
		"apps/pk/xbin.json":  `{"runtime":"go","partition":["user","global"],` + uses + `}`,
	})
	w.rescan()
	if st, _, _ := w.state(pbTile); st.String() != "partitioned" {
		t.Fatalf("%s: %s, want partitioned", pbTile, st)
	}
	b := w.b
	b.Version = "test"
	if err := b.barrier.Init("backup-pass"); err != nil {
		t.Fatal(err)
	}
	f := &partBk{partWS: w, arch: &memArchiver{keys: map[string][]memVersion{}, unseal: unsealWith(b)}}
	b.ProxyHandler = f.arch
	return f
}

// kv writes key=value into tile's docs (or board) as p.
func (f *partBk) kv(p auth.Principal, res, key, value string) {
	f.t.Helper()
	if code, body := nsKV(f.t, f.b, "PUT", p, "res:"+pbTile+"/"+res+"/"+key, value); code != 200 {
		f.t.Fatalf("kv PUT %s as %s/%s: %d %s", key, p.Component, p.Partition, code, body)
	}
}

// get reads docs' key as p ("" when absent).
func (f *partBk) get(p auth.Principal, key string) string {
	f.t.Helper()
	code, body := nsKV(f.t, f.b, "GET", p, "res:"+pbTile+"/docs/"+key, "")
	if code != 200 {
		return ""
	}
	return body
}

func (f *partBk) vault(p auth.Principal, method, key, body string) (int, string) {
	f.t.Helper()
	rec := call(f.t, map[string]http.HandlerFunc{"GET": f.b.apiVaultGet, "PUT": f.b.apiVaultPut, "DELETE": f.b.apiVaultDelete}[method],
		p, method, "/vault/"+pbTile+"/"+key, body, map[string]string{"rest": pbTile + "/" + key})
	return rec.Code, rec.Body.String()
}

func (f *partBk) cron(p auth.Principal, name string) {
	f.t.Helper()
	rec := call(f.t, f.b.apiCronPut, p, "PUT", "/cron/jobs", `{"name":"`+name+`","resource":"res:apps/pk/beat","schedule":"@every 5m","path":"/tick"}`, nil)
	if rec.Code != 200 {
		f.t.Fatalf("cron PUT %s: %d %s", name, rec.Code, rec.Body.String())
	}
}

// fill gives the global instance, alice and bob data in every store a
// partition has: a kv key each (and a shared one), a vault key, a cron job.
func (f *partBk) fill() {
	f.t.Helper()
	f.kv(instanceOf(pbTile, ""), "docs", "k", "global-"+pbMarker)
	for _, u := range []string{"alice", "bob"} {
		f.kv(partInst(pbTile, u), "docs", "k", u+"-"+pbMarker)
		if code, body := f.vault(partTerm(pbTile, u), "PUT", "token", `{"value":"`+u+`-secret-`+pbMarker+`"}`); code != 200 {
			f.t.Fatalf("%s's vault: %d %s", u, code, body)
		}
		f.cron(partInst(pbTile, u), u+"-job")
	}
	f.kv(partInst(pbTile, "alice"), "board", "shared", "on-the-board")
	f.cron(instanceOf(pbTile, ""), "global-job")
}

// backup backs the tile up as an admin would (POST /backup), answering its
// JSON.
func (f *partBk) backup() map[string]any {
	f.t.Helper()
	rec := call(f.t, f.b.apiBackupNow, auth.Principal{Owner: true}, "POST", "/backup", `{"component":"`+pbTile+`"}`, nil)
	if rec.Code != 200 {
		f.t.Fatalf("backup: %d %s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return out
}

// restore is POST /partitions/restore as p.
func (f *partBk) restore(p auth.Principal, body string) (int, map[string]any) {
	f.t.Helper()
	rec := call(f.t, f.b.apiPartitionRestore, p, "POST", "/partitions/restore", body, nil)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

// plain is the archive's tar, unsealed.
func (f *partBk) plain(key string) []byte {
	f.t.Helper()
	vs := f.arch.keys[key]
	if len(vs) == 0 {
		f.t.Fatalf("no archive under %s", key)
	}
	return unsealWith(f.b)(f.t, vs[0].body)
}

func (f *partBk) historyOps(tile string) []modeHistory {
	rec := f.record(tile)
	if rec == nil {
		return nil
	}
	return rec.History
}

// covers PD-25 PD-56 S15 11§2 11§4 — a sealed round trip of one person's
// partition: a backup writes alice's and bob's partitions each under
// .partitions.<TileKey>.main.<pkey>, sealed under its own part: key (the
// PUT names it), holding her data, her vault file (its values still sealed
// by the vault: no secret in the clear), her cron job and her record — and
// the tile's main and data archives hold nothing of either person: no
// data, no row, no partition id (S15; the manifest lists global's cron job
// only). Alice restores her own partition after the typed confirmation:
// her data, vault and job come back; bob's are untouched. Erasing her
// part: key makes her archive unrestorable — the refusal says when and why
// — while bob's still restores, and the erase is in the tile's history.
func TestPartitionArchiveRoundTrip(t *testing.T) {
	f := newPartBk(t)
	b := f.b
	f.fill()
	out := f.backup()
	parts, _ := out["partitions"].(map[string]any)
	if parts == nil || parts["archived"] != float64(2) || parts["failed"] != nil {
		t.Fatalf("backup answer: %v", out)
	}
	alicePK, bobPK := f.pkeyOf("alice"), f.pkeyOf("bob")
	aliceKey, bobKey := partitionArchiveKey(pbTile, "main", alicePK), partitionArchiveKey(pbTile, "main", bobPK)
	for who, key := range map[string]string{"alice": aliceKey, "bob": bobKey} {
		vs := f.arch.keys[key]
		if len(vs) != 1 || !backup.IsSealed(vs[0].body) || bytes.Contains(vs[0].body, []byte(pbMarker)) {
			t.Fatalf("%s's archive: %d versions, or not sealed", who, len(vs))
		}
		h, _, err := backup.ReadSealHeader(bytes.NewReader(vs[0].body))
		k, kerr := b.backupKeys().read(h.Subkey)
		if err != nil || kerr != nil || h.Kind != backup.KindPartition || f.arch.subkeys[vs[0].v] != h.Subkey ||
			k.Subject != partitionBackupSubject(pbTile, "main", f.pkeyOf(who)) || k.Tile != pbTile {
			t.Errorf("%s's archive header %+v, key %+v %v %v", who, h, k, err, kerr)
		}
	}
	// alice's archive: hers, and nobody else's
	m, ms := f.arch.latest(t, aliceKey)
	names := map[string]string{}
	for _, x := range ms {
		names[x.name] = string(x.body)
	}
	if m.Partition == nil || m.Partition.User != "alice" || m.Partition.ID != alicePK || m.Kind != backup.KindPartition ||
		m.Schema != backup.SchemaSplit || m.Resources["docs"] != "kv" || m.Resources["board"] != "" {
		t.Errorf("alice's manifest: %+v %+v", m, m.Partition)
	}
	kvj := names[backup.KVName]
	if !strings.Contains(kvj, b64("alice-"+pbMarker)) || strings.Contains(kvj, b64("bob-"+pbMarker)) ||
		strings.Contains(kvj, b64("global-"+pbMarker)) || strings.Contains(kvj, b64("on-the-board")) {
		t.Errorf("alice's data: %s", kvj)
	}
	if v := names[backup.PartVaultName]; v == "" || strings.Contains(v, pbMarker) {
		t.Errorf("alice's vault file: missing, or a value in the clear: %q", v)
	}
	if !strings.Contains(names[backup.PartRegsPrefix+depCronFile], "alice-job") || strings.Contains(names[backup.PartRegsPrefix+depCronFile], "bob-job") {
		t.Errorf("alice's cron file: %s", names[backup.PartRegsPrefix+depCronFile])
	}
	if !strings.Contains(names[backup.PartRecordName], `"alice"`) || names[backup.PartNSName] == "" {
		t.Errorf("alice's records: %q %q", names[backup.PartRecordName], names[backup.PartNSName])
	}
	// S15: the tile's own archives hold nothing of anyone's partition
	for _, key := range []string{backupKey(pbTile), dataArchiveKey(pbTile)} {
		body := f.plain(key)
		for _, bad := range []string{"alice-" + pbMarker, "bob-" + pbMarker, b64("alice-" + pbMarker), b64("bob-" + pbMarker),
			"alice-job", "bob-job", alicePK, bobPK, "partition.json", "secret-" + pbMarker} {
			if bytes.Contains(body, []byte(bad)) {
				t.Errorf("%s holds %q", key, bad)
			}
		}
	}
	mm, _ := f.arch.latest(t, backupKey(pbTile))
	if len(mm.CronJobs) != 1 || !strings.Contains(string(mm.CronJobs[0]), "global-job") || mm.Partition != nil {
		t.Errorf("the main manifest's cron rows: %s", mm.CronJobs)
	}
	if !bytes.Contains(f.plain(dataArchiveKey(pbTile)), []byte(b64("global-"+pbMarker))) {
		t.Error("the data archive lacks global's data")
	}

	// alice changes everything, then restores her own partition
	alice := personP(t, f.partWS, "alice")
	f.kv(partInst(pbTile, "alice"), "docs", "k", "changed")
	f.kv(partInst(pbTile, "alice"), "docs", "new", "after-the-backup")
	if code, _ := f.vault(partTerm(pbTile, "alice"), "DELETE", "token", ""); code != 200 {
		t.Fatalf("vault delete: %d", code)
	}
	if rec := call(t, b.apiCronDelete, partInst(pbTile, "alice"), "DELETE", "/cron/jobs/alice-job", "", map[string]string{"name": "alice-job"}); rec.Code != 200 {
		t.Fatalf("cron delete: %d %s", rec.Code, rec.Body.String())
	}
	f.kv(partInst(pbTile, "bob"), "docs", "k", "bob-changed")
	req := `{"tile":"apps/pk"}`
	if code, out := f.restore(alice, req); code != 400 || out["confirm"] != "apps/pk user:alice" {
		t.Fatalf("a restore without the typed confirmation: %d %v", code, out)
	}
	if code, out := f.restore(alice, `{"tile":"apps/pk","dryRun":true}`); code != 200 || out["dryRun"] != true || f.get(partInst(pbTile, "alice"), "k") != "changed" {
		t.Fatalf("dry run: %d %v", code, out)
	}
	code, out := f.restore(alice, `{"tile":"apps/pk","confirm":"apps/pk user:alice"}`)
	if code != 200 || out["data"] != true || out["vault"] != true || out["earlierHolder"] != false {
		t.Fatalf("alice's restore: %d %v", code, out)
	}
	if got := f.get(partInst(pbTile, "alice"), "k"); got != "alice-"+pbMarker {
		t.Errorf("alice's key after the restore: %q", got)
	}
	if got := f.get(partInst(pbTile, "alice"), "new"); got != "" {
		t.Errorf("a key written after the backup survived the restore: %q", got)
	}
	if code, body := f.vault(partInst(pbTile, "alice"), "GET", "token", ""); code != 200 || !strings.Contains(body, "alice-secret") {
		t.Errorf("alice's vault after the restore: %d %s", code, body)
	}
	if rows := b.cron.partRows(partTarget{tile: pbTile, dep: "main", pkey: alicePK}); len(rows) != 1 || rows[0].Name != "alice-job" {
		t.Errorf("alice's jobs after the restore: %+v", rows)
	}
	if got := f.get(partInst(pbTile, "bob"), "k"); got != "bob-changed" {
		t.Errorf("bob's data moved with alice's restore: %q", got)
	}
	if h := f.historyOps(pbTile); len(h) == 0 || h[len(h)-1].Op != modeOpPartitionRestore || h[len(h)-1].Partition != alicePK || h[len(h)-1].By != "alice" {
		t.Errorf("the tile's history after the restore: %+v", h)
	}

	// bob's archive served under alice's key (an archiver's swap): refused
	f.arch.set(aliceKey, f.arch.keys[bobKey][0].body)
	if code, out := f.restore(alice, `{"tile":"apps/pk","confirm":"apps/pk user:alice"}`); code != 409 || !strings.Contains(fmtErr(out), "isn't sealed under this partition's backup key") {
		t.Errorf("bob's archive served as alice's: %d %v", code, out)
	}
	f.arch.keys[aliceKey] = f.arch.keys[aliceKey][1:]

	// erasing alice's part: key makes her archive unrestorable — an archiver
	// without erase collection still serves it: refused, with when and why
	f.arch.noEraseGC = true
	n, _, err := b.ErasePartitionBackups(pbTile, "main", alicePK, "partition reset", "alice")
	if err != nil || n != 1 {
		t.Fatalf("erase: %d %v", n, err)
	}
	if len(f.arch.keys[aliceKey]) != 1 {
		t.Fatalf("the archive without erase collection: %d versions", len(f.arch.keys[aliceKey]))
	}
	code, out = f.restore(alice, `{"tile":"apps/pk","confirm":"apps/pk user:alice"}`)
	if want := "was erased on " + time.Now().UTC().Format("2006-01-02") + " (partition reset)"; code != 409 || !strings.Contains(fmtErr(out), want) {
		t.Errorf("alice's erased archive: %d %v, want %q", code, out, want)
	}
	h := f.historyOps(pbTile)
	if last := h[len(h)-1]; last.Op != modeOpBackupErase || last.Partition != alicePK || last.Reason != "partition reset" || last.By != "alice" || last.Wiped["subkeys"] != 1 {
		t.Errorf("the erase in the tile's history: %+v", last)
	}
	bob := personP(t, f.partWS, "bob")
	if code, out := f.restore(bob, `{"tile":"apps/pk","confirm":"apps/pk user:bob"}`); code != 200 || f.get(partInst(pbTile, "bob"), "k") != "bob-"+pbMarker {
		t.Errorf("bob's restore: %d %v", code, out)
	}
	// the next backup seals alice's partition under a new key (gen 2)
	f.arch.noEraseGC = false
	f.backup()
	if code, out := f.restore(alice, `{"tile":"apps/pk","confirm":"apps/pk user:alice"}`); code != 200 {
		t.Errorf("alice's new archive: %d %v", code, out)
	}
}

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

// covers PD-43 11§4 — partition archive rules: the same uid restores after
// the typed confirmation, by the person or an admin; an archive of an
// earlier holder of the id (deleted and recreated since) only by an admin
// naming the id again (to) — the person is told — and never by the person;
// never into another id (the archive's person decides; a to naming another
// is refused); a person may not restore someone else's; no tile principal
// (a partition's own code, its terminal) and no view-as restores; an
// unsealed archive under a partition's key is refused; a tile that isn't
// partitioned now gives 409; and a partition archive is never a tile (POST
// /restore, a single file of it).
func TestPartitionArchiveRules(t *testing.T) {
	f := newPartBk(t)
	b := f.b
	var pushed []string
	b.SetPartitionPush(func(user, kind, title, body, link, collapse string) { pushed = append(pushed, user+" "+kind) })
	f.fill()
	f.backup()
	alicePK := f.pkeyOf("alice")
	aliceKey := partitionArchiveKey(pbTile, "main", alicePK)
	admin, alice, carol := personP(t, f.partWS, "bob"), personP(t, f.partWS, "alice"), personP(t, f.partWS, "carol")
	confirm := `,"confirm":"apps/pk user:alice"`

	// the admin, the same uid: 200, and alice is told
	if code, out := f.restore(admin, `{"tile":"apps/pk","user":"alice"`+confirm+`}`); code != 200 {
		t.Fatalf("an admin's restore of alice's partition: %d %v", code, out)
	}
	if strings.Join(pushed, ",") != "alice tile.partition-restored" {
		t.Errorf("pushes: %v", pushed)
	}
	// never into another id
	for _, c := range []struct {
		p    auth.Principal
		body string
		code int
		want string
	}{
		{admin, `{"tile":"apps/pk","user":"bob","partitionId":"` + alicePK + `","confirm":"apps/pk user:bob"}`, 403, "never into another id"},
		{admin, `{"tile":"apps/pk","user":"alice","to":"bob"` + confirm + `}`, 400, "never into another id"},
		{carol, `{"tile":"apps/pk","user":"alice"` + confirm + `}`, 403, "only their own"},
		{partInst(pbTile, "alice"), `{"tile":"apps/pk"` + confirm + `}`, 403, "a person's own act"},
		{partTerm(pbTile, "alice"), `{"tile":"apps/pk"` + confirm + `}`, 403, "a person's own act"},
		{viewAsAlice, `{"tile":"apps/pk"` + confirm + `}`, 403, "a person's own act"},
		{alice, `{"tile":"apps/x","confirm":"apps/x user:alice"}`, 409, "isn't partitioned now"},
	} {
		if code, out := f.restore(c.p, c.body); code != c.code || !strings.Contains(fmtErr(out), c.want) {
			t.Errorf("%s as %s/%s: %d %v, want %d %q", c.body, c.p.Component, c.p.UserID, code, out, c.code, c.want)
		}
	}
	// the partitioned tile's archive can't be restored as the tile, nor read a file at a time
	f.arch.set(backupKey(pbTile), f.arch.keys[aliceKey][0].body)
	if _, _, err := b.restoreTile(pbTile, "latest"); err == nil || !strings.Contains(err.Error(), "a person's partition") {
		t.Errorf("a partition archive restored as the tile: %v", err)
	}
	if _, code, err := b.extractMember(pbTile, "latest", backup.KVName); code != 409 || err == nil {
		t.Errorf("a file of a partition archive served as the tile's: %d %v", code, err)
	}
	// an unsealed archive under alice's key: refused
	var plain bytes.Buffer
	bw := backup.NewWriter(&plain)
	_ = bw.Manifest(backup.Manifest{Schema: backup.SchemaSplit, Kind: backup.KindPartition, Component: pbTile,
		Partition: &backup.PartitionRef{ID: alicePK, User: "alice", UID: f.uid("alice"), Deployment: "main"}})
	_ = bw.Close()
	f.arch.set(aliceKey, plain.Bytes())
	if code, out := f.restore(alice, `{"tile":"apps/pk"`+confirm+`}`); code != 409 || !strings.Contains(fmtErr(out), "isn't sealed") {
		t.Errorf("an unsealed partition archive: %d %v", code, out)
	}
	f.arch.keys[aliceKey] = f.arch.keys[aliceKey][1:]

	// alice's id deleted and recreated: her archive is an earlier holder's
	st := b.Users
	if _, err := st.Delete("alice"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1100 * time.Millisecond) // a new incarnation: another second
	if _, err := st.Upsert(users.User{ID: "alice", Role: users.RoleUser, Tiles: map[string]string{"apps/*": users.LevelRead}}, "password1"); err != nil {
		t.Fatal(err)
	}
	alice = personP(t, f.partWS, "alice")
	newPK, _, err := b.partitionKeyOf("alice")
	if err != nil || newPK == alicePK {
		t.Fatalf("the recreated alice's partition id: %s %v", newPK, err)
	}
	earlier := `{"tile":"apps/pk","user":"alice","partitionId":"` + alicePK + `"` + confirm
	if code, out := f.restore(alice, earlier+`}`); code != 403 || !strings.Contains(fmtErr(out), "earlier holder") {
		t.Errorf("the recreated alice restoring an earlier holder's archive: %d %v", code, out)
	}
	if code, out := f.restore(admin, earlier+`}`); code != 400 || !strings.Contains(fmtErr(out), `to: "alice"`) {
		t.Errorf("an admin without to: %d %v", code, out)
	}
	pushed = nil
	code, out := f.restore(admin, earlier+`,"to":"alice"}`)
	if code != 200 || out["earlierHolder"] != true || out["partitionId"] != newPK || out["from"] != alicePK {
		t.Fatalf("an admin's restore with to: %d %v", code, out)
	}
	if got := f.get(partInst(pbTile, "alice"), "k"); got != "alice-"+pbMarker {
		t.Errorf("the recreated alice's data after the restore: %q", got)
	}
	if rows := b.cron.partRows(partTarget{tile: pbTile, dep: "main", pkey: newPK}); len(rows) != 1 {
		t.Errorf("the restored job isn't the new partition's: %+v", rows)
	}
	if strings.Join(pushed, ",") != "alice tile.partition-restored" {
		t.Errorf("pushes: %v", pushed)
	}
	if h := f.historyOps(pbTile); !strings.Contains(h[len(h)-1].Reason, "earlier holder") {
		t.Errorf("history: %+v", h[len(h)-1])
	}
	// listing: alice's own, and an admin's view of an earlier holder's
	rec := call(t, b.apiPartitionBackups, admin, "GET", "/partitions/backups?tile=apps/pk&user=alice&partitionId="+alicePK, "", nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"versions":[{`) {
		t.Errorf("an admin's listing: %d %s", rec.Code, rec.Body.String())
	}
	rec = call(t, b.apiPartitionBackups, carol, "GET", "/partitions/backups?tile=apps/pk&user=alice", "", nil)
	if rec.Code != 403 {
		t.Errorf("carol lists alice's: %d", rec.Code)
	}
}

// fmtErr is an answer's error text.
func fmtErr(out map[string]any) string {
	s, _ := out["error"].(string)
	return s
}

// uid is the person's uid now.
func (f *partBk) uid(id string) string {
	u, _ := f.b.Users.Get(id)
	return u.UID
}

// covers PD-44 11§3 11§4 — a switch erases ns: and part:, never tile:: after
// a switch from people's partitions to unpartitioned, the tile's main
// archive restores its source and says its data was erased, each person's
// archive refuses with the switch's erasure, and the tile: key is kept;
// then a plaintext archive made before the switch restores only with the
// typed confirmation naming the switch, into the global instance's
// namespace.
func TestPartitionSwitchErasesArchives(t *testing.T) {
	f := newPartBk(t)
	b := f.b
	f.fill()
	f.backup()
	alicePK := f.pkeyOf("alice")
	keys, _ := b.backupKeys().list()
	subjects := map[string]bool{}
	for _, k := range keys {
		subjects[strings.SplitN(k.Subject, ":", 2)[0]] = true
	}
	if !subjects["tile"] || !subjects["ns"] || !subjects["part"] {
		t.Fatalf("keys before the switch: %+v", keys)
	}
	aliceArchive := f.arch.keys[partitionArchiveKey(pbTile, "main", alicePK)][0].body
	f.arch.noEraseGC = true // the archiver keeps the dead versions: they must not read
	f.write(map[string]string{"apps/pk/xbin.json": `{"runtime":"go","uses":[{"target":"res:apps/pk/docs","role":"writer"}]}`})
	f.rescan()
	if st, _, _ := f.state(pbTile); st.String() != "pending" {
		t.Fatalf("after dropping partition: %s", st)
	}
	rec := call(t, b.apiPartitionMode, auth.Principal{Owner: true}, "POST", "/partitions/mode",
		`{"tile":"apps/pk","act":"switch","from":{"user":true,"global":true},"to":null,"confirm":"apps/pk"}`, nil)
	if rec.Code != 200 {
		t.Fatalf("switch: %d %s", rec.Code, rec.Body.String())
	}
	tombs, _ := b.backupKeys().tombstones()
	erased := map[string]bool{}
	for _, tb := range tombs {
		erased[strings.SplitN(tb.Subject, ":", 2)[0]] = true
	}
	if !erased["ns"] || !erased["part"] || erased["tile"] {
		t.Errorf("the switch erased %v, want ns and part, never tile", erased)
	}
	keys, _ = b.backupKeys().list()
	if len(keys) != 1 || !strings.HasPrefix(keys[0].Subject, "tile:") {
		t.Errorf("keys after the switch: %+v", keys)
	}
	// the sealed main archive from before the switch: it would bring back
	// the rows the switch deleted, so it asks too; its data is erased
	sw, ok, _ := b.lastDeletingSwitch(pbTile)
	if !ok {
		t.Fatal("no switch in the history")
	}
	date := sw.At.UTC().Format("2006-01-02")
	if _, _, err := b.restoreTile(pbTile, "latest"); !errors.As(err, new(preSwitchError)) {
		t.Errorf("a sealed pre-switch archive without the confirmation: %v", err)
	}
	r, _, err := b.restoreTileConfirmed(pbTile, "latest", date)
	if err != nil || r.DataErased == "" || !strings.Contains(r.DataErased, "partition mode switch") {
		t.Errorf("the main archive after the switch: %+v %v", r, err)
	}
	if _, err := b.openArchive(aliceArchive); err == nil || !strings.Contains(err.Error(), "was erased on") {
		t.Errorf("alice's archive after the switch: %v", err)
	}
	if code, out := f.restore(personP(t, f.partWS, "alice"), `{"tile":"apps/pk","confirm":"apps/pk user:alice"}`); code != 409 || !strings.Contains(fmtErr(out), "isn't partitioned now") {
		t.Errorf("a partition restore into the switched tile: %d %v", code, out)
	}

	// a plaintext archive from before the switch: the typed confirmation
	var plain bytes.Buffer
	bw := backup.NewWriter(&plain)
	kv, _ := json.Marshal(map[string]map[string]string{"docs": {"old": b64("from-before-the-switch")}})
	_ = bw.Manifest(backup.Manifest{Component: pbTile, Scope: pbTile, ScopeRoot: true, Resources: map[string]string{"docs": "kv"},
		Created: sw.At.Add(-time.Hour).UTC().Format(time.RFC3339), Includes: []string{"source", "data"}})
	_ = bw.File(backup.KVName, 0o644, kv)
	_ = bw.Close()
	f.arch.set(backupKey(pbTile), plain.Bytes())
	rec = call(t, b.apiRestore, auth.Principal{Owner: true}, "POST", "/restore", `{"component":"apps/pk"}`, nil)
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), "brings back data the switch deleted") || !strings.Contains(rec.Body.String(), `"confirm":"`+date+`"`) {
		t.Fatalf("a pre-switch archive without the confirmation: %d %s", rec.Code, rec.Body.String())
	}
	if got := f.get(instanceOf(pbTile, ""), "old"); got != "" {
		t.Fatalf("the refused restore wrote: %q", got)
	}
	rec = call(t, b.apiRestore, auth.Principal{Owner: true}, "POST", "/restore", `{"component":"apps/pk","confirm":"`+date+`"}`, nil)
	if rec.Code != 200 {
		t.Fatalf("a confirmed pre-switch restore: %d %s", rec.Code, rec.Body.String())
	}
	if got := f.get(instanceOf(pbTile, ""), "old"); got != "from-before-the-switch" {
		t.Errorf("the pre-switch data in global's namespace: %q", got)
	}
	// an archive made after the switch (a second later: archives name their
	// second) needs nothing
	time.Sleep(time.Until(sw.At.Truncate(time.Second).Add(1100 * time.Millisecond)))
	f.backup()
	if rec := call(t, b.apiRestore, auth.Principal{Owner: true}, "POST", "/restore", `{"component":"apps/pk"}`, nil); rec.Code != 200 {
		t.Errorf("a post-switch archive: %d %s", rec.Code, rec.Body.String())
	}
}

// covers PD-26 11§3 — a person deleted, then swept: the partition's
// records (a partition with a vault and a job but no namespace data) and
// its part: key go together, recorded in the tile's history; the other
// person's key stays.
func TestPartitionSweepErasesArchives(t *testing.T) {
	f := newPartBk(t)
	b := f.b
	if code, body := f.vault(partTerm(pbTile, "alice"), "PUT", "token", `{"value":"v"}`); code != 200 {
		t.Fatalf("vault: %d %s", code, body)
	}
	f.cron(partInst(pbTile, "bob"), "bob-job")
	f.backup()
	alicePK, bobPK := f.pkeyOf("alice"), f.pkeyOf("bob")
	for _, pk := range []string{alicePK, bobPK} {
		if len(f.arch.keys[partitionArchiveKey(pbTile, "main", pk)]) != 1 {
			t.Fatalf("no archive of %s", pk)
		}
	}
	b.PartitionUserDeleted("alice", f.uid("alice"))
	b.sweepPartitionNamespaces(time.Now().Add(partitionRetention + time.Hour))
	if exists(filepath.Join(f.root, "data", "partitions", util.TileKey(pbTile), "main", alicePK)) {
		t.Error("alice's partition wasn't swept")
	}
	keys, _ := b.backupKeys().list()
	for _, k := range keys {
		if k.Subject == partitionBackupSubject(pbTile, "main", alicePK) {
			t.Error("alice's part: key survived her sweep")
		}
	}
	if len(f.arch.keys[partitionArchiveKey(pbTile, "main", alicePK)]) != 0 || len(f.arch.keys[partitionArchiveKey(pbTile, "main", bobPK)]) != 1 {
		t.Error("the archiver's collection took the wrong archives")
	}
	h := f.historyOps(pbTile)
	if last := h[len(h)-1]; last.Op != modeOpBackupErase || last.Partition != alicePK || last.Reason != "partition swept: user-deleted" {
		t.Errorf("the sweep's erase in the history: %+v", last)
	}
}

// covers PD-25 11§1 — a plaintext-vault workspace archives no person's
// partition, and says so; a tile without people's partitions answers POST
// /backup as before.
func TestPartitionArchivesNeedSealing(t *testing.T) {
	w := partRouteWS(t)
	ws, _ := os.ReadFile(filepath.Join(w.root, "xbin.json"))
	w.write(map[string]string{
		"xbin.json":          strings.Replace(string(ws), `{"schema":1,`, `{"schema":1,"bindings":{"*":{"@archive":{"ref":"apps/archiver"}}},`, 1),
		"apps/pk/scope.json": `{"resources":{"docs":{"type":"kv"}}}`,
		"apps/pk/xbin.json":  `{"runtime":"go","partition":["user","global"],"uses":[{"target":"res:apps/pk/docs","role":"writer"}]}`,
	})
	w.rescan()
	arch := &memArchiver{keys: map[string][]memVersion{}}
	w.b.ProxyHandler = arch
	if code, body := nsKV(t, w.b, "PUT", partInst(pbTile, "alice"), "res:apps/pk/docs/k", "v"); code != 200 {
		t.Fatalf("kv: %d %s", code, body)
	}
	rec := call(t, w.b.apiBackupNow, auth.Principal{Owner: true}, "POST", "/backup", `{"component":"apps/pk"}`, nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "plaintext-vault") {
		t.Fatalf("backup: %d %s", rec.Code, rec.Body.String())
	}
	for key := range arch.keys {
		if strings.HasPrefix(key, ".partitions.") {
			t.Errorf("a partition archive in the clear: %s", key)
		}
	}
	rec = call(t, w.b.apiBackupNow, auth.Principal{Owner: true}, "POST", "/backup", `{"component":"apps/x"}`, nil)
	if rec.Code != 200 || strings.Contains(rec.Body.String(), "partitions") {
		t.Errorf("an unpartitioned tile's backup answer: %d %s", rec.Code, rec.Body.String())
	}
}
