//go:build linux

package tilesbx

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/sandbox"
	"github.com/xbin-dev/xbin/internal/sandbox/agentcore"
)

// filesEnv runs sandboxes on the fake launcher, whose agent serves file
// operations beneath a temp dir: roots has each sandbox's, as of its last
// start.
type filesEnv struct {
	*fakeEnv
	roots    map[string]string
	starting string // the sandbox the launcher is starting
}

type launcherFunc func(*sandbox.Spec, LaunchOpts) (Proc, error)

func (f launcherFunc) Start(s *sandbox.Spec, o LaunchOpts) (Proc, error) { return f(s, o) }

func newFilesEnv(t *testing.T, mut ...func(*Options)) *filesEnv {
	fe := &filesEnv{fakeEnv: newFakeEnv(t, mut...), roots: map[string]string{}}
	fe.m.launcher = launcherFunc(func(s *sandbox.Spec, o LaunchOpts) (Proc, error) {
		fe.starting = s.Hostname
		return fe.l.Start(s, o)
	})
	fe.l.mod = func(o *agentcore.Options) { fe.roots[fe.starting] = o.Root }
	return fe
}

// req calls the runtime as p with a raw body (nil: none) and its length
// (-1: unknown, as a chunked upload).
func (fe *filesEnv) req(p auth.Principal, method, target string, body io.Reader, n int64) *httptest.ResponseRecorder {
	return doRaw(fe.testEnv, p, method, target, body, n)
}

// doRaw is testEnv.do with a raw body. A response the handler cut part-way
// (http.ErrAbortHandler) answers statusCut.
func doRaw(e *testEnv, p auth.Principal, method, target string, body io.Reader, n int64) (w *httptest.ResponseRecorder) {
	e.t.Helper()
	r := httptest.NewRequest(method, target, body)
	r.ContentLength = n
	r = r.WithContext(auth.WithPrincipal(r.Context(), p))
	w = httptest.NewRecorder()
	defer func() {
		if v := recover(); v == http.ErrAbortHandler {
			w.Code = statusCut
		} else if v != nil {
			panic(v)
		}
	}()
	e.mux.ServeHTTP(w, r)
	return w
}

const statusCut = 599 // doRaw: the handler cut the response

func (fe *filesEnv) put(name, path string, q url.Values, body string) *httptest.ResponseRecorder {
	fe.t.Helper()
	if q == nil {
		q = url.Values{}
	}
	q.Set("path", path)
	return fe.req(mgr, "PUT", "/sandboxes/"+name+"/files/content?"+q.Encode(), strings.NewReader(body), int64(len(body)))
}

func (fe *filesEnv) query(name, route string, q url.Values) *httptest.ResponseRecorder {
	fe.t.Helper()
	return fe.req(mgr, "GET", "/sandboxes/"+name+"/"+route+"?"+q.Encode(), nil, 0)
}

func (fe *filesEnv) stat(name, path string) fileStat {
	fe.t.Helper()
	w := fe.query(name, "files/stat", url.Values{"path": {path}})
	fe.want(w, http.StatusOK, "")
	return decodeAs[fileStat](fe.t, w)
}

func decodeAs[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("%v: %s", err, w.Body.String())
	}
	return v
}

func etagOf(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var eb errorBody
	if err := json.Unmarshal(w.Body.Bytes(), &eb); err != nil {
		t.Fatal(err)
	}
	return eb.ETag
}

