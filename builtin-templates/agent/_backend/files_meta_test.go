package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// D136: a session file knows its hash, its source and the version it
// replaced, and keeps earlier versions for file_diff; an old row (no meta)
// reads as unknown, never wrong.
func TestFileHashProvenance(t *testing.T) {
	ag := newTestAgent(t, newTestDB(t))
	bindSbx(t, "apps/cs")
	r, cfg, _ := sbxRun(t, ag, "p", "none")
	mustTool(t, ag, r, cfg, "w1", "file_write", map[string]any{"path": "a.txt", "content": "one\ntwo\n"})
	f, err := ag.db.replFile(r.ID, "a.txt")
	if err != nil || f.SHA256 != shaHex([]byte("one\ntwo\n")) || f.Source == nil || f.Source.Tool != "file_write" || f.Source.Call != "w1" || f.Parent != 0 {
		t.Fatalf("after file_write: %+v %+v %v", f, f.Source, err)
	}
	mustTool(t, ag, r, cfg, "e1", "file_edit", map[string]any{"path": "a.txt", "old_string": "two", "new_string": "2"})
	f, _ = ag.db.replFile(r.ID, "a.txt")
	if f.Version != 2 || f.Parent != 1 || f.Source.Tool != "file_edit" || f.Source.Call != "e1" {
		t.Fatalf("after file_edit: %+v %+v", f, f.Source)
	}
	if v, err := ag.db.fileVersion(r.ID, "a.txt", 1); err != nil || v.Content != "one\ntwo\n" || v.Source.Tool != "file_write" {
		t.Fatalf("v1 kept: %+v %v", v, err)
	}
	if _, err := ag.db.fileVersion(r.ID, "a.txt", 7); err == nil || !strings.Contains(err.Error(), "earlier ones kept: v1") {
		t.Fatalf("a version not kept: %v", err)
	}
	// an upload says so; the same content twice is flagged
	if _, err := ag.acceptUpload(context.Background(), r.ID, "b.txt", "text/plain", strings.NewReader("one\n2\n")); err != nil {
		t.Fatal(err)
	}
	info := mustTool(t, ag, r, cfg, "i", "file_info", map[string]any{"path": "a.txt"})
	for _, want := range []string{"session:a.txt — session file", "sha256 " + shaHex([]byte("one\n2\n")), "source: written by file_edit",
		"replaced v1", "same content as: b.txt", "v1 · 8 B · sha " + shortSHA(shaHex([]byte("one\ntwo\n"))) + " · written by file_write"} {
		if !strings.Contains(info, want) {
			t.Fatalf("file_info lacks %q:\n%s", want, info)
		}
	}
	list := mustTool(t, ag, r, cfg, "l", "file_list", nil)
	if !strings.Contains(list, "b.txt (6 B) · v1 · sha "+shortSHA(shaHex([]byte("one\n2\n")))+" · uploaded by a person · same content as a.txt") {
		t.Fatalf("file_list:\n%s", list)
	}
	// a row from before D136 (or an older backend's write): unknown, computed on demand
	if _, err := ag.db.q.Exec(`UPDATE repl_files SET version=version+1 WHERE run_id=? AND path='a.txt'`, r.ID); err != nil {
		t.Fatal(err)
	}
	f, _ = ag.db.replFile(r.ID, "a.txt")
	if f.SHA256 != "" || f.Source != nil || ag.fileSHA(context.Background(), f) != shaHex([]byte("one\n2\n")) {
		t.Fatalf("stale meta must read as unknown: %+v", f)
	}
	if info := mustTool(t, ag, r, cfg, "i2", "file_info", map[string]any{"path": "a.txt"}); !strings.Contains(info, "source: not recorded") {
		t.Fatalf("file_info of an old row:\n%s", info)
	}
	// deleting a file drops its history; deleting the run too
	if _, err := ag.db.replDeleteFileBlobs(r.ID, "a.txt"); err != nil {
		t.Fatal(err)
	}
	if vs := ag.db.fileVersions(r.ID, "a.txt"); len(vs) != 0 {
		t.Fatalf("versions outlive their file: %d", len(vs))
	}
}

