package broker

// Seeding a deployment's data (08-data §8; 06-security T8; 14-implementation
// WP-42), on deploy_env_test.go's newNSBroker workspace with its stand-in
// plane and fake gocryptfs (nsFakeVolumes): a "mounted" volume is a plain
// directory, so the copies run for real — kv and blob in process, rsync and
// python3 directly on the host, as confine runs them without isolation.
// Tests that need those tools skip without them; seed_linux_test.go runs
// the confined copies in real sandboxes over real volumes.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
	"golang.org/x/sys/unix"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/confine"
	"github.com/xbin-dev/xbin/internal/deployments"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/sandbox"
	"github.com/xbin-dev/xbin/internal/util"
)

// seedFx is newNSBroker's workspace with volumes faked and unsealed, main's
// mounted, apps/calendar's dev pinned to a checkpoint declaring calDevScope,
// a disk with room, and every confined run recorded (runs) before it runs.
type seedFx struct {
	*nsFx
	plane *nsPlane
	mu    sync.Mutex
	runs  []confine.Cmd
}

func newSeedFx(t *testing.T) *seedFx {
	t.Helper()
	b, plane := newNSBroker(t)
	nsFakeVolumes(t, b)
	f := &seedFx{nsFx: &nsFx{t: t, b: b, root: b.Reg.Root}, plane: plane}
	plane.set(t, b, fxCalendar, util.MainDeployment, map[string]string{util.MainDeployment: "", "dev": nsCheckpoint(t, calDevScope)})
	b.MountEncrypted()
	prevFree, prevRun := seedDiskFree, seedRun
	seedDiskFree = func(string) (int64, int64) { return 900 << 30, 1000 << 30 }
	seedRun = func(ctx context.Context, c confine.Cmd) (confine.Result, error) {
		f.mu.Lock()
		f.runs = append(f.runs, c)
		f.mu.Unlock()
		return prevRun(ctx, c)
	}
	t.Cleanup(func() { seedDiskFree, seedRun = prevFree, prevRun })
	return f
}

// fakeRuns makes every confined run answer out (stdout) and err without
// running anything, still recording it.
func (f *seedFx) fakeRuns(fn func(c confine.Cmd) (string, error)) {
	seedRun = func(_ context.Context, c confine.Cmd) (confine.Result, error) {
		f.mu.Lock()
		f.runs = append(f.runs, c)
		f.mu.Unlock()
		out, err := fn(c)
		return confine.Result{Stdout: []byte(out)}, err
	}
}

// seedReq is a confirmed seed of dep of tile.
func seedReq(tile, dep string) deployments.SeedRequest {
	return deployments.SeedRequest{Tile: tile, Deployment: dep, Confirm: deployments.ConfirmCopyData}
}

// seed seeds as p, with every other claimant allowed, and waits for the
// copy to finish.
func (f *seedFx) seed(p auth.Principal, req deployments.SeedRequest) (SeedFacts, error) {
	f.t.Helper()
	return f.seedWith(p, req, allow)
}

func (f *seedFx) seedWith(p auth.Principal, req deployments.SeedRequest, authorize func(string) error) (SeedFacts, error) {
	f.t.Helper()
	facts, err := f.b.SeedDeploymentData(p, req, authorize, f.stop)
	if err == nil && facts.Done != nil {
		select {
		case <-facts.Done:
		case <-time.After(2 * time.Minute):
			f.t.Fatal("the seed didn't finish")
		}
	}
	return facts, err
}

// mount is resource name's volume in scope's namespace in dep, as the seed
// sees it: a plain directory under the fake.
func (f *seedFx) mount(scope, dep, name string) string {
	return f.b.resMount(f.keys(scope, dep, name), false)
}

// write puts files under dir.
func (f *seedFx) write(dir string, files map[string]string) {
	f.t.Helper()
	nsWrite(f.t, dir, files)
}

// needTools skips without the host tools a direct (unisolated) copy runs.
func needTools(t *testing.T) {
	t.Helper()
	for _, tool := range []string{"rsync", "python3"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("no %s on this host: the direct copy can't run", tool)
		}
	}
}

// sqliteDB makes a rollback-journal sqlite database of n rows at p (host
// python3).
func sqliteDB(t *testing.T, p string, n int) {
	t.Helper()
	script := `import sqlite3, sys
c = sqlite3.connect(sys.argv[1])
c.execute("create table t(x)")
c.executemany("insert into t values(?)", [(i,) for i in range(int(sys.argv[2]))])
c.commit()`
	if out, err := exec.Command("python3", "-c", script, p, fmt.Sprint(n)).CombinedOutput(); err != nil {
		t.Fatalf("making %s: %v %s", p, err, out)
	}
}

// sqliteRows counts t's rows in the database at p.
func sqliteRows(t *testing.T, p string) string {
	t.Helper()
	out, err := exec.Command("python3", "-c", `import sqlite3, sys
print(sqlite3.connect("file:" + sys.argv[1] + "?mode=ro", uri=True).execute("select count(*) from t").fetchone()[0])`, p).CombinedOutput()
	if err != nil {
		t.Fatalf("reading %s: %v %s", p, err, out)
	}
	return strings.TrimSpace(string(out))
}

