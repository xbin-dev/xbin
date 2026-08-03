package broker

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/registry"
)

// plainWorkspace: apps/store declares a PLAIN filesystem resource (D43) plus a
// sqlite resource wrongly marked plain; apps/enc declares a normal (encrypted)
// filesystem resource. Both components use their own resources.
func plainWorkspace(t *testing.T) *Broker {
	t.Helper()
	root := t.TempDir()
	write := func(rel, content string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("xbin.json", `{"schema":1}`)
	write("apps/store/scope.json", `{"resources":{
		"data":{"type":"filesystem","plain":true},
		"db":{"type":"sqlite","plain":true}}}`)
	write("apps/store/xbin.json", `{"runtime":"go",
		"uses":[{"target":"res:apps/store/data","role":"writer"}]}`)
	write("apps/store/index.html", `<html></html>`)
	write("apps/enc/scope.json", `{"resources":{"files":{"type":"filesystem"}}}`)
	write("apps/enc/xbin.json", `{"runtime":"go",
		"uses":[{"target":"res:apps/enc/files","role":"writer"}]}`)
	write("apps/enc/index.html", `<html></html>`)
	reg, err := registry.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	b, err := New(reg, events.NewHub(), false)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// A plain resource provisions as a plaintext dir in the legacy layout, is
// ready without any vault, and its env var points straight at that dir.
func TestPlainResourceProvision(t *testing.T) {
	b := plainWorkspace(t) // New() ran Provision()

	dir := filepath.Join(b.Reg.Root, "data", "resources", "apps~store", "data")
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		t.Fatalf("plain resource dir not provisioned: %v", err)
	}
	// Never resenc-mounted: no decrypted-view mountpoint may exist for it.
	if _, err := os.Stat(filepath.Join(b.Reg.Root, ".xbin", "resenc", "apps~store", "data")); err == nil {
		t.Fatal("plain resource must not get a resenc mountpoint")
	}

	if !b.resPlain("apps/store", "data") {
		t.Fatal("resPlain must report the declared plain filesystem resource")
	}
	// Ready with no gocryptfs and no initialized vault — that's the point.
	if !b.fsReady("apps/store", "data") {
		t.Fatal("plain resource must be ready regardless of vault state")
	}

	c, ok := b.Reg.Component("apps/store")
	if !ok {
		t.Fatal("component missing")
	}
	var got string
	for _, e := range b.EnvFor(c) {
		if strings.HasPrefix(e, "XBIN_RES_DATA=") {
			got = strings.TrimPrefix(e, "XBIN_RES_DATA=")
		}
	}
	if got != dir {
		t.Fatalf("XBIN_RES_DATA = %q, want the plain dir %q", got, dir)
	}
}

// The flag is filesystem-only: a plain-marked sqlite resource stays on the
// encrypted path (not ready without a vault) and never reports as plain.
func TestPlainFlagNonFilesystemIgnored(t *testing.T) {
	b := plainWorkspace(t)
	if b.resPlain("apps/store", "db") {
		t.Fatal("plain on type sqlite must be ignored")
	}
	if b.fsReady("apps/store", "db") {
		t.Fatal("plain-marked sqlite must stay on the encrypted (not-ready) path")
	}
}

// Vault-state semantics: a component whose only file resources are plain is
// never held and never stopped by a seal; one using an encrypted resource is
// both held (vault not ready here) and stopped on seal.
func TestPlainResourceHoldAndSeal(t *testing.T) {
	b := plainWorkspace(t)

	if b.EncryptionHold("apps/store") {
		t.Fatal("only-plain component must not be held on vault state")
	}
	if !b.EncryptionHold("apps/enc") {
		t.Fatal("encrypted-resource component must be held while encryption is not ready")
	}

	cs, _ := b.Reg.Component("apps/store")
	ce, _ := b.Reg.Component("apps/enc")
	if b.componentUsesFileRes(cs) {
		t.Fatal("plain resources must not count as encrypted file-res deps")
	}
	if !b.componentUsesFileRes(ce) {
		t.Fatal("encrypted filesystem resource must count")
	}

	var stopped []string
	b.StopBackend = func(comp string) { stopped = append(stopped, comp) }
	b.SealResources()
	if len(stopped) != 1 || stopped[0] != "apps/enc" {
		t.Fatalf("seal must stop only encrypted-resource components, stopped %v", stopped)
	}
}

// GET /resources reports plain (raw declared flag, so doctor can flag misuse)
// and encRemnant (a now-plain resource with a leftover cipher tree).
func TestResourcesAPIPlainFields(t *testing.T) {
	b := plainWorkspace(t)

	// Simulate the resource having been encrypted before it was made plain.
	cipher := filepath.Join(b.Reg.Root, "data", "resources-enc", "apps~store", "data")
	if err := os.MkdirAll(cipher, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cipher, "gocryptfs.conf"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	r := httptest.NewRequest("GET", "/resources", nil)
	r = r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{Owner: true}))
	w := httptest.NewRecorder()
	b.apiResources(w, r)
	if w.Code != 200 {
		t.Fatalf("GET /resources: %d %s", w.Code, w.Body)
	}
	var rows []struct {
		ID         string `json:"id"`
		Type       string `json:"type"`
		Plain      bool   `json:"plain"`
		EncRemnant bool   `json:"encRemnant"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	byID := map[string]struct {
		ID         string `json:"id"`
		Type       string `json:"type"`
		Plain      bool   `json:"plain"`
		EncRemnant bool   `json:"encRemnant"`
	}{}
	for _, r := range rows {
		byID[r.ID] = r
	}
	if r := byID["res:apps/store/data"]; !r.Plain || !r.EncRemnant {
		t.Fatalf("plain resource with cipher leftovers: %+v", r)
	}
	if r := byID["res:apps/store/db"]; !r.Plain || r.EncRemnant {
		// Raw declared flag — the doctor uses plain+type!=filesystem to warn.
		t.Fatalf("plain-marked sqlite row: %+v", r)
	}
	if r := byID["res:apps/enc/files"]; r.Plain || r.EncRemnant {
		t.Fatalf("encrypted resource row: %+v", r)
	}

	// The runtime listing (admin tile) carries the honored flag only.
	var plain, enc bool
	for _, ri := range b.ResourceUsage() {
		switch ri.ID {
		case "res:apps/store/data":
			plain = ri.Plain
		case "res:apps/store/db":
			if ri.Plain {
				t.Fatal("ResourceUsage must not mark ignored plain flags")
			}
		case "res:apps/enc/files":
			enc = ri.Plain
		}
	}
	if !plain || enc {
		t.Fatalf("ResourceUsage plain flags: store/data=%v enc/files=%v", plain, enc)
	}
}
