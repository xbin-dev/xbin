package main

// The v0.3.64 regressions (dated amendments under D133, D135 and D136):
// render_html streams its step and shows big reports, sandbox copies keep
// apart; finish is worded by depth; the port tools ask the manager again
// before refusing; a gone sandbox comes off the conversation; the
// copied-from note names the new source; sandbox_copy blames the side that
// failed.

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// stepEvents drains a subscriber's queue for steps of kind.
func stepEvents(s *subscriber, kind string) []*Step {
	var out []*Step
	for _, ev := range s.drain() {
		if st, ok := ev.Data.(*Step); ok && ev.Type == evStep && st.Kind == kind {
			out = append(out, st)
		}
	}
	return out
}

func TestRenderHTMLStreamsAndShowsBigReports(t *testing.T) {
	ag, mux := accessFixture(t)
	bindSbx(t, "apps/cs")
	r, cfg, box := sbxRun(t, ag, "rep", "none")
	sub, _, _ := ag.eng.hub.subscribe(r.ID, ag.eng.hub.now(), who{kind: whoSystem})
	defer ag.eng.hub.unsubscribe(sub)

	// a render is streamed at once (the pane opens during the turn)
	mustTool(t, ag, r, cfg, "w", "file_write", map[string]any{"path": "small.html", "content": "<p>hi</p>"})
	mustTool(t, ag, r, cfg, "r1", "render_html", map[string]any{"path": "small.html"})
	if got := stepEvents(sub, "render"); len(got) != 1 || !strings.Contains(got[0].Detail, `"path":"small.html"`) {
		t.Fatalf("the render step wasn't streamed: %+v", got)
	}

	// a report over the text cap is a binary session file (text/html), and renders
	big := "<!doctype html><html><body>" + strings.Repeat("<p>row of the report</p>\n", 4000) + "</body></html>"
	put(t, box, "a/index.html", big)
	out := mustTool(t, ag, r, cfg, "r2", "render_html", map[string]any{"path": "./a/index.html"})
	if !strings.Contains(out, "rendered index.html (") {
		t.Fatalf("a big report: %q", out)
	}
	f, err := ag.db.replFile(r.ID, "index.html")
	if err != nil || !f.Binary || f.Mime != "text/html" || f.Bytes != len(big) {
		t.Fatalf("stored as: %+v %v", f, err)
	}
	if got := stepEvents(sub, "render"); len(got) != 1 {
		t.Fatalf("the big render wasn't streamed: %+v", got)
	}
	// GET /runs/{id}/file answers its text, for the pane (which paints it
	// through the same static-snapshot policy as any render)
	req := httptest.NewRequest("GET", fmt.Sprintf("/runs/%d/file?path=index.html", r.ID), nil)
	req.Header.Set("X-XBin-From", "apps/agent")
	req.Header.Set("X-XBin-Role", "admin")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	var got ReplFile
	if err := json.Unmarshal(w.Body.Bytes(), &got); w.Code != 200 || err != nil || got.Content != big || !got.Binary {
		t.Fatalf("GET file of a big report: %d %v (content %d bytes)", w.Code, err, len(got.Content))
	}
	// over the render cap: refused, saying the cap
	put(t, box, "huge/index.html", "<p>"+strings.Repeat("x", maxRenderBytes)+"</p>")
	if _, err := tool(t, ag, r, cfg, "r3", "render_html", map[string]any{"path": "./huge/index.html"}); err == nil || !strings.Contains(err.Error(), "up to "+humanBytes(maxRenderBytes)) {
		t.Fatalf("over the cap: %v", err)
	}

	// another …/index.html keeps apart: never a version of a's
	put(t, box, "b/index.html", "<p>b</p>")
	out = mustTool(t, ag, r, cfg, "r4", "render_html", map[string]any{"path": "./b/index.html"})
	if !strings.Contains(out, "to the session file b/index.html") || !strings.Contains(out, "rendered b/index.html (") {
		t.Fatalf("b's index.html: %q", out)
	}
	if f, _ := ag.db.replFile(r.ID, "index.html"); f.Version != 1 || f.Bytes != len(big) {
		t.Fatalf("a's copy was overwritten: %+v", f)
	}
	// a repeated render of one path stays under its name (a new version when it changed)
	put(t, box, "b/index.html", "<p>b2</p>")
	if out := mustTool(t, ag, r, cfg, "r5", "render_html", map[string]any{"path": "./b/index.html"}); !strings.Contains(out, "session file b/index.html") || !strings.Contains(out, "v2, replacing v1") {
		t.Fatalf("b again: %q", out)
	}
	if out := mustTool(t, ag, r, cfg, "r6", "render_html", map[string]any{"path": "./a/index.html"}); !strings.HasPrefix(out, "unchanged: the session file index.html") {
		t.Fatalf("a again: %q", out)
	}
	// a session file the model wrote isn't a sandbox copy's to overwrite
	mustTool(t, ag, r, cfg, "w2", "file_write", map[string]any{"path": "page.html", "content": "<p>mine</p>"})
	put(t, box, "site/page.html", "<p>theirs</p>")
	if out := mustTool(t, ag, r, cfg, "r7", "render_html", map[string]any{"path": "./site/page.html"}); !strings.Contains(out, "session file site/page.html") {
		t.Fatalf("over the model's own file: %q", out)
	}
	if f, _ := ag.db.replFile(r.ID, "page.html"); f.Content != "<p>mine</p>" {
		t.Fatalf("the model's page.html: %+v", f)
	}
	// a third index.html, in another b/: the sandbox's name keeps it apart
	put(t, box, "x/b/index.html", "<p>xb</p>")
	if out := mustTool(t, ag, r, cfg, "r8", "render_html", map[string]any{"path": "./x/b/index.html"}); !strings.Contains(out, "session file rep/b/index.html") {
		t.Fatalf("a third: %q", out)
	}
}