// hashTree fingerprints every entry under dir: its path, type, mode, link
// target and content. A missing dir is "".
func hashTree(t *testing.T, dir string) string {
	t.Helper()
	h := sha256.New()
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		} else if err != nil {
			return err
		}
		fi, err := os.Lstat(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		fmt.Fprintf(h, "%s %v\n", rel, fi.Mode())
		switch {
		case fi.Mode()&fs.ModeSymlink != 0:
			l, _ := os.Readlink(p)
			fmt.Fprintln(h, "->", l)
		case fi.Mode().IsRegular():
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			h.Write(data)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// rawPairs is bucket's raw stored pairs in scope's namespace in dep.
func (f *seedFx) rawPairs(scope, dep, name string) map[string]string {
	f.t.Helper()
	k := f.keys(scope, dep, name)
	db, err := f.b.kvDB(k, false)
	if err != nil {
		f.t.Fatal(err)
	}
	out := map[string]string{}
	_ = kvView(db, func(tx *bolt.Tx) error {
		if bk := tx.Bucket([]byte(k.Bucket)); bk != nil {
			return bk.ForEach(func(k, v []byte) error { out[string(k)] = string(v); return nil })
		}
		return nil
	})
	return out
}

// kvWrites is kv.db's page writes so far: it moves with every write
// transaction.
func (f *seedFx) kvWrites() int64 {
	s := f.b.kv.db.Stats()
	return s.TxStats.GetWrite()
}

// gatedPut stores a kv value as the kv API does, past scope's write gate in
// dep: it waits while an act holds the gate.
func (f *seedFx) gatedPut(scope, dep, name, key, val string) bool {
	release, ok := f.b.nsWriting(httptest.NewRecorder(), scope, dep)
	if !ok {
		return false
	}
	defer release()
	f.putKV(scope, dep, name, key, val)
	return true
}

// covers D127i T8 T9 — seeding is a tile manager's act in a person's own
// session, carrying the copy-data confirmation (08-data §8.1; 11-contract
// §1.8): terminal and agent tokens (a manager's own included), the tile's
// backends, an element holding xbin:users, view-as sessions and people who
// don't manage the tile are refused before anything is read or made; a dry
// run answers the facts and creates nothing; a manager's seed commits,
// naming who seeded.
func TestSeedManagerOnly(t *testing.T) {
	f := newSeedFx(t)
	b := f.b
	f.fakeRuns(func(confine.Cmd) (string, error) { return "", nil })
	if err := b.Users.SetOwner(fxCalendar, "user:"+fxWriter); err != nil {
		t.Fatal(err)
	}
	if err := b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) {
		ws.Grants = append(ws.Grants, registry.Grant{From: fxConsole, Target: "xbin:users", Role: "admin"})
	}); err != nil {
		t.Fatal(err)
	}
	ana, wes := deployPerson(t, b, fxAdmin), deployPerson(t, b, fxWriter)
	anaTerm := auth.Principal{Component: fxCalendar, Via: "terminal", UserID: fxAdmin, User: ana.User, Access: ana.Access}
	anaAgent := anaTerm // an agent session's token: a terminal token targeting dev
	anaAgent.Deployment = "dev"
	viewAs := wes
	viewAs.Impersonator = fxAdmin
	session := "a tile manager's act, done in a person's own session"
	notManager := "a tile manager's act: the tile's owner, its org's admins, or a workspace admin"
	for _, c := range []struct {
		name string
		p    auth.Principal
		code int
		why  string
	}{
		{"a manager's terminal token", anaTerm, 403, session},
		{"a manager's agent token", anaAgent, 403, session},
		{"dev's own backend", auth.Principal{Component: fxCalendar, Via: "instance", Deployment: "dev"}, 403, session},
		{"the primary's frame", auth.Principal{Component: fxCalendar, Via: "frame", UserID: fxWriter}, 403, session},
		{"an element holding xbin:users", auth.Principal{Component: fxConsole, Via: "instance"}, 403, session},
		{"a terminal-level user", deployPerson(t, b, fxTerm), 403, notManager},
		{"a reader", deployPerson(t, b, fxReader), 403, notManager},
		{"a view-as session", viewAs, 403, "view-as"},
	} {
		_, err := f.seed(c.p, seedReq(fxCalendar, "dev"))
		var de *deployments.Error
		if !errors.As(err, &de) || de.Status != c.code || !strings.Contains(de.Msg, c.why) {
			t.Errorf("%s: %v, want %d %q", c.name, err, c.code, c.why)
		}
	}
	noConfirm := seedReq(fxCalendar, "dev")
	noConfirm.Confirm = ""
	_, err := f.seed(wes, noConfirm)
	wantErr(t, err, 400, `seeding dev copies main's data, which may be personal: send confirm:"copy-data" to proceed`)
	if exists(f.nsRoot(fxCalendar, "dev")) || len(f.stops) > 0 || len(f.runs) > 0 {
		t.Fatalf("a refused seed made %v, stopped %v, ran %d", exists(f.nsRoot(fxCalendar, "dev")), f.stops, len(f.runs))
	}

	f.putKV(fxCalendar, util.MainDeployment, "events", "k", "v")
	dry := seedReq(fxCalendar, "dev")
	dry.DryRun = true
	facts, err := f.seed(wes, dry)
	want := SeedFacts{Scope: fxCalendar, From: "main", Deployment: "dev", Claimants: []string{fxCalendar},
		Stops: []string{fxCalendar + "+dev"}, Copies: []string{"db", "events", "files", "pics"},
		Empty: []string{"devkv", "devonly", "devpics"}, Skipped: []string{}, Consistency: "online"}
	facts.Bytes = 0
	if err != nil || !reflect.DeepEqual(facts, want) {
		t.Fatalf("dry run: %v\n got %+v\nwant %+v", err, facts, want)
	}
	if exists(f.nsRoot(fxCalendar, "dev")) || exists(filepath.Join(f.root, ".xbin", "resenc", deploymentsLevel)) ||
		len(f.stops) > 0 || len(f.runs) > 0 {
		t.Fatal("a dry run made a namespace or a volume, stopped a deployment or ran a copy")
	}

	if _, err := f.seed(wes, seedReq(fxCalendar, "dev")); err != nil {
		t.Fatal(err)
	}
	m := f.meta(fxCalendar, "dev")
	if m.State != nsSeeded || m.By != "user:"+fxWriter || m.From != "main" || m.Busy != "" {
		t.Errorf("after a manager's seed: %+v", m)
	}
	if got := f.getKV(fxCalendar, "dev", "events", "k"); got != "v" {
		t.Errorf("dev's events/k = %q, want the primary's", got)
	}
	if _, err := f.seed(ana, seedReq(fxCalendar, "dev")); err != nil { // an admin is a manager of every tile
		t.Errorf("an admin's seed: %v", err)
	}
}

