package main

// fsb_test.go — the reference manager against the sandbox-manager contract's
// conformance suite (sdk/sandboxcontract, docs/sandbox-manager.md), and what
// only the fake has: its test hooks, and what a manager whose sandboxes are
// host directories must refuse (TestFake…).

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/sdk/sandboxcontract"
)

// fakeTarget serves a fresh reference manager with the knobs a check asks
// for (a Target's Fresh, too).
func fakeTarget(t *testing.T, k sandboxcontract.Knobs) sandboxcontract.Target {
	t.Helper()
	_, tg := newFake(t, func(m *fsbManager) { m.Ring, m.FileMax, m.Caps = k.OutputRing, k.FileMax, k.Caps })
	return tg
}

// newFake serves a reference manager; tweak sets its knobs.
func newFake(t *testing.T, tweak ...func(*fsbManager)) (*fsbManager, sandboxcontract.Target) {
	t.Helper()
	m := &fsbManager{Root: t.TempDir(), DefaultFrom: "apps/nobody", Grace: 200 * time.Millisecond}
	for _, f := range tweak {
		f(m)
	}
	srv := httptest.NewServer(m)
	t.Cleanup(func() { srv.Close(); m.Close() }) // before TempDir's own cleanup
	return m, sandboxcontract.Target{URL: srv.URL, Grace: m.Grace, Fresh: fakeTarget}
}

func TestContract(t *testing.T) {
	tg := fakeTarget(t, sandboxcontract.Knobs{})
	tg.Strict = true // the reference manager passes what the suite only warns about
	tg.Caps = []string{"exec", "files", "tar", "snapshots", "clone", "archive"}
	if fsbHasPTY() {
		tg.Caps = append(tg.Caps, "tty")
	}
	sandboxcontract.Run(t, tg)
}

// TestContractBeforeStdio: a manager from before the stdio capability — it
// ignores split, and its stdio route is one it doesn't know (404
// not-found) — still passes caps/missing, as it did before the suite knew
// stdio: the suite takes not-found there as well as unsupported.
func TestContractBeforeStdio(t *testing.T) {
	t.Parallel()
	var fresh func(*testing.T, sandboxcontract.Knobs) sandboxcontract.Target
	fresh = func(t *testing.T, k sandboxcontract.Knobs) sandboxcontract.Target {
		t.Helper()
		m := &fsbManager{Root: t.TempDir(), DefaultFrom: "apps/nobody", Grace: 200 * time.Millisecond,
			Ring: k.OutputRing, FileMax: k.FileMax, Caps: k.Caps}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/stdio") && !m.hasCap("stdio") {
				fsbFail(w, http.StatusNotFound, "not-found", "no such route")
				return
			}
			m.ServeHTTP(w, r)
		}))
		t.Cleanup(func() { srv.Close(); m.Close() })
		return sandboxcontract.Target{URL: srv.URL, Grace: m.Grace, Fresh: fresh}
	}
	tg := fresh(t, sandboxcontract.Knobs{})
	tg.Skip = map[string]string{}
	for _, s := range []string{"hello", "sandboxes", "partitions", "people", "lifecycle", "run", "execs", "tty", "stdio", "files", "tar", "snapshots", "ports"} {
		tg.Skip[s] = "TestContract runs it; this one is caps/missing's"
	}
	sandboxcontract.Run(t, tg)
}

// TestContractPolicingTTY: a manager built to the earlier suite that
// refuses a consumer backend's terminal for an asserted person it wouldn't
// admit on a verified call gets tty/backend's warning — the check skips,
// saying why — not a failure, in the release that adds the check.
func TestContractPolicingTTY(t *testing.T) {
	t.Parallel()
	if !fsbHasPTY() {
		t.Skip("no pseudo-terminals here")
	}
	m := &fsbManager{Root: t.TempDir(), DefaultFrom: "apps/nobody", Grace: 200 * time.Millisecond}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/tty") && r.Header.Get("X-XBin-User") == "" && r.Header.Get("Sbx-User") != "" {
			fsbFail(w, http.StatusForbidden, "not-allowed", "terminals are for verified people here")
			return
		}
		m.ServeHTTP(w, r)
	}))
	t.Cleanup(func() { srv.Close(); m.Close() })
	tg := sandboxcontract.Target{URL: srv.URL, Grace: m.Grace, Skip: map[string]string{}}
	for _, s := range []string{"hello", "sandboxes", "partitions", "people", "lifecycle", "run", "execs", "stdio", "files", "tar", "snapshots", "ports", "caps"} {
		tg.Skip[s] = "TestContract runs it; this one is tty/backend's warning"
	}
	sandboxcontract.Run(t, tg)
}

func q(p string) string { return url.QueryEscape(p) }

// --- the reference manager itself -------------------------------------------------------------

