//go:build integration

package test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// Sealed backups across an upgrade and a downgrade
// (plans/partitions/11-backup-encryption.md §Tests; PD-25, PD-56): the
// previous release (XBIN_DOWNGRADE_BIN) writes a plaintext archive in a
// workspace with a vault; the new xbind restores it, seals its own backups
// (main and data archives), restores them, erases the data key (the source
// still restores, and the answer says the data was erased) and then every
// key (refused); the previous release again restores the old plaintext
// archive and refuses a sealed one, writing nothing.

const (
	sbArchiver = "apps/sb-arch"
	sbTile     = "apps/sb-probe"
)

// covers PD-25 PD-56 11§4 11§7 — the upgrade, erase and downgrade path of
// sealed backups on the real binaries.
func TestSealedBackupsUpgrade(t *testing.T) {
	old := downgradeBinOrSkip(t)
	t.Setenv("XBIN_VAULT_PASSPHRASE", "sealed-backups-itest") // a vault barrier: backups seal
	store := t.TempDir()
	h := startDowngradeAs(t, old)
	a := h.a

	// the previous release: an archiver, a tile with data, a plaintext archive
	writeTestArchiver(t, h.ws, store)
	writeProbe(t, h.ws, sbTile, "m1")
	sbWait(t, a, "/api/"+sbTile+"/v", "m1") // built and serving: the previous release knows no /deployments
	sbWait(t, a, "/api/"+sbArchiver+"/archive/none/versions", `"versions"`)
	sbMust(t, a, "POST", "/api/xbin/bindings", `{"component":"*","slot":"@archive","provider":"`+sbArchiver+`"}`)
	sbPutKV(t, a, "plain-v1")
	vOld := sbBackup(t, a)
	if body := sbStored(t, store, sbKey(t, store, false), vOld); bytes.HasPrefix(body, []byte("XBINSEAL")) || !bytes.Contains(body, []byte("backup.json")) {
		t.Fatalf("the previous release's archive isn't a plain tar: %.40q", body)
	}

	// the upgrade: the old archive restores
	h.stop(t)
	h.start(t, xbindBin)
	writeProbe(t, h.ws, sbTile, "m2")
	sbPutKV(t, a, "changed")
	sbRestore(t, a, vOld, 200, "")
	sbSee(t, a, h.ws, "m1", "plain-v1")

	// a sealed backup: main and data archives, no plaintext in either
	vSealed := sbBackup(t, a)
	mainKey := sbKey(t, store, false)
	dataKey := sbKey(t, store, true)
	for _, x := range []struct{ key, v string }{{mainKey, vSealed}, {dataKey, sbNewest(t, store, dataKey)}} {
		body := sbStored(t, store, x.key, x.v)
		if !bytes.HasPrefix(body, []byte("XBINSEAL")) || bytes.Contains(body, []byte("plain-v1")) || bytes.Contains(body, []byte("backup.json")) ||
			bytes.Contains(body, []byte("m1")) && bytes.Contains(body, []byte("probe.txt")) {
			t.Fatalf("%s %s isn't sealed, or holds plaintext: %.40q", x.key, x.v, body)
		}
		if id, err := os.ReadFile(filepath.Join(store, x.key, x.v+".subkey")); err != nil || !strings.HasPrefix(string(id), "bk-") {
			t.Errorf("%s: the PUT carried no X-XBin-Backup-Subkey (%q %v)", x.key, id, err)
		}
	}
	if _, body := a.do("GET", "/api/xbin/alerts", ""); !strings.Contains(body, `"kind":"backup-keys"`) {
		t.Errorf("no backup-keys alert before an export: %s", body)
	}
	sbPutKV(t, a, "changed-again")
	sbRestore(t, a, vSealed, 200, "")
	sbSee(t, a, h.ws, "m1", "plain-v1")

	// erase the data key: the source restores, the data doesn't, and the
	// answer says why
	sbMust(t, a, "POST", "/api/xbin/backup/erase", `{"component":"`+sbTile+`","what":"data"}`)
	writeProbe(t, h.ws, sbTile, "m3")
	sbPutKV(t, a, "after-erase")
	sbRestore(t, a, vSealed, 200, "this backup's data was erased on")
	sbSee(t, a, h.ws, "m1", "after-erase")
	// ... and every key: the archive is refused
	sbMust(t, a, "POST", "/api/xbin/backup/erase", `{"component":"`+sbTile+`","what":"all"}`)
	sbRestore(t, a, vSealed, 502, "this backup's data was erased on")
	vSealed2 := sbBackup(t, a) // new keys, gen 2

	// the previous release again: the plaintext archive restores; a sealed
	// one is refused, and nothing is written
	h.stop(t)
	h.start(t, old)
	writeProbe(t, h.ws, sbTile, "m4")
	sbPutKV(t, a, "under-old")
	sbRestore(t, a, vSealed2, -1, "")
	sbSee(t, a, h.ws, "m4", "under-old")
	sbRestore(t, a, vOld, 200, "")
	sbSee(t, a, h.ws, "m1", "plain-v1")
}