// covers D127i PO-2 T8 SC-DATA — a seed only reads the primary's namespace
// (08-data §8.5): main's ciphertext, its mounted views, its kv pairs and
// kv.db itself (no write transaction for a non-main target) are unchanged,
// and no ns.json is made for main; what dev gets is re-keyed: the stored
// values differ from main's, and a main value copied byte for byte into
// dev's bucket doesn't decode under dev's label.
func TestSeedNeverWritesPrimary(t *testing.T) {
	needTools(t)
	f := newSeedFx(t)
	b := f.b
	for i := range 50 {
		f.putKV(fxCalendar, util.MainDeployment, "events", fmt.Sprintf("k%02d", i), fmt.Sprintf("value %d", i))
	}
	f.write(f.mount(fxCalendar, "main", "files"), map[string]string{"a.txt": "alpha", "sub/b.txt": "beta"})
	f.write(f.mount(fxCalendar, "main", "db"), map[string]string{"notes.txt": "beside the db"})
	sqliteDB(t, filepath.Join(f.mount(fxCalendar, "main", "db"), "db.sqlite"), 100)
	f.write(f.mount(fxCalendar, "main", "pics"), map[string]string{"p1.bin": "picture one", "dir/p2.bin": "picture two"})
	sk := util.ScopeKey(fxCalendar)
	trees := []string{filepath.Join(f.root, "data", "resources-enc", sk), filepath.Join(f.root, ".xbin", "resenc", sk)}
	before := []string{hashTree(t, trees[0]), hashTree(t, trees[1])}
	pairs, writes := f.rawPairs(fxCalendar, "main", "events"), f.kvWrites()

	facts, err := f.seed(deployPerson(t, b, fxAdmin), seedReq(fxCalendar, "dev"))
	if err != nil {
		t.Fatal(err)
	}
	for i, dir := range trees {
		if got := hashTree(t, dir); got != before[i] {
			t.Errorf("the seed changed the primary's %s", dir)
		}
	}
	if got := f.rawPairs(fxCalendar, "main", "events"); !reflect.DeepEqual(got, pairs) {
		t.Error("the seed changed main's kv pairs")
	}
	if got := f.kvWrites(); got != writes {
		t.Errorf("kv.db took %d page writes during a seed into dev's own kv file", got-writes)
	}
	if exists(filepath.Join(f.root, "data", "resources-enc", deploymentsLevel, escS(fxCalendar), "main")) {
		t.Error("the seed made metadata for main")
	}

	m := f.meta(fxCalendar, "dev")
	if m.State != nsSeeded || m.Consistency != "online" || m.FromCheckpoint != "worktree" || len(m.Resources) != 4 ||
		m.Resources["events"] == 0 || len(m.History) == 0 || m.History[len(m.History)-1].Op != "seed" {
		t.Errorf("dev's ns.json: %+v", m)
	}
	if got := f.getKV(fxCalendar, "dev", "events", "k07"); got != "value 7" {
		t.Errorf("dev's events/k07 = %q", got)
	}
	devPairs := f.rawPairs(fxCalendar, "dev", "events")
	if len(devPairs) != 50 || devPairs["k07"] == pairs["k07"] {
		t.Errorf("dev holds %d pairs; k07 stored as main's bytes: %v", len(devPairs), devPairs["k07"] == pairs["k07"])
	}
	if _, err := b.decodeKV(f.keys(fxCalendar, "dev", "events").KVLabel, []byte(pairs["k07"])); err == nil {
		t.Error("main's stored value decodes under dev's label: a misplaced copy wouldn't fail closed")
	}
	for rel, want := range map[string]string{"files/a.txt": "alpha", "files/sub/b.txt": "beta", "db/notes.txt": "beside the db",
		"pics/p1.bin": "picture one", "pics/dir/p2.bin": "picture two"} {
		name, file, _ := strings.Cut(rel, "/")
		if got, err := os.ReadFile(filepath.Join(f.mount(fxCalendar, "dev", name), file)); err != nil || string(got) != want {
			t.Errorf("dev's %s: %q %v, want %q", rel, got, err, want)
		}
	}
	if got := sqliteRows(t, filepath.Join(f.mount(fxCalendar, "dev", "db"), "db.sqlite")); got != "100" {
		t.Errorf("dev's db.sqlite holds %s rows, want 100", got)
	}
	var tools []string
	for _, c := range f.runs {
		tools = append(tools, c.Argv[0])
	}
	if !slices.Equal(tools, []string{"rsync", "python3", "rsync", "python3"}) {
		t.Errorf("confined runs %v: want rsync and python3 for db and files, nothing for kv or blob", tools)
	}
	if facts.Bytes <= 0 {
		t.Errorf("facts.Bytes = %d", facts.Bytes)
	}
}

