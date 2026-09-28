package broker

// Backups of tile deployments' data (08-data §11; 05-model §11; 11-contract
// §1.8; 14-implementation WP-44a): deployment archives, the primary's on
// every backup, opt-in archives with their own retention, restores into a
// namespace, the registration files a main archive carries, and offload of
// every namespace. Built on deploy_env_test.go's zero-state workspace, whose
// apps/cal roots a scope with two kv resources and binds an archiver.

import (
	"archive/tar"
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/backup"
	"github.com/xbin-dev/xbin/internal/deployments"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/resenc"
	"github.com/xbin-dev/xbin/internal/util"
)

const bkTile = "apps/cal"

// memArchiver is an archiver tile that keeps what it is given, as the
// builtin s3 archiver does: versions per key, listed newest first, served
// back and deleted. fail names a key whose PUTs answer 502.
type memArchiver struct {
	mu   sync.Mutex
	n    int
	keys map[string][]memVersion // newest first
	puts []string                // keys, in PUT order
	fail string
}

type memVersion struct {
	v    string
	body []byte
}

func (a *memArchiver) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !auth.PrincipalOf(r).Owner {
		http.Error(w, "not the owner", http.StatusForbidden)
		return
	}
	rest, ok := strings.CutPrefix(r.URL.Path, "/api/apps/archiver/archive/")
	if !ok {
		http.Error(w, "no route", http.StatusNotFound)
		return
	}
	key, tail, _ := strings.Cut(rest, "/")
	vs := a.keys[key]
	switch v, _ := strings.CutPrefix(tail, "versions/"); {
	case r.Method == "PUT" && tail == "":
		if key == a.fail {
			http.Error(w, "bucket unreachable", http.StatusBadGateway)
			return
		}
		body, _ := io.ReadAll(r.Body)
		a.n++
		ver := fmt.Sprintf("v%03d", a.n)
		a.keys[key] = append([]memVersion{{ver, body}}, vs...)
		a.puts = append(a.puts, key)
		_, _ = fmt.Fprintf(w, `{"version":%q,"size":%d}`, ver, len(body))
	case r.Method == "GET" && tail == "versions":
		list := []map[string]any{}
		for _, x := range vs {
			list = append(list, map[string]any{"version": x.v, "time": "2026-09-27T10:00:00Z", "size": len(x.body)})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"versions": list})
	case r.Method == "GET" && v == "latest" && len(vs) > 0:
		_, _ = w.Write(vs[0].body)
	case r.Method == "GET" || r.Method == "DELETE":
		i := slices.IndexFunc(vs, func(x memVersion) bool { return x.v == v })
		switch {
		case i < 0:
			http.Error(w, "no such version", http.StatusNotFound)
		case r.Method == "GET":
			_, _ = w.Write(vs[i].body)
		default:
			a.keys[key] = slices.Delete(vs, i, i+1)
			_, _ = w.Write([]byte(`{"ok":true}`))
		}
	default:
		http.Error(w, "no route", http.StatusNotFound)
	}
}

// versions lists key's versions, newest first.
func (a *memArchiver) versions(key string) []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []string
	for _, x := range a.keys[key] {
		out = append(out, x.v)
	}
	return out
}

// latest is key's newest archive, read.
func (a *memArchiver) latest(t *testing.T, key string) (backup.Manifest, []archiveMember) {
	t.Helper()
	a.mu.Lock()
	vs := a.keys[key]
	a.mu.Unlock()
	if len(vs) == 0 {
		t.Fatalf("no archive under %s", key)
	}
	ms := readArchive(t, vs[0].body)
	var m backup.Manifest
	if len(ms) == 0 || ms[0].name != backup.ManifestName || json.Unmarshal(ms[0].body, &m) != nil {
		t.Fatalf("%s: backup.json isn't first", key)
	}
	return m, ms
}

// set stores body as key's newest version (a tampered archive).
func (a *memArchiver) set(key string, body []byte) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.n++
	v := fmt.Sprintf("v%03d", a.n)
	a.keys[key] = append([]memVersion{{v, body}}, a.keys[key]...)
	return v
}

// bkPlane stands in for the deployments plane: each tile's primary and its
// deployments beyond main, and their registration files in the plane's
// place, data/deployments/<TileKey>/<name>/.
type bkPlane struct {
	mu      sync.Mutex
	root    string
	primary map[string]string
	deps    map[string][]string
}

func (f *bkPlane) has(tile, dep string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return dep == util.MainDeployment || slices.Contains(f.deps[tile], dep)
}

func (f *bkPlane) setPrimary(tile, dep string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.primary[tile] = dep
}

func (f *bkPlane) regPath(tile, dep, file string) string {
	files, _ := deploymentFiles(tile, dep)
	return filepath.Join(f.root, filepath.FromSlash(files.Records), file)
}