func TestFinishSpecByDepth(t *testing.T) {
	desc := func(depth int) (string, string) {
		for _, s := range toolSpecs(defaultConfig(), depth, nil) {
			if s.Function.Name == "finish" {
				p := s.Function.Parameters["properties"].(map[string]any)["result"].(map[string]any)
				return s.Function.Description, p["description"].(string)
			}
		}
		t.Fatalf("no finish at depth %d", depth)
		return "", ""
	}
	top, topArg := desc(0)
	if !strings.Contains(top, "End your turn") || !strings.Contains(top, "SHORT status line") ||
		!strings.Contains(top, "full answer or report in your normal reply BEFORE calling finish") || !strings.Contains(topArg, "not the answer") {
		t.Fatalf("top-level finish: %q / %q", top, topArg)
	}
	sub, subArg := desc(1)
	if !strings.Contains(sub, "the answer your parent receives") || !strings.Contains(subArg, "full answer") || strings.Contains(sub, "SHORT") {
		t.Fatalf("a subagent's finish: %q / %q", sub, subArg)
	}
}

// A manager updated after its hello was cached (ports added) is asked
// again before preview_port refuses; a refusal names the manager and what
// it offers; sandbox_info shows both caps and whether a live preview works;
// xbind's "unsupported" (a sandbox agent from before ports) says to restart.
func TestPortsAskTheManagerAgain(t *testing.T) {
	ag := newTestAgent(t, newTestDB(t))
	m := bindSbx(t, "apps/cs")["apps/cs"]
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<p>up</p>")
	}))
	defer srv.Close()
	port := srv.Listener.Addr().(*net.TCPAddr).Port
	r, cfg, _ := sbxRun(t, ag, "web", "none")

	m.Caps = []string{"exec", "files", "tar"} // the manager as it was
	out := mustTool(t, ag, r, cfg, "i", "sandbox_info", nil)
	if !strings.Contains(out, "manager caps exec,files,tar · live preview (preview_port): not available — its manager, Fake sandboxes (test fixture)") {
		t.Fatalf("sandbox_info before the update:\n%s", out)
	}
	_, err := tool(t, ag, r, cfg, "p", "preview_port", map[string]any{"port": port})
	if err == nil || !strings.Contains(err.Error(), "doesn't serve ports") || !strings.Contains(err.Error(), "(it offers: exec, files, tar)") {
		t.Fatalf("no ports: %v", err)
	}
	m.Caps = nil // updated: every capability, ports too — the cached hello says otherwise for minutes
	out = mustTool(t, ag, r, cfg, "p", "preview_port", map[string]any{"port": port})
	if !strings.Contains(out, "live to the human") {
		t.Fatalf("after the update: %q", out)
	}
	if out := mustTool(t, ag, r, cfg, "i", "sandbox_info", nil); !strings.Contains(out, "live preview (preview_port): available") || !strings.Contains(out, "manager caps exec,files,tar,") {
		t.Fatalf("sandbox_info after the update:\n%s", out)
	}
	// xbind's refusal for a sandbox agent from before ports, verbatim, and what to do
	m.FailNext("port", http.StatusNotImplemented, "unsupported", "the sandbox's agent doesn't serve ports (it predates them): restart the sandbox")
	_, err = tool(t, ag, r, cfg, "p", "preview_port", map[string]any{"port": port})
	if err == nil || !strings.Contains(err.Error(), "the sandbox's agent doesn't serve ports (it predates them): restart the sandbox") ||
		!strings.Contains(err.Error(), "then preview_port again") {
		t.Fatalf("an old sandbox agent: %v", err)
	}
}

