package xbin

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeGateway serves h on a fresh gateway socket and points the SDK's
// Client() at it, with the instance token "tok".
func fakeGateway(t *testing.T, h http.Handler) *Sandboxes {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "gw.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: h}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	t.Setenv("XBIN_GATEWAY", sock)
	t.Setenv("XBIN_TOKEN", "tok")
	clientOnce = sync.Once{}
	t.Cleanup(func() { clientOnce = sync.Once{} })
	return SandboxAPI()
}

// seenReq is what the fake runtime received.
type seenReq struct {
	Method, URI, Body, Auth, CType string
}

// recorder answers every call with the next canned answer and keeps what
// it saw.
type recorder struct {
	mu     sync.Mutex
	seen   []seenReq
	status int
	body   string
	header http.Header
}

func (rc *recorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	rc.mu.Lock()
	rc.seen = append(rc.seen, seenReq{r.Method, r.RequestURI, string(b), r.Header.Get("Authorization"), r.Header.Get("Content-Type")})
	status, body, hdr := rc.status, rc.body, rc.header
	rc.mu.Unlock()
	for k, vs := range hdr {
		w.Header()[k] = vs
	}
	if status == 0 {
		status = 200
	}
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

func (rc *recorder) answer(status int, body string) {
	rc.mu.Lock()
	rc.status, rc.body, rc.header = status, body, nil
	rc.mu.Unlock()
}

func (rc *recorder) take() []seenReq {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	s := rc.seen
	rc.seen = nil
	return s
}

func ptr[T any](v T) *T { return &v }

// Every call's method, path, query and body, the Bearer on each, and the
// answers decoded.
func TestSandboxCalls(t *testing.T) {
	rc := &recorder{}
	sbx := fakeGateway(t, rc)
	ctx := context.Background()
	sb := sbx.Sandbox("sb-1")
	const info = `{"name":"sb-1","state":"running","mode":"vm","accel":"kvm","memMiB":2048,"net":{"egress":"class:internet","reach":"internet"},"base":{"version":"v7"},"version":3,"someNewField":1}`

	cases := []struct {
		name        string
		status      int
		answer      string
		call        func() (any, error)
		method, uri string
		body        string // JSON (compared as values) or raw
		ctype       string
		check       func(t *testing.T, got any)
	}{
		{name: "runtime", answer: `{"enabled":true,"isolation":true,"modes":[{"mode":"namespace"}],"unavailable":[{"mode":"vm","reason":"no"}],"users":"root","egress":[{"class":"none","reach":"none"}],"caps":["exec"],"limits":{"sandboxes":8,"perSandbox":{"maxMemMiB":8192},"fileMax":67108864,"flows":{"tcp":1024,"udp":256}},"used":{"running":1}}`,
			call:   func() (any, error) { return sbx.Runtime(ctx) },
			method: "GET", uri: "/api/xbin/sandboxes/runtime",
			check: func(t *testing.T, got any) {
				rt := got.(*SandboxRuntime)
				if !rt.Isolation || rt.Users != "root" || rt.Modes[0].Mode != "namespace" || rt.Unavailable[0].Reason != "no" ||
					rt.Limits.Sandboxes != 8 || rt.Limits.PerSandbox.MaxMemMiB != 8192 || rt.Limits.FileMax != 64<<20 || rt.Used.Running != 1 ||
					rt.Limits.Flows != (SandboxFlows{TCP: 1024, UDP: 256}) {
					t.Fatalf("%+v", rt)
				}
			}},
		{name: "list", answer: `{"sandboxes":[` + info + `]}`,
			call:   func() (any, error) { return sbx.List(ctx) },
			method: "GET", uri: "/api/xbin/sandboxes",
			check: func(t *testing.T, got any) {
				l := got.([]SandboxInfo)
				if len(l) != 1 || l[0].Name != "sb-1" || l[0].Net.Reach != "internet" || l[0].Accel != "kvm" {
					t.Fatalf("%+v", l)
				}
			}},
		{name: "create", status: 201, answer: info,
			call: func() (any, error) {
				return sbx.Create(ctx, SandboxSpec{Name: "sb-1", Mode: "vm", MemMiB: 2048, Net: &SandboxNet{Egress: "class:internet"},
					Mounts:    []SandboxMount{{Res: "res:apps/m/work", Path: "shared", At: "/mnt/shared"}, {Source: true, At: "/opt/m"}},
					Defaults:  &SandboxDefaults{Cwd: "/work", UID: ptr(0), Env: map[string]string{"HOME": "/root"}},
					AutoStart: ptr(false), ClientID: "c-1", For: "apps/agent", ForUser: "alice"})
			},
			method: "POST", uri: "/api/xbin/sandboxes", ctype: "application/json",
			body: `{"name":"sb-1","mode":"vm","memMiB":2048,"net":{"egress":"class:internet"},
				"mounts":[{"res":"res:apps/m/work","path":"shared","at":"/mnt/shared"},{"source":true,"at":"/opt/m"}],
				"defaults":{"cwd":"/work","uid":0,"env":{"HOME":"/root"}},"for":"apps/agent","forUser":"alice","autoStart":false,"clientId":"c-1"}`,
			check: func(t *testing.T, got any) {
				if in := got.(*SandboxInfo); in.Name != "sb-1" || in.Version != 3 || in.Base.Version != "v7" {
					t.Fatalf("%+v", in)
				}
			}},
		{name: "get", answer: info, call: func() (any, error) { return sbx.Get(ctx, "sb-1") }, method: "GET", uri: "/api/xbin/sandboxes/sb-1"},
		{name: "patch", answer: info,
			call: func() (any, error) {
				return sbx.Patch(ctx, "sb-1", SandboxPatch{MemMiB: ptr(4096), Labels: &map[string]string{}, AutoStart: ptr(true), Version: 3})
			},
			method: "PATCH", uri: "/api/xbin/sandboxes/sb-1", body: `{"memMiB":4096,"labels":{},"autoStart":true,"version":3}`},
		{name: "delete", status: 204, call: func() (any, error) { return nil, sbx.Delete(ctx, "sb-1") }, method: "DELETE", uri: "/api/xbin/sandboxes/sb-1"},
		{name: "start", answer: info, call: func() (any, error) { return sbx.Start(ctx, "sb-1", 1500*time.Millisecond) },
			method: "POST", uri: "/api/xbin/sandboxes/sb-1/start?wait=2"},
		{name: "stop", answer: info, call: func() (any, error) { return sbx.Stop(ctx, "sb-1", 0) }, method: "POST", uri: "/api/xbin/sandboxes/sb-1/stop"},
		{name: "reset", answer: info, call: func() (any, error) { return sbx.Reset(ctx, "sb-1", 0) }, method: "POST", uri: "/api/xbin/sandboxes/sb-1/reset"},
		{name: "rebase", answer: info, call: func() (any, error) { return sbx.Rebase(ctx, "sb-1", time.Second) }, method: "POST", uri: "/api/xbin/sandboxes/sb-1/rebase?wait=1"},
		{name: "copy", status: 204,
			call:   func() (any, error) { return nil, sbx.Copy(ctx, SandboxPath{"a", "/x"}, SandboxPath{"b", "/y"}, true) },
			method: "POST", uri: "/api/xbin/sandboxes/copy", body: `{"from":{"sandbox":"a","path":"/x"},"to":{"sandbox":"b","path":"/y"},"overwrite":true}`},
		{name: "run", answer: `{"exitCode":null,"signal":"KILL","timedOut":true,"ms":5,"output":{"head":"x","bytes":1}}`,
			call: func() (any, error) {
				return sb.Run(ctx, RunRequest{Cmd: "go test", Env: map[string]string{"CI": "1"}, TimeoutMs: 1000, Merge: true, UID: ptr(1000), ForUser: "alice"})
			},
			method: "POST", uri: "/api/xbin/sandboxes/sb-1/run", body: `{"cmd":"go test","env":{"CI":"1"},"timeoutMs":1000,"merge":true,"uid":1000,"forUser":"alice"}`,
			check: func(t *testing.T, got any) {
				r := got.(*RunResult)
				if r.ExitCode != nil || r.Signal != "KILL" || !r.TimedOut || r.Output.Head != "x" || r.Stdout != nil {
					t.Fatalf("%+v", r)
				}
			}},
		{name: "exec", status: 201, answer: `{"id":"ab12cd-1","state":"running","tty":true,"exitCode":null,"started":1}`,
			call: func() (any, error) {
				return sb.Exec(ctx, ExecRequest{Argv: []string{"bash", "-l"}, TTY: true, Rows: 24, Cols: 80, Stdin: true, Label: "sh", ClientID: "k:1"})
			},
			method: "POST", uri: "/api/xbin/sandboxes/sb-1/execs", body: `{"argv":["bash","-l"],"tty":true,"rows":24,"cols":80,"stdin":true,"label":"sh","clientId":"k:1"}`,
			check: func(t *testing.T, got any) {
				if e := got.(*ExecInfo); e.ID != "ab12cd-1" || !e.TTY || e.State != "running" {
					t.Fatalf("%+v", e)
				}
			}},
		{name: "execs", answer: `{"execs":[{"id":"ab12cd-1","state":"exited","exitCode":0}]}`,
			call:   func() (any, error) { return sb.Execs(ctx) },
			method: "GET", uri: "/api/xbin/sandboxes/sb-1/execs",
			check: func(t *testing.T, got any) {
				if l := got.([]ExecInfo); len(l) != 1 || *l[0].ExitCode != 0 {
					t.Fatalf("%+v", l)
				}
			}},
		{name: "get exec", answer: `{"id":"ab12cd-2"}`, call: func() (any, error) { return sb.GetExec(ctx, "ab12cd-2") }, method: "GET", uri: "/api/xbin/sandboxes/sb-1/execs/ab12cd-2"},
		{name: "kill", status: 204, call: func() (any, error) { return nil, sb.Kill(ctx, "ab12cd-1") }, method: "DELETE", uri: "/api/xbin/sandboxes/sb-1/execs/ab12cd-1"},
		{name: "output", answer: `{"start":5,"end":8,"total":8,"data":"aGV5","encoding":"base64","state":"running","exitCode":null}`,
			call: func() (any, error) {
				return sb.Output(ctx, "ab12cd-1", OutputQuery{Since: 5, Max: 10, WaitMs: 100, Encoding: "base64"})
			},
			method: "GET", uri: "/api/xbin/sandboxes/sb-1/execs/ab12cd-1/output?encoding=base64&max=10&since=5&waitMs=100",
			check: func(t *testing.T, got any) {
				if b, err := got.(*OutputChunk).Bytes(); err != nil || string(b) != "hey" {
					t.Fatalf("%q %v", b, err)
				}
			}},
		{name: "stdin eof", status: 204, call: func() (any, error) { return nil, sb.Stdin(ctx, "ab12cd-1", nil, true) },
			method: "POST", uri: "/api/xbin/sandboxes/sb-1/execs/ab12cd-1/stdin?eof=1", ctype: "application/octet-stream"},
		{name: "signal", status: 204, call: func() (any, error) { return nil, sb.Signal(ctx, "ab12cd-1", "INT", false) },
			method: "POST", uri: "/api/xbin/sandboxes/sb-1/execs/ab12cd-1/signal", body: `{"signal":"INT","group":false}`},
		{name: "resize", status: 204, call: func() (any, error) { return nil, sb.Resize(ctx, "ab12cd-1", 24, 80) },
			method: "POST", uri: "/api/xbin/sandboxes/sb-1/execs/ab12cd-1/resize", body: `{"rows":24,"cols":80}`},
		{name: "stat", answer: `{"path":"/work/a b","type":"file","size":3,"mode":"0644","etag":"e1"}`,
			call:   func() (any, error) { return sb.Stat(ctx, "/work/a b") },
			method: "GET", uri: "/api/xbin/sandboxes/sb-1/files/stat?path=%2Fwork%2Fa+b",
			check: func(t *testing.T, got any) {
				if st := got.(*FileStat); st.Mode != "0644" || st.ETag != "e1" || st.Size != 3 {
					t.Fatalf("%+v", st)
				}
			}},
		{name: "list files", answer: `{"path":"/","entries":[{"name":"etc","type":"dir"}],"truncated":true}`,
			call:   func() (any, error) { return sb.List(ctx, "/", 5) },
			method: "GET", uri: "/api/xbin/sandboxes/sb-1/files/list?limit=5&path=%2F",
			check: func(t *testing.T, got any) {
				if l := got.(*FileList); !l.Truncated || l.Entries[0].Name != "etc" {
					t.Fatalf("%+v", l)
				}
			}},
		{name: "mkdir", status: 204, call: func() (any, error) { return nil, sb.Mkdir(ctx, "/w/d", true) },
			method: "POST", uri: "/api/xbin/sandboxes/sb-1/files/mkdir", body: `{"path":"/w/d","parents":true}`},
		{name: "remove", status: 204, call: func() (any, error) { return nil, sb.Remove(ctx, "/w/d", false) },
			method: "POST", uri: "/api/xbin/sandboxes/sb-1/files/remove", body: `{"path":"/w/d"}`},
		{name: "move", status: 204, call: func() (any, error) { return nil, sb.Move(ctx, "/a", "/b", true) },
			method: "POST", uri: "/api/xbin/sandboxes/sb-1/files/move", body: `{"from":"/a","to":"/b","overwrite":true}`},
		{name: "get tar", answer: "TAR",
			call: func() (any, error) {
				rd, err := sb.GetTar(ctx, "/w", []string{"node_modules", ".git"})
				if err != nil {
					return nil, err
				}
				defer rd.Close()
				b, err := io.ReadAll(rd)
				return string(b), err
			},
			method: "GET", uri: "/api/xbin/sandboxes/sb-1/tar?exclude=node_modules&exclude=.git&path=%2Fw",
			check: func(t *testing.T, got any) {
				if got != "TAR" {
					t.Fatal(got)
				}
			}},
		{name: "snapshots", answer: `{"snapshots":[{"id":"s-1","name":"clean","created":5}]}`,
			call:   func() (any, error) { return sb.Snapshots(ctx) },
			method: "GET", uri: "/api/xbin/sandboxes/sb-1/snapshots",
			check: func(t *testing.T, got any) {
				if l := got.([]Snapshot); len(l) != 1 || l[0].ID != "s-1" {
					t.Fatalf("%+v", l)
				}
			}},
		{name: "snapshot", status: 201, answer: `{"id":"s-2","name":"n"}`, call: func() (any, error) { return sb.Snapshot(ctx, "n", "c-9") },
			method: "POST", uri: "/api/xbin/sandboxes/sb-1/snapshots", body: `{"name":"n","clientId":"c-9"}`},
		{name: "snapshot pending", status: 202, answer: `{"id":"s-3","name":"n","created":7,"pending":true}`,
			call:   func() (any, error) { return sb.Snapshot(ctx, "n", "") },
			method: "POST", uri: "/api/xbin/sandboxes/sb-1/snapshots", body: `{"name":"n"}`,
			check: func(t *testing.T, got any) {
				if s := got.(*Snapshot); s.ID != "s-3" || !s.Pending {
					t.Fatalf("%+v", s)
				}
			}},
		{name: "restore", answer: info, call: func() (any, error) { return sb.RestoreSnapshot(ctx, "s-2") },
			method: "POST", uri: "/api/xbin/sandboxes/sb-1/snapshots/s-2/restore"},
		{name: "delete snapshot", status: 204, call: func() (any, error) { return nil, sb.DeleteSnapshot(ctx, "s-2") },
			method: "DELETE", uri: "/api/xbin/sandboxes/sb-1/snapshots/s-2"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rc.answer(c.status, c.answer)
			got, err := c.call()
			if err != nil {
				t.Fatal(err)
			}
			seen := rc.take()
			if len(seen) != 1 {
				t.Fatalf("%d requests", len(seen))
			}
			s := seen[0]
			if s.Method != c.method || s.URI != c.uri || s.Auth != "Bearer tok" {
				t.Fatalf("sent %s %s (%s), want %s %s", s.Method, s.URI, s.Auth, c.method, c.uri)
			}
			if c.ctype != "" && s.CType != c.ctype {
				t.Fatalf("Content-Type %q", s.CType)
			}
			if c.body != "" {
				var want, have any
				if err := json.Unmarshal([]byte(c.body), &want); err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal([]byte(s.Body), &have); err != nil || fmt.Sprint(want) != fmt.Sprint(have) {
					t.Fatalf("body %s, want %s", s.Body, c.body)
				}
			} else if s.Method == "POST" && s.Body != "" {
				t.Fatalf("body %q, want none", s.Body)
			}
			if c.check != nil {
				c.check(t, got)
			}
		})
	}
}