// sandbox_download overwrites in place: the same file again writes nothing
// (etag, then sha256), a changed one is the next version with the old one
// kept, keep_both is the old suffix; binary files are replaced in place too.
func TestSandboxDownloadInPlace(t *testing.T) {
	ag := newTestAgent(t, newTestDB(t))
	bindSbx(t, "apps/cs")
	r, cfg, box := sbxRun(t, ag, "d", "none")
	hp := put(t, box, "out/report.html", "<h1>v1</h1>\n<p>a</p>\n")
	out := mustTool(t, ag, r, cfg, "d1", "sandbox_download", map[string]any{"path": "out/report.html"})
	if !strings.HasPrefix(out, "downloaded "+box.Workdir+"/out/report.html to the session file report.html (text, ") {
		t.Fatalf("first: %q", out)
	}
	f, _ := ag.db.replFile(r.ID, "report.html")
	if f.Source == nil || f.Source.Kind != "sandbox" || f.Source.Path != box.Workdir+"/out/report.html" || f.Source.ETag == "" || f.Source.Call != "d1" {
		t.Fatalf("its source: %+v", f.Source)
	}
	if out := mustTool(t, ag, r, cfg, "d2", "sandbox_download", map[string]any{"path": "out/report.html"}); !strings.HasPrefix(out, "unchanged: the session file report.html already holds") {
		t.Fatalf("again: %q", out)
	}
	// the same bytes written again: a new etag, the same hash
	time.Sleep(20 * time.Millisecond)
	if err := os.WriteFile(hp, []byte("<h1>v1</h1>\n<p>a</p>\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out := mustTool(t, ag, r, cfg, "d3", "sandbox_download", map[string]any{"path": "out/report.html"}); !strings.HasPrefix(out, "unchanged:") {
		t.Fatalf("rewritten, same content: %q", out)
	}
	put(t, box, "out/report.html", "<h1>v2</h1>\n<p>a</p>\n")
	out = mustTool(t, ag, r, cfg, "d4", "sandbox_download", map[string]any{"path": "out/report.html"})
	if !strings.Contains(out, "session file report.html (text, 21 B) — v2, replacing v1, copied from the sandbox") || !strings.Contains(out, `file_diff {"a": "report.html"}`) {
		t.Fatalf("changed: %q", out)
	}
	files, _ := ag.db.replFiles(r.ID)
	if len(files) != 1 {
		t.Fatalf("no -2 copies: %d files", len(files))
	}
	diff := mustTool(t, ag, r, cfg, "df", "file_diff", map[string]any{"a": "report.html"})
	if !strings.Contains(diff, "-<h1>v1</h1>\n+<h1>v2</h1>\n <p>a</p>") || !strings.Contains(diff, "session:report.html@1") {
		t.Fatalf("file_diff of the two versions:\n%s", diff)
	}
	// the sandbox copy changing is flagged
	put(t, box, "out/report.html", "<h1>v3</h1>\n")
	if list := mustTool(t, ag, r, cfg, "l", "file_list", nil); !strings.Contains(list, "changed in the sandbox since") {
		t.Fatalf("file_list misses the drift:\n%s", list)
	}
	if out := mustTool(t, ag, r, cfg, "d5", "sandbox_download", map[string]any{"path": "out/report.html", "keep_both": true}); !strings.Contains(out, "session file report-2.html") {
		t.Fatalf("keep_both: %q", out)
	}
	// a binary file, in place: the old object is kept as the earlier version
	png := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{1}, 50)...)
	put(t, box, "chart.png", string(png))
	mustTool(t, ag, r, cfg, "b1", "sandbox_download", map[string]any{"path": "chart.png"})
	put(t, box, "chart.png", string(append(png, 2)))
	if out := mustTool(t, ag, r, cfg, "b2", "sandbox_download", map[string]any{"path": "chart.png"}); !strings.Contains(out, "(image/png, 59 B) — v2, replacing v1") {
		t.Fatalf("binary in place: %q", out)
	}
	vs := ag.db.fileVersions(r.ID, "chart.png")
	if len(vs) != 1 || !vs[0].Binary || vs[0].SHA256 != shaHex(png) {
		t.Fatalf("the earlier binary version: %+v", vs)
	}
	if blobs := ag.db.runBlobs([]int64{r.ID}); len(blobs) != 2 {
		t.Fatalf("both objects are the run's: %v", blobs)
	}
	if out := mustTool(t, ag, r, cfg, "bd", "file_diff", map[string]any{"a": "chart.png"}); !strings.HasPrefix(out, "binary content differs") {
		t.Fatalf("binary diff: %q", out)
	}
}