// covers T8 T2 C5 L10 — the filesystem copy is confined (08-data §8.3;
// 06-security ledger L10): rsync sees only the two volumes, the primary's
// read-only (the sqlite step's read-write, for a WAL reader), with no
// network; a symlink the primary's backend planted toward .xbin/token is
// copied as a symlink and never dereferenced, hard links and xattrs are
// kept, a FIFO is left out (never opened), and nothing of the token reaches
// dev. The blob copy opens beneath its volume and skips links and special
// files.
func TestSeedCopyConfinedNoFollow(t *testing.T) {
	needTools(t)
	f := newSeedFx(t)
	token := filepath.Join(f.root, ".xbin", "token")
	nsWrite(t, filepath.Dir(token), map[string]string{"token": "SECRET-TOKEN"})
	from, to := f.mount(fxCalendar, "main", "files"), f.mount(fxCalendar, "dev", "files")
	f.write(from, map[string]string{"a.txt": "alpha", "h1": "hard"})
	for _, err := range []error{os.Symlink(token, filepath.Join(from, "token-link")), os.Symlink("a.txt", filepath.Join(from, "rel-link")),
		os.Link(filepath.Join(from, "h1"), filepath.Join(from, "h2")), syscall.Mkfifo(filepath.Join(from, "fifo"), 0o600)} {
		if err != nil {
			t.Fatal(err)
		}
	}
	xattr := unix.Setxattr(filepath.Join(from, "a.txt"), "user.seed", []byte("kept"), 0) == nil
	pics := f.mount(fxCalendar, "main", "pics")
	f.write(pics, map[string]string{"ok.bin": "fine"})
	if err := os.Symlink(token, filepath.Join(pics, "leak")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(pics, "pipe"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := f.seed(deployPerson(t, f.b, fxAdmin), seedReq(fxCalendar, "dev")); err != nil {
		t.Fatal(err)
	}
	if m := f.meta(fxCalendar, "dev"); m.State != nsSeeded {
		t.Fatalf("dev's ns.json: %+v", m)
	}
	var rsync, py *confine.Cmd
	for i, c := range f.runs {
		if c.Dir == to {
			switch c.Argv[0] {
			case "rsync":
				rsync = &f.runs[i]
			case "python3":
				py = &f.runs[i]
			}
		}
	}
	switch {
	case rsync == nil || py == nil:
		t.Fatalf("runs %+v: want rsync and python3 into %s", f.runs, to)
	case !reflect.DeepEqual(rsync.Binds, []sandbox.Bind{{Src: from, Dst: from, RO: true}}) || rsync.ReadOnlyDir || rsync.Net != confine.NetNone:
		t.Errorf("rsync binds %+v (read-only dir %v, net %v): want only the primary's volume, read-only", rsync.Binds, rsync.ReadOnlyDir, rsync.Net)
	case !slices.Equal(rsync.Argv, []string{"rsync", "-aHX", "--no-D", "--numeric-ids", from + "/", to + "/"}):
		t.Errorf("rsync argv %q", rsync.Argv)
	case !reflect.DeepEqual(py.Binds, []sandbox.Bind{{Src: from, Dst: from}}) || py.Net != confine.NetNone:
		t.Errorf("python3 binds %+v: want the primary's volume read-write, for a WAL reader", py.Binds)
	}
	if l, err := os.Readlink(filepath.Join(to, "token-link")); err != nil || l != token {
		t.Errorf("token-link in dev: %q %v, want the symlink itself", l, err)
	}
	if l, err := os.Readlink(filepath.Join(to, "rel-link")); err != nil || l != "a.txt" {
		t.Errorf("rel-link in dev: %q %v", l, err)
	}
	h1, err1 := os.Lstat(filepath.Join(to, "h1"))
	h2, err2 := os.Lstat(filepath.Join(to, "h2"))
	if err1 != nil || err2 != nil || !os.SameFile(h1, h2) {
		t.Errorf("hard links not kept: %v %v", err1, err2)
	}
	if _, err := os.Lstat(filepath.Join(to, "fifo")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the FIFO was copied (%v): special files are left out", err)
	}
	buf := make([]byte, 16)
	if n, err := unix.Getxattr(filepath.Join(to, "a.txt"), "user.seed", buf); xattr && (err != nil || string(buf[:n]) != "kept") {
		t.Errorf("the xattr wasn't kept: %q %v", buf[:n], err)
	}
	devPics := f.mount(fxCalendar, "dev", "pics")
	for _, name := range []string{"leak", "pipe"} {
		if _, err := os.Lstat(filepath.Join(devPics, name)); err == nil {
			t.Errorf("the blob copy copied %s", name)
		}
	}
	if got, _ := os.ReadFile(filepath.Join(devPics, "ok.bin")); string(got) != "fine" {
		t.Errorf("dev's ok.bin = %q", got)
	}
	for _, dir := range []string{to, devPics} {
		_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err == nil && d.Type().IsRegular() {
				if data, _ := os.ReadFile(p); bytes.Contains(data, []byte("SECRET-TOKEN")) {
					t.Errorf("the token's content reached %s", p)
				}
			}
			return nil
		})
	}
}

