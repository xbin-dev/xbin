package broker

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/backup"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/resenc"
	"github.com/xbin-dev/xbin/internal/users"
	"github.com/xbin-dev/xbin/internal/util"
)

// sealedBroker is a broker over a workspace with one tile and one sealed
// component, xbin/fix: a kv resource, one declared slot, a table grant.
func sealedBroker(t *testing.T) *Broker {
	t.Helper()
	b := sealedBrokerNoBarrier(t)
	// a vault barrier: its archives are sealed, as a sealed component's
	// must be (no gocryptfs: nothing mounts)
	b.resenc = resenc.New(b.Reg.Root, "", nil)
	if err := b.barrier.Init("sealed-pass"); err != nil {
		t.Fatal(err)
	}
	return b
}

func sealedBrokerNoBarrier(t *testing.T) *Broker {
	t.Helper()
	root := t.TempDir()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.WriteFile(filepath.Join(root, "xbin.json"), []byte(`{"schema":1}`), 0o644))
	must(os.MkdirAll(filepath.Join(root, "apps", "plain"), 0o755))
	must(os.WriteFile(filepath.Join(root, "apps", "plain", "index.html"), []byte("<html></html>"), 0o644))
	dir := filepath.Join(root, ".xbin", "sealed", "fix", "0123456789abcdef01234567")
	must(os.MkdirAll(dir, 0o755))
	must(os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html>sealed</html>"), 0o444))
	code := registry.SealedCode{Path: "xbin/fix", Dir: dir,
		Manifest: []byte(`{"uses":[{"target":"res:xbin/fix/state","role":"writer"},{"target":"apps/plain","role":"reader"}],
			"interfaces":{"llm":{"kind":"http","service":"openai"}}}`),
		Scope:  []byte(`{"resources":{"state":{"type":"kv"}}}`),
		Slots:  []string{"llm"},
		Grants: []registry.Use{{Target: "cap:open-links", Role: "writer"}}}
	reg, err := registry.OpenSealed(root, func() []registry.SealedCode { return []registry.SealedCode{code} })
	must(err)
	b, err := New(reg, events.NewHub(), false)
	must(err)
	st, err := users.Open(t.TempDir())
	must(err)
	b.Users = st
	b.Sealed = SealedHooks{
		DataFormat: func(string) int { return 3 },
		RestoreRefusal: func(_ string, data int, release string) error {
			if data > 3 {
				return errors.New("this backup can't be restored here: written by xbin " + release)
			}
			return nil
		},
	}
	return b
}

// putKV sets key k of the sealed component's kv resource (as a restore
// writes one); getKV reads it back.
func putKV(t *testing.T, b *Broker, v string) {
	t.Helper()
	body := `{"state":{"k":"` + base64.StdEncoding.EncodeToString([]byte(v)) + `"}}`
	if err := b.loadKV("xbin/fix", []byte(body)); err != nil {
		t.Fatal(err)
	}
}

func getKV(t *testing.T, b *Broker) string {
	t.Helper()
	rk, err := b.resKeys(resTarget{Scope: "xbin/fix", Name: "state"}, util.MainDeployment)
	if err != nil {
		t.Fatal(err)
	}
	v, _ := base64.StdEncoding.DecodeString(b.dumpKV(rk)["k"])
	return string(v)
}

