//go:build linux && integration

package tilesbx

// Files, tar and copy (WP-18) against live tile sandboxes, driven through
// the routes as the manager tile apps/mgr (live_linux_test.go's harness):
// over a minimal lower with the kernel overlay, and again with
// fuse-overlayfs when one is found. Each case runs per mode in
// liveFileModes.

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/sandbox"
)

// liveFileModes are the modes the file routes are tested in. VM mode
// joins with WP-16, which builds it.
var liveFileModes = []string{ModeNamespace}

func TestLiveFiles(t *testing.T) {
	if !sandbox.Available() {
		t.Skip("unprivileged user namespaces unavailable")
	}
	bin := liveBinaries(t)
	fuse := findFuseOverlayfs()
	for _, mode := range liveFileModes {
		t.Run(mode, func(t *testing.T) {
			t.Run("minimal", func(t *testing.T) {
				t.Setenv("XBIN_FUSE_OVERLAYFS", "none") // the kernel overlay
				testLiveFiles(t, bin, mode)
			})
			t.Run("minimal-fuse", func(t *testing.T) {
				if fuse == "" {
					t.Skip("no fuse-overlayfs")
				}
				t.Setenv("XBIN_FUSE_OVERLAYFS", fuse)
				testLiveFiles(t, bin, mode)
			})
		})
	}
}

