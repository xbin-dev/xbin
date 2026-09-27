package main

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// sbxTestManager is a reference manager (hack/fakesandbox, copied as
// fsb_fake_test.go) served for one bound provider.
type sbxTestManager struct {
	*fsbManager
	srv *httptest.Server
}

// bindSbx starts a reference manager per provider ("apps/x" or
// "apps/x#inst") and injects the sandboxes slot as the runner would. The
// managers see this agent (apps/agent) as their consumer.
func bindSbx(t *testing.T, providers ...string) map[string]*sbxTestManager {
	t.Helper()
	out := map[string]*sbxTestManager{}
	var eps []map[string]string
	for _, p := range providers {
		m := &sbxTestManager{fsbManager: &fsbManager{Root: t.TempDir(), DefaultFrom: "apps/agent", Grace: 200 * time.Millisecond}}
		m.srv = httptest.NewServer(m.fsbManager)
		t.Cleanup(func() {
			m.srv.Close()
			m.Close()
		})
		out[p] = m
		prov, inst, _ := strings.Cut(p, "#")
		ep := map[string]string{"provider": prov, "url": m.srv.URL + "/", "service": "sandbox-manager"}
		if inst != "" {
			ep["instance"] = inst
		}
		eps = append(eps, ep)
	}
	raw, _ := json.Marshal(eps)
	t.Setenv("XBIN_IFACE_SANDBOXES", string(raw))
	old := sbxClient
	sbxClient = func() *http.Client { return http.DefaultClient }
	forgetHellos()
	invalidateSandboxCatalog()
	t.Cleanup(func() {
		sbxClient = old
		forgetHellos()
		invalidateSandboxCatalog()
	})
	return out
}

// mkSandbox creates a sandbox at a manager directly, as user (asserted).
func mkSandbox(t *testing.T, provider, user string, req sbxCreate) *sbxSandbox {
	t.Helper()
	c, err := sbxDial(provider, user)
	if err != nil {
		t.Fatal(err)
	}
	if req.Name == "" {
		req.Name = "box"
	}
	b, err := c.Create(context.Background(), req)
	if err != nil {
		t.Fatalf("create at %s: %v", provider, err)
	}
	invalidateSandboxCatalog()
	return b
}

func (m *sbxTestManager) count(method, path string) int {
	n := 0
	for _, c := range m.Calls() {
		if c.Method == method && c.Path == path {
			n++
		}
	}
	return n
}

func TestSandboxManagersEnv(t *testing.T) {
	t.Setenv("XBIN_IFACE_SANDBOXES", "")
	if m := sandboxManagers(); len(m) != 0 {
		t.Fatalf("unbound: %+v", m)
	}
	t.Setenv("XBIN_IFACE_SANDBOXES", `[{"provider":"apps/cs","url":"http://xbin/api/apps/cs/","service":"sandbox-manager"},
		{"provider":"apps/cs","instance":"eu","url":"http://xbin/api/apps/cs#eu"},
		{"provider":"apps/nourl","url":""},
		{"provider":"apps/other","url":"http://x","service":"openai"},
		{"provider":"apps/cs","url":"http://dup"}]`)
	m := sandboxManagers()
	if len(m) != 2 || m[0] != (sbxManager{"apps/cs", "http://xbin/api/apps/cs"}) || m[1].Provider != "apps/cs#eu" {
		t.Fatalf("managers: %+v", m)
	}
	if _, ok := boundManager("apps/cs#eu"); !ok {
		t.Fatal("an instance is found by its name")
	}
	if _, err := sbxDial("apps/gone", "alice"); sbxRefusal(err) != "unbound" || !strings.Contains(err.Error(), "no longer bound") {
		t.Fatalf("an unbound manager: %v", err)
	}
}

func TestSandboxRefs(t *testing.T) {
	for _, c := range []struct {
		ref, p, id string
		ok         bool
	}{
		{"apps/cs|sb-1", "apps/cs", "sb-1", true},
		{"apps/cs#eu|sb.1_x", "apps/cs#eu", "sb.1_x", true},
		{sandboxRef("apps/a", "b"), "apps/a", "b", true},
		{"sb-1", "", "", false},
		{"|sb-1", "", "", false},
		{"apps/cs|", "apps/cs", "", false},
		{"apps/cs|-x", "apps/cs", "-x", false},
		{"apps/cs|a/b", "apps/cs", "a/b", false},
	} {
		p, id, ok := splitSandboxRef(c.ref)
		if ok != c.ok || (ok && (p != c.p || id != c.id)) {
			t.Errorf("%q → %q %q %v", c.ref, p, id, ok)
		}
	}
	if _, _, err := sbxDialRef("nope", ""); sbxRefusal(err) != "invalid" {
		t.Fatalf("a bad ref: %v", err)
	}
}