// Stat, ranged reads, atomic writes with their preconditions, listings,
// mkdir, move and remove — through the routes, on a sandbox a file call
// starts (autoStart).
func TestFiles(t *testing.T) {
	fe := newFilesEnv(t)
	fe.create(ns("sb-1"))
	w := fe.put("sb-1", "/work/a.txt", url.Values{"mkdirs": {"1"}}, "hello")
	fe.want(w, http.StatusOK, "")
	if fe.fakeEnv.get("sb-1").State != StateRunning {
		t.Fatal("a file call didn't start the sandbox")
	}
	root := fe.roots["sb-1"]
	if b, _ := os.ReadFile(filepath.Join(root, "work", "a.txt")); string(b) != "hello" {
		t.Fatalf("the file: %q", b)
	}
	put := decodeAs[fileStat](t, w)
	st := fe.stat("sb-1", "/work/a.txt")
	if st.Path != "/work/a.txt" || st.Type != "file" || st.Size != 5 || st.Mode != "0644" || st.ETag == "" || st.ETag != put.ETag || st.MtimeMs == 0 {
		t.Fatalf("stat %+v, put %+v", st, put)
	}

	// content: the bytes, the stat's etag as the ETag (quoted), ranges
	w = fe.query("sb-1", "files/content", url.Values{"path": {"/work/a.txt"}})
	fe.want(w, http.StatusOK, "")
	if w.Body.String() != "hello" || w.Header().Get("ETag") != `"`+st.ETag+`"` || w.Header().Get("Content-Length") != "5" {
		t.Fatalf("read %q %v", w.Body.String(), w.Header())
	}
	w = fe.query("sb-1", "files/content", url.Values{"path": {"/work/a.txt"}, "offset": {"1"}, "length": {"3"}})
	if w.Body.String() != "ell" || w.Header().Get("Content-Length") != "3" {
		t.Fatalf("ranged %q %v", w.Body.String(), w.Header())
	}
	if w = fe.query("sb-1", "files/content", url.Values{"path": {"/work/a.txt"}, "offset": {"9"}}); w.Code != 200 || w.Body.Len() != 0 {
		t.Fatalf("past the end: %d %q", w.Code, w.Body.String())
	}
	w = fe.req(mgr, "HEAD", "/sandboxes/sb-1/files/content?path=/work/a.txt", nil, 0)
	if w.Code != 200 || w.Body.Len() != 0 || w.Header().Get("ETag") != `"`+st.ETag+`"` || w.Header().Get("Content-Length") != "5" {
		t.Fatalf("HEAD: %d %q %v", w.Code, w.Body.String(), w.Header())
	}
	fe.want(fe.query("sb-1", "files/content", url.Values{"path": {"/work/a.txt"}, "offset": {"-1"}}), 400, RefInvalid)
	fe.want(fe.query("sb-1", "files/content", url.Values{"path": {"/work/none"}}), 404, RefNotFound)
	fe.want(fe.query("sb-1", "files/content", url.Values{"path": {"/work"}}), 400, RefInvalid)

	// ifMatch takes the header's ETag, quoted, and the stat's, bare
	header := w.Header().Get("ETag")
	w = fe.put("sb-1", "/work/a.txt", url.Values{"ifMatch": {`"` + st.ETag + `"`}}, "hello, world")
	fe.want(w, http.StatusOK, "")
	e2 := decodeAs[fileStat](t, w).ETag
	if e2 == st.ETag || header != `"`+st.ETag+`"` {
		t.Fatalf("an atomic replace kept its etag %q (header %q)", e2, header)
	}
	w = fe.put("sb-1", "/work/a.txt", url.Values{"ifMatch": {st.ETag}}, "lost update")
	fe.want(w, http.StatusPreconditionFailed, RefPrecondition)
	if etagOf(t, w) != e2 {
		t.Fatalf("412's etag %q, want the current %q", etagOf(t, w), e2)
	}
	fe.want(fe.put("sb-1", "/work/a.txt", url.Values{"ifMatch": {e2}}, "v3"), http.StatusOK, "")
	if b, _ := os.ReadFile(filepath.Join(root, "work", "a.txt")); string(b) != "v3" {
		t.Fatalf("after the writes: %q", b)
	}
	// ifNoneMatch=* creates only; mode; mkdirs
	w = fe.put("sb-1", "/work/a.txt", url.Values{"ifNoneMatch": {"*"}}, "x")
	fe.want(w, http.StatusPreconditionFailed, RefPrecondition)
	if etagOf(t, w) == "" {
		t.Fatal("412 without the current etag")
	}
	w = fe.put("sb-1", "/work/b.sh", url.Values{"ifNoneMatch": {"*"}, "mode": {"0755"}}, "#!/bin/sh\n")
	fe.want(w, http.StatusOK, "")
	if st := decodeAs[fileStat](t, w); st.Mode != "0755" {
		t.Fatalf("mode: %+v", st)
	}
	fe.want(fe.put("sb-1", "/work/new/c.txt", nil, "x"), http.StatusNotFound, RefNotFound)
	for _, q := range []url.Values{{"ifNoneMatch": {"etag"}}, {"ifNoneMatch": {"*"}, "ifMatch": {"x"}}, {"mode": {"9"}},
		{"mode": {"17777"}}, {"mkdirs": {"yes"}}, {"ifMatch": {strings.Repeat("a", 200)}}} {
		fe.want(fe.put("sb-1", "/work/x", q, "x"), http.StatusBadRequest, RefInvalid)
	}

	// list
	w = fe.query("sb-1", "files/list", url.Values{"path": {"/work"}})
	fe.want(w, http.StatusOK, "")
	l := decodeAs[fileList](t, w)
	if l.Path != "/work" || l.Truncated || len(l.Entries) != 2 || l.Entries[0].Name != "a.txt" || l.Entries[1].Name != "b.sh" ||
		l.Entries[1].Mode != "0755" || l.Entries[0].Size != 2 || l.Entries[0].Type != "file" {
		t.Fatalf("list %+v", l)
	}
	if l := decodeAs[fileList](t, fe.query("sb-1", "files/list", url.Values{"path": {"/work"}, "limit": {"1"}})); !l.Truncated || len(l.Entries) != 1 {
		t.Fatalf("list limit=1: %+v", l)
	}
	fe.want(fe.query("sb-1", "files/list", url.Values{"path": {"/work"}, "limit": {"0"}}), 400, RefInvalid)
	fe.want(fe.query("sb-1", "files/list", url.Values{"path": {"/work/a.txt"}}), 400, RefInvalid)

	// mkdir, move, remove
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/files/mkdir", map[string]any{"path": "/work/d"}), http.StatusNoContent, "")
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/files/mkdir", map[string]any{"path": "/work/d"}), 400, RefInvalid)
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/files/mkdir", map[string]any{"path": "/work/d/e/f", "parents": true}), http.StatusNoContent, "")
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/files/move", map[string]any{"from": "/work/a.txt", "to": "/work/d/a.txt"}), http.StatusNoContent, "")
	fe.want(fe.query("sb-1", "files/stat", url.Values{"path": {"/work/a.txt"}}), 404, RefNotFound)
	w = fe.do(mgr, "POST", "/sandboxes/sb-1/files/move", map[string]any{"from": "/work/b.sh", "to": "/work/d/a.txt"})
	fe.want(w, http.StatusPreconditionFailed, RefPrecondition)
	if etagOf(t, w) != fe.stat("sb-1", "/work/d/a.txt").ETag {
		t.Fatal("a move's 412 doesn't carry the destination's etag")
	}
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/files/move", map[string]any{"from": "/work/b.sh", "to": "/work/d/a.txt", "overwrite": true}), http.StatusNoContent, "")
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/files/remove", map[string]any{"path": "/work/d"}), 400, RefInvalid)
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/files/remove", map[string]any{"path": "/work/d", "recursive": true}), http.StatusNoContent, "")
	fe.want(fe.query("sb-1", "files/stat", url.Values{"path": {"/work/d"}}), 404, RefNotFound)
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/files/remove", map[string]any{}), 400, RefInvalid)
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/files/remove", nil), 400, RefInvalid)
}