func (f *bkPlane) install(b *Broker) {
	primary := func(tile string) string {
		f.mu.Lock()
		defer f.mu.Unlock()
		return cmp.Or(f.primary[tile], util.MainDeployment)
	}
	b.DeploymentAnswers = DeploymentAnswers{
		PrimaryOf: primary,
		DeploymentsOf: func(tile string) (string, []string) {
			f.mu.Lock()
			deps := slices.Clone(f.deps[tile])
			f.mu.Unlock()
			return primary(tile), append([]string{util.MainDeployment}, deps...)
		},
		ReadDeploymentFile: func(tile, dep, file string) ([]byte, error) { return os.ReadFile(f.regPath(tile, dep, file)) },
		WriteDeploymentFile: func(tile, dep, file string, data []byte) error {
			if dep == util.MainDeployment || !f.has(tile, dep) {
				return util.NoDeployment(tile, dep)
			}
			p := f.regPath(tile, dep, file)
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				return err
			}
			return os.WriteFile(p, data, 0o600)
		},
		RemoveDeploymentFile: func(tile, dep, file string) error {
			if err := os.Remove(f.regPath(tile, dep, file)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			return nil
		},
	}
	b.DeploymentHooks = DeploymentHooks{
		DeploymentExists: f.has,
		DeploymentCodeRoot: func(c *registry.Component, dep string) (string, bool, error) {
			return c.Dir, false, nil // every deployment follows the work tree
		},
	}
}

// bkFx is the fixture: zeroDataBroker with the stand-in plane, apps/cal
// holding a record (so its main archive has a deployment section) and
// deployments dev and qa, an archiver that keeps archives, and a vault, so
// kv values are sealed under each namespace's labels.
type bkFx struct {
	t     *testing.T
	b     *Broker
	root  string
	arch  *memArchiver
	plane *bkPlane
	ns    *nsFx // its kv and ns.json helpers
	stops []string
}

func newBkFx(t *testing.T, fakeVolumes bool) *bkFx {
	t.Helper()
	b := zeroDataBroker(t)
	b.Version = "test"
	root := b.Reg.Root
	f := &bkFx{t: t, b: b, root: root, arch: &memArchiver{keys: map[string][]memVersion{}},
		plane: &bkPlane{root: root, primary: map[string]string{}, deps: map[string][]string{bkTile: {"dev", "qa"}}}}
	b.ProxyHandler = f.arch
	f.plane.install(b)
	if fakeVolumes {
		nsFakeVolumes(t, b)
	} else {
		b.resenc = resenc.New(root, "", nil) // no gocryptfs: nothing mounts, even where XBIN_GOCRYPTFS is set
		if err := b.barrier.Init("backup-pass"); err != nil {
			t.Fatal(err)
		}
	}
	f.ns = &nsFx{t: t, b: b, root: root}
	writeTestRecord(t, root, bkTile, testRecord(bkTile, b.ownerRef(bkTile), ""))
	return f
}

func (f *bkFx) stop(tile, dep string) { f.stops = append(f.stops, tile+"+"+dep) }

// kv is dep's events resource: key → value, "" for none.
func (f *bkFx) kv(dep string, keys ...string) string {
	var out []string
	for _, k := range keys {
		out = append(out, k+"="+f.ns.getKV(bkTile, dep, "events", k))
	}
	return strings.Join(out, " ")
}

// dataMembers are an archive's data/ members.
func dataMembers(ms []archiveMember) map[string]string {
	out := map[string]string{}
	for _, m := range ms {
		if strings.HasPrefix(m.name, backup.DataPrefix) {
			out[m.name] = string(m.body)
		}
	}
	return out
}

// restoreReq is a POST /deployments/restore body.
func restoreReq(dep, into string, confirm bool) deployments.RestoreRequest {
	r := deployments.RestoreRequest{Tile: bkTile, Deployment: dep, Into: into}
	if confirm {
		r.Confirm = deployments.ConfirmEraseData
	}
	return r
}