// A sandbox deleted elsewhere comes off the conversation the first time a
// tool finds it gone: another attached one becomes active, or none is left.
func TestGoneSandboxIsDetached(t *testing.T) {
	ag := newTestAgent(t, newTestDB(t))
	bindSbx(t, "apps/cs")
	r, cfg, a, b := sbxPair(t, ag)
	c := mkSandbox(t, "apps/cs", "", sbxCreate{Name: "c"})
	cfg.Attached = append(cfg.Attached, sbxBindingOf(c))
	if err := storeBinding(ag.db, r.ID, func(x *Config) error { x.Attached = cfg.Attached; return nil }); err != nil {
		t.Fatal(err)
	}
	conn, err := sbxDial("apps/cs", "")
	if err != nil {
		t.Fatal(err)
	}
	del := func(box *sbxSandbox) {
		t.Helper()
		if err := conn.Delete(context.Background(), box.ID); err != nil {
			t.Fatal(err)
		}
	}
	fresh := func() Config {
		t.Helper()
		rc, err := ag.db.runConfig(r.ID)
		if err != nil {
			t.Fatal(err)
		}
		return rc
	}
	sub, _, _ := ag.eng.hub.subscribe(r.ID, ag.eng.hub.now(), who{kind: whoSystem})
	defer ag.eng.hub.unsubscribe(sub)

	// the active one: detached, the first other attached becomes active
	del(a)
	_, err = tool(t, ag, r, cfg, "b1", "bash", map[string]any{"command": "true"})
	if err == nil || !strings.Contains(err.Error(), `the sandbox "a" is gone (deleted, or no longer shared with this agent) and was detached; the active sandbox is now "b" from your next step (attached: "b", "c")`) {
		t.Fatalf("the active one gone: %v", err)
	}
	rc := fresh()
	if rc.Sandbox == nil || rc.Sandbox.Ref != sandboxRef("apps/cs", b.ID) || len(rc.Attached) != 2 {
		t.Fatalf("after: %+v %+v", rc.Sandbox, rc.Attached)
	}
	runEv := false
	for _, ev := range sub.drain() {
		runEv = runEv || ev.Type == evRun
	}
	if !runEv {
		t.Fatal("the change wasn't streamed")
	}
	// the next step works in b
	if out := mustTool(t, ag, r, rc, "b2", "bash", map[string]any{"command": "echo $SANDBOX_NAME"}); !strings.Contains(out, "b\n") {
		t.Fatalf("in b: %q", out)
	}
	// an attached (not active) one: detached, the active one stays; sandbox_info says what is active
	del(c)
	out := mustTool(t, ag, r, rc, "i", "sandbox_info", nil)
	if !strings.Contains(out, `the sandbox "c" is gone`) || !strings.Contains(out, `the active sandbox is still "b"`) || !strings.Contains(out, `active now: "b"`) {
		t.Fatalf("sandbox_info with c gone:\n%s", out)
	}
	if rc = fresh(); len(rc.Attached) != 1 || rc.Sandbox.Ref != sandboxRef("apps/cs", b.ID) {
		t.Fatalf("after c: %+v %+v", rc.Sandbox, rc.Attached)
	}
	// the last one: nothing bound (the sandbox tools go away with it)
	del(b)
	if _, err := tool(t, ag, r, rc, "b3", "bash", map[string]any{"command": "true"}); err == nil ||
		!strings.Contains(err.Error(), "no sandbox is bound now — ask the user to bind one, or sandbox_create") {
		t.Fatalf("the last one gone: %v", err)
	}
	if rc = fresh(); rc.Sandbox != nil || len(rc.Attached) != 0 {
		t.Fatalf("after b: %+v %+v", rc.Sandbox, rc.Attached)
	}
}