// hello negotiates protocol 1, is cached, and refuses a manager without the
// required capabilities — the catalog lists it with the reason.
func TestManagerHello(t *testing.T) {
	ms := bindSbx(t, "apps/good", "apps/lame")
	ms["apps/lame"].Caps = []string{"exec", "tar"}
	ctx := context.Background()
	good, _ := boundManager("apps/good")
	h, err := managerHello(ctx, good)
	if err != nil || h.Protocol != 1 || !h.has("files") || h.title("apps/good") != "Fake sandboxes (test fixture)" || h.Limits.FileMax == 0 {
		t.Fatalf("hello: %+v %v", h, err)
	}
	_, _ = managerHello(ctx, good)
	if n := ms["apps/good"].count("GET", "/sbx/hello"); n != 1 {
		t.Fatalf("hello is cached: asked %d times", n)
	}
	if q := ms["apps/good"].Calls()[0].Query; q != "protocol=1" {
		t.Fatalf("hello asks for protocol 1: %q", q)
	}
	lame, _ := boundManager("apps/lame")
	if _, err := managerHello(ctx, lame); sbxRefusal(err) != "unsupported" || !strings.Contains(err.Error(), "exec and files") {
		t.Fatalf("no files capability: %v", err)
	}
	mkSandbox(t, "apps/good", "alice", sbxCreate{})
	cat := sandboxCatalog(ctx)
	if len(cat.Managers) != 2 || !cat.Managers[0].OK || cat.Managers[1].OK || cat.Managers[1].Refusal != "unsupported" {
		t.Fatalf("managers: %+v", cat.Managers)
	}
	if len(cat.Sandboxes) != 1 || cat.Sandboxes[0].Provider != "apps/good" {
		t.Fatalf("a refused manager's sandboxes aren't listed: %+v", cat.Sandboxes)
	}
	if ms["apps/lame"].count("GET", "/sbx/sandboxes") != 0 {
		t.Fatal("a refused manager isn't asked for its sandboxes")
	}
}

// The catalog merges every manager with qualified refs, is cached, forgets
// itself on invalidation, and reports a manager that is down.
func TestSandboxCatalog(t *testing.T) {
	ms := bindSbx(t, "apps/a", "apps/b#eu")
	ctx := context.Background()
	a1 := mkSandbox(t, "apps/a", "alice", sbxCreate{Name: "one"})
	b1 := mkSandbox(t, "apps/b#eu", "bob", sbxCreate{Name: "two", Visibility: "team"})
	cat := sandboxCatalog(ctx)
	var refs []string
	for _, e := range cat.Sandboxes {
		refs = append(refs, e.Ref+"="+e.Box.Name)
	}
	if got, want := strings.Join(refs, ","), "apps/a|"+a1.ID+"=one,apps/b#eu|"+b1.ID+"=two"; got != want {
		t.Fatalf("refs: %s, want %s", got, want)
	}
	if cat.Sandboxes[0].Manager != "Fake sandboxes (test fixture)" || cat.Sandboxes[0].Box.Owner.User != "alice" || !cat.Sandboxes[0].Box.Owner.Asserted {
		t.Fatalf("an entry: %+v", cat.Sandboxes[0])
	}
	// made elsewhere (the manager's own UI): not seen until the cache goes
	(&sbxConn{M: sbxManager{"apps/a", ms["apps/a"].srv.URL}}).Create(ctx, sbxCreate{Name: "three"})
	if len(sandboxCatalog(ctx).Sandboxes) != 2 {
		t.Fatal("the catalog is cached")
	}
	invalidateSandboxCatalog()
	if len(sandboxCatalog(ctx).Sandboxes) != 3 {
		t.Fatal("an invalidated catalog is read again")
	}
	ms["apps/b#eu"].srv.Close()
	invalidateSandboxCatalog()
	cat = sandboxCatalog(ctx)
	if len(cat.Sandboxes) != 2 || cat.Managers[1].OK || cat.Managers[1].Refusal != "unreachable" || cat.Managers[1].Error == "" {
		t.Fatalf("a manager down: %+v", cat.Managers)
	}
	if cat.Managers[0].Limits == nil || len(cat.Managers[0].Images) != 1 || len(cat.Managers[0].Egress) != 2 {
		t.Fatalf("what a manager offers: %+v", cat.Managers[0])
	}
}