func testLiveFiles(t *testing.T, bin, mode string) {
	le := newLiveEnv(t, bin, "")
	for _, name := range []string{"sb-1", "sb-2"} {
		le.create(map[string]any{"name": name, "mode": mode, "mounts": []any{
			map[string]any{"res": "res:apps/mgr/work", "at": "/mnt/work"},
			map[string]any{"res": "res:apps/mgr/ro", "at": "/mnt/ro"}}})
	}
	raw := func(method, target string, body []byte, n int64) *httpResult {
		w := doRaw(le.testEnv, mgr, method, target, bytes.NewReader(body), n)
		return &httpResult{code: w.Code, body: w.Body.Bytes(), header: w.Header()}
	}
	put := func(name, p string, q url.Values, body string) *httpResult {
		if q == nil {
			q = url.Values{}
		}
		q.Set("path", p)
		return raw("PUT", "/sandboxes/"+name+"/files/content?"+q.Encode(), []byte(body), int64(len(body)))
	}
	get := func(name, route string, q url.Values) *httpResult {
		return raw("GET", "/sandboxes/"+name+"/"+route+"?"+q.Encode(), nil, 0)
	}
	upper := func(name string) string {
		d, _ := le.m.defs.get(le.k, name)
		cur, _ := le.m.CurDir(le.k, d)
		return filepath.Join(cur, "upper")
	}
	// (every file lives below /work or another directory: fuse-overlayfs
	// wedges on a file created directly in its root — WP-15a's note)

	t.Run("stat, list and ranged content", func(t *testing.T) {
		r := put("sb-1", "/work/a.txt", url.Values{"mkdirs": {"1"}}, "hello, world")
		r.want(t, http.StatusOK, "") // a file call starts the sandbox (autoStart)
		st := decodeRes[fileStat](t, r)
		if st.Type != "file" || st.Size != 12 || st.Mode != "0644" || st.ETag == "" {
			t.Fatalf("the write's stat: %+v", st)
		}
		if again := decodeRes[fileStat](t, get("sb-1", "files/stat", url.Values{"path": {"/work/a.txt"}})); again != st {
			t.Fatalf("stat %+v, the write's %+v", again, st)
		}
		l := decodeRes[fileList](t, get("sb-1", "files/list", url.Values{"path": {"/work"}}))
		if len(l.Entries) != 1 || l.Entries[0].Name != "a.txt" || l.Entries[0].Size != 12 {
			t.Fatalf("list: %+v", l)
		}
		r = get("sb-1", "files/content", url.Values{"path": {"/work/a.txt"}, "offset": {"7"}, "length": {"5"}})
		r.want(t, http.StatusOK, "")
		if string(r.body) != "world" || r.header.Get("ETag") != `"`+st.ETag+`"` {
			t.Fatalf("ranged read: %q %v", r.body, r.header)
		}
		if b, _ := os.ReadFile(filepath.Join(upper("sb-1"), "work", "a.txt")); string(b) != "hello, world" {
			t.Fatalf("the upper: %q", b)
		}
	})

	t.Run("atomic writes and their preconditions", func(t *testing.T) {
		host := filepath.Join(upper("sb-1"), "work", "a.txt")
		var before, after unix.Stat_t
		_ = unix.Stat(host, &before)
		r := get("sb-1", "files/content", url.Values{"path": {"/work/a.txt"}})
		header := r.header.Get("ETag")
		r = put("sb-1", "/work/a.txt", url.Values{"ifMatch": {header}}, "v2") // the header's ETag, quoted
		r.want(t, http.StatusOK, "")
		e2 := decodeRes[fileStat](t, r).ETag
		_ = unix.Stat(host, &after)
		if before.Ino == after.Ino || `"`+e2+`"` == header {
			t.Fatalf("not replaced atomically: inode %d → %d, etag %s → %s", before.Ino, after.Ino, header, e2)
		}
		r = put("sb-1", "/work/a.txt", url.Values{"ifMatch": {strings.Trim(header, `"`)}}, "lost")
		r.want(t, http.StatusPreconditionFailed, RefPrecondition)
		var eb errorBody
		if _ = json.Unmarshal(r.body, &eb); eb.ETag != e2 {
			t.Fatalf("412: %+v, want the etag %s", eb, e2)
		}
		st := decodeRes[fileStat](t, get("sb-1", "files/stat", url.Values{"path": {"/work/a.txt"}}))
		put("sb-1", "/work/a.txt", url.Values{"ifMatch": {st.ETag}}, "v3").want(t, http.StatusOK, "") // the stat's, bare
		put("sb-1", "/work/a.txt", url.Values{"ifNoneMatch": {"*"}}, "x").want(t, http.StatusPreconditionFailed, RefPrecondition)
		put("sb-1", "/work/b.txt", url.Values{"ifNoneMatch": {"*"}}, "b").want(t, http.StatusOK, "")
		put("sb-1", "/work/deep/er/c.txt", nil, "c").want(t, http.StatusNotFound, RefNotFound)
		put("sb-1", "/work/deep/er/c.txt", url.Values{"mkdirs": {"1"}}, "c").want(t, http.StatusOK, "")
		if b, _ := os.ReadFile(host); string(b) != "v3" {
			t.Fatalf("the file: %q", b)
		}
		raw("PUT", "/sandboxes/sb-1/files/content?path=/work/big", []byte("x"), fileMax+1).want(t, http.StatusRequestEntityTooLarge, RefTooLarge)
	})

	t.Run("a tar round trip keeps symlinks; entries land inside", func(t *testing.T) {
		in := tarOf(t, [][2]string{{"t/", "dir"}, {"t/f", "F"}, {"t/link", "-> f"}, {"t/abs", "-> /etc/hostname"},
			{"../escaped", "!"}, {"/rooted", "R"}})
		raw("PUT", "/sandboxes/sb-1/tar?path=/work/in&mkdirs=1", in, int64(len(in))).want(t, http.StatusNoContent, "")
		r := get("sb-1", "tar", url.Values{"path": {"/work/in"}})
		r.want(t, http.StatusOK, "")
		got := untar(t, r.body)
		for k, v := range map[string]string{"t/f": "F", "t/link": "-> f", "t/abs": "-> /etc/hostname", "rooted": "R"} {
			if got[k] != v {
				t.Fatalf("%s: %q, want %q (%v)", k, got[k], v, got)
			}
		}
		get("sb-1", "files/stat", url.Values{"path": {"/work/escaped"}}).want(t, http.StatusNotFound, RefNotFound)
		if _, err := os.Lstat(filepath.Join(upper("sb-1"), "work", "escaped")); err == nil {
			t.Fatal("../escaped landed outside the directory")
		}
	})

	t.Run("a symlink leads inside the sandbox, never to the host", func(t *testing.T) {
		var rnd [6]byte
		_, _ = rand.Read(rnd[:])
		name := "xbin-tilesbx-" + hex.EncodeToString(rnd[:])
		in := tarOf(t, [][2]string{{"l", "-> /etc"}})
		raw("PUT", "/sandboxes/sb-1/tar?path=/work", in, int64(len(in))).want(t, http.StatusNoContent, "")
		put("sb-1", "/work/l/"+name, nil, "the sandbox's").want(t, http.StatusOK, "")
		if _, err := os.Lstat("/etc/" + name); err == nil {
			os.Remove("/etc/" + name)
			t.Fatalf("the write reached the host's /etc/%s", name)
		}
		if b, _ := os.ReadFile(filepath.Join(upper("sb-1"), "etc", name)); string(b) != "the sandbox's" {
			t.Fatalf("the sandbox's /etc/%s: %q", name, b)
		}
		if r := get("sb-1", "files/content", url.Values{"path": {"/etc/" + name}}); string(r.body) != "the sandbox's" {
			t.Fatalf("read back: %d %q", r.code, r.body)
		}
	})

	t.Run("a read-only mount refuses", func(t *testing.T) {
		put("sb-1", "/mnt/ro/x", nil, "no").want(t, http.StatusBadRequest, RefInvalid)
		if ents, _ := os.ReadDir(le.ro); len(ents) != 0 {
			t.Fatalf("the reader's resource holds %v", ents)
		}
		put("sb-1", "/mnt/work/x", nil, "yes").want(t, http.StatusOK, "")
		if b, _ := os.ReadFile(filepath.Join(le.work, "x")); string(b) != "yes" {
			t.Fatalf("the writer's resource: %q", b)
		}
	})

	t.Run("copy between two sandboxes", func(t *testing.T) {
		cp := func(as auth.Principal, from, fp, to, tp string) *httpResult {
			w := le.do(as, "POST", "/sandboxes/copy", map[string]any{"from": map[string]any{"sandbox": from, "path": fp},
				"to": map[string]any{"sandbox": to, "path": tp}})
			return &httpResult{code: w.Code, body: w.Body.Bytes(), header: w.Header()}
		}
		cp(mgr, "sb-1", "/work/in/t", "sb-2", "/work/t").want(t, http.StatusNoContent, "") // sb-2 starts for it
		l := decodeRes[fileList](t, get("sb-2", "files/list", url.Values{"path": {"/work/t"}}))
		ents := map[string]fileEntry{}
		for _, e := range l.Entries {
			ents[e.Name] = e
		}
		if ents["f"].Size != 1 || ents["link"].Type != "symlink" || ents["link"].Target != "f" || ents["abs"].Target != "/etc/hostname" {
			t.Fatalf("the copy: %+v", l)
		}
		cp(mgr, "sb-1", "/work/a.txt", "sb-2", "/work/a.txt").want(t, http.StatusNoContent, "")
		if r := get("sb-2", "files/content", url.Values{"path": {"/work/a.txt"}}); string(r.body) != "v3" {
			t.Fatalf("the file copied: %q", r.body)
		}
		cp(mgr, "sb-1", "/work/a.txt", "sb-2", "/work/a.txt").want(t, http.StatusPreconditionFailed, RefPrecondition)
		cp(mgr2, "sb-1", "/work/a.txt", "sb-2", "/work/x").want(t, http.StatusNotFound, RefNotFound) // another tile's
	})

	t.Run("a tar of / leaves out /proc, /sys and /dev", func(t *testing.T) {
		r := get("sb-1", "tar", url.Values{"path": {"/"}, "exclude": {"opt"}})
		r.want(t, http.StatusOK, "")
		got := untar(t, r.body)
		for k := range got {
			if strings.HasPrefix(k, "proc/") || strings.HasPrefix(k, "sys/") || strings.HasPrefix(k, "dev/") {
				t.Fatalf("a tar of / holds %s", k)
			}
		}
		if got["work/a.txt"] != "v3" {
			t.Fatalf("a tar of /: %d entries, work/a.txt %q", len(got), got["work/a.txt"])
		}
	})
}

// httpResult is a recorded answer.
type httpResult struct {
	code   int
	body   []byte
	header http.Header
}

func (r *httpResult) want(t *testing.T, code int, refusal string) {
	t.Helper()
	if r.code != code {
		t.Fatalf("status %d, want %d: %s", r.code, code, r.body)
	}
	if refusal != "" && !strings.Contains(string(r.body), `"refusal":"`+refusal+`"`) {
		t.Fatalf("answer %s, want refusal %q", r.body, refusal)
	}
}

func decodeRes[T any](t *testing.T, r *httpResult) T {
	t.Helper()
	r.want(t, http.StatusOK, "")
	var v T
	if err := json.Unmarshal(r.body, &v); err != nil {
		t.Fatalf("%v: %s", err, r.body)
	}
	return v
}