// The copied-from note names the sandbox this download came from, with the
// replaced version's source in parentheses.
func TestDownloadNamesItsSource(t *testing.T) {
	ag := newTestAgent(t, newTestDB(t))
	bindSbx(t, "apps/cs")
	r, cfg, a, b := sbxPair(t, ag)
	put(t, a, "r.txt", "from a\n")
	put(t, b, "r.txt", "from b\n")
	out := mustTool(t, ag, r, cfg, "d1", "sandbox_download", map[string]any{"path": "r.txt"})
	if !strings.HasPrefix(out, "downloaded "+a.Workdir+`/r.txt from the sandbox "a" to the session file r.txt (text, 7 B)`) {
		t.Fatalf("from a: %q", out)
	}
	bb := sbxBindingOf(b)
	if err := storeBinding(ag.db, r.ID, func(c *Config) error { return attachSandbox(c, bb) }); err != nil {
		t.Fatal(err)
	}
	cfg, _ = ag.db.runConfig(r.ID)
	out = mustTool(t, ag, r, cfg, "d2", "sandbox_download", map[string]any{"path": "r.txt"})
	want := "downloaded " + b.Workdir + `/r.txt from the sandbox "b" to the session file r.txt (text, 7 B) — v2, replacing v1 (copied from the sandbox "a": ` + a.Workdir + "/r.txt"
	if !strings.HasPrefix(out, want) {
		t.Fatalf("from b over a's:\n got: %q\nwant: %q…", out, want)
	}
}

// cutter cuts the next GET whose path ends in suffix after n bytes of its
// body: a source sandbox whose stream fails mid-way.
type cutter struct {
	h      http.Handler
	mu     sync.Mutex
	suffix string
	n      int
}

func (c *cutter) arm(suffix string, n int) { c.mu.Lock(); c.suffix, c.n = suffix, n; c.mu.Unlock() }

func (c *cutter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	cut := c.suffix != "" && r.Method == "GET" && strings.HasSuffix(r.URL.Path, c.suffix)
	n := c.n
	if cut {
		c.suffix = ""
	}
	c.mu.Unlock()
	if cut {
		w = &cutWriter{ResponseWriter: w, left: n}
	}
	c.h.ServeHTTP(w, r)
}

type cutWriter struct {
	http.ResponseWriter
	left int
}

func (w *cutWriter) Write(p []byte) (int, error) {
	if len(p) <= w.left {
		w.left -= len(p)
		return w.ResponseWriter.Write(p)
	}
	_, _ = w.ResponseWriter.Write(p[:w.left])
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
	panic(http.ErrAbortHandler) // the connection drops mid-body
}

