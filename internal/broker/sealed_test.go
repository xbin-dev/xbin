package broker

import (
	"bytes"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/backup"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/users"
	"github.com/xbin-dev/xbin/internal/util"
)

// sealedBroker is a broker over a workspace with one tile and one sealed
// component, xbin/fix: a kv resource, one declared slot, a table grant.
func sealedBroker(t *testing.T) *Broker {
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
	if _, err := b.restore("xbin/fix", bytes.NewReader(arch), nil); err != nil {
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
	if _, err := b.restore("xbin/fix", bytes.NewReader(forged.Bytes()), nil); err != nil {
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