// badExecIDs change the route or fail the runtime's exec id grammar;
// badSnapshotIDs fail the snapshot id grammar.
var (
	badExecIDs = []string{"", ".", "..", "nope", "e1", "s-1", "a/b", "ab12cd-1/../x", "../ab12cd-1", "..%2F..%2Fsb-2",
		"ab12cd-1%2Fx", "ab12cd-1?x=1", "ab12cd-1#f", "ab12cd-1/", "AB12CD-1", "ab12cd-", "ab12c-1", "ab12cd-1234567890123",
		"ab12cd-1\n", " ab12cd-1", "ab12cd-1.", "ab12cd-%31"}
	badSnapshotIDs = []string{"", ".", "..", "s1", "s-", "s-x", "S-1", "s-1/..", "s-1/restore", "s-1%2F", "s-1?", "s-1#",
		"s-1234567890123", "ab12cd-1", "s-1\n"}
)

// A name, exec or snapshot id that would change the route, or an id that
// fails the runtime's grammar, is refused before anything is sent — by
// every call that takes one.
func TestSandboxRouteSegments(t *testing.T) {
	rc := &recorder{}
	sbx := fakeGateway(t, rc)
	ctx := context.Background()
	for _, name := range []string{"", ".", "..", "a/b", "runtime", "policy", "copy"} {
		if _, err := sbx.Get(ctx, name); !isInvalid(err) {
			t.Fatalf("name %q: %v", name, err)
		}
	}
	sb := sbx.Sandbox("sb-1")
	for _, id := range badExecIDs {
		if IsExecID(id) {
			t.Fatalf("IsExecID(%q)", id)
		}
		calls := map[string]func() error{
			"GetExec": func() error { _, err := sb.GetExec(ctx, id); return err },
			"Kill":    func() error { return sb.Kill(ctx, id) },
			"Output":  func() error { _, err := sb.Output(ctx, id, OutputQuery{}); return err },
			"Stdin":   func() error { return sb.Stdin(ctx, id, strings.NewReader("x"), true) },
			"Signal":  func() error { return sb.Signal(ctx, id, "INT", true) },
			"Resize":  func() error { return sb.Resize(ctx, id, 1, 1) },
			"DialTTY": func() error { _, err := sb.DialTTY(ctx, id, TTYOptions{}); return err },
			"Follow": func() error {
				for _, err := range sb.Follow(ctx, id, 0) {
					return err
				}
				return nil
			},
		}
		for name, call := range calls {
			if err := call(); !isInvalid(err) {
				t.Fatalf("%s(%q): %v", name, id, err)
			}
		}
	}
	for _, id := range badSnapshotIDs {
		if IsSnapshotID(id) {
			t.Fatalf("IsSnapshotID(%q)", id)
		}
		if _, err := sb.RestoreSnapshot(ctx, id); !isInvalid(err) {
			t.Fatalf("RestoreSnapshot(%q): %v", id, err)
		}
		if err := sb.DeleteSnapshot(ctx, id); !isInvalid(err) {
			t.Fatalf("DeleteSnapshot(%q): %v", id, err)
		}
	}
	if n := len(rc.take()); n != 0 {
		t.Fatalf("%d requests sent", n)
	}
	for _, id := range []string{"ab12cd-1", "000000-0", "ffffff-123456789012"} {
		if !IsExecID(id) {
			t.Fatalf("IsExecID(%q) = false", id)
		}
	}
	for _, id := range []string{"s-0", "s-1", "s-123456789012"} {
		if !IsSnapshotID(id) {
			t.Fatalf("IsSnapshotID(%q) = false", id)
		}
	}
	// a long id is cut short in the refusal (it may be a consumer's)
	if _, err := sb.GetExec(ctx, strings.Repeat("x", 4096)); !isInvalid(err) || len(err.Error()) > 200 {
		t.Fatalf("a long id: %v", err)
	}
}