// C1: a bare key or session:x is a session file, /… and ./… a sandbox file
// (when one is bound); a miss says where the file is instead.
func TestFilePaths(t *testing.T) {
	for _, c := range []struct {
		in   string
		sbx  bool
		want fileRef
		err  string
	}{
		{"report.html", true, fileRef{key: "report.html"}, ""},
		{"session:report.html", true, fileRef{key: "report.html"}, ""},
		{"session:report.html@3", true, fileRef{key: "report.html", ver: 3}, ""},
		{"data/x.json", true, fileRef{key: "data/x.json"}, ""},
		{"/work/r.html", true, fileRef{sandbox: true, path: "/work/r.html"}, ""},
		{"./r.html", true, fileRef{sandbox: true, path: "./r.html"}, ""},
		{"~/r.html", true, fileRef{sandbox: true, path: "~/r.html"}, ""},
		{"sandbox:r.html", true, fileRef{sandbox: true, path: "r.html"}, ""},
		{"file:///work/r.html", true, fileRef{sandbox: true, path: "/work/r.html"}, ""},
		{"./r.html", false, fileRef{key: "r.html"}, ""}, // as it always was
		{"/work/r.html", false, fileRef{}, "no sandbox is bound"},
		{"sandbox:r.html", false, fileRef{}, "no sandbox is bound"},
		{"session:../x", true, fileRef{}, "bad path"},
	} {
		got, err := parseFileRef(c.in, c.sbx)
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Fatalf("%q (sbx %v): %v, want %q", c.in, c.sbx, err, c.err)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Fatalf("%q (sbx %v): %+v %v, want %+v", c.in, c.sbx, got, err, c.want)
		}
	}

	ag := newTestAgent(t, newTestDB(t))
	bindSbx(t, "apps/cs")
	r, cfg, box := sbxRun(t, ag, "c1", "none")
	put(t, box, "notes.md", "# in the sandbox\n")
	mustTool(t, ag, r, cfg, "w", "file_write", map[string]any{"path": "todo.md", "content": "- a\n- b\n"})
	if out := mustTool(t, ag, r, cfg, "r1", "file_read", map[string]any{"path": "./notes.md"}); !strings.Contains(out, "1\t# in the sandbox") {
		t.Fatalf("a sandbox path: %q", out)
	}
	if out := mustTool(t, ag, r, cfg, "r2", "file_read", map[string]any{"path": "session:todo.md"}); out != "- a\n- b\n" {
		t.Fatalf("session:x: %q", out)
	}
	_, err := tool(t, ag, r, cfg, "r3", "file_read", map[string]any{"path": "notes.md"})
	if err == nil || !strings.Contains(err.Error(), `no such file "notes.md" — not a session file, but the sandbox has `+box.Workdir+"/notes.md") {
		t.Fatalf("a session miss the sandbox has: %v", err)
	}
	_, err = tool(t, ag, r, cfg, "r4", "file_read", map[string]any{"path": "./todo.md"})
	if err == nil || !strings.Contains(err.Error(), "doesn't exist in the sandbox — session:todo.md is a session file") {
		t.Fatalf("a sandbox miss the session has: %v", err)
	}
	// file_diff across the two places, and an identical pair
	put(t, box, "todo.md", "- a\n- c\n")
	diff := mustTool(t, ag, r, cfg, "d", "file_diff", map[string]any{"a": "todo.md", "b": "./todo.md"})
	if !strings.Contains(diff, "--- session:todo.md\n+++ "+box.Workdir+"/todo.md\n@@ -1,2 +1,2 @@\n - a\n-- b\n+- c") {
		t.Fatalf("across places:\n%s", diff)
	}
	put(t, box, "todo.md", "- a\n- b\n")
	if out := mustTool(t, ag, r, cfg, "d2", "file_diff", map[string]any{"a": "session:todo.md", "b": box.Workdir + "/todo.md"}); !strings.HasPrefix(out, "identical:") {
		t.Fatalf("identical: %q", out)
	}
	if out := mustTool(t, ag, r, cfg, "i", "file_info", map[string]any{"path": "./todo.md"}); !strings.Contains(out, "sha256 "+shaHex([]byte("- a\n- b\n"))+" (text)") {
		t.Fatalf("file_info of a sandbox file:\n%s", out)
	}
	// render_html and file_view take sandbox paths, copying them in place
	put(t, box, "site/index.html", "<h1>hi</h1>")
	out := mustTool(t, ag, r, cfg, "h1", "render_html", map[string]any{"path": "./site/index.html"})
	if !strings.Contains(out, "downloaded "+box.Workdir+"/site/index.html to the session file index.html") || !strings.Contains(out, "rendered index.html (11 B, v1)") {
		t.Fatalf("render_html of a sandbox path: %q", out)
	}
	if out := mustTool(t, ag, r, cfg, "h2", "render_html", map[string]any{"path": "./site/index.html"}); !strings.HasPrefix(out, "unchanged: the session file index.html") {
		t.Fatalf("again: %q", out)
	}
	steps, _ := ag.db.steps(r.ID)
	renders := 0
	for _, s := range steps {
		if s.Kind == "render" {
			renders++
		}
	}
	if renders != 2 {
		t.Fatalf("render steps: %d", renders)
	}
	put(t, box, "chart.png", "\x89PNG\r\n\x1a\nxxxx")
	cfg.Features = map[string]bool{"streaming": false, "vision": true}
	out = mustTool(t, ag, r, cfg, "v", "file_view", map[string]any{"path": "./chart.png"})
	if !strings.HasPrefix(out, "Showing chart.png (image/png, 12 B)") {
		t.Fatalf("file_view of a sandbox path: %q", out)
	}
	if got := shownImages(tcall("v", "file_view", `{"path":"./chart.png"}`), out); len(got) != 1 || got[0] != "chart.png" {
		t.Fatalf("the image it shows: %v", got)
	}
	// an old file_view result (a bare key) still shows its image
	if got := shownImages(tcall("", "file_view", `{"path":"photo.png"}`), "Showing photo.png (image/png, 3 KB) — the image is attached after these tool results."); len(got) != 1 || got[0] != "photo.png" {
		t.Fatalf("an old result: %v", got)
	}

	// no sandbox: a sandbox path says so
	plain := defaultConfig()
	pr, _ := ag.startRunOpts(runOpts{Title: "p", Cfg: plain, Hold: true})
	if _, err := tool(t, ag, pr, plain, "x", "file_read", map[string]any{"path": "/work/x"}); err == nil || !strings.Contains(err.Error(), "no sandbox is bound") {
		t.Fatalf("no sandbox: %v", err)
	}
}

