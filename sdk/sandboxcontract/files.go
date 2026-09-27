package sandboxcontract

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// --- files ------------------------------------------------------------------------------

var fileChecks = []check{
	{"content", func(t *testing.T, e *env) {
		a := e.as("a")
		sb := a.Create(map[string]any{"name": "files"})
		id, wd := sb.ID, sb.Workdir
		if st := a.Stat(id, wd); st.Type != "dir" || st.Path != wd {
			t.Fatalf("stat workdir: %+v", st)
		}
		a.Refused("GET", "/sandboxes/"+id+"/files/stat?path="+q(wd+"/none"), nil, 404, "not-found")
		st1 := a.Put(id, wd+"/a.txt", "one", "")
		if st1.Type != "file" || st1.Size != 3 || st1.ETag == "" || st1.Path != wd+"/a.txt" {
			t.Fatalf("PUT: %+v", st1)
		}
		var body []byte
		hdr := a.Call("GET", "/sandboxes/"+id+"/files/content?path="+q(wd+"/a.txt"), nil, 200, &body)
		if string(body) != "one" || strings.Trim(hdr.Get("ETag"), `"`) != st1.ETag {
			t.Fatalf("GET: %q etag %s, want %s", body, hdr.Get("ETag"), st1.ETag)
		}
		if st := a.Stat(id, wd+"/a.txt"); st.ETag != st1.ETag {
			t.Fatalf("stat etag %s, PUT's %s", st.ETag, st1.ETag)
		}
		if st2 := a.Put(id, wd+"/a.txt", "two", ""); st2.ETag == st1.ETag {
			t.Fatal("the etag didn't change with the content")
		}
		// ranged reads
		a.Put(id, wd+"/digits", "0123456789", "")
		for rng, want := range map[string]string{"offset=2&length=3": "234", "offset=8": "89", "length=2": "01", "offset=20": ""} {
			var b []byte
			a.Call("GET", "/sandboxes/"+id+"/files/content?path="+q(wd+"/digits")+"&"+rng, nil, 200, &b)
			if string(b) != want {
				t.Errorf("%s: %q, want %q", rng, b, want)
			}
		}
		// mkdirs, mode
		if st := a.Put(id, wd+"/d1/d2/f", "deep", "&mkdirs=1&mode=755"); st.Size != 4 {
			t.Fatalf("PUT mkdirs: %+v", st)
		}
		if m, err := strconv.ParseUint(a.Stat(id, wd+"/d1/d2/f").Mode, 8, 32); err != nil || m != 0o755 {
			t.Fatalf("mode: %o %v", m, err)
		}
		a.Refused("PUT", "/sandboxes/"+id+"/files/content?path="+q(wd+"/x/y/f"), []byte("x"), 404, "not-found")
		a.Refused("GET", "/sandboxes/"+id+"/files/content?path="+q(wd+"/none"), nil, 404, "not-found")
		// symlinks resolve inside the sandbox
		a.Sh(id, "ln -s a.txt link")
		if st := a.Stat(id, wd+"/link"); st.Type != "symlink" || st.Target != "a.txt" {
			t.Fatalf("stat of a symlink: %+v", st)
		}
		if got := a.Read(id, wd+"/link"); got != "two" {
			t.Fatalf("read through a symlink: %q", got)
		}
	}},
	{"conditional", func(t *testing.T, e *env) {
		a := e.as("a")
		sb := a.Create(map[string]any{"name": "cond"})
		id, p := sb.ID, sb.Workdir+"/a.txt"
		st1 := a.Put(id, p, "v1", "")
		st2 := a.Put(id, p, "v2", "&ifMatch="+st1.ETag)
		r := a.Refused("PUT", "/sandboxes/"+id+"/files/content?path="+q(p)+"&ifMatch="+st1.ETag, []byte("v3"), 412, "precondition")
		if r.ETag != st2.ETag {
			t.Fatalf("412 carries the current etag %s: %+v", st2.ETag, r)
		}
		a.Put(id, p, "v3", "&ifMatch="+q(`"`+st2.ETag+`"`)) // as the ETag header quotes it
		if r := a.Refused("PUT", "/sandboxes/"+id+"/files/content?path="+q(p)+"&ifNoneMatch=*", []byte("v4"), 412, "precondition"); r.ETag == "" {
			t.Fatalf("ifNoneMatch=* on a file: %+v", r)
		}
		a.Put(id, sb.Workdir+"/new.txt", "fresh", "&ifNoneMatch=*")
		if got := a.Read(id, p); got != "v3" {
			t.Fatalf("content: %q", got)
		}
	}},
	{"tree", func(t *testing.T, e *env) {
		a := e.as("a")
		sb := a.Create(map[string]any{"name": "tree"})
		id, wd := sb.ID, sb.Workdir
		post := func(op string, body map[string]any, want int) {
			t.Helper()
			a.Call("POST", "/sandboxes/"+id+"/files/"+op, body, want, nil)
		}
		a.Put(id, wd+"/a", "A", "")
		a.Put(id, wd+"/b", "B", "")
		post("mkdir", map[string]any{"path": wd + "/sub"}, http.StatusNoContent)
		post("mkdir", map[string]any{"path": wd + "/p/q/r", "parents": true}, http.StatusNoContent)
		a.Refused("POST", "/sandboxes/"+id+"/files/mkdir", map[string]any{"path": wd + "/x/y"}, 404, "not-found")
		var l struct {
			Path    string
			Entries []struct {
				Name, Type, Mode string
				Size, MtimeMs    int64
			}
			Truncated bool
		}
		a.Call("GET", "/sandboxes/"+id+"/files/list?path="+q(wd), nil, 200, &l)
		got := map[string]string{}
		for _, e := range l.Entries {
			got[e.Name] = e.Type
		}
		if l.Path != wd || l.Truncated || len(got) != 4 || got["a"] != "file" || got["b"] != "file" || got["sub"] != "dir" || got["p"] != "dir" {
			t.Fatalf("list: %+v", l)
		}
		a.Call("GET", "/sandboxes/"+id+"/files/list?path="+q(wd)+"&limit=1", nil, 200, &l)
		if len(l.Entries) != 1 || !l.Truncated {
			t.Fatalf("list limit=1: %+v", l)
		}
		// remove
		post("remove", map[string]any{"path": wd + "/a"}, http.StatusNoContent)
		a.Refused("GET", "/sandboxes/"+id+"/files/stat?path="+q(wd+"/a"), nil, 404, "not-found")
		if resp, _, _ := a.Do(context.Background(), "POST", "/sandboxes/"+id+"/files/remove", map[string]any{"path": wd + "/p"}); resp.StatusCode < 400 {
			t.Fatalf("removing a non-empty directory without recursive: %d", resp.StatusCode)
		}
		post("remove", map[string]any{"path": wd + "/p", "recursive": true}, http.StatusNoContent)
		a.Refused("POST", "/sandboxes/"+id+"/files/remove", map[string]any{"path": wd + "/p"}, 404, "not-found")
		// move
		post("move", map[string]any{"from": wd + "/b", "to": wd + "/sub/c"}, http.StatusNoContent)
		if got := a.Read(id, wd+"/sub/c"); got != "B" {
			t.Fatalf("moved: %q", got)
		}
		a.Refused("GET", "/sandboxes/"+id+"/files/stat?path="+q(wd+"/b"), nil, 404, "not-found")
		a.Put(id, wd+"/d", "D", "")
		if resp, _, _ := a.Do(context.Background(), "POST", "/sandboxes/"+id+"/files/move", map[string]any{"from": wd + "/sub/c", "to": wd + "/d"}); resp.StatusCode < 400 {
			t.Fatalf("a move onto a file without overwrite: %d", resp.StatusCode)
		}
		if got := a.Read(id, wd+"/d"); got != "D" {
			t.Fatalf("a refused move changed the target: %q", got)
		}
		post("move", map[string]any{"from": wd + "/sub/c", "to": wd + "/d", "overwrite": true}, http.StatusNoContent)
		if got := a.Read(id, wd+"/d"); got != "B" {
			t.Fatalf("overwritten: %q", got)
		}
		a.Refused("POST", "/sandboxes/"+id+"/files/move", map[string]any{"from": wd + "/none", "to": wd + "/e"}, 404, "not-found")
	}},
	{"paths", func(t *testing.T, e *env) {
		a := e.as("a")
		sb := a.Create(map[string]any{"name": "paths"})
		id, wd := sb.ID, sb.Workdir
		for _, p := range []string{"", "work/x", "./x"} { // paths are absolute
			under := func(n string) string { // a path under p: none under "" ("/n" would be absolute)
				if p == "" {
					return ""
				}
				return p + "/" + n
			}
			a.Refused("GET", "/sandboxes/"+id+"/files/stat?path="+q(p), nil, 400, "invalid")
			a.Refused("GET", "/sandboxes/"+id+"/files/list?path="+q(p), nil, 400, "invalid")
			a.Refused("PUT", "/sandboxes/"+id+"/files/content?path="+q(under("f")), []byte("x"), 400, "invalid")
			a.Refused("POST", "/sandboxes/"+id+"/files/mkdir", map[string]any{"path": under("d")}, 400, "invalid")
		}
		a.Put(id, wd+"/f", "x", "")
		a.Refused("POST", "/sandboxes/"+id+"/files/move", map[string]any{"from": wd + "/f", "to": "f2"}, 400, "invalid")
	}},
	{"too-large", func(t *testing.T, e *env) {
		f, _ := e.fresh(Knobs{FileMax: 1024})
		a := f.as("a")
		fmax := f.hello.Limits["fileMax"]
		if fmax > 256<<20 {
			t.Skipf("limits.fileMax is %d: too big to write past here (give the Target a Fresh with the FileMax knob)", fmax)
		}
		sb := a.Create(map[string]any{"name": "big"})
		id, wd := sb.ID, sb.Workdir
		a.Refused("PUT", "/sandboxes/"+id+"/files/content?path="+q(wd+"/f"), bytes.Repeat([]byte("x"), int(fmax)+1), 413, "too-large")
		a.Put(id, wd+"/f", strings.Repeat("x", int(fmax)), "")
		a.Sh(id, fmt.Sprintf("head -c %d /dev/zero > big", 2*fmax))
		a.Refused("GET", "/sandboxes/"+id+"/files/content?path="+q(wd+"/big"), nil, 413, "too-large")
		var b []byte
		a.Call("GET", "/sandboxes/"+id+"/files/content?path="+q(wd+"/big")+fmt.Sprintf("&offset=%d&length=%d", fmax, fmax/2), nil, 200, &b)
		if int64(len(b)) != fmax/2 {
			t.Fatalf("a ranged read of a big file: %d bytes", len(b))
		}
	}},
}