func isInvalid(err error) bool {
	var e *SandboxError
	return errors.As(err, &e) && e.Refusal == "invalid" && e.Status == 400
}

// Refusals come back as *SandboxError, with the errors.Is sentinels.
func TestSandboxErrors(t *testing.T) {
	var mu sync.Mutex
	status, body, hdr := 0, "", http.Header{}
	sbx := fakeGateway(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		for k, v := range hdr {
			w.Header()[k] = v
		}
		w.WriteHeader(status)
		io.WriteString(w, body)
	}))
	set := func(s int, b string, h http.Header) {
		mu.Lock()
		status, body, hdr = s, b, h
		mu.Unlock()
	}
	ctx := context.Background()
	sb := sbx.Sandbox("sb-1")

	set(404, `{"error":"no sandbox sb-1","refusal":"not-found"}`, nil)
	_, err := sbx.Get(ctx, "sb-1")
	var se *SandboxError
	if !errors.As(err, &se) || se.Status != 404 || se.Message != "no sandbox sb-1" || !errors.Is(err, ErrSandboxNotFound) || errors.Is(err, ErrSandboxLost) {
		t.Fatalf("404: %#v", err)
	}
	set(410, `{"error":"gone","refusal":"lost"}`, nil)
	if _, err := sb.Output(ctx, "ab12cd-1", OutputQuery{}); !errors.Is(err, ErrSandboxLost) {
		t.Fatalf("410: %v", err)
	}
	set(409, `{"error":"stopped","refusal":"state","state":"stopped"}`, nil)
	if err := sb.Resize(ctx, "ab12cd-1", 1, 1); !errors.Is(err, ErrSandboxState) || !errors.As(err, &se) || se.State != "stopped" {
		t.Fatalf("409: %v", err)
	}
	set(412, `{"error":"changed","refusal":"precondition","etag":"\"abc\""}`, nil)
	if _, err := sb.WriteFile(ctx, "/f", strings.NewReader("x"), WriteOptions{IfMatch: "old"}); !errors.As(err, &se) || se.Refusal != "precondition" || se.ETag != "abc" {
		t.Fatalf("412: %v", err)
	}
	set(503, `{"error":"starting","refusal":"unavailable","retryAfterMs":1500}`, nil)
	if _, err := sbx.Runtime(ctx); !errors.As(err, &se) || se.RetryAfter != 1500*time.Millisecond || !strings.Contains(se.Error(), "unavailable: starting") {
		t.Fatalf("503: %v", err)
	}
	// not a refusal of the runtime's: an xbind without these routes, a gateway
	set(404, "404 page not found\n", nil)
	if _, err := sbx.Get(ctx, "sb-1"); !errors.As(err, &se) || se.Refusal != "" || se.Message != "404 page not found" || errors.Is(err, ErrSandboxNotFound) {
		t.Fatalf("plain 404: %#v", err)
	}
	set(503, "busy", http.Header{"Retry-After": {"3"}})
	if _, err := sbx.List(ctx); !errors.As(err, &se) || se.RetryAfter != 3*time.Second {
		t.Fatalf("Retry-After: %#v", err)
	}
	set(403, `{"error":"missing grant","detail":"cap:sandboxes"}`, nil)
	if _, err := sbx.List(ctx); !errors.As(err, &se) || se.Status != 403 || se.Message != "missing grant" {
		t.Fatalf("xbind's own 403: %#v", err)
	}
	// a *SandboxError goes back out unchanged
	rec := httptest.NewRecorder()
	WriteSandboxError(rec, &SandboxError{Status: 409, Refusal: "state", Message: "stopped", State: "stopped"})
	if rec.Code != 409 || strings.TrimSpace(rec.Body.String()) != `{"error":"stopped","refusal":"state","state":"stopped"}` {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	rec = httptest.NewRecorder()
	WriteSandboxError(rec, errors.New("boom"))
	if rec.Code != 500 {
		t.Fatal(rec.Code)
	}
}