// Paths are absolute, clean, UTF-8 and short enough for the agent's
// request line: anything else is 400 before the agent hears of it.
func TestFilePaths(t *testing.T) {
	fe := newFilesEnv(t)
	fe.create(ns("sb-1"))
	bad := []string{"", "work", "./work", "/work/", "//work", "/work/../etc", "/work/./x", "/a\x00b", "/a\xffb",
		"/" + strings.Repeat("a", 4100), "/" + strings.Repeat("<", 1000)}
	for _, p := range bad {
		fe.want(fe.query("sb-1", "files/stat", url.Values{"path": {p}}), 400, RefInvalid)
	}
	if n := fe.l.count(); n != 0 {
		t.Fatalf("a refused path started the sandbox (%d starts)", n)
	}
	// a long path the line holds reaches the agent (which finds nothing)
	fe.want(fe.query("sb-1", "files/stat", url.Values{"path": {"/" + strings.Repeat("a/", 1900) + "a"}}), 404, RefNotFound)
	// a move's two paths count together
	long := "/" + strings.Repeat("b", 2100)
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/files/move", map[string]any{"from": long, "to": long + "c"}), 400, RefInvalid)
	// excludes too
	q := url.Values{"path": {"/"}}
	for i := 0; i < 40; i++ {
		q.Add("exclude", strings.Repeat("x", 100))
	}
	fe.want(fe.query("sb-1", "tar", q), 400, RefInvalid)
	fe.want(fe.query("sb-1", "tar", url.Values{"path": {"/"}, "exclude": {"["}}), 400, RefInvalid)
}