// covers D127t T8 — a shared (scope, name) namespace (08-data §6.3): seeding
// apps/shop's dev needs the manager gate on every claimant, and the 403
// names the tile that blocks it, the broker's own gate and the plane's
// judgement alike; a seed stops every claimant's dev, not a sibling
// without one, copies into the one namespace they share, and announces op
// data to each claimant tile.
func TestSharedScopeSeedNeedsEveryTile(t *testing.T) {
	f := newSeedFx(t)
	b := f.b
	shop := `{"resources":{"orders":{"type":"kv"},"events":{"type":"bus"}}}`
	f.plane.set(t, b, fxShop, util.MainDeployment, map[string]string{util.MainDeployment: "", "dev": nsCheckpoint(t, shop)})
	f.plane.set(t, b, fxShopAdmin, util.MainDeployment, map[string]string{util.MainDeployment: "", "dev": nsCheckpoint(t, "")})
	if err := b.Users.SetOwner(fxShop, "user:"+fxTerm); err != nil {
		t.Fatal(err)
	}
	f.putKV(fxShop, util.MainDeployment, "orders", "o1", "first order")
	tom, ana := deployPerson(t, b, fxTerm), deployPerson(t, b, fxAdmin)
	blocked := `seed of apps/shop's "dev" data needs a tile manager on apps/shop/admin too`
	_, err := f.seed(tom, seedReq(fxShop, "dev"))
	wantErr(t, err, 403, blocked)
	refuse := func(tile string) error {
		if tile == fxShopAdmin {
			return &deployments.Error{Status: http.StatusForbidden, Kind: deployments.KindAuthority, Msg: "the plane says no"}
		}
		return nil
	}
	_, err = f.seedWith(ana, seedReq(fxShop, "dev"), refuse)
	wantErr(t, err, 403, blocked)
	if exists(f.nsRoot(fxShop, "dev")) || len(f.stops) > 0 {
		t.Fatal("a refused shared seed made the namespace or stopped a deployment")
	}

	evs, cancel := b.Hub.Subscribe(func(e events.Event) bool { return e.Type == "deployments" })
	defer cancel()
	facts, err := f.seed(ana, seedReq(fxShopAdmin, "dev")) // from either claimant: the one namespace
	if err != nil {
		t.Fatal(err)
	}
	claimants := []string{fxShop, fxShopAdmin}
	if !slices.Equal(facts.Claimants, claimants) || !slices.Equal(f.stops, []string{fxShop + "+dev", fxShopAdmin + "+dev"}) {
		t.Errorf("claimants %v, stops %v: want both tiles' dev and not apps/shop/stats", facts.Claimants, f.stops)
	}
	if got := f.getKV(fxShop, "dev", "orders", "o1"); got != "first order" {
		t.Errorf("(apps/shop, dev)'s orders/o1 = %q", got)
	}
	seen := map[string][]string{}
	for len(seen[fxShop]) < 2 || len(seen[fxShopAdmin]) < 2 {
		select {
		case e := <-evs:
			d, _ := e.Data.(map[string]string)
			if d["op"] == "data" && d["deployment"] == "dev" {
				seen[e.Component] = append(seen[e.Component], d["busy"]+"/"+d["state"])
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("op data events: %v", seen)
		}
	}
	for _, tile := range claimants {
		if want := []string{"seeding/empty", "/seeded"}; !slices.Equal(seen[tile], want) {
			t.Errorf("%s's op data events: %v, want %v", tile, seen[tile], want)
		}
	}
}

// covers D127n 08-data §6.7 — a seed across divergent declarations copies the
// resources both codes declare with one type; those only dev declares start
// empty (whatever the namespace held before is replaced); those only main
// declares, or with another type, are skipped and listed, in the answer and
// in ns.json. Nothing of a skipped resource reaches dev.
func TestSeedWithDivergentDeclarations(t *testing.T) {
	f := newSeedFx(t)
	b := f.b
	devScope := `{"resources":{"events":{"type":"kv"},"pics":{"type":"blob"},"files":{"type":"blob"},
		"newkv":{"type":"kv"},"bus":{"type":"bus"}}}`
	f.plane.set(t, b, fxCalendar, util.MainDeployment, map[string]string{util.MainDeployment: "", "dev": nsCheckpoint(t, devScope)})
	f.putKV(fxCalendar, util.MainDeployment, "events", "e", "event")
	f.write(f.mount(fxCalendar, "main", "pics"), map[string]string{"p.bin": "picture"})
	f.write(f.mount(fxCalendar, "main", "files"), map[string]string{"secret.txt": "main's files"})
	f.putKV(fxCalendar, "dev", "newkv", "stale", "from before")

	facts, err := f.seed(deployPerson(t, b, fxAdmin), seedReq(fxCalendar, "dev"))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(facts.Copies, []string{"events", "pics"}) || !slices.Equal(facts.Empty, []string{"newkv"}) ||
		!slices.Equal(facts.Skipped, []string{"db", "files"}) {
		t.Errorf("copies %v, empty %v, skipped %v", facts.Copies, facts.Empty, facts.Skipped)
	}
	if m := f.meta(fxCalendar, "dev"); m.State != nsSeeded || !slices.Equal(m.Skipped, []string{"db", "files"}) {
		t.Errorf("dev's ns.json: %+v", m)
	}
	if len(f.runs) != 0 {
		t.Errorf("confined runs %+v: nothing file-backed but blob is copied", f.runs)
	}
	if got := f.getKV(fxCalendar, "dev", "events", "e"); got != "event" {
		t.Errorf("dev's events/e = %q", got)
	}
	if got := f.getKV(fxCalendar, "dev", "newkv", "stale"); got != "" {
		t.Errorf("dev's newkv kept %q: a seed replaces the namespace", got)
	}
	if got, _ := os.ReadFile(filepath.Join(f.mount(fxCalendar, "dev", "pics"), "p.bin")); string(got) != "picture" {
		t.Errorf("dev's pics/p.bin = %q", got)
	}
	_ = filepath.WalkDir(f.nsRoot(fxCalendar, "dev"), func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if data, _ := os.ReadFile(p); bytes.Contains(data, []byte("main's files")) {
				t.Errorf("a skipped resource's data reached %s", p)
			}
		}
		return nil
	})
}

