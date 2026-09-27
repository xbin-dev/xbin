package broker

// The key function for per-deployment data (08-data §2, §3; NP-08-1,
// NP-08-11): main's keys are today's, every other namespace's are its own
// and no path today's code computes can produce them, the resource-name
// rule, the namespace kv files, the tile-keyed files a new deployment starts
// without, and the guard that keeps every physical key in deploydata.go.

import (
	"bytes"
	"errors"
	"fmt"
	"go/scanner"
	"go/token"
	"io/fs"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	bolt "go.etcd.io/bbolt"

	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/util"
)

// groundA and groundB are tile paths whose 32-bit CompKeys collide
// (apps~a-very-long-tile-na-9e17acc3), ground by hashing numbered paths.
const (
	groundA = "apps/a-very-long-tile-name/13730"
	groundB = "apps/a-very-long-tile-name/126235"
)

// keyScopes are adversarial scope paths: util.ScopeKey's collision
// (apps~x, apps/x), escS's escapes (%, ~, %7E, %25), the qualifier and dot
// forms of 12-compat PO-2, nesting, CompKey's 24-character truncation and a
// ground CompKey collision.
var keyScopes = []string{
	"apps/x", "apps~x", "apps%7Ex", "apps%x", "apps%25x", "apps%257Ex", "apps+x", "apps.x",
	"apps/x/y", "apps~x~y", "apps/x~y", "apps~x/y", "apps/x+dev", "x", "workspace",
	"apps/a-very-long-tile-name/inner", "apps/a-very-long-tile-name/other", groundA, groundB,
}