// sandbox_copy says which side failed: a source stream cut short is the
// source's (not "the destination's manager didn't answer"), a destination's
// refusal is the destination's — for a file and for a directory.
func TestSandboxCopyBlamesTheFailingSide(t *testing.T) {
	ag := newTestAgent(t, newTestDB(t))
	var cut *cutter
	m := bindSbxWith(t, func(h http.Handler) http.Handler { cut = &cutter{h: h}; return cut }, "apps/cs")["apps/cs"]
	r, cfg, a, b := sbxPair(t, ag)
	put(t, a, "big.bin", strings.Repeat("0123456789abcdef", 8192))
	put(t, a, "tree/big.bin", strings.Repeat("0123456789abcdef", 8192))
	refB := sandboxRef("apps/cs", b.ID)
	from := func(p string) map[string]any { return map[string]any{"path": p} }
	to := func(p string) map[string]any { return map[string]any{"sandbox": refB, "path": p} }
	srcWhere, dstWhere := `reading "a":`+a.Workdir, `writing "b":`+b.Workdir

	cut.arm("/files/content", 1000)
	_, err := tool(t, ag, r, cfg, "c1", "sandbox_copy", map[string]any{"from": from("big.bin"), "to": to("big.bin")})
	if err == nil || !strings.Contains(err.Error(), srcWhere+"/big.bin failed") || strings.Contains(err.Error(), "didn't answer") {
		t.Fatalf("a file's source cut short: %v", err)
	}
	cut.arm("/tar", 2000)
	_, err = tool(t, ag, r, cfg, "c2", "sandbox_copy", map[string]any{"from": from("tree"), "to": to("t")})
	if err == nil || !strings.Contains(err.Error(), srcWhere+"/tree failed") {
		t.Fatalf("a directory's source cut short: %v", err)
	}
	m.FailNext("write", http.StatusInsufficientStorage, "unavailable", "the disk is full")
	_, err = tool(t, ag, r, cfg, "c3", "sandbox_copy", map[string]any{"from": from("big.bin"), "to": to("big.bin")})
	if err == nil || !strings.Contains(err.Error(), dstWhere+"/big.bin failed") || !strings.Contains(err.Error(), "the disk is full") {
		t.Fatalf("the destination refusing a file: %v", err)
	}
	m.FailNext("tar-put", http.StatusInsufficientStorage, "unavailable", "the disk is full")
	_, err = tool(t, ag, r, cfg, "c4", "sandbox_copy", map[string]any{"from": from("tree"), "to": to("t")})
	if err == nil || !strings.Contains(err.Error(), dstWhere+"/t failed") {
		t.Fatalf("the destination refusing a tree: %v", err)
	}
	// the source's own refusals name it
	m.FailNext("tar-get", http.StatusServiceUnavailable, "unavailable", "busy")
	if _, err = tool(t, ag, r, cfg, "c5", "sandbox_copy", map[string]any{"from": from("tree"), "to": to("t")}); err == nil || !strings.Contains(err.Error(), srcWhere+"/tree failed") {
		t.Fatalf("the source refusing a tree: %v", err)
	}
	// and a copy that works still does
	mustTool(t, ag, r, cfg, "c6", "sandbox_copy", map[string]any{"from": from("big.bin"), "to": to("ok.bin")})
	if fi, err := os.Stat(filepath.Join(b.Workdir, "ok.bin")); err != nil || fi.Size() != 16*8192 {
		t.Fatalf("the good copy: %v %v", fi, err)
	}
}