// Follow reads across a gap the ring dropped and on to the exec's end,
// skipping empty long-polls; an error ends it.
func TestSandboxFollow(t *testing.T) {
	b64 := base64.StdEncoding.EncodeToString
	var mu sync.Mutex
	var queries []string
	polls := 0
	sbx := fakeGateway(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		mu.Lock()
		queries = append(queries, r.URL.RawQuery)
		mu.Unlock()
		if !strings.HasSuffix(r.URL.Path, "/execs/ab12cd-1/output") {
			w.WriteHeader(410)
			io.WriteString(w, `{"error":"gone","refusal":"lost"}`)
			return
		}
		if q.Get("encoding") != "base64" || q.Get("waitMs") != "30000" {
			t.Errorf("query %s", r.URL.RawQuery)
		}
		switch q.Get("since") {
		case "": // 0: the ring dropped the first 100 bytes
			fmt.Fprintf(w, `{"start":100,"end":110,"total":110,"ringStart":100,"data":%q,"encoding":"base64","state":"running"}`, b64([]byte("0123456789")))
		case "110":
			mu.Lock()
			polls++
			p := polls
			mu.Unlock()
			if p == 1 { // a long-poll that ran out
				io.WriteString(w, `{"start":110,"end":110,"total":110,"ringStart":100,"data":"","encoding":"base64","state":"running"}`)
				return
			}
			fmt.Fprintf(w, `{"start":110,"end":115,"total":115,"ringStart":100,"data":%q,"encoding":"base64","state":"running"}`, b64([]byte{0xff, 'a', 'b', 'c', 'd'}))
		case "115":
			io.WriteString(w, `{"start":115,"end":115,"total":115,"ringStart":100,"data":"","encoding":"base64","state":"exited","exitCode":3}`)
		default:
			t.Errorf("since %s", q.Get("since"))
		}
	}))
	ctx := context.Background()
	var got []OutputChunk
	var data []byte
	for c, err := range sbx.Sandbox("sb-1").Follow(ctx, "ab12cd-1", 0) {
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, c)
		b, err := c.Bytes()
		if err != nil {
			t.Fatal(err)
		}
		data = append(data, b...)
	}
	if len(got) != 3 || got[0].Start != 100 || got[2].State != "exited" || *got[2].ExitCode != 3 || string(data) != "0123456789\xffabcd" {
		t.Fatalf("%d chunks %+v, data %q", len(got), got, data)
	}
	// stopping early sends no more reads
	mu.Lock()
	queries = nil
	mu.Unlock()
	for range sbx.Sandbox("sb-1").Follow(ctx, "ab12cd-1", 0) {
		break
	}
	mu.Lock()
	if len(queries) != 1 {
		t.Fatalf("reads after a break: %v", queries)
	}
	mu.Unlock()
	// a lost exec ends it with the error
	n := 0
	for _, err := range sbx.Sandbox("sb-1").Follow(ctx, "ab12cd-9", 0) {
		n++
		if !errors.Is(err, ErrSandboxLost) {
			t.Fatalf("lost: %v", err)
		}
	}
	if n != 1 {
		t.Fatal(n)
	}
}