// A body past fileMax is 413: at once by its Content-Length, or once the
// stream passes it — and nothing is committed.
func TestFileMax(t *testing.T) {
	fe := newFilesEnv(t)
	fe.create(ns("sb-1"))
	fe.want(fe.put("sb-1", "/f", nil, "x"), http.StatusOK, "") // (the kernel overlay's root: fine in the fake)
	w := fe.req(mgr, "PUT", "/sandboxes/sb-1/files/content?path=/big", strings.NewReader("x"), fileMax+1)
	fe.want(w, http.StatusRequestEntityTooLarge, RefTooLarge)
	w = fe.req(mgr, "PUT", "/sandboxes/sb-1/files/content?path=/big", io.LimitReader(zeroReader{}, fileMax+1), -1)
	fe.want(w, http.StatusRequestEntityTooLarge, RefTooLarge)
	waitEntries(t, fe.roots["sb-1"], 1)
	// a stale ifMatch is refused before the body is sent
	w = fe.req(mgr, "PUT", "/sandboxes/sb-1/files/content?path=/f&ifMatch=nope", io.LimitReader(zeroReader{}, 8<<20), -1)
	fe.want(w, http.StatusPreconditionFailed, RefPrecondition)
	// a body that breaks off commits nothing
	w = fe.req(mgr, "PUT", "/sandboxes/sb-1/files/content?path=/cut", io.MultiReader(strings.NewReader("part"), errReader{}), -1)
	fe.want(w, http.StatusBadRequest, RefInvalid)
	waitEntries(t, fe.roots["sb-1"], 1)
}

// waitEntries waits (the agent cleans up once it notices a dropped
// connection) until dir holds n entries.
func waitEntries(t *testing.T, dir string, n int) {
	t.Helper()
	for i := 0; ; i++ {
		ents, _ := os.ReadDir(dir)
		if len(ents) == n {
			return
		}
		if i == 200 {
			t.Fatalf("%s holds %v, want %d entries", dir, ents, n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) { clear(p); return len(p), nil }

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("the client went away") }

// untar reads a tar into name → (type, body or link target).
func untar(t *testing.T, b []byte) map[string]string {
	t.Helper()
	out := map[string]string{}
	tr := tar.NewReader(bytes.NewReader(b))
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatalf("tar: %v", err)
		}
		body, _ := io.ReadAll(tr)
		switch h.Typeflag {
		case tar.TypeSymlink:
			out[h.Name] = "-> " + h.Linkname
		case tar.TypeDir:
			out[h.Name] = "dir"
		default:
			out[h.Name] = string(body)
		}
	}
}

// tarOf builds a tar: name → body, "-> target" for a symlink, "dir".
func tarOf(t *testing.T, ents [][2]string) []byte {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range ents {
		h := &tar.Header{Name: e[0], Mode: 0o644, Typeflag: tar.TypeReg, Size: int64(len(e[1]))}
		switch {
		case e[1] == "dir":
			h.Typeflag, h.Mode, h.Size = tar.TypeDir, 0o755, 0
		case strings.HasPrefix(e[1], "-> "):
			h.Typeflag, h.Linkname, h.Size = tar.TypeSymlink, e[1][3:], 0
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			_, _ = tw.Write([]byte(e[1]))
		}
	}
	_ = tw.Close()
	return buf.Bytes()
}