func TestFakeHooks(t *testing.T) {
	t.Parallel()
	m, tg := newFake(t)
	a := tg.As(t, "apps/a")
	// FailNext: the next call of an op answers a refusal, once
	m.FailNext("create", 503, "unavailable", "the substrate is down")
	if er := a.Refused("POST", "/sandboxes", map[string]any{"name": "x"}, 503, "unavailable"); er.Error != "the substrate is down" {
		t.Fatalf("FailNext: %+v", er)
	}
	sb := a.Asserting("alice").Create(map[string]any{"name": "hooks"})
	m.FailNext("output", 429, "limit", "busy")
	x := a.Exec(sb.ID, map[string]any{"cmd": "true"})
	a.Refused("GET", "/sandboxes/"+sb.ID+"/execs/"+x.ID+"/output", nil, 429, "limit")
	a.Drain(sb.ID, x.ID)
	m.FailNext("tty", 503, "unavailable", "no terminals today")
	a.Refused("GET", "/sandboxes/"+sb.ID+"/tty", nil, 503, "unavailable")
	// GateExecs: exec starts wait for the release
	release := m.GateExecs()
	started := make(chan sandboxcontract.Exec, 1)
	go func() {
		_, b, _ := a.Do(context.Background(), "POST", "/sandboxes/"+sb.ID+"/execs", map[string]any{"cmd": "echo gated"})
		var g sandboxcontract.Exec
		_ = json.Unmarshal(b, &g)
		started <- g
	}()
	select {
	case <-started:
		t.Fatal("an exec started through the gate")
	case <-time.After(150 * time.Millisecond):
	}
	release()
	release() // idempotent
	g := <-started
	if out, _ := a.Drain(sb.ID, g.ID); out != "gated\n" {
		t.Fatalf("the gated exec: %q", out)
	}
	// a gated exec killed before it starts never runs
	release = m.GateExecs()
	go func() {
		_, b, _ := a.Do(context.Background(), "POST", "/sandboxes/"+sb.ID+"/execs", map[string]any{"cmd": "echo never > never"})
		var g sandboxcontract.Exec
		_ = json.Unmarshal(b, &g)
		started <- g
	}()
	eventually(t, 5*time.Second, "the gated exec is held", func() bool {
		var l struct{ Execs []sandboxcontract.Exec }
		a.Call("GET", "/sandboxes/"+sb.ID+"/execs", nil, 200, &l)
		return len(l.Execs) == 3
	})
	a.Call("POST", "/sandboxes/"+sb.ID+"/stop", nil, 200, nil)
	release()
	if g := <-started; g.State != "killed" {
		t.Fatalf("a gated exec stopped before it started: %+v", g)
	}
	a.Refused("GET", "/sandboxes/"+sb.ID+"/files/stat?path="+q(sb.Workdir+"/never"), nil, 404, "not-found")
	// Fail412: the next conditional writes fail; unconditional ones don't count
	p := sb.Workdir + "/f"
	st := a.Put(sb.ID, p, "v1", "")
	m.Fail412(1)
	a.Put(sb.ID, p, "v1", "")
	a.Refused("PUT", "/sandboxes/"+sb.ID+"/files/content?path="+q(p)+"&ifMatch="+st.ETag, []byte("v2"), 412, "precondition")
	a.Put(sb.ID, p, "v2", "&ifMatch="+st.ETag)
	// Box, Calls
	if bx, ok := m.Box(sb.ID); !ok || bx.Owner.User != "alice" {
		t.Fatalf("Box: %+v %v", bx, ok)
	}
	var seen bool
	for _, c := range m.Calls() {
		if c.Method == "POST" && c.Path == "/sbx/sandboxes" && c.From == "apps/a" && c.SbxUser == "alice" && strings.Contains(c.Body, `"hooks"`) {
			seen = true
		}
	}
	if !seen {
		t.Fatalf("Calls lacks the create: %+v", m.Calls())
	}
}