// covers 05-model §11 D127c D127h PO-9 SC-AUDIT T11 — (M2) TestBackupCoversDeployments'
// deployment half: while main is the primary a tile's backup writes its main
// archive alone, with main's data only and the registration files of its
// other deployments under deployments/registrations/; with the primary
// reassigned to dev, every backup first archives dev's data, under
// .deployments.<TileKey>.dev at schema 2 (no source, terminal layer or
// cron and bus rows), and the main archive — still main's data, at schema
// 1 — lists that version. Other non-primary data is opt-in, under its own
// key, through the per-deployment backup act, and a scheduled backup prunes
// main's key and the primary's to the tile's retention, never another
// deployment's; a deployment's own schedule prunes only its key. Restoring
// the main archive restores both, dev's by replace; a deployment archive
// is never restored as a tile.
func TestBackupCoversDeploymentsM2(t *testing.T) {
	f := newBkFx(t, false)
	b := f.b
	f.ns.putKV(bkTile, util.MainDeployment, "events", "m", "main-1")
	f.ns.putKV(bkTile, "dev", "events", "d", "dev-1")
	f.ns.putKV(bkTile, "qa", "events", "q", "qa-1")
	if err := b.SetDeploymentBackupSchedule(bkTile, "qa", ptr("@every 24h"), ptr(1), false); err != nil {
		t.Fatal(err)
	}
	mainKey, devKey, qaKey := backupKey(bkTile), archiveKey(bkTile, "dev"), archiveKey(bkTile, "qa")
	if want := ".deployments." + util.TileKey(bkTile) + ".dev"; devKey != want || strings.Count(devKey, "/") != 0 {
		t.Fatalf("dev's archive key %q, want %q", devKey, want)
	}

	// main is the primary: the main archive alone, main's data, the others' files
	if _, err := b.doBackup(bkTile); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.arch.puts, []string{mainKey}) {
		t.Fatalf("with main the primary, a backup PUT %v", f.arch.puts)
	}
	m, ms := f.arch.latest(t, mainKey)
	if m.Schema != 1 || m.Deployment != "" || m.Deployments == nil || m.Deployments.Archives != nil {
		t.Errorf("main archive: schema %d, deployment %q, section %+v", m.Schema, m.Deployment, m.Deployments)
	}
	if kv := dataMembers(ms)[backup.KVName]; !strings.Contains(kv, `"m":`) || strings.Contains(kv, `"d":`) || strings.Contains(kv, `"q":`) {
		t.Errorf("the main archive's kv isn't main's alone: %s", kv)
	}
	var regs []string
	for _, x := range ms {
		if strings.HasPrefix(x.name, backup.RegistrationsPrefix) {
			regs = append(regs, x.name)
		}
	}
	if want := []string{backup.RegistrationsPrefix + "qa/backup-schedule.json"}; !reflect.DeepEqual(regs, want) {
		t.Errorf("registration members %v, want %v", regs, want)
	}

	// the primary is dev: dev's archive first, listed by the main archive
	f.plane.setPrimary(bkTile, "dev")
	f.arch.puts = nil
	if _, err := b.doBackup(bkTile); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.arch.puts, []string{devKey, mainKey}) {
		t.Fatalf("with dev the primary, a backup PUT %v, want dev's archive, then main's", f.arch.puts)
	}
	dm, dms := f.arch.latest(t, devKey)
	if dm.Schema != backup.SchemaDeployment || dm.Deployment != "dev" || dm.Component != bkTile || dm.Scope != bkTile ||
		!reflect.DeepEqual(dm.Includes, []string{"data"}) || dm.CronJobs != nil || dm.BusSubs != nil || dm.Deployments != nil {
		t.Errorf("dev's archive manifest: %+v", dm)
	}
	for _, x := range dms[1:] {
		if !strings.HasPrefix(x.name, backup.DataPrefix) {
			t.Errorf("dev's archive holds %s", x.name)
		}
	}
	if kv := dataMembers(dms)[backup.KVName]; !strings.Contains(kv, `"d":`) || strings.Contains(kv, `"m":`) {
		t.Errorf("dev's archive kv isn't dev's: %s", kv)
	}
	m, ms = f.arch.latest(t, mainKey)
	if m.Schema != 1 || m.Deployments == nil || !reflect.DeepEqual(m.Deployments.Archives, map[string]string{"dev": f.arch.versions(devKey)[0]}) {
		t.Errorf("the main archive doesn't list dev's: %+v", m.Deployments)
	}
	if kv := dataMembers(ms)[backup.KVName]; !strings.Contains(kv, `"m":`) || strings.Contains(kv, `"d":`) {
		t.Errorf("after the reassignment the main archive's kv isn't main's: %s", kv)
	}

	// opt-in: qa's data under its own key, on request
	ans, err := b.BackupDeploymentData(bkTile, "qa", false)
	if err != nil || ans.OK != "true" || ans.Deployment != "qa" || ans.Version != f.arch.versions(qaKey)[0] {
		t.Fatalf("qa's backup: %+v %v", ans, err)
	}
	if _, err := b.BackupDeploymentData(bkTile, "qa", false); err != nil {
		t.Fatal(err)
	}
	if ans, err := b.BackupDeploymentData(bkTile, "qa", true); err != nil || ans.Version != "" || len(f.arch.versions(qaKey)) != 2 {
		t.Errorf("a dry run archived: %+v %v", ans, err)
	}
	list, err := b.DeploymentBackups(bkTile, "qa")
	if err != nil || len(list.Versions) != 2 || list.Archiver != "apps/archiver" || list.Deployment != "qa" {
		t.Errorf("qa's backups: %+v %v", list, err)
	}

	// the tile's schedule prunes main's and dev's keys, never qa's
	b.runScheduledBackup(backupSchedule{Component: bkTile, Schedule: "@every 1h", Retention: 1})
	if len(f.arch.versions(mainKey)) != 1 || len(f.arch.versions(devKey)) != 1 || len(f.arch.versions(qaKey)) != 2 {
		t.Errorf("retention: main %v, dev %v, qa %v", f.arch.versions(mainKey), f.arch.versions(devKey), f.arch.versions(qaKey))
	}
	// qa's own schedule prunes qa's key only, to its own retention
	b.runDeploymentBackup(bkTile, "qa")
	if len(f.arch.versions(qaKey)) != 1 || len(f.arch.versions(mainKey)) != 1 {
		t.Errorf("qa's schedule: qa %v, main %v", f.arch.versions(qaKey), f.arch.versions(mainKey))
	}
	// dev's own schedule doesn't run while dev is the primary
	if err := b.SetDeploymentBackupSchedule(bkTile, "dev", ptr("@every 24h"), nil, false); err != nil {
		t.Fatal(err)
	}
	before := len(f.arch.puts)
	b.runDeploymentBackup(bkTile, "dev")
	if len(f.arch.puts) != before || b.DeploymentBackupSchedule(bkTile, "dev") != nil {
		t.Error("a primary's own schedule ran, or shows in the state")
	}

	// restoring the main archive restores both; dev's by replace
	f.ns.putKV(bkTile, util.MainDeployment, "events", "m", "main-changed")
	f.ns.putKV(bkTile, "dev", "events", "d", "dev-changed")
	f.ns.putKV(bkTile, "dev", "events", "stale", "x")
	mm, listed, err := b.restoreTile(bkTile, "")
	if err != nil || mm.Component != bkTile {
		t.Fatalf("restore: %v", err)
	}
	if listed == nil || !reflect.DeepEqual(listed.Restored, []string{"dev"}) || len(listed.Skipped) != 0 {
		t.Errorf("the listed archives: %+v", listed)
	}
	if got := f.kv("dev", "d", "stale") + " " + f.kv(util.MainDeployment, "m"); got != "d=dev-1 stale= m=main-1" {
		t.Errorf("after the restore: %s", got)
	}
	if st := f.ns.meta(bkTile, "dev"); st.State != nsRestored || st.From != "dev" {
		t.Errorf("dev's data state after the restore: %+v", st)
	}

	// a deployment archive is never restored as a tile
	_, dmsBody := f.arch.latest(t, devKey)
	if _, err := b.restore(bkTile, bytes.NewReader(writeArchive(t, dmsBody)), nil); err == nil || !strings.Contains(err.Error(), "POST /deployments/restore") {
		t.Errorf("a deployment archive restored as a tile: %v", err)
	}
	// ... and an older xbind, which reads schema 1 at most, refuses it
	if dm.Schema <= backup.Schema || m.Schema != backup.Schema {
		t.Errorf("schemas: deployment archive %d, main archive %d", dm.Schema, m.Schema)
	}
}