func TestUnifiedDiff(t *testing.T) {
	a := "1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n11\n12\n"
	b := "1\n2\nthree\n4\n5\n6\n7\n8\n9\n10\n11\n12\n13\n"
	out, rm, add := unifiedDiff("a", "b", a, b)
	want := "--- a\n+++ b\n@@ -1,6 +1,6 @@\n 1\n 2\n-3\n+three\n 4\n 5\n 6\n@@ -10,3 +10,4 @@\n 10\n 11\n 12\n+13\n"
	if out != want || rm != 1 || add != 2 {
		t.Fatalf("got (-%d +%d):\n%s\nwant:\n%s", rm, add, out, want)
	}
	if out, _, _ := unifiedDiff("a", "b", "", "x\n"); out != "--- a\n+++ b\n@@ -0,0 +1 @@\n+x\n" {
		t.Fatalf("from empty:\n%s", out)
	}
}

// browser_check with a fake runner: the request it gets, the result shaped
// for the model, screenshots as session files it is shown, blocked requests
// named, and a sandbox without Node said so.
func TestBrowserCheckFake(t *testing.T) {
	old := browserLaunch
	t.Cleanup(func() { browserLaunch = old })
	browserLaunch = `d=$(printf %s "$1" | sed -n 's/.*"out_dir":"\([^"]*\)".*/\1/p')
u=$(printf %s "$1" | sed -n 's/.*"url":"\([^"]*\)".*/\1/p')
mkdir -p "$d" && printf %s "$1" > "$d.req.json"
printf '\211PNG\r\n\032\nfake' > "$d/shot-1-0ms.png"
printf 'noise\n` + browserMark + `{"ok":true,"node":"v22","url":"%s","title":"T","status":200,"load":{"state":"load","ms":12},"console":[{"type":"log","text":"hi"},{"type":"error","text":"boom","location":"x:1:1"}],"page_errors":[{"message":"x is not defined"}],"requests_failed":[{"url":"https://cdn.example.com/x.js","error":"net::ERR_NAME_NOT_RESOLVED"},{"url":"http://localhost:9/a","error":"net::ERR_CONNECTION_REFUSED"}],"snapshot":"- heading \\"Hi\\"","screenshots":[{"at_ms":0,"path":"%s/shot-1-0ms.png","bytes":12}],"script":{"value":{"n":3}}}\n' "$u" "$d"`
	ag := newTestAgent(t, newTestDB(t))
	bindSbx(t, "apps/cs")
	r, cfg, box := sbxRun(t, ag, "bc", "none")
	put(t, box, "index.html", "<h1>Hi</h1>")
	if !strings.Contains(specLine(cfg, 0), " browser_check ") {
		t.Fatal("the coding class with a sandbox gets browser_check")
	}
	if !sandboxSideEffect("browser_check", Config{Sandbox: &SandboxBinding{Egress: "internet"}}) {
		t.Fatal("with egress it is a side effect")
	}
	for _, c := range []struct {
		args map[string]any
		err  string
	}{
		{map[string]any{}, "needs a target"},
		{map[string]any{"target": "./index.html", "screenshots_ms": []any{1, 2, 3, 4, 5}}, "at most 4 screenshots"},
		{map[string]any{"target": "./index.html", "script": strings.Repeat("x", browserScriptMax+1)}, "keep it under"},
		{map[string]any{"target": "./nope.html"}, "doesn't exist in the sandbox"},
		{map[string]any{"target": "nope.html"}, `no such file "nope.html"`},
	} {
		if _, err := tool(t, ag, r, cfg, "x", "browser_check", c.args); err == nil || !strings.Contains(err.Error(), c.err) {
			t.Fatalf("%v: %v, want %q", c.args, err, c.err)
		}
	}
	out := mustTool(t, ag, r, cfg, "bc1", "browser_check", map[string]any{"target": "./index.html", "wait_ms": 500, "screenshots_ms": []any{0},
		"viewport": map[string]any{"width": 390, "height": 844}, "script": "return 1"})
	head := "browser_check file://" + box.Workdir + "/index.html — loaded in 12 ms · status 200 · 2 console message(s), 1 error(s) · 1 page error(s) · 2 failed request(s)\n" +
		browserShotsLead + "shots/index-0ms.png\n"
	if !strings.HasPrefix(out, head) {
		t.Fatalf("the headline:\n%s", out)
	}
	for _, want := range []string{`"file": "shots/index-0ms.png"`, `"blocked": "sandbox egress: this sandbox has no network`, `"n": 3`, `"title": "T"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("the result lacks %q:\n%s", want, out)
		}
	}
	if strings.Count(out, `"blocked"`) != 1 {
		t.Fatalf("localhost is never an egress block:\n%s", out)
	}
	if strings.Contains(out, "/shot-1-0ms.png") {
		t.Fatalf("the sandbox path of a copied screenshot is dropped:\n%s", out)
	}
	f, err := ag.db.replFile(r.ID, "shots/index-0ms.png")
	if err != nil || !f.Binary || f.Mime != "image/png" || f.Source == nil || f.Source.Kind != "browser" || f.Source.AtMs != 0 || f.Source.Call != "bc1" {
		t.Fatalf("the screenshot's session file: %+v %v", f, err)
	}
	if got := shownImages(tcall("bc1", "browser_check", ""), out); len(got) != 1 || got[0] != "shots/index-0ms.png" {
		t.Fatalf("the screenshots it shows: %v", got)
	}
	reqs, _ := filepath.Glob(filepath.Join(box.Home, ".cache/xbin-browser/out/*.req.json"))
	if len(reqs) != 1 {
		t.Fatalf("the request: %v", reqs)
	}
	rb, _ := os.ReadFile(reqs[0])
	for _, want := range []string{`"wait_ms":500`, `"screenshots_ms":[0]`, `"viewport":{"height":844,"width":390}`, `"script":"return 1"`, `"budget_ms":`} {
		if !strings.Contains(string(rb), want) {
			t.Fatalf("the runner's request lacks %s: %s", want, rb)
		}
	}
	if left, _ := filepath.Glob(filepath.Join(box.Home, ".cache/xbin-browser/out/*/shot-*.png")); len(left) != 0 {
		t.Fatalf("screenshots left in the sandbox: %v", left)
	}
	if _, err := os.Stat(filepath.Join(box.Home, ".cache/xbin-browser", browserRunnerName)); err != nil {
		t.Fatalf("the runner is in the sandbox: %v", err)
	}
	// a session file is copied in; the default is one screenshot at the end
	mustTool(t, ag, r, cfg, "w", "file_write", map[string]any{"path": "page.html", "content": "<p>x</p>"})
	mustTool(t, ag, r, cfg, "bc2", "browser_check", map[string]any{"target": "session:page.html"})
	if b, err := os.ReadFile(filepath.Join(box.Home, ".cache/xbin-browser/session", itoa(r.ID), "page.html")); err != nil || string(b) != "<p>x</p>" {
		t.Fatalf("the session file in the sandbox: %q %v", b, err)
	}
	// no Node: said so
	browserLaunch = `printf '\n` + browserMark + `{"ok":false,"error":"node-missing"}\n'; exit 127`
	if _, err := tool(t, ag, r, cfg, "bc3", "browser_check", map[string]any{"target": "./index.html"}); err == nil || !strings.Contains(err.Error(), "browser_check is not available in this sandbox") || !strings.Contains(err.Error(), "no Node.js") {
		t.Fatalf("no node: %v", err)
	}
	browserLaunch = `echo oops >&2; exit 3`
	if _, err := tool(t, ag, r, cfg, "bc4", "browser_check", map[string]any{"target": "./index.html"}); err == nil || !strings.Contains(err.Error(), "exited 3 without an answer:\noops") {
		t.Fatalf("a runner that dies: %v", err)
	}
}

// The real runner, where this machine has Node and Playwright
// (PLAYWRIGHT_DIR: a node_modules with playwright; browsers in
// ~/.cache/ms-playwright): an inline script writing the DOM, a
// console.error, a failed fetch, a module import that can't load.
func TestBrowserCheckRealRunner(t *testing.T) {
	pw := os.Getenv("PLAYWRIGHT_DIR")
	home, _ := os.UserHomeDir()
	if pw == "" || !exists(filepath.Join(pw, "node_modules", "playwright")) || !exists(filepath.Join(home, ".cache", "ms-playwright")) {
		t.Skip("no PLAYWRIGHT_DIR with playwright, or no ~/.cache/ms-playwright")
	}
	ag := newTestAgent(t, newTestDB(t))
	bindSbx(t, "apps/cs")
	r, cfg, box := sbxRun(t, ag, "real", "none")
	cfg.ToolTimeout = 110
	if err := os.Symlink(filepath.Join(pw, "node_modules"), filepath.Join(box.Workdir, "node_modules")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(box.Home, ".cache"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(home, ".cache", "ms-playwright"), filepath.Join(box.Home, ".cache", "ms-playwright")); err != nil {
		t.Fatal(err)
	}
	put(t, box, "site/index.html", `<!doctype html><title>Runner check</title><h1>Hello</h1><p id="out">static</p>
<script>
document.getElementById('out').textContent = 'written by script';
console.error('boom from the page');
fetch('http://127.0.0.1:9/nothing').catch(() => {});
</script>
<script type="module">import x from 'https://cdn.invalid/x.mjs'; x();</script>`)
	out := mustTool(t, ag, r, cfg, "real1", "browser_check", map[string]any{"target": "./site/index.html", "wait_ms": 300, "screenshots_ms": []any{0, 300},
		"script": "return await page.locator('#out').textContent()"})
	for _, want := range []string{`"title": "Runner check"`, `boom from the page`, `paragraph: written by script`, `https://cdn.invalid/x.mjs`,
		`"blocked": "sandbox egress`, `"value": "written by script"`, browserShotsLead + "shots/index-0ms.png, shots/index-300ms.png"} {
		if !strings.Contains(out, want) {
			t.Fatalf("lacks %q:\n%s", want, out)
		}
	}
	if f, err := ag.db.replFile(r.ID, "shots/index-300ms.png"); err != nil || f.Bytes < 1000 {
		t.Fatalf("a real screenshot: %+v %v", f, err)
	}
}