// startDowngradeAs is startDowngradePlain on bin (the shared daemon's
// flavour: --no-auth, not isolated), restartable as another binary.
func startDowngradeAs(t *testing.T, bin string) *dgHost {
	t.Helper()
	dir := t.TempDir()
	p := &dgPlain{ws: filepath.Join(dir, "ws"), addr: isoFreeAddr(t), logPath: filepath.Join(dir, "xbind.log")}
	t.Cleanup(func() {
		_ = p.halt()
		if t.Failed() {
			t.Logf("xbind (%s):\n%s", p.ws, isoTail(p.logPath, 80))
		}
		removeTree(p.ws)
	})
	p.start(t, bin)
	return &dgHost{ws: p.ws, a: dlAPI{url: "http://" + p.addr}, start: p.start,
		stop: func(t *testing.T) {
			t.Helper()
			if err := p.halt(); err != nil {
				t.Fatalf("stopping xbind: %v", err)
			}
		}}
}

// writeTestArchiver writes an archiver tile keeping archives under store:
// <store>/<key>/<version>, and the subkey its PUT named in <version>.subkey.
// It has no POST /archive/erase: an erased key's versions stay.
func writeTestArchiver(t *testing.T, ws, store string) {
	t.Helper()
	dir := filepath.Join(ws, filepath.FromSlash(sbArchiver))
	for _, f := range []struct{ rel, content string }{
		{"go.mod", "module probe/sb-arch\n\ngo 1.24\n\nrequire github.com/xbin-dev/xbin/sdk v0.0.0\n"},
		{"archive-dir.txt", store},
		{"backend/main.go", sbArchiverSource},
		{"xbin.json", `{"runtime":"go","provides":{"store":{"kind":"archive"}}}` + "\n"},
	} {
		writeIfChanged(t, filepath.Join(dir, filepath.FromSlash(f.rel)), f.content)
	}
}

const sbArchiverSource = `package main

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

func main() {
	wd, _ := os.Getwd()
	b, _ := os.ReadFile(filepath.Join(wd, "archive-dir.txt"))
	root := strings.TrimSpace(string(b))
	versions := func(key string) []string {
		ents, _ := os.ReadDir(filepath.Join(root, key))
		var out []string
		for _, e := range ents {
			if !strings.HasSuffix(e.Name(), ".subkey") {
				out = append(out, e.Name())
			}
		}
		sort.Sort(sort.Reverse(sort.StringSlice(out)))
		return out
	}
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /archive/{key}", func(w http.ResponseWriter, r *http.Request) {
		dir := filepath.Join(root, r.PathValue("key"))
		_ = os.MkdirAll(dir, 0o755)
		v := time.Now().UTC().Format("20060102T150405.000000000Z")
		body, _ := io.ReadAll(r.Body)
		_ = os.WriteFile(filepath.Join(dir, v), body, 0o644)
		if id := r.Header.Get("X-XBin-Backup-Subkey"); id != "" {
			_ = os.WriteFile(filepath.Join(dir, v+".subkey"), []byte(id), 0o644)
		}
		json.NewEncoder(w).Encode(map[string]any{"version": v, "size": len(body)})
	})
	mux.HandleFunc("GET /archive/{key}/versions", func(w http.ResponseWriter, r *http.Request) {
		out := []map[string]any{}
		for _, v := range versions(r.PathValue("key")) {
			out = append(out, map[string]any{"version": v, "time": time.Now().UTC().Format(time.RFC3339), "size": 0})
		}
		json.NewEncoder(w).Encode(map[string]any{"versions": out})
	})
	mux.HandleFunc("GET /archive/{key}/versions/{v}", func(w http.ResponseWriter, r *http.Request) {
		v := r.PathValue("v")
		if v == "latest" {
			if vs := versions(r.PathValue("key")); len(vs) > 0 {
				v = vs[0]
			}
		}
		b, err := os.ReadFile(filepath.Join(root, r.PathValue("key"), filepath.Base(v)))
		if err != nil {
			http.Error(w, "no such version", http.StatusNotFound)
			return
		}
		w.Write(b)
	})
	mux.HandleFunc("DELETE /archive/{key}/versions/{v}", func(w http.ResponseWriter, r *http.Request) {
		_ = os.Remove(filepath.Join(root, r.PathValue("key"), filepath.Base(r.PathValue("v"))))
		io.WriteString(w, "{\"ok\":true}")
	})
	xbin.Serve(mux)
}
`