// GET /runs/{id}/ports lists the conversation's live previews with a fresh
// probe each, and GET /runs/{id}/ports/{sbx}/{port} probes any port of a
// sandbox bound to it — for its participants only, never the page's body.
func TestPortProbeRoutes(t *testing.T) {
	ag, mux := accessFixture(t)
	bindSbx(t, "apps/cs")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<p>SECRET BODY</p>")
	}))
	defer srv.Close()
	port := srv.Listener.Addr().(*net.TCPAddr).Port
	free, _ := net.Listen("tcp", "127.0.0.1:0")
	freePort := free.Addr().(*net.TCPAddr).Port
	free.Close()
	box := mkSandbox(t, "apps/cs", "", sbxCreate{Name: "web"})
	cfg := defaultConfig()
	cfg.Class = "coding"
	cfg.Features = map[string]bool{"streaming": false}
	b := sbxBindingOf(box)
	cfg.Sandbox, cfg.Attached = &b, []SandboxBinding{b}
	r, err := ag.startRunOpts(runOpts{Title: "t", Cfg: cfg, Hold: true,
		Stamp: runStamp{Owner: "alice", Visibility: visPrivate, TeamRole: roleViewer, Origin: "chat"}})
	if err != nil {
		t.Fatal(err)
	}
	for u, role := range map[string]string{"carol": roleParticipant, "dave": roleViewer} {
		if _, err := ag.db.q.Exec(`INSERT INTO run_members (run_id, user, role, created) VALUES (?, ?, ?, 1)`, r.ID, u, role); err != nil {
			t.Fatal(err)
		}
	}
	ag.acl.flush(r.ID)
	mustTool(t, ag, r, cfg, "p1", "preview_port", map[string]any{"port": port, "path": "/a.html"})
	mustTool(t, ag, r, cfg, "p2", "preview_port", map[string]any{"port": port, "path": "/a.html"}) // the same one again: listed once
	ag.db.journal(r.ID, "live", map[string]any{"sandbox": box.ID, "name": "web", "port": freePort, "path": "/"})
	get := func(user, target string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest("GET", target, nil)
		req.Header.Set("X-XBin-From", "apps/agent")
		req.Header.Set("X-XBin-Role", "admin")
		req.Header.Set("X-XBin-User", user)
		req.Header.Set("X-XBin-User-Level", "read")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}
	w := get("carol", fmt.Sprintf("/runs/%d/ports", r.ID))
	var list struct{ Previews []portProbe }
	if err := json.Unmarshal(w.Body.Bytes(), &list); w.Code != 200 || err != nil || len(list.Previews) != 2 {
		t.Fatalf("the list: %d %s", w.Code, w.Body)
	}
	if strings.Contains(w.Body.String(), "SECRET BODY") {
		t.Fatal("a probe answered the page's body")
	}
	down, up := list.Previews[0], list.Previews[1] // newest first
	if down.Port != freePort || down.OK || down.Refusal != "not-listening" || !strings.Contains(down.Error, "nothing listens") {
		t.Fatalf("the preview whose server is gone: %+v", down)
	}
	if up.Port != port || !up.OK || up.Status != 200 || up.ContentType != "text/html" || up.Path != "/a.html" || up.Name != "web" || up.Run != r.ID {
		t.Fatalf("the live one: %+v", up)
	}
	w = get("carol", fmt.Sprintf("/runs/%d/ports/%s/%d?path=/b?x=1", r.ID, box.ID, port))
	var one portProbe
	if err := json.Unmarshal(w.Body.Bytes(), &one); w.Code != 200 || err != nil || !one.OK || one.Path != "/b?x=1" {
		t.Fatalf("one probe: %d %s", w.Code, w.Body)
	}
	if w := get("carol", fmt.Sprintf("/runs/%d/ports/%s/%d", r.ID, "sb-nope", port)); !strings.Contains(w.Body.String(), `"refusal":"not-attached"`) {
		t.Fatalf("a sandbox the run hasn't: %d %s", w.Code, w.Body)
	}
	if w := get("carol", fmt.Sprintf("/runs/%d/ports/%s/0", r.ID, box.ID)); w.Code != 400 {
		t.Fatalf("port 0: %d", w.Code)
	}
	if w := get("dave", fmt.Sprintf("/runs/%d/ports", r.ID)); w.Code != 403 {
		t.Fatalf("a viewer: %d %s", w.Code, w.Body)
	}
	if w := get("bob", fmt.Sprintf("/runs/%d/ports/%s/%d", r.ID, box.ID, port)); w.Code != 404 {
		t.Fatalf("a stranger: %d %s", w.Code, w.Body)
	}
	// the live route's refusals carry their code now (the pane's status strip reads it)
	if w := get("carol", fmt.Sprintf("/runs/%d/live/%s/%d/", r.ID, "sb-nope", port)); w.Code != 404 || !strings.Contains(w.Body.String(), `"refusal":"not-attached"`) {
		t.Fatalf("a live refusal: %d %s", w.Code, w.Body)
	}
}
