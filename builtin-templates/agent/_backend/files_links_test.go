package main

// D207: a report's file links (files_links.go) — recorded in its render
// step as {href, path}, sandbox files copied in beside it — and the routes
// that download session files (files_http.go): one as an attachment, many
// as a streamed zip.

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestHTMLHrefs(t *testing.T) {
	src := `<p><a href="a.pdf">a</a> <a href='a.pdf'>again</a> <a href="x&amp;y.csv" href="ignored">b</a>
	<area href="map.txt"> <link href="style.css"> <img src="i.png"> <a name="n">no href</a>
	<script>var s = '<a href="in-script.txt">'</script><textarea><a href="in-text.txt"></textarea><A HREF="UP.TXT">`
	got := htmlHrefs(src)
	want := []string{"a.pdf", "x&y.csv", "map.txt", "UP.TXT"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("hrefs: %q, want %q", got, want)
	}
}

func TestLinkRef(t *testing.T) {
	sess := reportAt{dir: "out"}
	box := reportAt{sandbox: true, dir: "/work/out"}
	for _, c := range []struct {
		href string
		at   reportAt
		sbx  bool
		want fileRef
		bare bool
		ok   bool
	}{
		{"data.csv", sess, false, fileRef{key: "out/data.csv"}, true, true},
		{"./data.csv?x=1#top", sess, false, fileRef{key: "out/data.csv"}, true, true},
		{"../top.csv", sess, false, fileRef{key: "top.csv"}, true, true},
		{"../../above.csv", sess, false, fileRef{}, false, false},
		{"my%20file.pdf", reportAt{dir: "."}, false, fileRef{}, false, false}, // a space is no session key
		{"session:data/b.pdf", sess, false, fileRef{key: "data/b.pdf"}, false, true},
		{"/abs/c.txt", sess, false, fileRef{}, false, false}, // no sandbox: nothing
		{"/abs/c.txt", sess, true, fileRef{sandbox: true, path: "/abs/c.txt"}, false, true},
		{"sandbox:./d.txt", sess, true, fileRef{sandbox: true, path: "./d.txt"}, false, true},
		{"file:///work/e.txt", sess, true, fileRef{sandbox: true, path: "/work/e.txt"}, false, true},
		{"data.csv", box, true, fileRef{sandbox: true, path: "/work/out/data.csv"}, false, true},
		{"../shared/f.csv", box, true, fileRef{sandbox: true, path: "/work/shared/f.csv"}, false, true},
		{"sandbox:g.txt", box, true, fileRef{sandbox: true, path: "/work/out/g.txt"}, false, true},
		{"my%20file.pdf", box, true, fileRef{sandbox: true, path: "/work/out/my file.pdf"}, false, true},
		{"#top", sess, true, fileRef{}, false, false},
		{"https://example.com/x.pdf", box, true, fileRef{}, false, false},
		{"mailto:a@b.c", box, true, fileRef{}, false, false},
		{"javascript:alert(1)", box, true, fileRef{}, false, false},
		{"data:text/plain,hi", box, true, fileRef{}, false, false},
		{"//evil.example/x", box, true, fileRef{}, false, false},
		{`..\x.txt`, box, true, fileRef{}, false, false},
		{"", box, true, fileRef{}, false, false},
	} {
		got, bare, ok := linkRef(c.href, c.at, c.sbx)
		if ok != c.ok || (ok && (got != c.want || bare != c.bare)) {
			t.Errorf("linkRef(%q, %+v, %v) = %+v bare=%v ok=%v; want %+v bare=%v ok=%v", c.href, c.at, c.sbx, got, bare, ok, c.want, c.bare, c.ok)
		}
	}
}

// renderStep is the newest render step's detail.
func renderStep(t *testing.T, db *DB, id int64) map[string]any {
	t.Helper()
	steps, _ := db.steps(id)
	for i := len(steps) - 1; i >= 0; i-- {
		if steps[i].Kind == "render" {
			var d map[string]any
			if err := json.Unmarshal([]byte(steps[i].Detail), &d); err != nil {
				t.Fatal(err)
			}
			return d
		}
	}
	t.Fatal("no render step")
	return nil
}