// covers P6 P29 T11 PO-2 — every storage key of a resource: main's are
// today's, byte for byte, for dep "" and "main" alike (the workspace scope,
// apps/cal and apps~cal, names containing "/"); every other namespace's are
// injective over adversarial scopes, names and deployment names, sit under
// the .deployments level no scope key reaches, never equal or nest in a main
// key, and give back their (scope, deployment) (NP-08-1). escS is
// injective and reversible, and the tile-keyed files of two paths whose
// CompKeys were ground to collide share nothing.
func TestDeploymentKeysDisjoint(t *testing.T) {
	b := deployBroker(t)
	root := b.Reg.Root
	ws := func(rel string) string { return filepath.Join(root, filepath.FromSlash(rel)) }

	t.Run("main is today", func(t *testing.T) {
		for _, c := range []struct {
			scope, name                     string
			dirKey, fsLabel, bucket, kvLabl string
		}{
			{"", "shared", "workspace", "workspace/shared", "res:workspace/shared", "kv:res:workspace/shared"},
			{"apps/cal", "db", "apps~cal", "apps~cal/db", "res:apps/cal/db", "kv:res:apps/cal/db"},
			{"apps~cal", "db", "apps~cal", "apps~cal/db", "res:apps~cal/db", "kv:res:apps~cal/db"},
			{"apps/cal", "a/b", "apps~cal", "apps~cal/a/b", "res:apps/cal/a/b", "kv:res:apps/cal/a/b"},
			{"apps/cal", ".x", "apps~cal", "apps~cal/.x", "res:apps/cal/.x", "kv:res:apps/cal/.x"},
		} {
			for _, dep := range []string{"", util.MainDeployment} {
				rt := resTarget{Scope: c.scope, Name: c.name}
				k, err := b.resKeys(rt, dep)
				if err != nil {
					t.Fatalf("%s in %q: %v", rt, dep, err)
				}
				want := resKeys{DirKey: c.dirKey, Name: c.name, FSLabel: c.fsLabel, Bucket: c.bucket, KVLabel: c.kvLabl}
				if k != want {
					t.Errorf("%s in %q:\n got %+v\nwant %+v", rt, dep, k, want)
				}
				// …and each is what today's code computed.
				if k.DirKey != util.ScopeKey(c.scope) || k.FSLabel != resLabel(util.ScopeKey(c.scope), c.name) ||
					k.Bucket != rt.String() || k.quotaKey() != util.ScopeKey(c.scope) {
					t.Errorf("%s in %q strays from ScopeKey/resLabel/String: %+v", rt, dep, k)
				}
				if db, err := b.kvDB(k, true); err != nil || db != b.kv.db {
					t.Errorf("%s in %q: kv file %v, %v; want data/kv.db", rt, dep, db, err)
				}
			}
			ns, err := scopeKeys(c.scope, "")
			wantNS := nsKeys{DirKey: c.dirKey, Quota: c.dirKey, Plain: "data/resources/" + c.dirKey, Enc: "data/resources-enc/" + c.dirKey}
			if err != nil || ns != wantNS {
				t.Errorf("scopeKeys(%q, main) = %+v, %v; want %+v", c.scope, ns, err, wantNS)
			}
		}
		if got, want := b.fsResPath("apps/calendar", "files", false), ws(".xbin/resenc/apps~calendar/files"); got != want {
			t.Errorf("mount path %q, want %q", got, want)
		}
		if got, want := b.fsResPath("apps/calendar", "db", true), ws(".xbin/resenc/apps~calendar/db/db.sqlite"); got != want {
			t.Errorf("sqlite path %q, want %q", got, want)
		}
		if got, want := b.resourcesRoot("apps/calendar"), ws("data/resources/apps~calendar"); got != want {
			t.Errorf("resources root %q, want %q", got, want)
		}
	})

	// Every key a resource has, as a flat list of what may never be shared.
	type keySet struct {
		ns, cipher, mount, fsLabel, kv, kvLabel string
	}
	keysOf := func(t *testing.T, rt resTarget, dep string) keySet {
		t.Helper()
		k, err := b.resKeys(rt, dep)
		if err != nil {
			t.Fatalf("%s in %q: %v", rt, dep, err)
		}
		kvFile := k.KVFile
		if kvFile == "" {
			kvFile = "data/kv.db"
		}
		return keySet{ns: k.NS, cipher: b.resenc.CipherDir(k.DirKey, k.Name), mount: b.resenc.MountDir(k.DirKey, k.Name),
			fsLabel: "fs:" + k.FSLabel, kv: kvFile + " " + k.Bucket, kvLabel: k.KVLabel}
	}
	names := []string{"db", "a", "a.b", ".hidden", "a/b", "b", "db-2", "x/y/z"}
	deps := []string{"dev", "staging", "d", "a-b", util.MainDeployment + "x", "abcdefghijklmnopqrstuvwx"}

	t.Run("beyond main", func(t *testing.T) {
		owner := map[string]string{} // key → the (scope, name, dep) that has it
		claim := func(kind, key, who string) {
			t.Helper()
			if prev, ok := owner[kind+"|"+key]; ok && prev != who {
				t.Errorf("%s %q is shared by %s and %s", kind, key, prev, who)
			}
			owner[kind+"|"+key] = who
		}
		var mainPaths []string
		for _, s := range keyScopes {
			for _, n := range names {
				m := keysOf(t, resTarget{Scope: s, Name: n}, "")
				mainPaths = append(mainPaths, m.cipher, m.mount)
			}
		}
		for _, s := range keyScopes {
			for _, d := range deps {
				nsWho := s + " in " + d
				for _, n := range names {
					rt := resTarget{Scope: s, Name: n}
					who := rt.String() + " in " + d
					k := keysOf(t, rt, d)
					claim("namespace", k.ns, nsWho)
					claim("cipher dir", k.cipher, who)
					claim("mount dir", k.mount, who)
					claim("fs label", k.fsLabel, who)
					claim("kv bucket", k.kv, who)
					claim("kv label", k.kvLabel, who)
					for _, p := range []struct{ path, under string }{
						{k.cipher, ws("data/resources-enc/.deployments") + "/"},
						{k.mount, ws(".xbin/resenc/.deployments") + "/"},
					} {
						if !strings.HasPrefix(p.path, p.under) {
							t.Errorf("%s: %s is outside %s", who, p.path, p.under)
						}
					}
					if !strings.HasPrefix(k.fsLabel, "fs:.deployments/") || !strings.HasPrefix(k.kvLabel, "kv:.deployments/") ||
						!strings.HasPrefix(k.kv, "data/resources-enc/.deployments/") {
						t.Errorf("%s: a label or kv file outside the .deployments level: %+v", who, k)
					}
					for _, mp := range mainPaths { // equal, or one inside the other
						for _, np := range []string{k.cipher, k.mount} {
							if np == mp || strings.HasPrefix(np, mp+"/") || strings.HasPrefix(mp, np+"/") {
								t.Errorf("%s: %s meets main's %s", who, np, mp)
							}
						}
					}
				}
				// The namespace gives back its scope and deployment.
				ns, _ := scopeKeys(s, d)
				rest, ok := strings.CutPrefix(ns.NS, ".deployments/")
				seg, dep, _ := strings.Cut(rest, "/")
				if back, okS := unescS(seg); !ok || !okS || back != s || dep != d {
					t.Errorf("namespace %q decodes to (%q, %q), want (%q, %q)", ns.NS, back, dep, s, d)
				}
				if ns.Quota != ns.NS || ns.Plain != "" || ns.Enc != "data/resources-enc/"+ns.NS || ns.DirKey != ns.NS+"/fs" {
					t.Errorf("namespace keys of %s: %+v", nsWho, ns)
				}
			}
		}
	})

	t.Run("escS", func(t *testing.T) {
		rng := rand.New(rand.NewPCG(39, 8))
		const alphabet = "a/~%7E25.+"
		seen := map[string]string{}
		for i := 0; i < 20000; i++ {
			buf := make([]byte, 1+rng.IntN(12))
			for j := range buf {
				buf[j] = alphabet[rng.IntN(len(alphabet))]
			}
			s := string(buf)
			e := escS(s)
			if prev, ok := seen[e]; ok && prev != s {
				t.Fatalf("escS(%q) = escS(%q) = %q", s, prev, e)
			}
			seen[e] = s
			if strings.Contains(e, "/") {
				t.Fatalf("escS(%q) = %q is not one segment", s, e)
			}
			if back, ok := unescS(e); !ok || back != s {
				t.Fatalf("unescS(escS(%q)) = %q, %v", s, back, ok)
			}
		}
		for in, want := range map[string]string{"apps/crm": "apps~crm", "apps~crm": "apps%7Ecrm", "a%b": "a%25b", "a%7Eb": "a%257Eb"} {
			if got := escS(in); got != want {
				t.Errorf("escS(%q) = %q, want %q", in, got, want)
			}
		}
		for _, seg := range []string{"", "a%7e", "a%", "a%41", "%2", "a/b"} {
			if back, ok := unescS(seg); ok {
				t.Errorf("unescS(%q) = %q accepted: escS never produces it", seg, back)
			}
		}
	})

	t.Run("tile keys", func(t *testing.T) {
		if util.CompKey(groundA) != util.CompKey(groundB) {
			t.Fatalf("the ground pair no longer collides: %s %s", util.CompKey(groundA), util.CompKey(groundB))
		}
		seen := map[string]string{}
		for _, tile := range []string{groundA, groundB, "apps/cal", "apps~cal", "apps/cal+dev", "apps/a-very-long-tile-name/inner"} {
			for _, d := range []string{"dev", "staging"} {
				f, err := deploymentFiles(tile, d)
				if err != nil {
					t.Fatal(err)
				}
				tk := util.TileKey(tile)
				for _, p := range []string{f.Vault, f.Prefs, f.Records, f.Derived} {
					if !strings.Contains(p, tk) || strings.Contains(p, util.CompKey(tile)) {
						t.Errorf("%s in %s: %q isn't keyed by the TileKey alone", tile, d, p)
					}
					if prev, ok := seen[p]; ok {
						t.Errorf("%q is shared by %s and %s in %s", p, prev, tile, d)
					}
					seen[p] = tile
				}
				want := depFiles{Vault: "data/vault/.deployments/" + tk + "/" + d + ".json",
					Prefs: ".deployments/" + tk + "/" + d + ".json", Records: "data/deployments/" + tk + "/" + d,
					Derived: ".xbin/deploy/" + tk + "/d/" + d}
				if f != want {
					t.Errorf("%s in %s:\n got %+v\nwant %+v", tile, d, f, want)
				}
			}
		}
		if checkpointStoreDir(root, groundA) == checkpointStoreDir(root, groundB) {
			t.Error("two ground-colliding paths share a checkpoint store")
		}
		for _, d := range []string{util.MainDeployment, "", "Dev", "a+b", "a/b", strings.Repeat("a", 25)} {
			if f, err := deploymentFiles("apps/cal", d); err == nil {
				t.Errorf("deploymentFiles(apps/cal, %q) = %+v: main keeps today's keys, and a name must be a deployment name", d, f)
			}
		}
	})
}

