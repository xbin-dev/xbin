package broker

import (
	"archive/tar"
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
)

// zeroDataArchiver stands in for the archiver tile behind the proxy: it
// records each call's method, path and body, and answers a version.
type zeroDataArchiver struct {
	mu    sync.Mutex
	calls []string
	body  []byte
}

func (a *zeroDataArchiver) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	a.mu.Lock()
	a.calls = append(a.calls, fmt.Sprintf("%s %s owner=%v", r.Method, r.URL.Path, auth.PrincipalOf(r).Owner))
	a.body = body
	a.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"version":"v1"}`))
}

// covers D119c SC-ZERO Z6 PO-2 PO-9 — a zero-state tile's backup is today's
// archive: sent to the archiver under the archive key CompKey(tile), with
// today's members in today's order and headers, and today's manifest — its
// own cron jobs and bus subscriptions, no deployment field, schema 1 — for a
// tile that roots its scope (source, the scope's kv data, its terminal
// layer) and for a nested one that doesn't (source only). The manifest's
// creation time is the one value masked.
func TestZeroStateBackupMembers(t *testing.T) {
	b := zeroDataBroker(t)
	b.Version = "test"
	root := b.Reg.Root
	arch := &zeroDataArchiver{}
	b.ProxyHandler = arch
	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// the tile's own repo, reproducible and runtime dirs, a symlink, a subdir
	write("apps/cal/.git/HEAD", "ref: refs/heads/main\n")
	write("apps/cal/node_modules/left-pad/index.js", "module.exports = 1\n")
	write("apps/cal/.xbin/cache/x", "cache\n")
	write("apps/cal/sub/deep.txt", "deep\n")
	if err := os.Symlink("index.html", filepath.Join(root, "apps/cal/link.html")); err != nil {
		t.Fatal(err)
	}
	// its terminal layer, with a VM disk that is never archived
	write(".xbin/term/apps~cal-c2360189/upper/etc/profile", "export A=1\n")
	write(".xbin/term/apps~cal-c2360189/vm/disk.img", "sparse\n")
	// data and registrations
	cal := auth.Principal{Component: "apps/cal", Via: "instance"}
	for rest, val := range map[string]string{"res:apps/cal/events/k1": "v1", "res:apps/cal/events/k2": "v2"} {
		if w := zeroDataCall(t, b.apiKVPut, "PUT", rest, val, cal); w.Code != 200 {
			t.Fatalf("kv put: %d %s", w.Code, w.Body.String())
		}
	}
	if w := zeroDataCall(t, b.apiCronPut, "PUT", "",
		`{"name":"tick","resource":"res:apps/cal/ticks","schedule":"@every 1h","path":"/tick"}`, cal); w.Code != 200 {
		t.Fatalf("cron put: %d %s", w.Code, w.Body.String())
	}
	if w := zeroDataCall(t, b.apiBusSubsPut, "PUT", "",
		`{"name":"s1","resource":"res:apps/cal/bus","path":"/on"}`, cal); w.Code != 200 {
		t.Fatalf("bus subscription put: %d %s", w.Code, w.Body.String())
	}

	created := regexp.MustCompile(`"created": "[^"]*"`)
	var kvJSON string
	backup := func(comp string) (call, members, manifest string) {
		t.Helper()
		v, err := b.doBackup(comp)
		if err != nil || v != "v1" {
			t.Fatalf("backup %s: %q %v", comp, v, err)
		}
		arch.mu.Lock()
		call, body := arch.calls[len(arch.calls)-1], arch.body
		arch.mu.Unlock()
		tr := tar.NewReader(bytes.NewReader(body))
		var lines []string
		for {
			h, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			content, _ := io.ReadAll(tr)
			if h.Name == "data/kv.json" {
				kvJSON = string(content)
			}
			if h.Name == "backup.json" {
				manifest = created.ReplaceAllString(string(content), `"created": "<masked>"`)
			}
			if h.ModTime.Unix() != 0 || h.Uid != 0 || h.Gid != 0 || h.Uname != "" || h.Gname != "" || h.Linkname != "" {
				t.Errorf("%s: header of %s carries more than name, mode, size and type: %+v", comp, h.Name, h)
			}
			lines = append(lines, fmt.Sprintf("%s %o %d %c", h.Name, h.Mode, h.Size, h.Typeflag))
		}
		return call, strings.Join(lines, "\n"), manifest
	}

	call, members, manifest := backup("apps/cal")
	if want := "PUT /api/apps/archiver/archive/apps~cal-c2360189 owner=true"; call != want {
		t.Errorf("archive call changed\n got: %s\nwant: %s", call, want)
	}
	wantMembers := strings.Join([]string{
		"backup.json 644 " + fmt.Sprint(len(zeroDataUnmask(manifest))) + " 0",
		"source/.git/HEAD 644 21 0",
		"source/index.html 644 13 0",
		"source/scope.json 644 192 0",
		"source/sub/deep.txt 644 5 0",
		"source/widget/xbin.json 644 74 0",
		"source/xbin.json 644 613 0",
		"data/kv.json 644 50 0",
		"term/upper/etc/profile 644 11 0",
	}, "\n")
	if members != wantMembers {
		t.Errorf("archive members changed\n got:\n%s\nwant:\n%s", members, wantMembers)
	}
	// kv data: every kv resource of the scope, values base64, plaintext
	if want := `{"events":{"k1":"djE=","k2":"djI="},"my-cache":{}}`; kvJSON != want {
		t.Errorf("data/kv.json changed\n got: %s\nwant: %s", kvJSON, want)
	}
	wantManifest := `{
  "schema": 1,
  "component": "apps/cal",
  "scope": "apps/cal",
  "scopeRoot": true,
  "resources": {
    "bus": "bus",
    "db": "sqlite",
    "events": "kv",
    "files": "filesystem",
    "my-cache": "kv",
    "pics": "blob",
    "ticks": "cron"
  },
  "xbinVersion": "test",
  "created": "<masked>",
  "includes": [
    "source",
    "data",
    "term-env"
  ],
  "cronJobs": [
    {
      "name": "tick",
      "resource": "res:apps/cal/ticks",
      "schedule": "@every 1h",
      "component": "apps/cal",
      "path": "/tick",
      "role": "writer"
    }
  ],
  "busSubscriptions": [
    {
      "name": "s1",
      "resource": "res:apps/cal/bus",
      "component": "apps/cal",
      "path": "/on",
      "role": "writer"
    }
  ]
}`
	if manifest != wantManifest {
		t.Errorf("archive manifest changed\n got:\n%s\nwant:\n%s", manifest, wantManifest)
	}

	// A nested tile that doesn't root its scope: source only, the scope named.
	call, members, manifest = backup("apps/cal/widget")
	if want := "PUT /api/apps/archiver/archive/apps~cal~widget-4289f1dd owner=true"; call != want {
		t.Errorf("nested archive call changed\n got: %s\nwant: %s", call, want)
	}
	if want := "backup.json 644 " + fmt.Sprint(len(zeroDataUnmask(manifest))) + " 0\nsource/xbin.json 644 74 0"; members != want {
		t.Errorf("nested archive members changed\n got:\n%s\nwant:\n%s", members, want)
	}
	if want := `{
  "schema": 1,
  "component": "apps/cal/widget",
  "scope": "apps/cal",
  "scopeRoot": false,
  "xbinVersion": "test",
  "created": "<masked>",
  "includes": [
    "source"
  ]
}`; manifest != want {
		t.Errorf("nested archive manifest changed\n got:\n%s\nwant:\n%s", manifest, want)
	}
}

// zeroDataUnmask restores the masked creation time's length (RFC 3339 in
// UTC, seconds: always 20 bytes), so the member size can be checked.
func zeroDataUnmask(masked string) string {
	return strings.Replace(masked, `"<masked>"`, `"2006-01-02T15:04:05Z"`, 1)
}