func archiveOf(t *testing.T, b *Broker, comp string) (backup.Manifest, []string, []byte) {
	t.Helper()
	c, ok := b.Reg.Component(comp)
	if !ok {
		t.Fatalf("no component %s", comp)
	}
	var buf bytes.Buffer
	bw := backup.NewWriter(&buf)
	if err := b.writeBackup(bw, c, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := bw.Close(); err != nil {
		t.Fatal(err)
	}
	br, err := backup.NewReader(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for {
		name, _, err := br.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	return br.M, names, buf.Bytes()
}

// sealedTar seals a plain archive under b's backup key for s, as a backup
// by xbind is.
func sealedTar(t *testing.T, b *Broker, s archiveSeal, plain []byte) []byte {
	t.Helper()
	id, subkey, err := b.backupKeys().subkeyFor(s.subject, s.tile)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	sw, err := backup.NewSealedWriter(&buf, subkey, id, s.kind)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sw.Write(plain); err != nil {
		t.Fatal(err)
	}
	if err := sw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// plainTar builds an archive from a manifest and members, as an archiver
// (or anyone) can.
func plainTar(t *testing.T, m backup.Manifest, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := backup.NewWriter(&buf)
	if err := w.Manifest(m); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		if err := w.File(name, 0o644, []byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// A sealed component is restored only from an archive xbind sealed itself:
// the archiver is a tile, and a plain tar is its word alone. Nothing of a
// refused archive is written, a member is never read out of its backup, and
// an archive carries no deployment record for it.
func TestSealedRestoreTakesOnlyXbindsOwnArchives(t *testing.T) {
	b := sealedBroker(t)
	putKV(t, b, "theirs")
	m, _, own := archiveOf(t, b, "xbin/fix")
	kv := func(v string) string { return `{"state":{"k":"` + base64.StdEncoding.EncodeToString([]byte(v)) + `"}}` }
	forged := plainTar(t, m, map[string]string{backup.KVName: kv("forged")})

	refused := func(what string, arch []byte, want string) {
		t.Helper()
		_, err := b.restore("xbin/fix", bytes.NewReader(arch), nil)
		var se *sealedError
		if err == nil || !errors.As(err, &se) || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v\n\twant a sealed refusal: …%s…", what, err, want)
		}
		if got := getKV(t, b); got != "theirs" {
			t.Errorf("%s: its data is now %q", what, got)
		}
	}
	// a plain archive — this very component's own backup, unsealed, and one
	// an archiver made up
	refused("its own archive as a plain tar", own, "this archive is a plain one")
	refused("a forged plain archive", forged, "this archive is a plain one")
	// sealed, but with a deployment record
	dm := m
	dm.Deployments = &backup.Deployments{}
	refused("a sealed archive with a deployment record", sealedTar(t, b, mainSeal("xbin/fix"), plainTar(t, dm, map[string]string{backup.KVName: kv("x")})), "it has no deployments")
	// sealed, with data newer than this release reads
	nm := m
	nm.SealedData, nm.XBinVersion = 4, "v9"
	if _, err := b.restore("xbin/fix", bytes.NewReader(sealedTar(t, b, mainSeal("xbin/fix"), plainTar(t, nm, map[string]string{backup.KVName: kv("x")}))), nil); err == nil || !strings.Contains(err.Error(), "v9") {
		t.Errorf("a sealed archive of newer data: %v", err)
	}
	// sealed, but another component's
	om := m
	om.Component, om.Scope = "apps/plain", "apps/plain"
	if _, err := b.restore("xbin/fix", bytes.NewReader(sealedTar(t, b, mainSeal("xbin/fix"), plainTar(t, om, map[string]string{backup.KVName: kv("x")}))), nil); err == nil {
		t.Error("a sealed archive of another component restored into it")
	}
	if got := getKV(t, b); got != "theirs" {
		t.Errorf("after the refused restores its data is %q", got)
	}
	// the one that restores: sealed under this workspace's key, its own
	putKV(t, b, "changed")
	if _, err := b.restore("xbin/fix", bytes.NewReader(sealedTar(t, b, mainSeal("xbin/fix"), own)), nil); err != nil {
		t.Fatalf("its own sealed archive: %v", err)
	}
	if got := getKV(t, b); got != "theirs" {
		t.Errorf("after the restore its data is %q", got)
	}
	// a tile restores a plain archive as it always did
	tm, _, tile := archiveOf(t, b, "apps/plain")
	if _, err := b.restore("apps/plain", bytes.NewReader(tile), nil); err != nil {
		t.Errorf("a tile's plain archive (%v): %v", tm.Includes, err)
	}
	// a file is never read out of its backup, however the path is written
	for _, comp := range []string{"xbin/fix", "/xbin/fix/", " xbin/fix", "xbin/none"} {
		_, code, err := b.extractMember(comp, "latest", backup.KVName)
		var se *sealedError
		if code != 400 || !errors.As(err, &se) {
			t.Errorf("extractMember(%q): %d %v, want a sealed refusal", comp, code, err)
		}
	}
	// without a vault barrier nothing of it is archived at all
	pb := plaintextVault(sealedBrokerNoBarrier(t))
	pb.ProxyHandler = &memArchiver{keys: map[string][]memVersion{}}
	if err := pb.Reg.MutateWorkspace(func(w *registry.WorkspaceManifest) {
		w.Bindings = map[string]map[string]registry.Binding{"*": {archiveSlot: registry.BindTo("apps/plain")}}
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := pb.doBackup("xbin/fix"); err == nil || !strings.Contains(err.Error(), "archived only sealed") {
		t.Errorf("a backup of it in plaintext-vault mode: %v", err)
	}
}

// A sealed component's backup is its data alone, with the data format it
// was in; a tile's is what it always was.
func TestSealedBackupIsDataOnly(t *testing.T) {
	b := sealedBroker(t)
	putKV(t, b, "v")
	m, names, arch := archiveOf(t, b, "xbin/fix")
	if !slices.Equal(m.Includes, []string{"data"}) || m.SealedData != 3 || !m.ScopeRoot || m.Scope != "xbin/fix" {
		t.Errorf("manifest: %+v", m)
	}
	for _, n := range names {
		if strings.HasPrefix(n, backup.SourcePrefix) || strings.HasPrefix(n, backup.TermPrefix) {
			t.Errorf("the archive holds %s: a sealed component's code is the release's", n)
		}
	}
	if !slices.Contains(names, backup.KVName) {
		t.Errorf("the archive lacks its data: %v", names)
	}
	tm, tnames, _ := archiveOf(t, b, "apps/plain")
	if tm.SealedData != 0 || !slices.Contains(tm.Includes, "source") || !slices.ContainsFunc(tnames, func(n string) bool { return strings.HasPrefix(n, backup.SourcePrefix) }) {
		t.Errorf("a tile's archive changed: %+v %v", tm, tnames)
	}

	// restored: the data is back, and nothing becomes a directory under xbin/
	putKV(t, b, "changed")
	if _, err := b.restore("xbin/fix", bytes.NewReader(sealedTar(t, b, mainSeal("xbin/fix"), arch)), nil); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := getKV(t, b); got != "v" {
		t.Errorf("after the restore its kv value is %q", got)
	}
	// an archive that carries source or a terminal layer for it (not one
	// this code writes): neither is written
	var forged bytes.Buffer
	fw := backup.NewWriter(&forged)
	fm := m
	fm.Includes = []string{"source", "data", "term-env"}
	if err := fw.Manifest(fm); err != nil {
		t.Fatal(err)
	}
	_ = fw.File(backup.SourcePrefix+"index.html", 0o644, []byte("from the archive"))
	_ = fw.File(backup.TermPrefix+"upper/x", 0o644, []byte("layer"))
	_ = fw.Close()
	if _, err := b.restore("xbin/fix", bytes.NewReader(sealedTar(t, b, mainSeal("xbin/fix"), forged.Bytes())), nil); err != nil {
		t.Fatalf("restore of an archive with source: %v", err)
	}
	if _, err := os.Stat(filepath.Join(b.Reg.Root, "xbin")); err == nil {
		t.Error("a restore made a directory at <workspace>/xbin")
	}
	if _, err := os.Stat(b.termDir("xbin/fix")); err == nil {
		t.Error("a restore made a terminal layer for a sealed component")
	}
	c, _ := b.Reg.Component("xbin/fix")
	if got, _ := os.ReadFile(filepath.Join(c.Dir, "index.html")); string(got) != "<html>sealed</html>" {
		t.Errorf("a restore changed its code: %q", got)
	}

	// newer data than this release reads: refused before anything is written
	newer := m
	newer.SealedData, newer.XBinVersion = 4, "v9"
	if err := b.sealedArchiveRefusal("xbin/fix", newer); err == nil || !strings.Contains(err.Error(), "v9") {
		t.Errorf("a newer archive: %v", err)
	}
	if err := b.sealedArchiveRefusal("xbin/fix", m); err != nil {
		t.Errorf("its own archive: %v", err)
	}
	if err := b.sealedArchiveRefusal("apps/plain", newer); err != nil {
		t.Errorf("a tile's restore asks the sealed set: %v", err)
	}
}

// What the broker decides about a sealed component without the API's gate:
// the table's grants hold, nothing waits for approval, it is never a
// provider, and only its declared slots bind.
func TestSealedGrantsAndBindings(t *testing.T) {
	b := sealedBroker(t)
	if role, ok := b.grantedRole("xbin/fix", "cap:open-links"); !ok || role != "writer" {
		t.Errorf("the table's grant: %q %v", role, ok)
	}
	if role, ok := b.grantedRole("xbin/fix", "res:xbin/fix/state"); !ok || role != "writer" {
		t.Errorf("its own scope's resource: %q %v", role, ok)
	}
	if _, ok := b.grantedRole("xbin/fix", "apps/plain"); ok {
		t.Error("a cross-scope use the table doesn't grant is granted")
	}
	if _, ok := b.grantedRole("apps/plain", "xbin/fix"); ok {
		t.Error("a tile holds a role on the sealed component")
	}
	for _, pg := range b.Pending() {
		if pg.From == "xbin/fix" {
			t.Errorf("a sealed component's use waits for approval: %+v", pg)
		}
	}
	if err := b.validateBinding("xbin/fix", "net", registry.BindTo("internet")); err == nil {
		t.Error("a slot the table doesn't declare validates")
	}
	if err := b.validateBinding("xbin/fix", "@archive", registry.BindTo("apps/plain")); err == nil || !strings.Contains(err.Error(), "system component") {
		t.Errorf("a pseudo-slot on a sealed component: %v", err)
	}
	if err := b.validateBinding("apps/plain", "llm", registry.BindTo("xbin/fix")); err == nil || !strings.Contains(err.Error(), "never the provider") {
		t.Errorf("a sealed component as provider: %v", err)
	}
	// no repo is made in its tree, and no merge driver configured
	b.EnsureComponentRepos()
	c, _ := b.Reg.Component("xbin/fix")
	if _, err := os.Stat(filepath.Join(c.Dir, ".git")); err == nil {
		t.Error("xbind made a git repo in a sealed component's tree")
	}
}

// What the release grants a sealed component is its backend's to use. Its
// page's principal (a frame token of it, any reader's) holds none of it at
// the places where a principal's component becomes authority — a call's
// role, a resource's role — and nobody but its backend uses what is its
// own, the root token and admins included.
func TestSealedAuthorityIsItsBackendsAlone(t *testing.T) {
	root := t.TempDir()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.WriteFile(filepath.Join(root, "xbin.json"), []byte(`{"schema":1,"resources":{"shared":{"type":"kv"}}}`), 0o644))
	must(os.MkdirAll(filepath.Join(root, "apps", "plain"), 0o755))
	must(os.WriteFile(filepath.Join(root, "apps", "plain", "index.html"), []byte("<html></html>"), 0o644))
	dir := filepath.Join(t.TempDir(), "fix")
	must(os.MkdirAll(dir, 0o755))
	code := registry.SealedCode{Path: "xbin/fix", Dir: dir, Manifest: []byte(`{"uses":[{"target":"res:xbin/fix/state","role":"writer"}]}`),
		Scope:  []byte(`{"resources":{"state":{"type":"kv"}}}`),
		Grants: []registry.Use{{Target: "apps/plain", Role: "reader"}, {Target: "res:workspace/shared", Role: "reader"}}}
	reg, err := registry.OpenSealed(root, func() []registry.SealedCode { return []registry.SealedCode{code} })
	must(err)
	b, err := New(reg, events.NewHub(), false)
	must(err)
	plain, _ := reg.Component("apps/plain")
	self, _ := reg.Component("xbin/fix")

	backend := auth.Principal{Component: "xbin/fix", Via: "instance"}
	page := auth.Principal{Component: "xbin/fix", Via: "frame", UserID: "ann"}
	owner := auth.Principal{Owner: true, Via: "bearer"}
	tile := auth.Principal{Component: "apps/plain", Via: "instance"}
	const own, shared = "res:xbin/fix/state", "res:workspace/shared"

	// its backend: the release's grants, and what is its own
	if role, ok := b.Policy(backend, plain); !ok || role != "reader" {
		t.Errorf("its backend's call to a tile it is granted: %q %v", role, ok)
	}
	for _, res := range []string{own, shared} {
		if err := b.allowRes(backend, res, "reader"); err != nil {
			t.Errorf("its backend on %s: %v", res, err)
		}
	}
	// its page: its own backend, and nothing as the component
	if role, ok := b.Policy(page, self); !ok || role != "admin" {
		t.Errorf("its page's call to its own backend: %q %v", role, ok)
	}
	if role, ok := b.Policy(page, plain); ok {
		t.Errorf("its page called a tile the component is granted, as %q", role)
	}
	var se *sealedError
	for _, res := range []string{own, shared} {
		if err := b.allowRes(page, res, "reader"); !errors.As(err, &se) {
			t.Errorf("its page on %s: %v, want a sealed refusal", res, err)
		}
		if rt, ok := b.resScope(res); ok {
			if err := b.allowAt(page, reach{rt: rt, res: registry.Resource{Type: "kv"}, dep: util.MainDeployment}, "reader"); !errors.As(err, &se) {
				t.Errorf("its page reaching %s: %v, want a sealed refusal", res, err)
			}
		}
	}
	// the root token: a workspace resource as ever, the component's own never
	if err := b.allowRes(owner, shared, "writer"); err != nil {
		t.Errorf("the root token on a workspace resource: %v", err)
	}
	if err := b.allowRes(owner, own, "reader"); !errors.As(err, &se) || se.Status != 403 {
		t.Errorf("the root token on its resource: %v, want a sealed refusal", err)
	}
	if err := b.allowRes(tile, own, "reader"); !errors.As(err, &se) {
		t.Errorf("a tile's backend on its resource: %v, want a sealed refusal", err)
	}
	// and nothing calls into it but its own page and admins' sessions
	if role, ok := b.Policy(tile, self); ok {
		t.Errorf("a tile called into it as %q", role)
	}
}

// One spelling per component, where a name is kept: a restore and a
// registration take a component's path as xbind stores it, and nothing
// else. "/xbin/fix" was restored into as a component of its own — from a
// plain, forged archive, since the sealed test saw no sealed path — and its
// cron row then fired at /api//xbin/fix/…, which the proxy resolved into
// the sealed component's backend.
func TestSealedSpellingsWhereNamesAreKept(t *testing.T) {
	b := sealedBroker(t)
	putKV(t, b, "theirs")
	odd := []string{"/xbin/fix", "xbin/fix/", "//xbin/fix", "xbin//fix", "./xbin/fix", "apps/../xbin/fix", " xbin/fix", "xbin/./fix"}
	other := []string{"XBIN/fix", "xbin%2Ffix", "xbіn/fix", "xbin/nosuch", "/apps/plain", ".xbin", "data", "../outside"}
	for _, comp := range append(append([]string{}, odd...), other...) {
		if b.componentNamed(comp) && comp != "XBIN/fix" && comp != "xbin%2Ffix" && comp != "xbіn/fix" {
			t.Errorf("componentNamed(%q)", comp)
		}
		job, _ := json.Marshal(cronJob{Name: "planted", Schedule: "@every 1h", Component: comp, Path: "/tick", Role: "admin"})
		m := backup.Manifest{Component: comp, Scope: comp, ScopeRoot: true, Includes: []string{"data"}, CronJobs: []json.RawMessage{job}}
		arch := plainTar(t, m, map[string]string{backup.KVName: `{"state":{"k":"` + base64.StdEncoding.EncodeToString([]byte("forged")) + `"}}`})
		_, err := b.restore(comp, bytes.NewReader(arch), nil)
		if util.SealedPath(comp) || !util.ComponentPathOK(comp) {
			if err == nil {
				t.Errorf("a plain archive restored into %q", comp)
			}
		}
		if got := getKV(t, b); got != "theirs" {
			t.Fatalf("after a restore into %q its data is %q", comp, got)
		}
	}
	if b.cron != nil {
		b.cron.mu.Lock()
		for _, j := range b.cron.jobs {
			if util.SealedPath(j.Component) || j.Component != util.CanonPath(j.Component) {
				t.Errorf("a cron job was registered for %q", j.Component)
			}
		}
		b.cron.mu.Unlock()
	}
	if !b.componentNamed("xbin/fix") || !b.componentNamed("apps/plain") || !b.componentNamed("apps/not-yet-there") {
		t.Error("componentNamed refuses a component's own path")
	}
	// a delivery never leaves for a path that isn't one, whatever a file says
	reached := ""
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = r.URL.Path })
	for _, comp := range odd {
		if code, _ := DispatchViaProxy(h)(auth.Principal{Component: CronPrincipal, Via: "cron", Role: "admin"}, comp, "/tick"); code != 400 || reached != "" {
			t.Errorf("a cron delivery to %q: %d (reached %q)", comp, code, reached)
		}
		if code, _ := DispatchBodyViaProxy(h)(context.Background(), auth.Principal{Component: BusPrincipal, Via: "bus"}, comp, "/bus", nil); code != 400 || reached != "" {
			t.Errorf("a bus delivery to %q: %d (reached %q)", comp, code, reached)
		}
	}
	// and every sealed test sees every spelling: the decisions are the same
	for _, comp := range odd {
		var se *sealedError
		if err := b.allowRes(auth.Principal{Owner: true, Via: "bearer"}, "res:"+comp+"/state", "reader"); err == nil {
			t.Errorf("the root token on res:%s/state", comp)
		}
		if !errors.As(sealedNone(comp, "x"), &se) || !errors.As(sealedOwn(auth.Principal{Owner: true}, comp, "x"), &se) {
			t.Errorf("sealedNone/sealedOwn miss %q", comp)
		}
		if sealedOwn(auth.Principal{Component: "xbin/fix", Via: "instance"}, comp, "x") != nil {
			t.Errorf("its backend is refused what is its own, spelled %q", comp)
		}
		if _, _, err := b.extractMember(comp, "latest", backup.KVName); !errors.As(err, &se) {
			t.Errorf("extractMember(%q): %v", comp, err)
		}
	}
}