func ptr[T any](v T) *T { return &v }

// covers D119c SC-ZERO PO-9 Z6 — (08-data §14) a zero-state tile's main
// archive is today's, byte for byte, modulo the manifest's creation time:
// with and without a leftover checkpoint store, as a tar of exactly its
// members with today's headers (name, mode, size, regular file) — no mode
// or time beyond them, schema 1, no deployment fields — and POST /restore
// of it answers today's keys.
func TestZeroStateBackupBytes(t *testing.T) {
	b := zeroDataBroker(t)
	b.Version = "test"
	root := b.Reg.Root
	arch := &memArchiver{keys: map[string][]memVersion{}}
	b.ProxyHandler = arch
	ns := &nsFx{t: t, b: b, root: root}
	ns.putKV(bkTile, util.MainDeployment, "events", "k1", "v1")
	ns.putKV(bkTile, util.MainDeployment, "my-cache", "k2", "v2")
	created := regexp.MustCompile(`"created": "[^"]*"`)
	backupBytes := func() []byte {
		t.Helper()
		if _, err := b.doBackup(bkTile); err != nil {
			t.Fatal(err)
		}
		arch.mu.Lock()
		defer arch.mu.Unlock()
		return created.ReplaceAll(arch.keys[backupKey(bkTile)][0].body, []byte(`"created": "2026-01-01T00:00:00Z"`))
	}
	base := backupBytes()
	if len(arch.puts) != 1 || arch.puts[0] != backupKey(bkTile) {
		t.Fatalf("a zero-state backup PUT %v", arch.puts)
	}

	// the same bytes as today's writer: each member, today's header only
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	tr := tar.NewReader(bytes.NewReader(base))
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(tr)
		if h.Name == backup.ManifestName {
			var m map[string]any
			if err := json.Unmarshal(body, &m); err != nil || m["schema"] != float64(1) || m["deployment"] != nil || m["deployments"] != nil {
				t.Errorf("the manifest isn't today's: %s", body)
			}
		}
		if err := tw.WriteHeader(&tar.Header{Name: h.Name, Mode: h.Mode, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		_, _ = tw.Write(body)
	}
	_ = tw.Close()
	if !bytes.Equal(buf.Bytes(), base) {
		t.Error("the zero-state archive carries more than today's headers")
	}

	// a leftover checkpoint store, no record: the same bytes
	store := checkpointStoreDir(root, bkTile)
	if err := os.MkdirAll(filepath.Join(store, "objects", "ab"), 0o755); err != nil {
		t.Fatal(err)
	}
	for rel, body := range map[string]string{"HEAD": "ref: refs/heads/deploy/main\n", "objects/ab/" + strings.Repeat("c", 38): "x"} {
		if err := os.WriteFile(filepath.Join(store, filepath.FromSlash(rel)), []byte(body), 0o444); err != nil {
			t.Fatal(err)
		}
	}
	if got := backupBytes(); !bytes.Equal(got, base) {
		t.Error("a leftover checkpoint store changed the zero-state archive")
	}

	// POST /restore of it: today's answer, no deployments key
	testUsers(t, b)
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/xbin/restore", strings.NewReader(`{"component":"apps/cal"}`))
	b.apiRestore(w, r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{Owner: true})))
	var ans map[string]any
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &ans) != nil {
		t.Fatalf("restore: %d %s", w.Code, w.Body.String())
	}
	keys := zeroDataKeys(ans)
	if want := []string{"component", "ok", "restored"}; !reflect.DeepEqual(keys, want) {
		t.Errorf("POST /restore answered %v, want today's %v", keys, want)
	}
}