// A sandbox of the fake is a host directory: whatever would leave it — a
// symlink to the host, a cwd through one, a path that climbs out — is
// refused (a real manager's paths resolve inside its own filesystem instead).
func TestFakeHostPaths(t *testing.T) {
	t.Parallel()
	_, tg := newFake(t)
	a := tg.As(t, "apps/a")
	sb := a.Create(map[string]any{"name": "escape"})
	id, wd := sb.ID, sb.Workdir
	for _, p := range []string{wd + "/" + strings.Repeat("../", 20) + "etc", "/etc"} {
		if strings.HasPrefix(wd, "/etc") {
			break
		}
		a.Refused("GET", "/sandboxes/"+id+"/files/stat?path="+q(p), nil, 400, "invalid")
		a.Refused("GET", "/sandboxes/"+id+"/files/list?path="+q(p), nil, 400, "invalid")
		a.Refused("PUT", "/sandboxes/"+id+"/files/content?path="+q(p+"/f"), []byte("x"), 400, "invalid")
		a.Refused("POST", "/sandboxes/"+id+"/files/mkdir", map[string]any{"path": p + "/d"}, 400, "invalid")
		a.Refused("GET", "/sandboxes/"+id+"/tar?path="+q(p), nil, 400, "invalid")
	}
	a.Sh(id, "ln -s / root")
	if st := a.Stat(id, wd+"/root"); st.Type != "symlink" { // the link itself is the sandbox's
		t.Fatalf("stat: %+v", st)
	}
	a.Refused("GET", "/sandboxes/"+id+"/files/content?path="+q(wd+"/root/etc/hostname"), nil, 400, "invalid")
	a.Refused("GET", "/sandboxes/"+id+"/files/list?path="+q(wd+"/root"), nil, 400, "invalid")
	a.Refused("PUT", "/sandboxes/"+id+"/files/content?path="+q(wd+"/root/tmp/fsb-escape"), []byte("x"), 400, "invalid")
	a.Refused("POST", "/sandboxes/"+id+"/run", map[string]any{"cmd": "pwd", "cwd": wd + "/root"}, 400, "invalid")
	a.Refused("GET", "/sandboxes/"+id+"/tar?path="+q(wd+"/root/etc"), nil, 400, "invalid")
	// a tar can't write through it either, nor put an absolute name at the root
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, name := range []string{"root/tmp/fsb-escape-tar", "/fsb-escape-abs"} {
		_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: 1, Typeflag: tar.TypeReg})
		_, _ = tw.Write([]byte("!"))
	}
	_ = tw.Close()
	a.Call("PUT", "/sandboxes/"+id+"/tar?path="+q(wd), buf.Bytes(), http.StatusNoContent, nil)
	if a.Sh(id, "test -e /tmp/fsb-escape-tar || test -e /fsb-escape-abs && echo there || echo absent") != "absent\n" {
		t.Fatal("a tar entry left the sandbox")
	}
	a.Call("POST", "/sandboxes/"+id+"/files/remove", map[string]any{"path": wd + "/root"}, http.StatusNoContent, nil) // the link, not /
}

// A tty exec started with stdin takes it into the terminal; its end of file
// is the terminal's ^D.
func TestFakeTTYStdin(t *testing.T) {
	t.Parallel()
	if !fsbHasPTY() {
		t.Skip("no host terminals here")
	}
	_, tg := newFake(t)
	a := tg.As(t, "apps/a")
	id := a.Create(map[string]any{"name": "tty-stdin"}).ID
	x := a.Exec(id, map[string]any{"cmd": "cat; echo done", "tty": true, "stdin": true})
	a.Call("POST", "/sandboxes/"+id+"/execs/"+x.ID+"/stdin", []byte("abc\n"), http.StatusNoContent, nil)
	eventually(t, 5*time.Second, "cat echoes the line", func() bool {
		return strings.Count(a.Chunk(id, x.ID, "since=0&waitMs=500").Data, "abc") == 2 // the terminal's echo, then cat's
	})
	a.Call("POST", "/sandboxes/"+id+"/execs/"+x.ID+"/stdin?eof=1", nil, http.StatusNoContent, nil)
	if out, c := a.Drain(id, x.ID); !strings.Contains(out, "done") || c.ExitCode == nil || *c.ExitCode != 0 {
		t.Fatalf("after ^D: %q %+v", out, c)
	}
}

// Its image advertises the scripted "fake" harness by default, whatever
// Harnesses says otherwise (empty: none); a tty exec gets a terminal's
// environment unless its env names it.
func TestFakeHarnessesAndTTYEnv(t *testing.T) {
	t.Parallel()
	harnesses := func(tg sandboxcontract.Target) []sandboxcontract.Harness {
		var h sandboxcontract.Hello
		tg.As(t, "apps/a").Call("GET", "/hello?protocol=1", nil, 200, &h)
		return h.Images[0].Harnesses
	}
	_, tg := newFake(t)
	if hs := harnesses(tg); len(hs) != 1 || hs[0].ID != "fake" || strings.Join(hs[0].Argv, " ") != "fakeacp" || hs[0].Login == "" {
		t.Fatalf("the default harnesses: %+v", hs)
	}
	_, own := newFake(t, func(m *fsbManager) { m.Harnesses = []fsbHarness{{ID: "fake", Argv: []string{"/bin/acp", "-x"}}} })
	if hs := harnesses(own); len(hs) != 1 || strings.Join(hs[0].Argv, " ") != "/bin/acp -x" {
		t.Fatalf("set harnesses: %+v", hs)
	}
	_, none := newFake(t, func(m *fsbManager) { m.Harnesses = []fsbHarness{} })
	if hs := harnesses(none); len(hs) != 0 {
		t.Fatalf("no harnesses: %+v", hs)
	}
	if !fsbHasPTY() {
		return
	}
	a := tg.As(t, "apps/a")
	id := a.Create(map[string]any{"name": "tty-env"}).ID
	x := a.Exec(id, map[string]any{"cmd": "echo \"$TERM/$COLORTERM/$LANG\"", "tty": true, "env": map[string]string{"LANG": "de_DE.UTF-8"}})
	if out, _ := a.Drain(id, x.ID); !strings.Contains(out, "xterm-256color/truecolor/de_DE.UTF-8") {
		t.Fatalf("a tty's environment: %q", out)
	}
}

// eventually polls cond for up to d.
func eventually(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(d); !cond(); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("not within %s: %s", d, what)
		}
	}
}