// covers P6 T2 NP-08-11 — the resource-name rule of the key function: main
// refuses only a ".." segment or a NUL, so every name that works today keeps
// working; every other namespace also refuses an empty name, a leading "/"
// and a "." or empty segment. Beyond main the workspace scope is never split,
// the deployment name is re-checked, the scope must be a workspace path and
// its encoding at most 200 bytes. Every name the registry accepts (D118's
// charset) passes in every namespace; a refused one never becomes a path.
// Resource data an archive files under the workspace scope is refused.
func TestResKeysNameRule(t *testing.T) {
	b := deployBroker(t)
	for _, c := range []struct {
		name      string
		main, dev bool
	}{
		{"db", true, true},
		{"a.b", true, true},
		{".hidden", true, true},
		{"a/b", true, true},
		{"..", false, false},
		{"../x", false, false},
		{"x/../y", false, false},
		{"x/..", false, false},
		{"a\x00b", false, false},
		{"", true, false},
		{"/x", true, false},
		{".", true, false},
		{"./x", true, false},
		{"x/.", true, false},
		{"a//b", true, false},
		{"x/", true, false},
	} {
		for dep, ok := range map[string]bool{util.MainDeployment: c.main, "dev": c.dev} {
			_, err := b.resKeys(resTarget{Scope: "apps/calendar", Name: c.name}, dep)
			switch {
			case ok && err != nil:
				t.Errorf("%q in %s refused: %v", c.name, dep, err)
			case !ok && err == nil:
				t.Errorf("%q in %s accepted", c.name, dep)
			case !ok && !strings.Contains(err.Error(), fmt.Sprintf("resource name %q in apps/calendar/scope.json is not a plain relative path", c.name)):
				t.Errorf("%q in %s: %v", c.name, dep, err)
			}
		}
	}
	if _, err := b.resKeys(resTarget{Name: "../x"}, ""); err == nil || !strings.Contains(err.Error(), "in the workspace's xbin.json") {
		t.Errorf("a workspace-level name: %v", err)
	}
	if p := b.fsResPath("apps/calendar", "../x", false); p != "" {
		t.Errorf("a refused name became a path: %q", p)
	}

	for _, c := range []struct{ scope, dep, want string }{
		{"", "dev", "the workspace's resources have one namespace"},
		{"apps/calendar", "Dev", `"Dev" is not a deployment name`},
		{"apps/calendar", "a+b", `"a+b" is not a deployment name`},
		{"apps/calendar", strings.Repeat("d", 25), "is not a deployment name"},
		{".hidden/x", "dev", "is not a workspace path"},
		{"apps/../x", "dev", "is not a workspace path"},
		{"data/x", "dev", "is not a workspace path"},
		{"apps/" + strings.Repeat("~", 66), "dev", "is too long for deployments beyond main"},
		{"apps/" + strings.Repeat("a", 196), "dev", "is too long for deployments beyond main"},
	} {
		if _, err := b.resKeys(resTarget{Scope: c.scope, Name: "db"}, c.dep); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("scope %q in %q: %v, want %q", c.scope, c.dep, err, c.want)
		}
	}
	if _, err := b.resKeys(resTarget{Scope: "apps/" + strings.Repeat("a", 195), Name: "db"}, "dev"); err != nil {
		t.Errorf("an encoded scope of exactly 200 bytes: %v", err)
	}
	if _, err := b.resKeys(resTarget{Scope: "", Name: "shared"}, ""); err != nil {
		t.Errorf("main's workspace scope: %v", err)
	}

	// Every name the registry accepts passes in every namespace.
	rng := rand.New(rand.NewPCG(39, 11))
	const first, rest = "abcXYZ019", "abcXYZ019._-"
	for i := 0; i < 5000; i++ {
		n := []byte{first[rng.IntN(len(first))]}
		for j := rng.IntN(20); j > 0; j-- {
			n = append(n, rest[rng.IntN(len(rest))])
		}
		if !registry.ValidResourceName(string(n)) {
			t.Fatalf("the generator made %q, which the registry refuses", n)
		}
		for _, dep := range []string{"", "dev"} {
			if _, err := b.resKeys(resTarget{Scope: "apps/calendar", Name: string(n)}, dep); err != nil {
				t.Fatalf("%q, which the registry accepts, refused in %q: %v", n, dep, err)
			}
		}
	}

	// An archive that files resource data under the workspace scope.
	if err := b.loadKV("", []byte(`{"shared":{"k":"dg=="}}`)); err == nil || !strings.Contains(err.Error(), "workspace scope") {
		t.Errorf("kv under the workspace scope: %v", err)
	}
	_ = b.kv.db.View(func(tx *bolt.Tx) error {
		if tx.Bucket([]byte("res:workspace/shared")) != nil || tx.Bucket([]byte("res:/shared")) != nil {
			t.Error("a refused archive wrote a bucket")
		}
		return nil
	})
	if _, err := b.restoreFileDest("", "wsfiles/x"); err == nil || !strings.Contains(err.Error(), "workspace scope") {
		t.Errorf("files under the workspace scope: %v", err)
	}
}