// Stdin, WriteFile and PutTar stream their bodies: the runtime sees the
// first part before the caller has written the rest.
func TestSandboxStreamingBodies(t *testing.T) {
	type got struct {
		uri, ctype string
		body       []byte
	}
	first := make(chan struct{}, 1)
	done := make(chan got, 1)
	sbx := fakeGateway(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		head := make([]byte, 5)
		if _, err := io.ReadFull(r.Body, head); err != nil {
			t.Error(err)
			return
		}
		first <- struct{}{}
		rest, _ := io.ReadAll(r.Body)
		done <- got{r.RequestURI, r.Header.Get("Content-Type"), append(head, rest...)}
		if strings.Contains(r.URL.Path, "/files/content") {
			io.WriteString(w, `{"path":"/f","type":"file","size":10,"etag":"e2"}`)
			return
		}
		w.WriteHeader(204)
	}))
	sb := sbx.Sandbox("sb-1")
	ctx := context.Background()
	cases := []struct {
		name, uri, ctype string
		call             func(r io.Reader) error
	}{
		{"stdin", "/api/xbin/sandboxes/sb-1/execs/ab12cd-1/stdin?eof=1", "application/octet-stream",
			func(r io.Reader) error { return sb.Stdin(ctx, "ab12cd-1", r, true) }},
		{"write file", "/api/xbin/sandboxes/sb-1/files/content?ifNoneMatch=%2A&mkdirs=1&mode=0600&path=%2Ff", "application/octet-stream",
			func(r io.Reader) error {
				st, err := sb.WriteFile(ctx, "/f", r, WriteOptions{Mode: "0600", Mkdirs: true, IfNoneMatch: "*"})
				if err == nil && st.ETag != "e2" {
					err = fmt.Errorf("stat %+v", st)
				}
				return err
			}},
		{"put tar", "/api/xbin/sandboxes/sb-1/tar?mkdirs=1&path=%2Fw", "application/x-tar",
			func(r io.Reader) error { return sb.PutTar(ctx, "/w", r, true) }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pr, pw := io.Pipe()
			errc := make(chan error, 1)
			go func() { errc <- c.call(pr) }()
			pw.Write([]byte("part1"))
			select {
			case <-first:
			case <-time.After(10 * time.Second):
				t.Fatal("the runtime saw nothing before the body ended: not streamed")
			}
			pw.Write([]byte("part2"))
			pw.Close()
			if err := <-errc; err != nil {
				t.Fatal(err)
			}
			g := <-done
			if g.uri != c.uri || g.ctype != c.ctype || string(g.body) != "part1part2" {
				t.Fatalf("%+v", g)
			}
		})
	}
}