// sbWait waits (bounded: a cold build) until GET path answers 200 holding
// want.
func sbWait(t *testing.T, a dlAPI, path, want string) {
	t.Helper()
	var code int
	var body string
	if !waitFor(func() bool { code, body = a.do("GET", path, ""); return code == 200 && strings.Contains(body, want) }, 4*time.Minute) {
		t.Fatalf("GET %s: %d %.200s, want %q", path, code, body, want)
	}
}

// sbMust sends one owner request, which must answer 200.
func sbMust(t *testing.T, a dlAPI, method, path, body string) string {
	t.Helper()
	code, out := a.do(method, path, body)
	if code != 200 {
		t.Fatalf("%s %s: %d %s", method, path, code, out)
	}
	return out
}

// sbPutKV sets the probe's kv key k (its backend built and running).
func sbPutKV(t *testing.T, a dlAPI, v string) {
	t.Helper()
	var code int
	var body string
	if !waitFor(func() bool { code, body = a.do("PUT", "/api/"+sbTile+"/kv/k", v); return code == 200 }, 3*time.Minute) {
		t.Fatalf("PUT the probe's kv: %d %s", code, body)
	}
}

func sbBackup(t *testing.T, a dlAPI) string {
	t.Helper()
	var out struct{ Version string }
	var code int
	var body string
	if !waitFor(func() bool {
		code, body = a.do("POST", "/api/xbin/backup", `{"component":"`+sbTile+`"}`)
		return code == 200 || !strings.Contains(body, "archiver") // the archiver's first build
	}, 3*time.Minute) || code != 200 || json.Unmarshal([]byte(body), &out) != nil || out.Version == "" {
		t.Fatalf("backup: %d %s", code, body)
	}
	return out.Version
}

// sbRestore restores version v; want is the code (-1: any refusal), and
// the answer must hold says.
func sbRestore(t *testing.T, a dlAPI, v string, want int, says string) {
	t.Helper()
	code, body := a.do("POST", "/api/xbin/restore", `{"component":"`+sbTile+`","version":"`+v+`"}`)
	t.Logf("restore %s: %d %.300s", v, code, body)
	if want == -1 && code < 400 || want != -1 && code != want || !strings.Contains(body, says) {
		t.Fatalf("restore %s: %d %s (want %d, %q)", v, code, body, want, says)
	}
}

// sbSee checks the probe's source file (probe.txt) and kv key k.
func sbSee(t *testing.T, a dlAPI, ws, file, kv string) {
	t.Helper()
	if b, _ := os.ReadFile(filepath.Join(ws, filepath.FromSlash(sbTile), probeFile)); string(b) != file {
		t.Errorf("the probe's %s: %q, want %q", probeFile, b, file)
	}
	var code int
	var body string
	if !waitFor(func() bool { code, body = a.do("GET", "/api/"+sbTile+"/kv/k", ""); return code == 200 && body == kv }, 3*time.Minute) {
		t.Errorf("the probe's kv k: %d %q, want %q", code, body, kv)
	}
}

// sbKey is the archive key under store of the probe's main archive (the
// only key not starting with ".") or of its data archive (.data.*).
func sbKey(t *testing.T, store string, data bool) string {
	t.Helper()
	ents, _ := os.ReadDir(store)
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
		if isData := strings.HasPrefix(e.Name(), ".data."); isData == data && (data || !strings.HasPrefix(e.Name(), ".")) {
			return e.Name()
		}
	}
	t.Fatalf("no %s archive key under the store: %v", map[bool]string{false: "main", true: "data"}[data], names)
	return ""
}

func sbNewest(t *testing.T, store, key string) string {
	t.Helper()
	ents, _ := os.ReadDir(filepath.Join(store, key))
	var vs []string
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), ".subkey") {
			vs = append(vs, e.Name())
		}
	}
	sort.Strings(vs)
	if len(vs) == 0 {
		t.Fatalf("no version under %s", key)
	}
	return vs[len(vs)-1]
}

func sbStored(t *testing.T, store, key, v string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(store, key, v))
	if err != nil {
		t.Fatal(err)
	}
	return b
}