// covers D127i 08-data §8.2 §8.3 — kv is copied consistently under the
// primary's write gate: with writers adding to two buckets in turn through
// the gate while the seed runs (and read transactions of 7 keys), dev gets
// both buckets at one moment — log's keys a prefix of events', at most one
// behind — and the writers resume once the gate lifts.
func TestSeedKVConsistentUnderWrites(t *testing.T) {
	f := newSeedFx(t)
	b := f.b
	two := `{"resources":{"events":{"type":"kv"},"log":{"type":"kv"}}}`
	nsWrite(t, b.Reg.Root, map[string]string{"apps/calendar/scope.json": two})
	f.plane.set(t, b, fxCalendar, util.MainDeployment, map[string]string{util.MainDeployment: "", "dev": nsCheckpoint(t, two)})
	prev := seedReadKeys
	seedReadKeys = 7
	t.Cleanup(func() { seedReadKeys = prev })
	for i := range 200 {
		f.putKV(fxCalendar, util.MainDeployment, "events", fmt.Sprintf("k%05d", i), "x")
		f.putKV(fxCalendar, util.MainDeployment, "log", fmt.Sprintf("k%05d", i), "x")
	}
	stop, done := make(chan struct{}), make(chan int)
	var pairs atomic.Int64
	go func() {
		i := 200
		for ; ; i, _ = i+1, pairs.Add(1) {
			select {
			case <-stop:
				done <- i
				return
			default:
			}
			key := fmt.Sprintf("k%05d", i)
			if !f.gatedPut(fxCalendar, util.MainDeployment, "events", key, "x") || !f.gatedPut(fxCalendar, util.MainDeployment, "log", key, "x") {
				done <- -1
				return
			}
		}
	}()
	for deadline := time.Now().Add(10 * time.Second); pairs.Load() < 20; time.Sleep(time.Millisecond) { // the writers are going
		if time.Now().After(deadline) {
			t.Fatal("the writers never got going")
		}
	}
	_, err := f.seed(deployPerson(t, b, fxAdmin), seedReq(fxCalendar, "dev"))
	close(stop)
	if n := <-done; err != nil || n < 0 {
		t.Fatalf("seed %v; writers stopped at %d", err, n)
	}
	events, log := f.rawPairs(fxCalendar, "dev", "events"), f.rawPairs(fxCalendar, "dev", "log")
	for i := range len(events) {
		if _, ok := events[fmt.Sprintf("k%05d", i)]; !ok {
			t.Fatalf("dev's events has a hole at %d of %d", i, len(events))
		}
	}
	for k := range log {
		if _, ok := events[k]; !ok {
			t.Fatalf("dev's log has %s, which its events lack: the buckets weren't copied at one moment", k)
		}
	}
	if d := len(events) - len(log); d < 0 || d > 1 {
		t.Errorf("dev's events holds %d keys, its log %d: more than one write apart", len(events), len(log))
	}
}

// covers 08-data §8.3 — kv is read and written in bounded transactions: 1000
// keys under bounds of 100 read and 40 written take at least 10 read
// transactions of kv.db and 25 commits into dev's own kv file (each commit
// writes a page and the meta), and kv.db takes no write transaction.
func TestSeedKVBoundedTransactions(t *testing.T) {
	f := newSeedFx(t)
	b := f.b
	pr, pw := seedReadKeys, seedWriteKeys
	seedReadKeys, seedWriteKeys = 100, 40
	t.Cleanup(func() { seedReadKeys, seedWriteKeys = pr, pw })
	for i := range 1000 {
		f.putKV(fxCalendar, util.MainDeployment, "events", fmt.Sprintf("k%04d", i), strings.Repeat("v", 20))
	}
	reads, writes := b.kv.db.Stats().TxN, f.kvWrites()
	if _, err := f.seed(deployPerson(t, b, fxAdmin), seedReq(fxCalendar, "dev")); err != nil {
		t.Fatal(err)
	}
	if n := b.kv.db.Stats().TxN - reads; n < 10 {
		t.Errorf("kv.db took %d read transactions for 1000 keys at 100 a transaction", n)
	}
	if got := f.kvWrites(); got != writes {
		t.Errorf("kv.db took %d page writes", got-writes)
	}
	db, err := b.kvDB(f.keys(fxCalendar, "dev", "events"), false)
	if err != nil || db == nil {
		t.Fatalf("dev's kv file: %v", err)
	}
	s := db.Stats()
	if w := s.TxStats.GetWrite(); w < 2*25 {
		t.Errorf("dev's kv file took %d page writes: fewer than 25 commits of 40 keys", w)
	}
	if m := f.meta(fxCalendar, "dev"); m.State != nsSeeded || len(f.rawPairs(fxCalendar, "dev", "events")) != 1000 {
		t.Errorf("dev after the seed: %+v", m)
	}
}