// linksOf: a step's links as href → path.
func linksOf(d map[string]any) map[string]string {
	out := map[string]string{}
	ls, _ := d["links"].([]any)
	for _, l := range ls {
		m := l.(map[string]any)
		out[m["href"].(string)] = m["path"].(string)
	}
	return out
}

func skippedOf(d map[string]any) []string {
	var out []string
	ss, _ := d["skipped"].([]any)
	for _, s := range ss {
		out = append(out, s.(map[string]any)["href"].(string))
	}
	return out
}

func TestRenderLinksSessionReport(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	id, _ := db.createRun("t", "", 0)
	run, _ := db.getRun(id)
	ctx := context.Background()
	for p, c := range map[string]string{"out/data.csv": "a,b\n", "data/b.txt": "b"} {
		if _, err := db.replPutFile(id, p, c, 0); err != nil {
			t.Fatal(err)
		}
	}
	report := `<h1>R</h1><a href="data.csv">data</a> <a href="session:data/b.txt">b</a> <a href="missing.pdf">gone</a>
		<a href="https://example.com">web</a> <a href="#top">top</a> <a href="../../x">above</a>`
	if _, err := db.replPutFile(id, "out/report.html", report, 0); err != nil {
		t.Fatal(err)
	}
	out, err := ag.runFileTool(ctx, run, Config{}, "render_html", map[string]any{"path": "out/report.html"})
	if err != nil {
		t.Fatal(err)
	}
	d := renderStep(t, db, id)
	if got, want := linksOf(d), map[string]string{"data.csv": "out/data.csv", "session:data/b.txt": "data/b.txt"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("links: %v, want %v", got, want)
	}
	if got := skippedOf(d); !reflect.DeepEqual(got, []string{"missing.pdf"}) {
		t.Fatalf("skipped: %v", got)
	}
	mustContain(t, "render_html", out, "download from the preview: out/data.csv, data/b.txt", "not offered as a download: missing.pdf — no session file out/missing.pdf")

	// a report without file links journals neither key (an older step's shape)
	if _, err := db.replPutFile(id, "plain.html", `<p><a href="https://x.dev">x</a></p>`, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := ag.runFileTool(ctx, run, Config{}, "render_html", map[string]any{"path": "plain.html"}); err != nil {
		t.Fatal(err)
	}
	if d := renderStep(t, db, id); d["links"] != nil || d["skipped"] != nil {
		t.Fatalf("no links, no keys: %v", d)
	}
}

func TestRenderLinksFromSandbox(t *testing.T) {
	ag, _ := accessFixture(t)
	bindSbx(t, "apps/cs")
	r, cfg, box := sbxRun(t, ag, "rep", "none")
	put(t, box, "out/data.csv", "a,b\n1,2\n")
	put(t, box, "out/sub/notes.txt", "notes")
	abs := put(t, box, "elsewhere/plan.txt", "plan")
	put(t, box, "out/dir/inside.txt", "x")
	put(t, box, "out/report.html", `<p><a href="data.csv">data</a> <a href="./data.csv">again</a> <a href="sub/notes.txt">notes</a>
		<a href="`+abs+`">plan</a> <a href="nope.pdf">missing</a> <a href="dir">a directory</a> <a href="https://x.dev/">web</a></p>`)
	out := mustTool(t, ag, r, cfg, "r1", "render_html", map[string]any{"path": "./out/report.html"})
	d := renderStep(t, ag.db, r.ID)
	want := map[string]string{"data.csv": "data.csv", "./data.csv": "data.csv", "sub/notes.txt": "notes.txt", abs: "plan.txt"}
	if got := linksOf(d); !reflect.DeepEqual(got, want) {
		t.Fatalf("links: %v, want %v", got, want)
	}
	if got := skippedOf(d); !reflect.DeepEqual(got, []string{"nope.pdf", "dir"}) {
		t.Fatalf("skipped: %v", got)
	}
	mustContain(t, "render_html", out, "copied from the sandbox now: data.csv, notes.txt, plan.txt", "not offered as a download: dir — ", "is a directory")
	if f, err := ag.db.replFile(r.ID, "data.csv"); err != nil || f.Content != "a,b\n1,2\n" || f.Source == nil || f.Source.Kind != "sandbox" {
		t.Fatalf("the linked file's copy: %+v %v", f, err)
	}
	// rendered again: the copies are up to date, nothing is copied — and a
	// changed file is a new version of the same copy
	put(t, box, "out/sub/notes.txt", "notes v2")
	out = mustTool(t, ag, r, cfg, "r2", "render_html", map[string]any{"path": "./out/report.html"})
	mustContain(t, "render again", out, "copied from the sandbox now: notes.txt")
	if f, _ := ag.db.replFile(r.ID, "notes.txt"); f.Content != "notes v2" || f.Version != 2 {
		t.Fatalf("the copy's new version: %+v", f)
	}
	// the session copy, rendered by its key, resolves beside its sandbox file still
	mustTool(t, ag, r, cfg, "r3", "render_html", map[string]any{"path": "report.html"})
	if got := linksOf(renderStep(t, ag.db, r.ID)); !reflect.DeepEqual(got, want) {
		t.Fatalf("by its session key: %v", got)
	}
	// a session report's bare link the session lacks is looked for in the sandbox's working directory
	put(t, box, "top.txt", "top")
	mustTool(t, ag, r, cfg, "w", "file_write", map[string]any{"path": "mine.html", "content": `<a href="top.txt">t</a> <a href="data.csv">d</a>`})
	mustTool(t, ag, r, cfg, "r4", "render_html", map[string]any{"path": "mine.html"})
	if got := linksOf(renderStep(t, ag.db, r.ID)); !reflect.DeepEqual(got, map[string]string{"top.txt": "top.txt", "data.csv": "data.csv"}) {
		t.Fatalf("a session report: %v", got)
	}
}

func TestDocPreviewLinks(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	id, _ := db.createRun("t", "", 0)
	run, _ := db.getRun(id)
	ctx := context.Background()
	if _, err := db.replPutFile(id, "docs/data.csv", "a,b\n", 0); err != nil {
		t.Fatal(err)
	}
	out, err := ag.runDocTool(ctx, run, Config{}, "doc_write", map[string]any{"path": "docs/r.docx",
		"markdown": "# R\n\nSee [the data](data.csv), [the web](https://x.dev) and [gone](gone.csv)."})
	if err != nil {
		t.Fatal(err)
	}
	d := renderStep(t, db, id)
	if d["doc"] != "docs/r.docx" || !reflect.DeepEqual(linksOf(d), map[string]string{"data.csv": "docs/data.csv"}) || !reflect.DeepEqual(skippedOf(d), []string{"gone.csv"}) {
		t.Fatalf("the doc's render step: %v", d)
	}
	mustContain(t, "doc_write", out, "download from the preview: docs/data.csv", "not offered as a download: gone.csv")
	pf, _ := db.replFile(id, "docs/r.docx.html")
	mustContain(t, "the preview", pf.Content, `<a href="data.csv">the data</a>`, `<a href="https://x.dev">the web</a>`)
}

func TestDownloadAndZipRoutes(t *testing.T) {
	ag, mux := accessFixture(t)
	id := runAs(t, ag, runStamp{Owner: "alice", Visibility: visPrivate, TeamRole: roleViewer, Origin: "chat"}, false)
	if _, err := ag.db.replPutFile(id, "out/notes.md", "# hi\n", 0); err != nil {
		t.Fatal(err)
	}
	png := tinyPNG(t, 4, 4)
	upload(t, ag, id, "shot.png", "image/png", png)

	w := callAs(t, mux, asAlice, "GET", fmt.Sprintf("/runs/%d/dl/out/notes.md", id), nil)
	if w.Code != 200 || w.Body.String() != "# hi\n" || w.Header().Get("Content-Disposition") != `attachment; filename=notes.md` ||
		w.Header().Get("X-Content-Type-Options") != "nosniff" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("dl text: %d %q %v", w.Code, w.Body.String(), w.Header())
	}
	if w := callAs(t, mux, asAlice, "GET", fmt.Sprintf("/runs/%d/dl/shot.png", id), nil); w.Code != 200 || !bytes.Equal(w.Body.Bytes(), png) || w.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("dl binary: %d %v", w.Code, w.Header())
	}
	if w := callAs(t, mux, asAlice, "GET", fmt.Sprintf("/runs/%d/dl/nope.txt", id), nil); w.Code != 404 {
		t.Fatalf("dl missing: %d", w.Code)
	}
	if w := callAs(t, mux, asAlice, "GET", fmt.Sprintf("/runs/%d/dl/a%%20b.txt", id), nil); w.Code != 400 {
		t.Fatalf("dl a bad key: %d", w.Code)
	}
	if w := callAs(t, mux, asBob, "GET", fmt.Sprintf("/runs/%d/dl/out/notes.md", id), nil); w.Code != 404 {
		t.Fatalf("bob downloads alice's private file: %d", w.Code)
	}

	readZip := func(w *httptest.ResponseRecorder) map[string]string {
		t.Helper()
		zr, err := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
		if err != nil {
			t.Fatalf("not a zip: %v", err)
		}
		got := map[string]string{}
		for _, f := range zr.File {
			rc, err := f.Open()
			if err != nil {
				t.Fatal(err)
			}
			b, _ := io.ReadAll(rc)
			rc.Close()
			got[f.Name] = string(b)
		}
		return got
	}
	w = callAs(t, mux, asAlice, "GET", fmt.Sprintf("/runs/%d/zip?path=out/notes.md&path=shot.png&path=out/notes.md&name=mine", id), nil)
	if w.Code != 200 || w.Header().Get("Content-Type") != "application/zip" || w.Header().Get("Content-Disposition") != `attachment; filename=mine.zip` {
		t.Fatalf("zip: %d %v %s", w.Code, w.Header(), w.Body.String())
	}
	if got := readZip(w); !reflect.DeepEqual(got, map[string]string{"out/notes.md": "# hi\n", "shot.png": string(png)}) {
		t.Fatalf("zip entries: %v", got)
	}
	w = callAs(t, mux, asAlice, "GET", fmt.Sprintf("/runs/%d/zip", id), nil) // none named: all of them
	if got := readZip(w); w.Code != 200 || len(got) != 2 || w.Header().Get("Content-Disposition") != fmt.Sprintf(`attachment; filename=run-%d-files.zip`, id) {
		t.Fatalf("zip of all: %d %v", w.Code, got)
	}
	for q, code := range map[string]int{"?path=nope.txt": 404, "?path=../x": 400, "?path=out/notes.md&path=": 400} {
		if w := callAs(t, mux, asAlice, "GET", fmt.Sprintf("/runs/%d/zip%s", id, q), nil); w.Code != code || w.Header().Get("Content-Type") == "application/zip" {
			t.Fatalf("zip%s: %d, want %d", q, w.Code, code)
		}
	}
	if w := callAs(t, mux, asBob, "GET", fmt.Sprintf("/runs/%d/zip", id), nil); w.Code != 404 {
		t.Fatalf("bob zips alice's private files: %d", w.Code)
	}
	empty := runAs(t, ag, runStamp{Owner: "alice", Visibility: visPrivate, TeamRole: roleViewer, Origin: "chat"}, false)
	if w := callAs(t, mux, asAlice, "GET", fmt.Sprintf("/runs/%d/zip", empty), nil); w.Code != 404 {
		t.Fatalf("zip of no files: %d", w.Code)
	}
}