// --- trees (tar) -------------------------------------------------------------------------

type tarEntry struct {
	typ  byte
	body string
	link string
}

func untar(t *testing.T, b []byte) map[string]tarEntry {
	t.Helper()
	out := map[string]tarEntry{}
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
		out[h.Name] = tarEntry{h.Typeflag, string(body), h.Linkname}
	}
}

var tarChecks = []check{
	{"get-put", func(t *testing.T, e *env) {
		a := e.as("a")
		sb := a.Create(map[string]any{"name": "tar"})
		id, wd := sb.ID, sb.Workdir
		a.Put(id, wd+"/t/a.txt", "A", "&mkdirs=1")
		a.Put(id, wd+"/t/sub/b.txt", "B", "&mkdirs=1")
		a.Put(id, wd+"/t/node_modules/x", "X", "&mkdirs=1")
		a.Sh(id, "ln -s a.txt t/l")
		var tb []byte
		hdr := a.Call("GET", "/sandboxes/"+id+"/tar?path="+q(wd+"/t")+"&exclude=node_modules", nil, 200, &tb)
		if hdr.Get("Content-Type") != "application/x-tar" {
			t.Fatalf("content type %q", hdr.Get("Content-Type"))
		}
		ents := untar(t, tb)
		if e := ents["a.txt"]; e.typ != tar.TypeReg || e.body != "A" {
			t.Fatalf("a.txt: %+v in %v", e, ents)
		}
		if e := ents["sub/b.txt"]; e.body != "B" {
			t.Fatalf("sub/b.txt: %+v in %v", e, ents)
		}
		if e := ents["l"]; e.typ != tar.TypeSymlink || e.link != "a.txt" {
			t.Fatalf("l: %+v", e)
		}
		for name := range ents {
			if strings.HasPrefix(name, "node_modules") || strings.HasPrefix(name, "/") || strings.HasPrefix(name, "t/") {
				t.Fatalf("entry %q (want names relative to path, the excluded left out)", name)
			}
		}
		// PUT: extracted under path; entries that climb out of it are skipped
		// (an absolute name may be skipped or land under path)
		var buf bytes.Buffer
		tw := tar.NewWriter(&buf)
		tr := tar.NewReader(bytes.NewReader(tb))
		for {
			h, err := tr.Next()
			if err != nil {
				break
			}
			_ = tw.WriteHeader(h)
			_, _ = io.Copy(tw, tr)
		}
		for _, name := range []string{"../evil", "a/../../evil2", "/abs"} {
			_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: 1, Typeflag: tar.TypeReg})
			_, _ = tw.Write([]byte("!"))
		}
		_ = tw.Close()
		a.Call("PUT", "/sandboxes/"+id+"/tar?path="+q(wd+"/u")+"&mkdirs=1", buf.Bytes(), http.StatusNoContent, nil)
		if got := a.Read(id, wd+"/u/sub/b.txt"); got != "B" {
			t.Fatalf("extracted: %q", got)
		}
		if st := a.Stat(id, wd+"/u/l"); st.Type != "symlink" || st.Target != "a.txt" {
			t.Fatalf("an extracted symlink: %+v", st)
		}
		for _, p := range []string{wd + "/evil", wd + "/evil2"} {
			a.Refused("GET", "/sandboxes/"+id+"/files/stat?path="+q(p), nil, 404, "not-found")
		}
		a.Refused("PUT", "/sandboxes/"+id+"/tar?path="+q(wd+"/missing"), buf.Bytes(), 404, "not-found")
		a.Refused("GET", "/sandboxes/"+id+"/tar?path="+q(wd+"/none"), nil, 404, "not-found")
		a.Refused("GET", "/sandboxes/"+id+"/tar?path="+q(wd+"/u/sub/b.txt"), nil, 400, "invalid") // not a directory
	}},
}