// A tar goes in (never outside its directory) and comes back out with its
// symlinks as links; a tar of / leaves out /proc, /sys and /dev.
func TestTar(t *testing.T) {
	fe := newFilesEnv(t)
	fe.create(ns("sb-1"))
	in := tarOf(t, [][2]string{{"src/", "dir"}, {"src/a.txt", "A"}, {"src/sub/b.o", "B"}, {"src/link", "-> a.txt"},
		{"src/abs", "-> /etc/passwd"}, {"../evil", "!"}, {"/rooted", "R"}})
	w := fe.req(mgr, "PUT", "/sandboxes/sb-1/tar?path=/dst&mkdirs=1", bytes.NewReader(in), int64(len(in)))
	fe.want(w, http.StatusNoContent, "")
	root := fe.roots["sb-1"]
	if _, err := os.Lstat(filepath.Join(root, "evil")); err == nil {
		t.Fatal("../evil landed outside the directory")
	}
	if b, _ := os.ReadFile(filepath.Join(root, "dst", "rooted")); string(b) != "R" {
		t.Fatalf("an absolute entry: %q", b)
	}
	w = fe.query("sb-1", "tar", url.Values{"path": {"/dst/src"}, "exclude": {"*.o"}})
	fe.want(w, http.StatusOK, "")
	if w.Header().Get("Content-Type") != "application/x-tar" {
		t.Fatalf("content type %q", w.Header().Get("Content-Type"))
	}
	got := untar(t, w.Body.Bytes())
	want := map[string]string{"a.txt": "A", "link": "-> a.txt", "abs": "-> /etc/passwd", "sub/": "dir"}
	if len(got) != len(want) {
		t.Fatalf("tar-get: %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("tar-get %s: %q, want %q (%v)", k, got[k], v, got)
		}
	}
	fe.want(fe.query("sb-1", "tar", url.Values{"path": {"/dst/src/a.txt"}}), 400, RefInvalid)
	fe.want(fe.query("sb-1", "tar", url.Values{"path": {"/none"}}), 404, RefNotFound)
	w = fe.req(mgr, "PUT", "/sandboxes/sb-1/tar?path=/x", strings.NewReader("x"), tarMax+1)
	fe.want(w, http.StatusRequestEntityTooLarge, RefTooLarge)
	junk := strings.Repeat("not a tar ", 100)
	fe.want(fe.req(mgr, "PUT", "/sandboxes/sb-1/tar?path=/dst", strings.NewReader(junk), int64(len(junk))), 400, RefInvalid)

	// the root
	for _, d := range []string{"proc/1", "sys/kernel", "dev/pts"} {
		_ = os.MkdirAll(filepath.Join(root, d), 0o755)
	}
	w = fe.query("sb-1", "tar", url.Values{"path": {"/"}})
	fe.want(w, http.StatusOK, "")
	got = untar(t, w.Body.Bytes())
	for k := range got {
		if strings.HasPrefix(k, "proc/") || strings.HasPrefix(k, "sys/") || strings.HasPrefix(k, "dev/") {
			t.Fatalf("a tar of / holds %s", k)
		}
	}
	if got["dst/src/a.txt"] != "A" {
		t.Fatalf("a tar of /: %v", got)
	}
}

// A tar that fails part-way is cut: the client never sees it end cleanly.
func TestTarCutPartWay(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root reads a mode-000 directory")
	}
	fe := newFilesEnv(t)
	fe.create(ns("sb-1"))
	fe.want(fe.put("sb-1", "/d/a/x", url.Values{"mkdirs": {"1"}}, strings.Repeat("x", 1<<20)), http.StatusOK, "")
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/files/mkdir", map[string]any{"path": "/d/b"}), http.StatusNoContent, "")
	locked := filepath.Join(fe.roots["sb-1"], "d", "b")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(locked, 0o755)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fe.mux.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), mgr)))
	}))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/sandboxes/sb-1/tar?path=/d")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if _, err := io.ReadAll(resp.Body); err == nil {
		t.Fatal("a tar that failed part-way ended cleanly")
	}
}