// fakeManager serves h as a manager tile's backend would (the consumer's
// side of Forward).
func fakeManager(t *testing.T, h http.HandlerFunc) *httptest.Server {
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

// Forward passes the manager's own query, streams both bodies, copies the
// status and headers, and never forwards the consumer's credentials or
// X-XBin-* headers — nor xbind's Set-Cookie back.
func TestSandboxForward(t *testing.T) {
	type seen struct {
		method, uri string
		header      http.Header
		body        string
	}
	seenc := make(chan seen, 4)
	sbx := fakeGateway(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		seenc <- seen{r.Method, r.RequestURI, r.Header.Clone(), string(b)}
		switch {
		case strings.HasSuffix(r.URL.Path, "/ab12cd-1/output"):
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("ETag", `"v1"`)
			w.Header().Set("Set-Cookie", "s=1")
			w.Header().Set("X-XBin-Leak", "1")
			io.WriteString(w, `{"start":5}`)
		case strings.HasSuffix(r.URL.Path, "/tar"):
			w.WriteHeader(204)
		default:
			w.WriteHeader(410)
			io.WriteString(w, `{"error":"gone","refusal":"lost"}`)
		}
	}))
	sb := sbx.Sandbox("sb-1")
	mgr := fakeManager(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/output":
			sb.Forward(w, r, ExecOutput("ab12cd-1"), url.Values{"since": {"5"}})
		case "/tar":
			sb.Forward(w, r, TarRoute(), url.Values{"path": {"/w"}})
		case "/lost":
			sb.Forward(w, r, ExecOutput("ab12cd-9"), nil)
		case "/bad":
			sb.Forward(w, r, ExecOutput("../../x"), nil)
		}
	})
	req, _ := http.NewRequest("GET", mgr.URL+"/output?since=1&frame=evil", nil)
	req.Header.Set("Cookie", "session=consumer")
	req.Header.Set("Authorization", "Bearer consumer")
	req.Header.Set("X-XBin-From", "apps/evil")
	req.Header.Set("Sbx-User", "mallory")
	req.Header.Set("X-Other", "kept")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	s := <-seenc
	if s.method != "GET" || s.uri != "/api/xbin/sandboxes/sb-1/execs/ab12cd-1/output?since=5" {
		t.Fatalf("forwarded %s %s", s.method, s.uri)
	}
	if s.header.Get("Authorization") != "Bearer tok" || s.header.Get("Cookie") != "" || s.header.Get("X-XBin-From") != "" ||
		s.header.Get("Sbx-User") != "" || s.header.Get("X-Other") != "kept" {
		t.Fatalf("forwarded headers %v", s.header)
	}
	if resp.StatusCode != 200 || string(b) != `{"start":5}` || resp.Header.Get("ETag") != `"v1"` ||
		resp.Header.Get("Content-Type") != "application/json" || resp.Header.Get("Set-Cookie") != "" || resp.Header.Get("X-XBin-Leak") != "" {
		t.Fatalf("answer %d %v %s", resp.StatusCode, resp.Header, b)
	}

	// a request body streams through
	resp, err = http.Post(mgr.URL+"/tar", "application/x-tar", strings.NewReader("TARBYTES"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	s = <-seenc
	if resp.StatusCode != 204 || s.method != "POST" || s.body != "TARBYTES" || s.header.Get("Content-Type") != "application/x-tar" {
		t.Fatalf("tar: %d %+v", resp.StatusCode, s)
	}

	// a refusal passes through unchanged
	resp, err = http.Get(mgr.URL + "/lost")
	if err != nil {
		t.Fatal(err)
	}
	b, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	<-seenc
	if resp.StatusCode != 410 || string(b) != `{"error":"gone","refusal":"lost"}` {
		t.Fatalf("lost: %d %s", resp.StatusCode, b)
	}

	// an id that would leave the sandbox's routes never goes out
	resp, err = http.Get(mgr.URL + "/bad")
	if err != nil {
		t.Fatal(err)
	}
	b, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 400 || !strings.Contains(string(b), `"refusal":"invalid"`) {
		t.Fatalf("bad id: %d %s", resp.StatusCode, b)
	}
	select {
	case s := <-seenc:
		t.Fatalf("forwarded %s", s.uri)
	default:
	}

	// xbind unreachable: 503 unavailable in the contract's shape
	t.Setenv("XBIN_GATEWAY", filepath.Join(t.TempDir(), "nothing.sock"))
	clientOnce = sync.Once{}
	down := SandboxAPI().Sandbox("sb-1")
	mgr2 := fakeManager(t, func(w http.ResponseWriter, r *http.Request) { down.Forward(w, r, TarRoute(), nil) })
	resp, err = http.Get(mgr2.URL)
	if err != nil {
		t.Fatal(err)
	}
	b, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 503 || !strings.Contains(string(b), `"refusal":"unavailable"`) {
		t.Fatalf("down: %d %s", resp.StatusCode, b)
	}
}

// Every builder's route, forwarded where it says, with the manager's
// method; a route whose id fails its grammar (or that no builder made) is
// answered 400 invalid, and the runtime sees nothing.
func TestSandboxRoutes(t *testing.T) {
	seenc := make(chan string, 64)
	sbx := fakeGateway(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenc <- r.Method + " " + r.RequestURI
		w.WriteHeader(204)
	}))
	sb := sbx.Sandbox("sb-1")
	var mu sync.Mutex
	var cur SandboxRoute
	mgr := fakeManager(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		rt := cur
		mu.Unlock()
		sb.Forward(w, r, rt, url.Values{"k": {"v"}})
	})
	do := func(method string, rt SandboxRoute) (int, string) {
		t.Helper()
		mu.Lock()
		cur = rt
		mu.Unlock()
		req, _ := http.NewRequest(method, mgr.URL+"/x", nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp.StatusCode, string(b)
	}
	const base = "/api/xbin/sandboxes/sb-1/"
	good := []struct {
		method string
		rt     SandboxRoute
		path   string
	}{
		{"GET", ExecRoute("ab12cd-1"), "execs/ab12cd-1"},
		{"DELETE", ExecRoute("ab12cd-1"), "execs/ab12cd-1"},
		{"GET", ExecOutput("ab12cd-1"), "execs/ab12cd-1/output"},
		{"POST", ExecStdin("ffffff-123456789012"), "execs/ffffff-123456789012/stdin"},
		{"POST", ExecSignal("000000-0"), "execs/000000-0/signal"},
		{"POST", ExecResize("ab12cd-1"), "execs/ab12cd-1/resize"},
		{"GET", ExecTTY("ab12cd-1"), "execs/ab12cd-1/tty"},
		{"GET", FilesRoute(FilesStat), "files/stat"},
		{"PUT", FilesRoute(FilesContent), "files/content"},
		{"GET", FilesRoute(FilesList), "files/list"},
		{"POST", FilesRoute(FilesMkdir), "files/mkdir"},
		{"POST", FilesRoute(FilesRemove), "files/remove"},
		{"POST", FilesRoute(FilesMove), "files/move"},
		{"GET", TarRoute(), "tar"},
		{"PUT", TarRoute(), "tar"},
	}
	for _, g := range good {
		if code, body := do(g.method, g.rt); code != 204 {
			t.Fatalf("%s %s: %d %s", g.method, g.path, code, body)
		}
		if got := <-seenc; got != g.method+" "+base+g.path+"?k=v" {
			t.Fatalf("%s %s went to %s", g.method, g.path, got)
		}
	}
	var bad []SandboxRoute
	for _, id := range badExecIDs {
		bad = append(bad, ExecRoute(id), ExecOutput(id), ExecStdin(id), ExecSignal(id), ExecResize(id), ExecTTY(id))
	}
	for _, op := range []FilesOp{"", "Stat", "stat/../../x", "..", "content?x", "chmod"} {
		bad = append(bad, FilesRoute(op))
	}
	bad = append(bad, SandboxRoute{})
	for _, rt := range bad {
		code, body := do("GET", rt)
		if code != 400 || !strings.Contains(body, `"refusal":"invalid"`) {
			t.Fatalf("%+v: %d %s", rt, code, body)
		}
	}
	// a relayed terminal to a bad exec id is refused the same way
	mgr2 := fakeManager(t, func(w http.ResponseWriter, r *http.Request) { sb.RelayTTY(w, r, "..%2Fsb-2", TTYOptions{}) })
	resp, err := http.Get(mgr2.URL)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 400 || !strings.Contains(string(b), `"refusal":"invalid"`) {
		t.Fatalf("RelayTTY: %d %s", resp.StatusCode, b)
	}
	select {
	case got := <-seenc:
		t.Fatalf("a refused route reached the runtime: %s", got)
	default:
	}
}