// covers P6 P14 PO-7 — one bbolt file per namespace beyond main: a read of a
// namespace nothing wrote to answers empty and creates nothing (08-data
// §3.7), the first write creates data/resources-enc/.deployments/<escS>/<d>/
// kv.db and never touches data/kv.db, a main value copied byte for byte
// fails closed under the namespace's label, and a namespace file can be
// closed and removed. The zero state gains no .deployments level.
func TestNamespaceKVFiles(t *testing.T) {
	b := deployBroker(t)
	root := b.Reg.Root
	nsDir := filepath.Join(root, "data", "resources-enc", ".deployments")
	absent := func(what string) {
		t.Helper()
		if _, err := os.Lstat(nsDir); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("%s created the .deployments level: %v", what, err)
		}
	}
	rt := resTarget{Scope: fxCalendar, Name: "events"}
	mainK, err := b.resKeys(rt, "")
	if err != nil {
		t.Fatal(err)
	}
	devK, err := b.resKeys(rt, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if want := "data/resources-enc/.deployments/apps~calendar/dev/kv.db"; devK.KVFile != want {
		t.Fatalf("dev's kv file %q, want %q", devK.KVFile, want)
	}
	if devK.Bucket != mainK.Bucket || devK.KVLabel == mainK.KVLabel {
		t.Fatalf("buckets keep the resource id, labels differ: main %+v, dev %+v", mainK, devK)
	}

	// Zero state: main's kv reads and writes create nothing beyond main.
	if err := b.barrier.Init("ns-kv-pass"); err != nil {
		t.Fatal(err)
	}
	stored, err := b.encodeKV(mainK.KVLabel, []byte("main's"))
	if err != nil {
		t.Fatal(err)
	}
	mainDB, _ := b.kvDB(mainK, true)
	if err := mainDB.Update(func(tx *bolt.Tx) error {
		bk, err := tx.CreateBucketIfNotExists([]byte(mainK.Bucket))
		if err != nil {
			return err
		}
		return bk.Put([]byte("k"), stored)
	}); err != nil {
		t.Fatal(err)
	}
	absent("main's kv")

	// A read beyond main: empty, and nothing made.
	if db, err := b.kvDB(devK, false); err != nil || db != nil {
		t.Fatalf("reading dev's namespace before any write: %v, %v", db, err)
	}
	if got := b.dumpKV(devK); len(got) != 0 {
		t.Fatalf("dev's namespace reads %v", got)
	}
	if db, _ := b.scopeKV(fxCalendar, "dev", false); db != nil {
		t.Fatal("scopeKV opened a namespace nothing wrote to")
	}
	absent("reading dev's namespace")

	// The first write makes the namespace's own file.
	devDB, err := b.kvDB(devK, true)
	if err != nil || devDB == nil || devDB == b.kv.db {
		t.Fatalf("dev's kv file: %v, %v", devDB, err)
	}
	if got, want := devDB.Path(), filepath.Join(root, filepath.FromSlash(devK.KVFile)); got != want {
		t.Fatalf("dev's kv file at %q, want %q", got, want)
	}
	if again, _ := b.scopeKV(fxCalendar, "dev", false); again != devDB {
		t.Fatal("the namespace file opens twice")
	}
	// main's ciphertext copied into dev's bucket fails closed under dev's label.
	if err := devDB.Update(func(tx *bolt.Tx) error {
		bk, err := tx.CreateBucketIfNotExists([]byte(devK.Bucket))
		if err != nil {
			return err
		}
		return bk.Put([]byte("k"), stored)
	}); err != nil {
		t.Fatal(err)
	}
	if pt, err := b.decodeKV(devK.KVLabel, stored); err == nil {
		t.Fatalf("main's value opened under dev's label: %q", pt)
	}
	if pt, err := b.decodeKV(mainK.KVLabel, stored); err != nil || !bytes.Equal(pt, []byte("main's")) {
		t.Fatalf("main's value under main's label: %q, %v", pt, err)
	}
	// data/kv.db holds only main's bucket.
	var buckets []string
	_ = b.kv.db.View(func(tx *bolt.Tx) error {
		return tx.ForEach(func(name []byte, _ *bolt.Bucket) error { buckets = append(buckets, string(name)); return nil })
	})
	if strings.Join(buckets, " ") != mainK.Bucket {
		t.Errorf("data/kv.db buckets: %v", buckets)
	}

	// Closed, the file can go; the next read answers empty again.
	if err := b.kv.closeNamespace(devK.NS); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(root, "data", "resources-enc", filepath.FromSlash(devK.NS))); err != nil {
		t.Fatal(err)
	}
	if db, err := b.kvDB(devK, false); err != nil || db != nil {
		t.Fatalf("after removal: %v, %v", db, err)
	}
	if _, err := b.kvDB(devK, true); err != nil {
		t.Fatal(err)
	}
	b.Close() // closes every namespace file with kv.db
	if len(b.kv.ns) != 0 {
		t.Errorf("Close left namespace files open: %v", b.kv.ns)
	}
}