// Copies between two sandboxes of one tile — a tree, a file, through
// symlinks — keep symlinks, honour overwrite, and never reach another
// tile's sandbox.
func TestCopy(t *testing.T) {
	fe := newFilesEnv(t)
	fe.create(ns("sb-1"))
	fe.create(ns("sb-2"))
	in := tarOf(t, [][2]string{{"a.txt", "A"}, {"sub/b.txt", "B"}, {"link", "-> a.txt"}})
	fe.want(fe.req(mgr, "PUT", "/sandboxes/sb-1/tar?path=/src&mkdirs=1", bytes.NewReader(in), int64(len(in))), http.StatusNoContent, "")
	cp := func(p auth.Principal, from, fp, to, tp string, overwrite bool) *httptest.ResponseRecorder {
		return fe.do(p, "POST", "/sandboxes/copy", map[string]any{"from": map[string]any{"sandbox": from, "path": fp},
			"to": map[string]any{"sandbox": to, "path": tp}, "overwrite": overwrite})
	}
	fe.want(cp(mgr, "sb-1", "/src", "sb-2", "/deep/copy", false), http.StatusNoContent, "")
	r2 := fe.roots["sb-2"]
	if b, _ := os.ReadFile(filepath.Join(r2, "deep", "copy", "sub", "b.txt")); string(b) != "B" {
		t.Fatalf("the tree: %q", b)
	}
	if l, _ := os.Readlink(filepath.Join(r2, "deep", "copy", "link")); l != "a.txt" {
		t.Fatalf("the symlink: %q", l)
	}
	w := cp(mgr, "sb-1", "/src", "sb-2", "/deep/copy", false)
	fe.want(w, http.StatusPreconditionFailed, RefPrecondition)
	if etagOf(t, w) == "" {
		t.Fatal("412 without the destination's etag")
	}
	fe.want(fe.put("sb-1", "/src/a.txt", nil, "A2"), http.StatusOK, "")
	fe.want(cp(mgr, "sb-1", "/src", "sb-2", "/deep/copy", true), http.StatusNoContent, "")
	if b, _ := os.ReadFile(filepath.Join(r2, "deep", "copy", "a.txt")); string(b) != "A2" {
		t.Fatalf("overwritten: %q", b)
	}
	// a file; a symlink to a file; a symlink to a directory
	fe.want(cp(mgr, "sb-1", "/src/a.txt", "sb-2", "/f/a.txt", false), http.StatusNoContent, "")
	fe.want(cp(mgr, "sb-1", "/src/a.txt", "sb-2", "/f/a.txt", false), http.StatusPreconditionFailed, RefPrecondition)
	fe.want(cp(mgr, "sb-1", "/src/link", "sb-2", "/f/a.txt", true), http.StatusNoContent, "")
	if b, _ := os.ReadFile(filepath.Join(r2, "f", "a.txt")); string(b) != "A2" {
		t.Fatalf("the file: %q", b)
	}
	fe.want(fe.req(mgr, "PUT", "/sandboxes/sb-1/tar?path=/src", bytes.NewReader(tarOf(t, [][2]string{{"dirlink", "-> sub"}})), -1), http.StatusNoContent, "")
	fe.want(cp(mgr, "sb-1", "/src/dirlink", "sb-2", "/viadir", false), http.StatusNoContent, "")
	if b, _ := os.ReadFile(filepath.Join(r2, "viadir", "b.txt")); string(b) != "B" {
		t.Fatalf("through a symlinked directory: %q", b)
	}
	// within one sandbox; into itself; what isn't there
	fe.want(cp(mgr, "sb-1", "/src/sub", "sb-1", "/other", false), http.StatusNoContent, "")
	fe.want(cp(mgr, "sb-1", "/src", "sb-1", "/src/in", false), 400, RefInvalid)
	fe.want(cp(mgr, "sb-1", "/", "sb-1", "/x", false), 400, RefInvalid)
	fe.want(cp(mgr, "sb-1", "/none", "sb-2", "/x", false), 404, RefNotFound)
	fe.want(cp(mgr, "sb-1", "/src", "sb-9", "/x", false), 404, RefNotFound)
	fe.want(cp(mgr, "sb-1", "src", "sb-2", "/x", false), 400, RefInvalid)
	fe.want(cp(mgr, "Bad", "/src", "sb-2", "/x", false), 400, RefInvalid)
	// another tile's: apps/mgr2 holds the cap and names only its own
	fe.want(cp(mgr2, "sb-1", "/src", "sb-1", "/x", false), 404, RefNotFound)
	w = fe.do(mgr2, "POST", "/sandboxes", ns("sb-3"))
	fe.want(w, http.StatusCreated, "")
	fe.want(cp(mgr2, "sb-3", "/", "sb-1", "/x", false), 404, RefNotFound)
	fe.want(cp(admin, "sb-1", "/src", "sb-2", "/x", false), 403, RefNotAllowed)
}