// Every typed call against the reference manager, each naming the person.
func TestSandboxClientRoutes(t *testing.T) {
	ms := bindSbx(t, "apps/cs")
	m := ms["apps/cs"]
	ctx := context.Background()
	c, _ := sbxDial("apps/cs", "alice")
	yes := false
	b, err := c.Create(ctx, sbxCreate{Name: "dev", Egress: "none", Labels: map[string]string{"k": "v"}, ClientID: "c1", Start: &yes})
	if err != nil || b.State != "stopped" || b.Owner.User != "alice" || b.Labels["k"] != "v" || b.Workdir == "" {
		t.Fatalf("create: %+v %v", b, err)
	}
	if again, _ := c.Create(ctx, sbxCreate{Name: "dev", Egress: "none", Labels: map[string]string{"k": "v"}, ClientID: "c1", Start: &yes}); again.ID != b.ID {
		t.Fatal("clientId makes a create idempotent")
	}
	if _, err := c.Create(ctx, sbxCreate{Name: "other", ClientID: "c1"}); sbxRefusal(err) != "exists" {
		t.Fatalf("a reused clientId: %v", err)
	}
	id := b.ID
	if got, err := c.Get(ctx, id); err != nil || got.Name != "dev" || !strings.Contains(string(got.Raw), `"isolation"`) {
		t.Fatalf("get: %+v %v", got, err)
	}
	if v := b.view(); v["isolation"] != "other" || v["id"] != id {
		t.Fatalf("the view keeps the manager's fields: %v", v)
	}
	name := "renamed"
	if p, err := c.Patch(ctx, id, sbxPatch{Name: &name, Members: &[]string{"bob"}}); err != nil || p.Name != name || len(p.Members) != 1 {
		t.Fatalf("patch: %+v %v", p, err)
	}
	if s, err := c.Lifecycle(ctx, id, "start", 5, false); err != nil || s.State != "running" {
		t.Fatalf("start: %+v %v", s, err)
	}
	if !strings.Contains(m.Calls()[len(m.Calls())-1].Query, "wait=5") {
		t.Fatal("start passes ?wait")
	}
	if _, err := c.Lifecycle(ctx, id, "explode", 0, false); sbxRefusal(err) != "invalid" {
		t.Fatalf("an unknown action: %v", err)
	}

	// run: blocking, head/tail, exit codes, cwd refused when missing
	rr, err := c.Run(ctx, id, sbxRunReq{Cmd: "echo hi; echo oops >&2; exit 3", TimeoutMs: 10000})
	if err != nil || rr.ExitCode != 3 || rr.Stdout.Head != "hi\n" || rr.Stderr.Head != "oops\n" {
		t.Fatalf("run: %+v %v", rr, err)
	}
	if _, err := c.Run(ctx, id, sbxRunReq{Cmd: "true", Cwd: "/nonexistent-dir"}); sbxRefusal(err) != "invalid" {
		t.Fatalf("a missing cwd: %v", err)
	}

	// execs: start with stdin, read by offset with a long-poll, signal, delete
	e, err := c.ExecStart(ctx, id, sbxExecReq{Cmd: "cat; echo done", Stdin: true, Label: "cat"})
	if err != nil || e.State != "running" {
		t.Fatalf("exec: %+v %v", e, err)
	}
	if err := c.ExecStdin(ctx, id, e.ID, []byte("ping\n"), true); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	var since int64
	for i := 0; i < 50; i++ {
		ch, err := c.ExecOutput(ctx, id, e.ID, since, 0, 2000, false)
		if err != nil {
			t.Fatal(err)
		}
		out.WriteString(ch.Data)
		since = ch.End
		if ch.State != "running" && ch.End == ch.Total {
			if ch.ExitCode == nil || *ch.ExitCode != 0 {
				t.Fatalf("exit: %+v", ch)
			}
			break
		}
	}
	if out.String() != "ping\ndone\n" {
		t.Fatalf("output: %q", out.String())
	}
	if ch, _ := c.ExecOutput(ctx, id, e.ID, 0, 3, 0, true); ch.Encoding != "base64" || ch.Data != "cGlu" {
		t.Fatalf("a base64 chunk of 3: %+v", ch)
	}
	if g, err := c.ExecGet(ctx, id, e.ID); err != nil || g.Label != "cat" || g.State != "exited" {
		t.Fatalf("exec get: %+v %v", g, err)
	}
	sl, err := c.ExecStart(ctx, id, sbxExecReq{Cmd: "sleep 30"})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.ExecSignal(ctx, id, sl.ID, "TERM", true); err != nil {
		t.Fatal(err)
	}
	if ch, _ := c.ExecOutput(ctx, id, sl.ID, 0, 0, 5000, false); ch.State == "running" {
		t.Fatalf("TERM ends it: %+v", ch)
	}
	if list, err := c.ExecList(ctx, id); err != nil || len(list) != 2 {
		t.Fatalf("execs: %d %v", len(list), err)
	}
	if err := c.ExecDelete(ctx, id, sl.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ExecGet(ctx, id, sl.ID); sbxRefusal(err) != "not-found" {
		t.Fatalf("a deleted exec: %v", err)
	}

	// files: write (create-only, etag-guarded), stat, read, ranges, list, mkdir, move, remove
	f := b.Workdir + "/a/x.txt"
	st, err := c.WriteFile(ctx, id, f, strings.NewReader("hello"), sbxWrite{Mkdirs: true, IfNoneMatch: true, Mode: "0600"})
	if err != nil || st.Size != 5 || st.ETag == "" || st.Mode != "0600" {
		t.Fatalf("write: %+v %v", st, err)
	}
	if _, err := c.WriteFile(ctx, id, f, strings.NewReader("x"), sbxWrite{IfNoneMatch: true}); sbxRefusal(err) != "precondition" {
		t.Fatalf("create-only over a file: %v", err)
	}
	var pe *sbxError
	_, err = c.WriteFile(ctx, id, f, strings.NewReader("x"), sbxWrite{IfMatch: "stale"})
	if !errors.As(err, &pe) || pe.Refusal != "precondition" || pe.ETag != st.ETag || !strings.Contains(err.Error(), "read it again") {
		t.Fatalf("a stale etag: %v", err)
	}
	if st2, err := c.WriteFile(ctx, id, f, strings.NewReader("hello world"), sbxWrite{IfMatch: st.ETag}); err != nil || st2.ETag == st.ETag {
		t.Fatalf("an etag-guarded edit: %+v %v", st2, err)
	}
	if s, err := c.Stat(ctx, id, f); err != nil || s.Type != "file" || s.Size != 11 {
		t.Fatalf("stat: %+v %v", s, err)
	}
	data, etag, err := c.ReadFile(ctx, id, f, 0, 0, 1<<20)
	if err != nil || string(data) != "hello world" || etag == "" {
		t.Fatalf("read: %q %q %v", data, etag, err)
	}
	if part, _, _ := c.ReadFile(ctx, id, f, 6, 3, 100); string(part) != "wor" {
		t.Fatalf("a range: %q", part)
	}
	if _, _, err := c.ReadFile(ctx, id, f, 0, 0, 4); sbxRefusal(err) != "too-large" {
		t.Fatalf("over max: %v", err)
	}
	if err := c.Mkdir(ctx, id, b.Workdir+"/p/q", true); err != nil {
		t.Fatal(err)
	}
	if err := c.Move(ctx, id, f, b.Workdir+"/p/q/y.txt", false); err != nil {
		t.Fatal(err)
	}
	ls, err := c.ListDir(ctx, id, b.Workdir+"/p/q", 0)
	if err != nil || len(ls.Entries) != 1 || ls.Entries[0].Name != "y.txt" || ls.Entries[0].Type != "file" {
		t.Fatalf("list: %+v %v", ls, err)
	}
	if _, err := c.Stat(ctx, id, f); sbxRefusal(err) != "not-found" {
		t.Fatalf("moved away: %v", err)
	}

	// tar out and back in elsewhere
	rc, err := c.TarGet(ctx, id, b.Workdir+"/p", nil)
	if err != nil {
		t.Fatal(err)
	}
	tb, _ := io.ReadAll(rc)
	rc.Close()
	names := map[string]bool{}
	for tr := tar.NewReader(bytes.NewReader(tb)); ; {
		h, err := tr.Next()
		if err != nil {
			break
		}
		names[strings.TrimPrefix(h.Name, "./")] = true
	}
	if !names["q/y.txt"] {
		t.Fatalf("tar names: %v", names)
	}
	if err := c.TarPut(ctx, id, b.Workdir+"/copy", bytes.NewReader(tb), true); err != nil {
		t.Fatal(err)
	}
	if s, err := c.Stat(ctx, id, b.Workdir+"/copy/q/y.txt"); err != nil || s.Size != 11 {
		t.Fatalf("extracted: %+v %v", s, err)
	}
	if err := c.Remove(ctx, id, b.Workdir+"/copy", true); err != nil {
		t.Fatal(err)
	}

	// refusals carry the contract's words
	m.FailNext("run", 429, "limit", "busy")
	if _, err := c.Run(ctx, id, sbxRunReq{Cmd: "true"}); sbxRefusal(err) != "limit" || !strings.Contains(err.Error(), "busy") {
		t.Fatalf("a limit: %v", err)
	}
	if _, err := c.Lifecycle(ctx, id, "stop", 0, false); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Lifecycle(ctx, id, "archive", 0, false); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Run(ctx, id, sbxRunReq{Cmd: "true"}); sbxRefusal(err) != "state" || !strings.Contains(err.Error(), "thaw it first") {
		t.Fatalf("an archived sandbox: %v", err)
	}
	if s, err := c.Lifecycle(ctx, id, "thaw", 0, true); err != nil || s.State != "running" {
		t.Fatalf("thaw and start: %+v %v", s, err)
	}
	if err := c.Delete(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Get(ctx, id); sbxRefusal(err) != "not-found" {
		t.Fatalf("deleted: %v", err)
	}

	// every call named alice, and nothing pretended to be xbind
	for _, call := range m.Calls() {
		if call.Path != "/sbx/hello" && call.SbxUser != "alice" {
			t.Fatalf("%s %s without Sbx-User", call.Method, call.Path)
		}
		if call.User != "" || call.From != "" {
			t.Fatalf("%s %s set an X-XBin header", call.Method, call.Path)
		}
	}
}

// GET /sandboxes shows each caller what they may see and do.
func TestSandboxesByCaller(t *testing.T) {
	_, mux := accessFixture(t)
	bindSbx(t, "apps/cs")
	alices := mkSandbox(t, "apps/cs", "alice", sbxCreate{Name: "alices", Members: []string{"carol"}})
	team := mkSandbox(t, "apps/cs", "bob", sbxCreate{Name: "team", Visibility: "team"})
	tiles := mkSandbox(t, "apps/cs", "", sbxCreate{Name: "tiles"})
	type row struct {
		Ref                              string
		Name                             string
		Manager                          string
		Mine, CanUse, CanManage, CanEdit bool
	}
	list := func(c caller) map[string]row {
		w := callAs(t, mux, c, "GET", "/sandboxes", nil)
		if w.Code != 200 {
			t.Fatalf("%v: %d %s", c, w.Code, w.Body)
		}
		var out struct {
			Sandboxes []row
			Managers  []sbxCatalogManager
		}
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		if len(out.Managers) != 1 || !out.Managers[0].OK {
			t.Fatalf("managers: %s", w.Body)
		}
		got := map[string]row{}
		for _, r := range out.Sandboxes {
			got[r.Name] = r
		}
		return got
	}
	a := list(asAlice)
	if len(a) != 2 || !a["alices"].Mine || !a["alices"].CanUse || !a["alices"].CanManage || !a["alices"].CanEdit ||
		a["team"].Mine || !a["team"].CanUse || a["team"].CanManage || a["alices"].Ref != "apps/cs|"+alices.ID || a["alices"].Manager == "" {
		t.Fatalf("alice: %+v", a)
	}
	if c := list(asCarol); len(c) != 2 || !c["alices"].CanUse || c["alices"].CanManage || c["alices"].CanEdit {
		t.Fatalf("carol, a member: %+v", c)
	}
	if b := list(asBob); len(b) != 1 || !b["team"].Mine {
		t.Fatalf("bob: %+v", b)
	}
	// a tile manager may stop or delete anything, use only what they could anyway
	if m := list(asMgr); len(m) != 3 || m["alices"].CanUse || !m["alices"].CanManage || m["alices"].CanEdit || !m["team"].CanUse ||
		m["tiles"].CanUse || !m["tiles"].CanEdit {
		t.Fatalf("mgr: %+v", m)
	}
	if s := list(asSystem); len(s) != 3 || !s["alices"].CanUse {
		t.Fatalf("system: %+v", s)
	}
	if e := list(asElement); len(e) != 3 || e["alices"].CanUse || !e["tiles"].CanUse || !e["team"].CanUse {
		t.Fatalf("element: %+v", e)
	}
	if v := list(asViewAs); len(v) != 0 {
		t.Fatalf("view-as: %+v", v)
	}
	_ = team
	_ = tiles
}