// covers P14 P29 T11 NP-08-1 — adding deployment dev first drops the files an
// earlier dev of the tile left (08-data §3.3; the plane's add calls
// DropDeploymentFiles before its record commits, removal calls it once dev
// is stopped): the vault file, every user's prefs for (tile, dev), the
// registration files, removed through the plane's records lock, and the
// derived-state directory. main's files, another deployment's, another
// tile's dev, the deploy journal, the materialized checkpoints and the
// shared (scope, dev) namespace stay. main and names outside the grammar
// are refused before anything is removed.
func TestAddDeploymentDropsStaleFiles(t *testing.T) {
	b := deployBroker(t)
	installZeroPlane(t, b)
	root := b.Reg.Root
	at := func(rel string) string { return filepath.Join(root, filepath.FromSlash(rel)) }
	write := func(rel string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(at(rel)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(at(rel), []byte("stale "+rel), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	tk, other := util.TileKey(fxCalendar), util.TileKey(fxEmail)
	userA, userB := util.CompKey("ana"), util.CompKey("tom")
	stale := []string{
		"data/vault/.deployments/" + tk + "/dev.json",
		"data/prefs/" + userA + "/.deployments/" + tk + "/dev.json",
		"data/prefs/" + userB + "/.deployments/" + tk + "/dev.json",
		".xbin/deploy/" + tk + "/d/dev/backend.log",
		".xbin/deploy/" + tk + "/d/dev/sbx/box/state.json",
	}
	for _, f := range registrationFileNames {
		stale = append(stale, "data/deployments/"+tk+"/dev/"+f)
	}
	kept := []string{
		"data/vault/" + util.CompKey(fxCalendar) + ".json",                    // main's vault
		"data/prefs/" + userA + "/" + util.CompKey(fxCalendar) + ".json",      // main's prefs
		"data/vault/.deployments/" + tk + "/staging.json",                     // another deployment's
		"data/prefs/" + userA + "/.deployments/" + tk + "/staging.json",       //
		"data/deployments/" + tk + "/staging/cron.json",                       //
		".xbin/deploy/" + tk + "/d/staging/backend.log",                       //
		"data/vault/.deployments/" + other + "/dev.json",                      // another tile's dev
		"data/prefs/" + userB + "/.deployments/" + other + "/dev.json",        //
		"data/deployments/" + other + "/dev/cron.json",                        //
		".xbin/deploy/" + other + "/d/dev/backend.log",                        //
		"data/deployments/" + tk + "/pending.json",                            // the deploy journal
		".xbin/deploy/" + tk + "/" + strings.Repeat("ab", 20) + "/index.html", // a materialized checkpoint
		"data/resources-enc/.deployments/apps~calendar/dev/kv.db",             // the shared namespace
		"data/resources-enc/.deployments/apps~calendar/dev/fs/files/gocryptfs.conf",
	}
	for _, rel := range append(append([]string{}, stale...), kept...) {
		write(rel)
	}

	for _, dep := range []string{util.MainDeployment, "", "Dev", "../x"} {
		if err := b.DropDeploymentFiles(fxCalendar, dep); err == nil {
			t.Errorf("DropDeploymentFiles(%q) accepted", dep)
		}
	}
	for _, rel := range stale {
		if _, err := os.Lstat(at(rel)); err != nil {
			t.Fatalf("a refused drop removed %s: %v", rel, err)
		}
	}

	if err := b.DropDeploymentFiles(fxCalendar, "dev"); err != nil {
		t.Fatal(err)
	}
	for _, rel := range stale {
		if _, err := os.Lstat(at(rel)); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s survived: %v", rel, err)
		}
	}
	for _, rel := range kept {
		if got, err := os.ReadFile(at(rel)); err != nil || string(got) != "stale "+rel {
			t.Errorf("%s was touched: %q, %v", rel, got, err)
		}
	}
	for _, dir := range []string{"data/deployments/" + tk + "/dev", ".xbin/deploy/" + tk + "/d/dev",
		"data/prefs/" + userB + "/.deployments/" + tk} {
		if _, err := os.Lstat(at(dir)); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("the emptied %s stayed: %v", dir, err)
		}
	}
	// Nothing left to drop is no error.
	if err := b.DropDeploymentFiles(fxCalendar, "dev"); err != nil {
		t.Errorf("a second drop: %v", err)
	}
	if err := b.DropDeploymentFiles(fxPlain, "fresh"); err != nil {
		t.Errorf("a drop with nothing on disk: %v", err)
	}
}

// adHocResourceKeys reports each place in src that builds a resource's
// physical key by hand: a use of util.ScopeKey, or a string literal
// starting "res:" joined with "+".
func adHocResourceKeys(src []byte) []string {
	fset := token.NewFileSet()
	file := fset.AddFile("", fset.Base(), len(src))
	var s scanner.Scanner
	s.Init(file, src, nil, 0)
	var found []string
	var prev [3]string // the last tokens, newest first
	var prevLit string
	for {
		pos, tok, lit := s.Scan()
		if tok == token.EOF {
			return found
		}
		text := tok.String()
		if lit != "" {
			text = lit
		}
		line := strconv.Itoa(fset.Position(pos).Line)
		switch {
		case tok == token.IDENT && lit == "ScopeKey" && prev[0] == "." && prev[1] == "util":
			found = append(found, line+": util.ScopeKey")
		case tok == token.ADD && strings.HasPrefix(prevLit, "res:"):
			found = append(found, line+": "+strconv.Quote(prevLit)+" +")
		}
		prevLit = ""
		if tok == token.STRING {
			prevLit, _ = strconv.Unquote(lit)
		}
		prev[2], prev[1], prev[0] = prev[1], prev[0], text
	}
}

// covers P6 PO-2 NP-08-1 — no code in internal/broker but deploydata.go
// builds a resource's physical key by hand (08-data §3.2): every
// util.ScopeKey use and every "res:"… + concatenation goes through the key
// function, so no store keyed off it by a later change can miss the
// namespaces beyond main. Test files are exempt; they pin the keys.
func TestNoAdHocResourceKeys(t *testing.T) {
	t.Run("guard", func(t *testing.T) {
		got := adHocResourceKeys([]byte("package p\n" +
			"var a = util.ScopeKey(s)\n" +
			"var b = \"res:\" + s + \"/\" + n\n" +
			"var c = `res:workspace/`+n\n" +
			"var d = f(util.ScopeKey)\n" +
			"// util.ScopeKey(s) and \"res:\" + s in a comment\n" +
			"var e = \"xres:\" + s + strings.HasPrefix(t, \"res:\")\n"))
		want := []string{"2: util.ScopeKey", `3: "res:" +`, `4: "res:workspace/" +`, "5: util.ScopeKey"}
		if strings.Join(got, "|") != strings.Join(want, "|") {
			t.Fatalf("the detector found %q, want %q", got, want)
		}
	})
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var bad []string
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || f == "deploydata.go" {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, hit := range adHocResourceKeys(src) {
			bad = append(bad, "internal/broker/"+f+":"+hit)
		}
	}
	if len(bad) > 0 {
		t.Fatalf("resource keys built by hand — compute them with resKeys or scopeKeys (deploydata.go), "+
			"which know every deployment's namespace (08-data §3.2):\n  %s", strings.Join(bad, "\n  "))
	}
}