var mgr2 = auth.Principal{Component: "apps/mgr2", Via: "instance"}

// A file call on a sandbox that isn't running: started with autoStart,
// 409 state without it.
func TestFilesNeedRunning(t *testing.T) {
	fe := newFilesEnv(t)
	fe.create(map[string]any{"name": "sb-1", "mode": "namespace", "autoStart": false})
	w := fe.query("sb-1", "files/stat", url.Values{"path": {"/"}})
	fe.want(w, http.StatusConflict, RefState)
	if eb := decodeAs[errorBody](t, w); eb.State != StateStopped {
		t.Fatalf("the refusal: %+v", eb)
	}
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/start", nil), http.StatusOK, "")
	fe.want(fe.query("sb-1", "files/stat", url.Values{"path": {"/"}}), http.StatusOK, "")
	// a failed auto-start says why
	fe.create(ns("sb-2"))
	fe.l.fail = errors.New("no namespaces here")
	w = fe.query("sb-2", "files/stat", url.Values{"path": {"/"}})
	fe.want(w, http.StatusConflict, RefState)
	if !strings.Contains(w.Body.String(), "no namespaces here") {
		t.Fatalf("the refusal: %s", w.Body.String())
	}
}

// blockingWriter holds a response's first body write until released.
type blockingWriter struct {
	*httptest.ResponseRecorder
	once    sync.Once
	blocked chan struct{}
	release chan struct{}
}

func (b *blockingWriter) Write(p []byte) (int, error) {
	b.once.Do(func() { close(b.blocked); <-b.release })
	return b.ResponseRecorder.Write(p)
}

// A tar stream that runs longer than idleStopMin holds the idle timer off
// for all of it (the sandbox has an operation under way until its last
// byte moved), and its end is the sandbox's latest activity.
func TestFileOpHoldsIdle(t *testing.T) {
	var mu sync.Mutex
	clock := time.UnixMilli(1790000000000)
	now := func() time.Time { mu.Lock(); defer mu.Unlock(); return clock }
	fe := newFilesEnv(t, func(o *Options) { o.Now = now })
	fe.create(ns("sb-1"))
	fe.want(fe.put("sb-1", "/big", nil, strings.Repeat("x", 4<<20)), http.StatusOK, "")
	fe.m.mu.Lock()
	b := fe.m.live[fe.k]["sb-1"]
	fe.m.mu.Unlock()
	held := func() (int, int64) {
		fe.m.mu.Lock()
		defer fe.m.mu.Unlock()
		return b.inflight, b.lastActive
	}
	if n, _ := held(); n != 0 {
		t.Fatalf("in flight after the write: %d", n)
	}
	bw := &blockingWriter{ResponseRecorder: httptest.NewRecorder(), blocked: make(chan struct{}), release: make(chan struct{})}
	r := httptest.NewRequest("GET", "/sandboxes/sb-1/tar?path=/", nil)
	r = r.WithContext(auth.WithPrincipal(r.Context(), mgr))
	done := make(chan struct{})
	go func() { fe.mux.ServeHTTP(bw, r); close(done) }()
	<-bw.blocked
	start := now().UnixMilli()
	idle := time.Duration(fe.fakeEnv.get("sb-1").IdleStopMin) * time.Minute
	mu.Lock()
	clock = clock.Add(idle + time.Minute)
	mu.Unlock()
	if n, last := held(); n != 1 || last != start {
		t.Fatalf("mid-stream: %d in flight, lastActive %d (the stream began at %d)", n, last, start)
	}
	close(bw.release)
	<-done
	if n, last := held(); n != 0 || last != now().UnixMilli() {
		t.Fatalf("after the stream: %d in flight, lastActive %d, want %d", n, last, now().UnixMilli())
	}
	if got := untar(t, bw.Body.Bytes()); len(got["big"]) != 4<<20 {
		t.Fatalf("the tar: %d entries", len(got))
	}
}