// covers T11 D127h PO-9 — (06-security T11.5; 08-data §11.4) a main
// archive's registration files come back only into the deployments that
// exist, row by row through the checks their routes make: a job on an
// undeclared resource, one with a bad schedule and a subscription with a
// bad name are left out; a row's own component is ignored (it registers
// for the tile and the file's deployment); rows merge by name into what the
// deployment holds; a deployment the tile doesn't have, main's directory,
// a file of another schema and one this xbind doesn't read restore
// nothing; the restored jobs are scheduled.
func TestRestoreRevalidatesRegistrations(t *testing.T) {
	f := newBkFx(t, false)
	b := f.b
	// what dev holds already
	if err := b.cron.rewriteDep(bkTile, "dev", func([]depCronRow) ([]depCronRow, error) {
		return []depCronRow{{Name: "kept", Resource: "res:apps/cal/ticks", Schedule: "@every 3h", Path: "/kept", Role: "writer"}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	cronFile := `{"schema":1,"jobs":[
		{"name":"j1","resource":"res:apps/cal/ticks","schedule":"@every 1h","path":"/t1"},
		{"name":"j2","resource":"res:apps/cal/ticks","schedule":"@every 2h","path":"/t2","component":"apps/mail","role":"reader"},
		{"name":"nope","resource":"res:apps/mail/nope","schedule":"@every 1h","path":"/n"},
		{"name":"bad","resource":"res:apps/cal/ticks","schedule":"every day","path":"/b"}]}`
	busFile := `{"schema":1,"subscriptions":[
		{"name":"s1","resource":"res:apps/cal/bus","prefix":"cal.","path":"/on"},
		{"name":"bad name!","resource":"res:apps/cal/bus","path":"/x"}]}`
	rec := testRecord(bkTile, b.ownerRef(bkTile), "")
	archive := writeArchive(t, []archiveMember{
		manifestJSON(t, backup.Manifest{Component: bkTile, Scope: bkTile, ScopeRoot: true, Includes: []string{"source"},
			Deployments: &backup.Deployments{Record: true}}),
		{backup.RecordName, 0o600, rec},
		{backup.RegistrationsPrefix + "dev/cron.json", 0o600, []byte(cronFile)},
		{backup.RegistrationsPrefix + "dev/bus-subscriptions.json", 0o600, []byte(busFile)},
		{backup.RegistrationsPrefix + "dev/ingress-hosts.json", 0o600, []byte(`{"schema":1,"hosts":["x.example.com"]}`)},
		{backup.RegistrationsPrefix + "qa/cron.json", 0o600, []byte(`{"schema":2,"jobs":[]}`)},
		{backup.RegistrationsPrefix + "gone/cron.json", 0o600, []byte(cronFile)},
		{backup.RegistrationsPrefix + "main/cron.json", 0o600, []byte(cronFile)},
		{"source/xbin.json", 0o644, []byte(`{"runtime":"go"}`)},
	})
	f.plane.mu.Lock()
	f.plane.deps[bkTile] = []string{"dev", "qa"}
	f.plane.mu.Unlock()
	if _, err := b.restore(bkTile, bytes.NewReader(archive), nil); err != nil {
		t.Fatal(err)
	}

	var cronDoc depCronDoc
	if err := b.readDepFile(bkTile, "dev", depCronFile, &cronDoc); err != nil {
		t.Fatal(err)
	}
	var jobs []string
	for _, j := range cronDoc.Jobs {
		jobs = append(jobs, fmt.Sprintf("%s %s %s %s", j.Name, j.Resource, j.Schedule, j.Role))
	}
	want := []string{"j1 res:apps/cal/ticks @every 1h writer", "j2 res:apps/cal/ticks @every 2h reader", "kept res:apps/cal/ticks @every 3h writer"}
	if !reflect.DeepEqual(jobs, want) {
		t.Errorf("dev's jobs after the restore:\n got %q\nwant %q", jobs, want)
	}
	raw, _ := os.ReadFile(f.plane.regPath(bkTile, "dev", depCronFile))
	if bytes.Contains(raw, []byte("component")) || bytes.Contains(raw, []byte("apps/mail")) {
		t.Errorf("a restored row kept its own component: %s", raw)
	}
	b.cron.mu.Lock()
	dj := b.cron.dep[depKey(bkTile, "dev", "j2")]
	b.cron.mu.Unlock()
	if dj == nil || dj.job.Component != bkTile || dj.dep != "dev" {
		t.Errorf("the restored job isn't scheduled as the tile's dev job: %+v", dj)
	}
	var busDoc depBusDoc
	if err := b.readDepFile(bkTile, "dev", depBusFile, &busDoc); err != nil {
		t.Fatal(err)
	}
	if len(busDoc.Subscriptions) != 1 || busDoc.Subscriptions[0].Name != "s1" || busDoc.Subscriptions[0].Prefix != "cal." {
		t.Errorf("dev's subscriptions after the restore: %+v", busDoc.Subscriptions)
	}
	for _, p := range []string{f.plane.regPath(bkTile, "dev", "ingress-hosts.json"), f.plane.regPath(bkTile, "qa", depCronFile),
		f.plane.regPath(bkTile, "gone", depCronFile)} {
		if exists(p) {
			t.Errorf("restored %s", p)
		}
	}
	if raw, _ := os.ReadFile(filepath.Join(f.root, "data", "cron-jobs.json")); bytes.Contains(raw, []byte(`"j1"`)) {
		t.Errorf("main's store took a deployment's row: %s", raw)
	}
}

// covers D127c D127i D127t T11 — (08-data §11.4, §14's restore rows; 11-contract
// §1.8) POST /deployments/restore's act: beyond main it replaces (a key the
// archive doesn't hold goes) and re-seals every value under the target's
// labels, so an archive restores into another deployment too; main merges
// unless replace is asked; the data state becomes restored. Before any
// write it refuses a target that doesn't exist (listing the choices), a
// target holding data without confirm, an archive naming another tile or
// of an unknown schema, and a held namespace. A dry run writes nothing.
func TestDeploymentRestoreReplaces(t *testing.T) {
	f := newBkFx(t, false)
	b := f.b
	f.ns.putKV(bkTile, "dev", "events", "a", "dev-a")
	f.ns.putKV(bkTile, "dev", "my-cache", "c", "dev-c")
	f.ns.putKV(bkTile, util.MainDeployment, "events", "m", "main-m")
	if _, err := b.BackupDeploymentData(bkTile, "dev", false); err != nil {
		t.Fatal(err)
	}
	if _, err := b.doBackup(bkTile); err != nil {
		t.Fatal(err)
	}
	f.ns.putKV(bkTile, "dev", "events", "a", "dev-a2")
	f.ns.putKV(bkTile, "dev", "events", "stale", "x")

	// refusals, before anything is written
	restore := func(req deployments.RestoreRequest) (deployments.RestoreAnswer, []string, error) {
		return b.RestoreDeploymentData(bkTile, "user:ana", req, allow, f.stop)
	}
	_, _, err := restore(restoreReq("dev", "nope", true))
	wantErr(t, err, http.StatusConflict, `has no deployment "nope" to restore into: its deployments are main, dev, qa`)
	_, _, err = restore(restoreReq("dev", "", false))
	wantErr(t, err, http.StatusBadRequest, `send confirm:"erase-data" to proceed`)
	_, _, err = restore(restoreReq("qa", "", true))
	wantErr(t, err, http.StatusNotFound, "has no archive of qa's data")
	dry := restoreReq("dev", "", true)
	dry.DryRun = true
	if ans, stops, err := restore(dry); err != nil || ans.Restored == "" || !reflect.DeepEqual(stops, []string{bkTile}) || len(f.stops) != 0 {
		t.Fatalf("dry run: %+v %v %v, stopped %v", ans, stops, err, f.stops)
	}
	foreign := func(m backup.Manifest) {
		t.Helper()
		f.arch.set(archiveKey(bkTile, "dev"), writeArchive(t, []archiveMember{manifestJSON(t, m),
			{backup.KVName, 0o644, []byte(`{"events":{"a":"aGFjaw=="}}`)}}))
		_, _, err := restore(restoreReq("dev", "", true))
		wantErr(t, err, http.StatusConflict, "nothing was restored")
	}
	foreign(backup.Manifest{Schema: 2, Component: "apps/mail", Deployment: "dev", Scope: "apps/mail", ScopeRoot: true})
	foreign(backup.Manifest{Schema: 2, Component: bkTile, Deployment: "qa", Scope: bkTile, ScopeRoot: true})
	foreign(backup.Manifest{Schema: 1, Component: bkTile, Scope: bkTile, ScopeRoot: true})
	if got := f.kv("dev", "a", "stale"); got != "a=dev-a2 stale=x" || len(f.stops) != 0 {
		t.Fatalf("a refused restore wrote %s, stopped %v", got, f.stops)
	}
	release, err := b.holdNS(nsOf(bkTile, "dev"), nsSeeding)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = restore(restoreReq("dev", "", true))
	wantErr(t, err, http.StatusConflict, "dev's data is being seeded")
	release()

	// dev's archive into dev: replaced, restored, every claimant stopped
	good := f.arch.versions(archiveKey(bkTile, "dev"))[len(f.arch.versions(archiveKey(bkTile, "dev")))-1]
	req := restoreReq("dev", "", true)
	req.Version = good
	ans, _, err := restore(req)
	if err != nil || ans.Into != "dev" || ans.Deployment != "dev" || ans.Restored != good || len(ans.Skipped) != 0 {
		t.Fatalf("restore dev: %+v %v", ans, err)
	}
	if got := f.kv("dev", "a", "stale"); got != "a=dev-a stale=" || f.ns.getKV(bkTile, "dev", "my-cache", "c") != "dev-c" {
		t.Errorf("dev after the restore: %s", got)
	}
	if st := f.ns.meta(bkTile, "dev"); st.State != nsRestored || st.From != "dev" || st.By != "user:ana" || st.Busy != "" {
		t.Errorf("dev's data state: %+v", st)
	}
	if !reflect.DeepEqual(f.stops, []string{bkTile + "+dev"}) {
		t.Errorf("stopped %v", f.stops)
	}

	// into another deployment: sealed under qa's labels
	req.Into = "qa"
	if _, _, err := restore(req); err != nil {
		t.Fatal(err)
	}
	if got := f.kv("qa", "a"); got != "a=dev-a" {
		t.Errorf("qa after restoring dev's archive into it: %s", got)
	}

	// the main archive's data into main: merged unless replace
	f.ns.putKV(bkTile, util.MainDeployment, "events", "m", "main-m2")
	f.ns.putKV(bkTile, util.MainDeployment, "events", "extra", "e")
	mainReq := restoreReq(util.MainDeployment, "", false)
	_, _, err = restore(mainReq)
	wantErr(t, err, http.StatusBadRequest, "restoring into main overwrites")
	mainReq.Confirm = deployments.ConfirmEraseData
	if _, _, err := restore(mainReq); err != nil {
		t.Fatal(err)
	}
	if got := f.kv(util.MainDeployment, "m", "extra"); got != "m=main-m extra=e" {
		t.Errorf("main merged: %s", got)
	}
	mainReq.Replace = ptr(true)
	if _, _, err := restore(mainReq); err != nil {
		t.Fatal(err)
	}
	if got := f.kv(util.MainDeployment, "m", "extra"); got != "m=main-m extra=" {
		t.Errorf("main replaced: %s", got)
	}
	// and into dev: the data part of the main archive, never its files
	if _, _, err := restore(restoreReq(util.MainDeployment, "dev", true)); err != nil {
		t.Fatal(err)
	}
	if got := f.kv("dev", "m", "a"); got != "m=main-m a=" {
		t.Errorf("dev after restoring main's archive into it: %s", got)
	}
	if _, err := os.Stat(filepath.Join(f.root, "apps", "cal", "xbin.json")); err != nil {
		t.Error(err)
	}
}

// covers 05-model §11 D119c — (08-data §11.5, §14's offload rows) offloading
// a tile archives every namespace of its deployments first, then its main
// archive, which lists them; only after every PUT is confirmed does it
// remove main's data, as today, and each other namespace's kv file. A
// failed PUT removes nothing. Re-enabling restores the main archive, then
// each archive it lists.
func TestOffloadCoversNamespaces(t *testing.T) {
	f := newBkFx(t, false)
	b := f.b
	f.ns.putKV(bkTile, util.MainDeployment, "events", "m", "main-1")
	f.ns.putKV(bkTile, "dev", "events", "d", "dev-1")
	f.ns.putKV(bkTile, "qa", "events", "q", "qa-1")
	devKV := filepath.Join(f.root, filepath.FromSlash(nsKVFile(".deployments/"+escS(bkTile)+"/dev")))
	if !exists(devKV) {
		t.Fatalf("no kv file at %s", devKV)
	}
	mainKey, devKey, qaKey := backupKey(bkTile), archiveKey(bkTile, "dev"), archiveKey(bkTile, "qa")

	f.arch.fail = qaKey
	if err := b.offload(bkTile, false); err == nil || !strings.Contains(err.Error(), "nothing removed") {
		t.Fatalf("offload with a failing PUT: %v", err)
	}
	if !exists(devKV) || f.kv(util.MainDeployment, "m") != "m=main-1" || len(f.arch.versions(mainKey)) != 0 {
		t.Fatal("a failed offload removed data, or wrote the main archive")
	}

	f.arch.fail, f.arch.puts = "", nil
	if err := b.offload(bkTile, false); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.arch.puts, []string{devKey, qaKey, mainKey}) {
		t.Errorf("offload PUT %v, want every namespace first, then the main archive", f.arch.puts)
	}
	m, _ := f.arch.latest(t, mainKey)
	if m.Deployments == nil || len(m.Deployments.Archives) != 2 {
		t.Errorf("the main archive lists %+v", m.Deployments)
	}
	if exists(devKV) || f.kv("dev", "d")+f.kv("qa", "q")+f.kv(util.MainDeployment, "m") != "d=q=m=" {
		t.Error("offload left data behind")
	}

	if _, err := b.doRestore(bkTile, ""); err != nil {
		t.Fatal(err)
	}
	if got := f.kv("dev", "d") + " " + f.kv("qa", "q") + " " + f.kv(util.MainDeployment, "m"); got != "d=dev-1 q=qa-1 m=main-1" {
		t.Errorf("re-enabled: %s", got)
	}
}

// covers D127c — (08-data §11.3; 11-contract §1.8) a deployment's own backup
// schedule lives in its registration directory, through the plane's file
// hooks, with retention 3 unless told; "" removes it; main's is today's
// row in data/backup-schedule.json. A bad schedule or retention is a 400,
// a deployment the tile doesn't have a 404, and a dry run writes nothing.
// Boot's load schedules what the files hold.
func TestDeploymentBackupSchedule(t *testing.T) {
	f := newBkFx(t, false)
	b := f.b
	path := f.plane.regPath(bkTile, "qa", depBackupFile)
	wantErr(t, b.SetDeploymentBackupSchedule(bkTile, "qa", ptr("every day"), nil, false), http.StatusBadRequest, "bad schedule")
	wantErr(t, b.SetDeploymentBackupSchedule(bkTile, "qa", ptr("@daily"), ptr(-1), false), http.StatusBadRequest, "retention")
	wantErr(t, b.SetDeploymentBackupSchedule(bkTile, "nope", ptr("@daily"), nil, false), http.StatusNotFound, `has no deployment "nope"`)
	wantErr(t, b.SetDeploymentBackupSchedule(bkTile, "qa", nil, nil, false), http.StatusBadRequest, "send schedule")
	if err := b.SetDeploymentBackupSchedule(bkTile, "qa", ptr("@daily"), nil, true); err != nil || exists(path) {
		t.Fatalf("dry run: %v, file %v", err, exists(path))
	}
	if err := b.SetDeploymentBackupSchedule(bkTile, "qa", ptr("0 3 * * *"), nil, false); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	var doc map[string]any
	if json.Unmarshal(raw, &doc) != nil || doc["schema"] != float64(1) || doc["schedule"] != "0 3 * * *" || doc["retention"] != float64(3) {
		t.Errorf("qa's schedule file: %s", raw)
	}
	if got := b.DeploymentBackupSchedule(bkTile, "qa"); got == nil || *got != (deployments.BackupSchedule{Schedule: "0 3 * * *", Retention: 3}) {
		t.Errorf("qa's backup state: %+v", got)
	}
	if raw, _ := os.ReadFile(filepath.Join(f.root, "data", "backup-schedule.json")); bytes.Contains(raw, []byte("qa")) {
		t.Errorf("today's schedule store took a deployment's row: %s", raw)
	}
	entries := func() int {
		v, ok := depBackupEntries.Load(b.cron)
		if !ok {
			return 0
		}
		tab := v.(*depBackupTab)
		tab.mu.Lock()
		defer tab.mu.Unlock()
		return len(tab.entries)
	}
	if entries() != 1 {
		t.Errorf("%d deployment schedules running", entries())
	}
	depBackupEntries.Delete(b.cron) // as after a restart
	b.LoadDeploymentBackupSchedules()
	if entries() != 1 {
		t.Error("boot's load didn't schedule qa's backups")
	}
	if err := b.SetDeploymentBackupSchedule(bkTile, "qa", ptr(""), nil, false); err != nil || exists(path) || entries() != 0 {
		t.Errorf("removing qa's schedule: %v, file %v, %d running", err, exists(path), entries())
	}
	// main's is the tile's: today's row
	if err := b.SetDeploymentBackupSchedule(bkTile, util.MainDeployment, ptr("@daily"), ptr(5), false); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(filepath.Join(f.root, "data", "backup-schedule.json"))
	var rows []backupSchedule
	if json.Unmarshal(raw, &rows) != nil || !reflect.DeepEqual(rows, []backupSchedule{{Component: bkTile, Schedule: "@daily", Retention: 5}}) {
		t.Errorf("main's schedule: %s", raw)
	}
}

// covers D119g T2 T11 — (08-data §11.6) rule C5 for a deployment archive and
// its restore, behaviourally: dev's filesystem volume holding a symlink to
// the FIFO, a FIFO of its own and a symlinked directory is archived without
// anything opening them — only its regular files go in, their modes kept —
// and a restore of that archive replaces the volume: a file the archive
// doesn't hold goes. The hostile-tree case the integrator wires into
// internal/confine's TestNoFollowingHostWalks.
func TestDeploymentArchiveNoFollow(t *testing.T) {
	w := newBackupTripwire(t)
	f := newBkFx(t, true)
	b := f.b
	k := f.ns.keys(bkTile, "dev", "files")
	if !b.ensureVolume(k, bkTile, "filesystem") {
		t.Fatal("the fake volume didn't mount")
	}
	mount := b.resMount(k, false)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("s"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(mount, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	for rel, mode := range map[string]os.FileMode{"a.txt": 0o640, "sub/run.sh": 0o755} {
		if err := os.WriteFile(filepath.Join(mount, rel), []byte(rel), mode); err != nil {
			t.Fatal(err)
		}
		_ = os.Chmod(filepath.Join(mount, rel), mode)
	}
	if err := os.Symlink(w.path, filepath.Join(mount, "evil")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(mount, "out")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(mount, "sub", "fifo"), 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := b.BackupDeploymentData(bkTile, "dev", false); done <- err }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the backup hung: it opened a FIFO")
	}
	_, ms := f.arch.latest(t, archiveKey(bkTile, "dev"))
	var got []string
	for _, m := range ms {
		if rel, ok := strings.CutPrefix(m.name, backup.FSPrefix+"files/"); ok {
			got = append(got, fmt.Sprintf("%s %o", rel, m.mode))
		}
	}
	sort.Strings(got)
	if want := []string{".fake-mounted 644", "a.txt 640", "sub/run.sh 755"}; !reflect.DeepEqual(got, want) {
		t.Errorf("dev's volume archived %v, want %v", got, want)
	}
	if w.tripped.Load() {
		t.Fatal("the backup opened the FIFO outside the volume")
	}

	// the restore replaces the volume
	if err := os.WriteFile(filepath.Join(mount, "new.txt"), []byte("n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := b.RestoreDeploymentData(bkTile, "user:ana", restoreReq("dev", "", true), allow, f.stop); err != nil {
		t.Fatal(err)
	}
	mount = b.resMount(k, false)
	if exists(filepath.Join(mount, "new.txt")) || exists(filepath.Join(mount, "evil")) || !exists(filepath.Join(mount, "sub", "run.sh")) {
		t.Error("the restore merged into dev's volume instead of replacing it")
	}
	if body, _ := os.ReadFile(filepath.Join(mount, "a.txt")); string(body) != "a.txt" {
		t.Errorf("a.txt after the restore: %q", body)
	}
}

// covers D119c PO-7 — (08-data §14's dry-run row, for this card's acts) on a
// zero-state tile, dry runs of the per-deployment backup, restore and
// schedule acts write nothing: no archive, no namespace, no ns.json, no
// schedule file or row.
func TestBackupActsDryRunCreateNothing(t *testing.T) {
	b := zeroDataBroker(t)
	root := b.Reg.Root
	arch := &memArchiver{keys: map[string][]memVersion{}}
	b.ProxyHandler = arch
	b.resenc = resenc.New(root, "", nil)
	if _, err := b.doBackup(bkTile); err != nil {
		t.Fatal(err)
	}
	before := listTree(t, root)
	if _, err := b.BackupDeploymentData(bkTile, util.MainDeployment, true); err != nil {
		t.Fatal(err)
	}
	req := deployments.RestoreRequest{Tile: bkTile, Deployment: util.MainDeployment, Confirm: deployments.ConfirmEraseData, DryRun: true}
	if _, stops, err := b.RestoreDeploymentData(bkTile, "user:ana", req, allow, func(string, string) { t.Error("a dry run stopped a deployment") }); err != nil ||
		!reflect.DeepEqual(stops, []string{bkTile, bkTile + "/widget"}) {
		t.Fatalf("restore dry run: %v %v", stops, err)
	}
	if err := b.SetDeploymentBackupSchedule(bkTile, util.MainDeployment, ptr("@daily"), nil, true); err != nil {
		t.Fatal(err)
	}
	if after := listTree(t, root); !reflect.DeepEqual(after, before) || len(arch.puts) != 1 {
		t.Errorf("dry runs changed the workspace (%d PUTs):\n before %v\n after  %v", len(arch.puts), before, after)
	}
}

// listTree lists every path under root, for a before/after comparison.
func listTree(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err == nil {
			rel, _ := filepath.Rel(root, p)
			out = append(out, rel)
		}
		return nil
	})
	return out
}