// covers D127i 08-data §8.5 — any failure leaves the namespace partial, naming
// the act and the step, and dev refuses to start until a reset or another
// seed: a kv value that doesn't decode (the error is never swallowed into
// a short copy), rsync failing (its exit 24, files vanished, is only a
// warning), and a sqlite resource's database failing integrity_check. A
// reset recovers; a later seed succeeds.
func TestSeedFailureLeavesPartial(t *testing.T) {
	f := newSeedFx(t)
	b := f.b
	ana := deployPerson(t, b, fxAdmin)
	k := f.keys(fxCalendar, util.MainDeployment, "events")
	f.putKV(fxCalendar, util.MainDeployment, "events", "good", "fine")
	if err := b.kv.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(k.Bucket)).Put([]byte("bad"), append([]byte{kvTagEnc}, "not a ciphertext"...))
	}); err != nil {
		t.Fatal(err)
	}
	f.fakeRuns(func(confine.Cmd) (string, error) { return "", nil })
	partial := func(step, why string) {
		t.Helper()
		m := f.meta(fxCalendar, "dev")
		if m.State != nsPartial || m.Failed != "seed" || m.Step != step || !strings.Contains(m.Error, why) || m.Busy != "" {
			t.Fatalf("dev's ns.json: %+v, want partial at %q (%q)", m, step, why)
		}
		want := "seed of apps/calendar for dev failed at " + step + ": reset or seed again"
		if err := b.nsStartBlocked(fxCalendar, "dev"); err == nil || err.Error() != want {
			t.Errorf("dev's start: %v, want %q", err, want)
		}
		if st := b.DeploymentData(fxCalendar, "dev"); st.State != nsPartial || st.Busy != "" {
			t.Errorf("dev's data state: %+v", st)
		}
	}
	if _, err := f.seed(ana, seedReq(fxCalendar, "dev")); err != nil {
		t.Fatal(err)
	}
	partial("copying events (kv)", "doesn't decode")

	if err := b.kv.db.Update(func(tx *bolt.Tx) error { return tx.Bucket([]byte(k.Bucket)).Delete([]byte("bad")) }); err != nil {
		t.Fatal(err)
	}
	f.fakeRuns(func(c confine.Cmd) (string, error) {
		if c.Argv[0] == "rsync" && strings.Contains(c.Dir, "files") {
			return "", &confine.ExitError{Code: 23, Stderr: "rsync: some files could not be transferred"}
		}
		return "", nil
	})
	if _, err := f.seed(ana, seedReq(fxCalendar, "dev")); err != nil {
		t.Fatal(err)
	}
	partial("copying files (filesystem)", "could not be transferred")

	f.fakeRuns(func(c confine.Cmd) (string, error) {
		switch {
		case c.Argv[0] == "rsync":
			return "", &confine.ExitError{Code: 24, Stderr: "some files vanished"}
		case strings.Contains(c.Dir, "/db"):
			return `{"rel":"db.sqlite","ok":false,"error":"integrity_check: row 3 missing","strict":true}` + "\n", nil
		}
		return `{"rel":"inner.db","ok":false,"error":"not a database","strict":false}` + "\n", nil
	})
	if _, err := f.seed(ana, seedReq(fxCalendar, "dev")); err != nil {
		t.Fatal(err)
	}
	partial("copying db (sqlite)", "row 3 missing")

	if _, err := b.ResetDeploymentData(fxCalendar, "dev", "user:"+fxAdmin, false, false, allow, f.stop); err != nil {
		t.Fatal(err)
	}
	if m := f.meta(fxCalendar, "dev"); m.State != nsEmpty || b.nsStartBlocked(fxCalendar, "dev") != nil {
		t.Errorf("after a reset: %+v", m)
	}
	f.fakeRuns(func(confine.Cmd) (string, error) { return "", nil })
	if _, err := f.seed(ana, seedReq(fxCalendar, "dev")); err != nil || f.meta(fxCalendar, "dev").State != nsSeeded {
		t.Errorf("a seed after the reset: %v %+v", err, f.meta(fxCalendar, "dev"))
	}
}

// covers D127i 08-data §8.4 NP-08-6 — online is the default; stop:true takes
// the stopped mode: the primary's deployments stop too, its namespace is
// held (data requests answer 503 with Retry-After, its write gate is taken)
// while the copy runs, and ns.json records the mode. A single-tenant volume
// and a VM primary with file resources require it: without stop:true the
// seed is refused, saying why, and nothing is made.
func TestSeedStoppedMode(t *testing.T) {
	f := newSeedFx(t)
	b := f.b
	ana := deployPerson(t, b, fxAdmin)
	var during []string
	f.fakeRuns(func(c confine.Cmd) (string, error) {
		if c.Argv[0] == "rsync" && len(during) == 0 {
			w := httptest.NewRecorder()
			ok := b.nsAvailable(w, fxCalendar, util.MainDeployment)
			g := b.nsTab().gate(nsOf(fxCalendar, util.MainDeployment))
			locked := !g.TryRLock()
			if !locked {
				g.RUnlock()
			}
			during = append(during, fmt.Sprintf("%v %d %s %v", ok, w.Code, w.Header().Get("Retry-After"), locked))
		}
		return "", nil
	})
	req := seedReq(fxCalendar, "dev")
	req.Stop = true
	facts, err := f.seed(ana, req)
	if err != nil {
		t.Fatal(err)
	}
	if facts.Consistency != "stopped" || !slices.Equal(f.stops, []string{fxCalendar + "+dev", fxCalendar + "+main"}) || facts.Downtime == 0 {
		t.Errorf("consistency %q, stops %v, downtime %d", facts.Consistency, f.stops, facts.Downtime)
	}
	if want := []string{"false 503 5 true"}; !slices.Equal(during, want) {
		t.Errorf("main during a stopped copy: %v, want %v (refused, 503, Retry-After, gate held)", during, want)
	}
	if m := f.meta(fxCalendar, "dev"); m.Consistency != "stopped" || b.busyAct(nsOf(fxCalendar, util.MainDeployment)) != "" {
		t.Errorf("after: %+v, main's hold %q", m, b.busyAct(nsOf(fxCalendar, util.MainDeployment)))
	}

	f.stops = nil
	if err := b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) {
		ws.Grants = append(ws.Grants, registry.Grant{From: fxCalendar, Target: ContainersCap, Role: "use"})
	}); err != nil {
		t.Fatal(err)
	}
	cal, _ := b.Reg.Component(fxCalendar)
	if !b.ContainersFor(cal) {
		t.Fatal("the fixture's grant doesn't give apps/calendar cap:containers")
	}
	_, err = f.seed(ana, seedReq(fxCalendar, "dev"))
	wantErr(t, err, 409, "seeding dev needs main stopped for the copy: files is a container store (a single-tenant volume)")
	dry := req
	dry.DryRun = true
	if facts, err := f.seed(ana, dry); err != nil || facts.Consistency != "stopped" || facts.StopWhy == "" {
		t.Errorf("a stopped dry run: %+v %v", facts, err)
	}
	if err := b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) {
		ws.Grants = slices.DeleteFunc(ws.Grants, func(g registry.Grant) bool { return g.Target == ContainersCap })
	}); err != nil {
		t.Fatal(err)
	}

	calManifest, err := os.ReadFile(filepath.Join(b.Reg.Root, "apps/calendar/xbin.json"))
	if err != nil {
		t.Fatal(err)
	}
	nsWrite(t, b.Reg.Root, map[string]string{"apps/calendar/xbin.json": strings.Replace(string(calManifest), `{"runtime":"go",`, `{"runtime":"go","vm":true,`, 1)})
	if err := b.Reg.Rescan(); err != nil {
		t.Fatal(err)
	}
	_, err = f.seed(ana, seedReq(fxCalendar, "dev"))
	wantErr(t, err, 409, "apps/calendar runs main in a VM with file resources")
	if len(f.stops) > 0 {
		t.Errorf("a refused seed stopped %v", f.stops)
	}
}