func wsAccept(key string) string {
	h := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	return base64.StdEncoding.EncodeToString(h[:])
}

// RelayTTY and RelayNewTTY tunnel a WebSocket upgrade byte for byte: the
// consumer's handshake key reaches the runtime, whose accept comes back;
// the bytes after it pass both ways unchanged; the consumer's cookie,
// credential, X-XBin-*, Sbx-User and extension offer don't pass.
func TestSandboxRelayTTY(t *testing.T) {
	seenc := make(chan *http.Request, 2)
	sbx := fakeGateway(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenc <- r
		conn, brw, err := http.NewResponseController(w).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		fmt.Fprintf(brw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n", wsAccept(r.Header.Get("Sec-WebSocket-Key")))
		brw.Flush()
		buf := make([]byte, 64)
		for {
			n, err := brw.Read(buf)
			if err != nil {
				return
			}
			conn.Write(append([]byte("echo:"), buf[:n]...))
		}
	}))
	sb := sbx.Sandbox("sb-1")
	mgr := fakeManager(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/new" {
			sb.RelayNewTTY(w, r, TTYStart{Cwd: "/w", Rows: 24, Cols: 80, UID: ptr(0), ForUser: "alice", SessionID: "s-1"})
			return
		}
		sb.RelayTTY(w, r, "ab12cd-1", TTYOptions{SessionID: "s-1", SandboxID: "sb-x", ForUser: "alice"})
	})
	dial := func(path string) (net.Conn, *bufio.Reader, *http.Response) {
		c, err := net.Dial("tcp", mgr.Listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.Close() })
		fmt.Fprintf(c, "GET %s HTTP/1.1\r\nHost: m\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"+
			"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n"+
			"Sec-WebSocket-Extensions: permessage-deflate\r\nCookie: s=1\r\nAuthorization: Bearer consumer\r\n"+
			"X-XBin-From: apps/evil\r\nSbx-User: bob\r\n\r\n", path)
		br := bufio.NewReader(c)
		resp, err := http.ReadResponse(br, nil)
		if err != nil {
			t.Fatal(err)
		}
		return c, br, resp
	}
	c, br, resp := dial("/attach?sessionId=evil")
	if resp.StatusCode != 101 || resp.Header.Get("Sec-WebSocket-Accept") != wsAccept("dGhlIHNhbXBsZSBub25jZQ==") {
		t.Fatalf("handshake %d %v", resp.StatusCode, resp.Header)
	}
	r := <-seenc
	if r.URL.Path != "/api/xbin/sandboxes/sb-1/execs/ab12cd-1/tty" || r.URL.RawQuery != "forUser=alice&sandboxId=sb-x&sessionId=s-1" {
		t.Fatalf("attached %s", r.RequestURI)
	}
	h := r.Header
	if h.Get("Authorization") != "Bearer tok" || h.Get("Sec-WebSocket-Key") != "dGhlIHNhbXBsZSBub25jZQ==" || h.Get("Upgrade") != "websocket" ||
		h.Get("Cookie") != "" || h.Get("X-XBin-From") != "" || h.Get("Sbx-User") != "" || h.Get("Sec-WebSocket-Extensions") != "" {
		t.Fatalf("forwarded headers %v", h)
	}
	// a masked binary frame, as a browser sends it, and raw bytes back
	frame := []byte{0x82, 0x83, 1, 2, 3, 4, 'l' ^ 1, 's' ^ 2, '\r' ^ 3}
	c.Write(frame)
	got := make([]byte, 5+len(frame))
	c.SetReadDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.ReadFull(br, got); err != nil || !bytes.Equal(got, append([]byte("echo:"), frame...)) {
		t.Fatalf("relayed %q %v", got, err)
	}

	_, _, resp = dial("/new")
	r = <-seenc
	if resp.StatusCode != 101 || r.URL.Path != "/api/xbin/sandboxes/sb-1/tty" || r.URL.RawQuery != "cols=80&cwd=%2Fw&forUser=alice&rows=24&sessionId=s-1&uid=0" {
		t.Fatalf("new tty: %d %s", resp.StatusCode, r.RequestURI)
	}
}