// D136's tools state their scope in their first sentence (after D134's
// tooldesc_test.go): pinned, so a change to one is a reviewed change.
func TestD136ToolDescriptionFirstSentences(t *testing.T) {
	want := map[string]string{
		"browser_check": "Load a page in a real headless Chromium inside your sandbox and report what happened: " +
			"console, errors, failed requests, an accessibility snapshot, screenshots.",
		"sandbox_download": "Copy a file from the coding sandbox into the session files (up to 16 MB), to keep it with the conversation or hand it to people.",
		"file_read":        "Read a session file, or — given a sandbox path (/…, ./…) — a file in the coding sandbox, as its read does.",
		"file_info": "Describe one file — a session file (its sha256, source, the version it replaced, earlier versions kept, " +
			"whether its sandbox copy changed since) or a sandbox file (size, mode, etag, sha256, its session copies).",
		"file_diff": "A unified diff (diff -u) of two text files, each a session file (a version of one: session:x@2) or a sandbox file.",
	}
	cfg := defaultConfig()
	cfg.Class = "coding"
	b := SandboxBinding{Ref: "apps/cs|sb-1", Name: "box", Egress: "none"}
	cfg.Sandbox, cfg.Attached = &b, []SandboxBinding{b}
	got := map[string]string{}
	for _, s := range toolSpecs(cfg, 0, nil) {
		got[s.Function.Name] = s.Function.Description
	}
	for name, w := range want {
		if f := firstSentence(got[name]); f != w {
			t.Errorf("%s's first sentence changed:\n got: %s\nwant: %s", name, f, w)
		}
	}
	// without a sandbox, file_read reads session files only
	for _, s := range toolSpecs(defaultConfig(), 0, nil) {
		if s.Function.Name == "file_read" && firstSentence(s.Function.Description) != "Read a session file." {
			t.Errorf("file_read without a sandbox: %s", s.Function.Description)
		}
	}
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func tcall(id, name, args string) toolCall {
	c := toolCall{ID: id, Type: "function"}
	c.Function.Name, c.Function.Arguments = name, args
	return c
}