// covers D127i D119h 08-data §8.2 §12 — what a seed refuses before it holds
// anything: a workspace-scope tile (its resources have one namespace), the
// primary as the target, an unknown deployment, a namespace another act
// holds (409 data busy, for the primary's too), a sealed vault, a primary
// over the target's disk limit, and a low disk.
func TestSeedRefusals(t *testing.T) {
	f := newSeedFx(t)
	b := f.b
	ana := deployPerson(t, b, fxAdmin)
	f.plane.set(t, b, fxChat, util.MainDeployment, map[string]string{util.MainDeployment: "", "dev": nsCheckpoint(t, "")})
	f.putKV(fxCalendar, util.MainDeployment, "events", "k", strings.Repeat("x", 4096))
	check := func(req deployments.SeedRequest, status int, why string) {
		t.Helper()
		_, err := f.seed(ana, req)
		wantErr(t, err, status, why)
	}
	check(seedReq(fxChat, "dev"), 409, "apps/chat is in the workspace scope, whose resources have one namespace")
	check(seedReq(fxCalendar, "main"), 409, "main is the primary of apps/calendar")
	check(seedReq(fxCalendar, "nope"), 404, `apps/calendar has no deployment "nope"`)
	check(seedReq(fxCalendar, "Dev"), 400, "deployment names are lowercase letters")
	for _, id := range []nsID{nsOf(fxCalendar, "dev"), nsOf(fxCalendar, util.MainDeployment)} {
		release, err := b.holdNS(id, nsResetting)
		if err != nil {
			t.Fatal(err)
		}
		check(seedReq(fxCalendar, "dev"), 409, id.dep+"'s data is being reset")
		release()
	}
	b.barrier.Seal()
	check(seedReq(fxCalendar, "dev"), 503, "vault is sealed")
	if err := b.barrier.Unseal("namespace-pass"); err != nil {
		t.Fatal(err)
	}
	old := b.disk
	b.disk = newDiskMon(b.Reg.Root, 1024, func() map[string]int64 { return nil })
	check(seedReq(fxCalendar, "dev"), 409, "over dev's disk limit (1.0KB)")
	b.disk = old
	seedDiskFree = func(string) (int64, int64) { return 90 << 30, 1000 << 30 }
	check(seedReq(fxCalendar, "dev"), 409, "the workspace disk is low")
	if exists(f.nsRoot(fxCalendar, "dev")) || len(f.stops) > 0 || len(f.runs) > 0 {
		t.Error("a refused seed made the namespace, stopped a deployment or ran a copy")
	}
}

// covers 08-data §8.3 — the blob walk visits regular files and directories
// only, parents first, each opened beneath its root: a symlink (to a file,
// to a directory, out of the tree) and a FIFO are skipped, never opened.
func TestSeedBlobWalkBeneath(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	nsWrite(t, root, map[string]string{"a": "1", "d/b": "22", "d/e/c": "333"})
	nsWrite(t, outside, map[string]string{"secret": "no"})
	for _, err := range []error{os.Symlink(filepath.Join(outside, "secret"), filepath.Join(root, "out")),
		os.Symlink("d", filepath.Join(root, "dlink")), os.Symlink("a", filepath.Join(root, "alink")),
		syscall.Mkfifo(filepath.Join(root, "d", "pipe"), 0o600)} {
		if err != nil {
			t.Fatal(err)
		}
	}
	var got []string
	if err := walkBeneath(root, func(rel string, f *os.File, fi fs.FileInfo) error {
		if f == nil {
			got = append(got, rel+"/")
			return nil
		}
		got = append(got, fmt.Sprintf("%s=%d", rel, fi.Size()))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sort.Strings(got)
	if want := []string{"a=1", "d/", "d/b=2", "d/e/", "d/e/c=3"}; !slices.Equal(got, want) {
		t.Errorf("walked %v, want %v", got, want)
	}
}
