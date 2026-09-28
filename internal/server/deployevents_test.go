package server_test

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/boot"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/users"
	"github.com/xbin-dev/xbin/internal/util"
)

// evNodeServer is the smallest node backend: it listens, and answers.
const evNodeServer = "require('http').createServer((q, s) => s.end('ok')).listen(process.env.XBIN_SOCKET);\n"

// evDaemon boots an xbind in-process (auth on, no isolation) on a
// zero-state workspace and returns its address and owner token.
func evDaemon(t *testing.T) (ws, addr, owner string) {
	t.Helper()
	return evDaemonWith(t, map[string]string{
		"xbin.json":                     `{"schema":1}`,
		"apps/ev/xbin.json":             `{}`,
		"apps/ev/index.html":            "<!doctype html><html><head></head><body>ev</body></html>\n",
		"apps/evnode/xbin.json":         `{"runtime":"node"}`,
		"apps/evnode/backend/server.js": evNodeServer,
		"apps/evbroken/xbin.json":       `{"runtime":"node"}`,
		"apps/evbroken/README.txt":      "no backend/server.js: every build fails\n",
		"notes+ideas/xbin.json":         `{}`,
		"notes+ideas/index.html":        "<!doctype html><html><head></head><body>n</body></html>\n",
	})
}

// evDaemonWith is evDaemon on a workspace of the given files.
func evDaemonWith(t *testing.T, files map[string]string) (ws, addr, owner string) {
	t.Helper()
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	ws = filepath.Join(t.TempDir(), "ws")
	for rel, body := range files {
		p := filepath.Join(ws, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if sdk, err := filepath.Abs("../../sdk"); err == nil {
		t.Setenv("XBIN_SDK_PATH", sdk)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ready := make(chan string, 1)
	cfg := &boot.Config{Workspace: ws, Listener: ln, Listen: ln.Addr().String(), InsecureVault: true,
		Privileges: boot.NoPrivileges{}, Stdout: io.Discard, Version: "test", LimitMem: "2G",
		Ready: func(a string) { ready <- a }}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- boot.Run(ctx, cfg) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Error("the daemon did not stop")
		}
	})
	select {
	case addr = <-ready:
	case err := <-done:
		t.Fatalf("boot: %v", err)
	case <-time.After(30 * time.Second):
		t.Fatal("the daemon never served")
	}
	tok, err := os.ReadFile(filepath.Join(ws, ".xbin", "token"))
	if err != nil {
		t.Fatal(err)
	}
	return ws, addr, strings.TrimSpace(string(tok))
}

// evTS masks the one run-to-run value of today's events: a status
// report's unix timestamp.
var evTS = regexp.MustCompile(`"ts":[0-9]+`)

// covers PO-5 Z3 SC-ZERO — the exact bytes a /ws/events subscriber
// receives for a zero-state workspace's events, from their real
// producers: a save's reload (the watch loop), a backend's first start
// and a save's sequence reload, build-start, build-ok, a failing build's
// reload, build-start, build-error, and status reports. No event carries a
// deployment key and no deployments event appears; a tile whose own path
// holds + is named as itself. With no node on PATH the running-backend
// sequences are skipped, said so. Hand-maintained goldens: changing one is
// a compat change (12-compat.md).
func TestEventBytesZeroState(t *testing.T) {
	if testing.Short() {
		t.Skip("boots a workspace")
	}
	_, err := exec.LookPath("node")
	haveNode := err == nil
	ws, addr, owner := evDaemon(t)
	hdr := http.Header{"Authorization": {"Bearer " + owner}}
	conn, resp, err := websocket.DefaultDialer.Dial("ws://"+addr+"/ws/events", hdr)
	if err != nil {
		t.Fatalf("events socket: %v %v", err, resp)
	}
	defer conn.Close()
	frames := make(chan string, 64)
	go func() {
		for {
			_, b, err := conn.ReadMessage()
			if err != nil {
				close(frames)
				return
			}
			frames <- evTS.ReplaceAllString(string(b), `"ts":<ts>`)
		}
	}()
	// expect reads exactly the frames want names, in order, then checks that
	// nothing else arrives for a bounded while (a negative: no condition to
	// wait for).
	expect := func(step string, want ...string) {
		t.Helper()
		for i, w := range want {
			select {
			case got := <-frames:
				if got != w {
					t.Errorf("%s: frame %d\n  got  %q\n  want %q", step, i+1, got, w)
				}
			case <-time.After(60 * time.Second):
				t.Fatalf("%s: frame %d never arrived (want %q)", step, i+1, w)
			}
		}
		select {
		case got := <-frames:
			t.Errorf("%s: an extra frame %q", step, got)
		case <-time.After(700 * time.Millisecond):
		}
	}
	save := func(rel string) {
		t.Helper()
		f, err := os.OpenFile(filepath.Join(ws, filepath.FromSlash(rel)), os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = f.WriteString("\n")
		f.Close()
	}
	call := func(method, path, body string) {
		t.Helper()
		req, _ := http.NewRequest(method, "http://"+addr+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+owner)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, r.Body)
		r.Body.Close()
	}

	save("apps/ev/index.html")
	expect("a static tile's save", `{"type":"reload","component":"apps/ev"}`+"\n")
	save("notes+ideas/index.html")
	expect("a + tile's save", `{"type":"reload","component":"notes+ideas"}`+"\n")

	call("GET", "/api/apps/evbroken/ping", "")
	expect("a backend that can't build, first request",
		`{"type":"build-start","component":"apps/evbroken"}`+"\n",
		`{"type":"build-error","component":"apps/evbroken","text":"build failed:\nentry backend/server.js not found (set \"entry\" in xbin.json)"}`+"\n")
	save("apps/evbroken/README.txt")
	expect("a failing build's save",
		`{"type":"reload","component":"apps/evbroken"}`+"\n",
		`{"type":"build-start","component":"apps/evbroken"}`+"\n",
		`{"type":"build-error","component":"apps/evbroken","text":"build failed:\nentry backend/server.js not found (set \"entry\" in xbin.json)"}`+"\n")

	if haveNode {
		call("GET", "/api/apps/evnode/ping", "")
		expect("a backend's first start",
			`{"type":"build-start","component":"apps/evnode"}`+"\n",
			`{"type":"build-ok","component":"apps/evnode"}`+"\n")
		save("apps/evnode/backend/server.js")
		expect("a backend's save and swap",
			`{"type":"reload","component":"apps/evnode"}`+"\n",
			`{"type":"build-start","component":"apps/evnode"}`+"\n",
			`{"type":"build-ok","component":"apps/evnode"}`+"\n")
	} else {
		t.Log("SKIP (partial): no node on PATH — the running-backend sequences are not checked")
	}

	call("POST", "/api/xbin/tile-report", `{"level":"warn","message":"disk low","component":"apps/ev"}`)
	expect("a status report", `{"type":"status","component":"apps/ev","data":{"level":"warn","message":"disk low","ts":<ts>}}`+"\n")
	call("POST", "/api/xbin/tile-report?component=notes%2Bideas", `{"level":"info","message":"hi","transient":true}`)
	expect("a transient status on a + tile", `{"type":"status","component":"notes+ideas","data":{"level":"info","message":"hi","transient":true,"ts":<ts>}}`+"\n")
	call("POST", "/api/xbin/tile-report", `{"level":"ok","component":"apps/ev"}`)
	expect("a status cleared", `{"type":"status","component":"apps/ev","data":{"level":"ok","message":"","ts":<ts>}}`+"\n")
}

// ---- non-primary deployments' events (D127h, D127r; 11-contract §3) ----

// evFrame is one /ws/events frame as a subscriber receives it.
type evFrame struct {
	raw        string
	Type       string         `json:"type"`
	Component  string         `json:"component"`
	Deployment string         `json:"deployment"`
	Text       string         `json:"text"`
	Data       map[string]any `json:"data"`
}

func (f evFrame) op() string  { s, _ := f.Data["op"].(string); return s }
func (f evFrame) dep() string { s, _ := f.Data["deployment"].(string); return s }

// evSock is one /ws/events subscriber, its frames in arrival order.
type evSock struct {
	name   string
	frames chan evFrame
}

// dialEvents subscribes to base's /ws/events with the given headers and
// query (a frame token rides ?frame=, as a browser's WebSocket does).
func dialEvents(t *testing.T, base, name string, h http.Header, query string) *evSock {
	t.Helper()
	u := "ws://" + strings.TrimPrefix(base, "http://") + "/ws/events"
	if query != "" {
		u += "?" + query
	}
	conn, resp, err := websocket.DefaultDialer.Dial(u, h)
	if err != nil {
		t.Fatalf("%s: events socket: %v %v", name, err, resp)
	}
	t.Cleanup(func() { conn.Close() })
	s := &evSock{name: name, frames: make(chan evFrame, 256)}
	go func() {
		defer close(s.frames)
		for {
			_, b, err := conn.ReadMessage()
			if err != nil {
				return
			}
			f := evFrame{raw: string(b)}
			_ = json.Unmarshal(b, &f)
			s.frames <- f
		}
	}()
	return s
}

// until returns the frames up to and including the first one match
// accepts, failing the test when none arrives within d.
func (s *evSock) until(t *testing.T, what string, d time.Duration, match func(evFrame) bool) []evFrame {
	t.Helper()
	var got []evFrame
	deadline := time.After(d)
	for {
		select {
		case f, ok := <-s.frames:
			if !ok {
				t.Fatalf("%s: the socket closed waiting for %s", s.name, what)
			}
			got = append(got, f)
			if match(f) {
				return got
			}
		case <-deadline:
			t.Fatalf("%s: %s never arrived; got %d frames: %v", s.name, what, len(got), raws(got))
		}
	}
}

// quiet returns the frames that arrive within d (a negative: nothing to
// wait for).
func (s *evSock) quiet(d time.Duration) []evFrame {
	var got []evFrame
	deadline := time.After(d)
	for {
		select {
		case f, ok := <-s.frames:
			if !ok {
				return got
			}
			got = append(got, f)
		case <-deadline:
			return got
		}
	}
}

func raws(fs []evFrame) []string {
	out := make([]string, len(fs))
	for i, f := range fs {
		out[i] = f.raw
	}
	return out
}

// sentinelReload matches the bare reload of tile, a zero-state event every
// subscriber receives: frames before it are all a subscriber got.
func sentinelReload(tile string) func(evFrame) bool {
	return func(f evFrame) bool { return f.Type == "reload" && f.Component == tile }
}

// ---- the server plane, with real credentials ----

// dlEvents is a server on a workspace holding apps/crm (with an
// xbin.window sub-path apps/crm/editor) and apps/other, under a Policy
// whose apps/crm has the deployments main (the primary), dev and staging,
// and apps/other main and dev: alice is an admin; on apps/crm wanda writes
// and rita reads; nora has no access.
type dlEvents struct {
	url string
	a   *auth.Auth
	hub *events.Hub
}

// dlPolicy is the fixture's Policy. Its Addressed is the plane's rule
// (deployments.Plane.Addressed) without protection, and its BusAllows
// admits a principal of apps/crm to the bus events of the namespace its
// credential binds, standing in for the broker's busFilter.
type dlPolicy struct{ server.NoopPolicy }

var dlDeployments = map[string][]string{"apps/crm": {"main", "dev", "staging"}, "apps/other": {"main", "dev"}}

func (dlPolicy) HasDeployment(tile, name string) bool {
	return name == util.MainDeployment || slices.Contains(dlDeployments[tile], name)
}
func (dlPolicy) Primary(string) string { return util.MainDeployment }
func (d dlPolicy) Addressed(p auth.Principal, tile string) (string, error) {
	switch {
	case p.Component != tile:
		return util.MainDeployment, nil
	case p.Deployment == "":
		return util.MainDeployment, nil
	case !d.HasDeployment(tile, p.Deployment):
		return "", util.NoDeployment(tile, p.Deployment)
	}
	return p.Deployment, nil
}
func (dlPolicy) BusAllows(p auth.Principal, e events.Event) bool {
	ns := e.Deployment
	if ns == "" {
		ns = util.MainDeployment
	}
	bound := p.Deployment
	if bound == "" {
		bound = util.MainDeployment
	}
	return p.Component == "apps/crm" && bound == ns
}

func newDLEvents(t *testing.T) *dlEvents {
	t.Helper()
	root := t.TempDir()
	for rel, body := range map[string]string{
		"xbin.json":             `{"schema":1}`,
		"apps/crm/xbin.json":    `{}`,
		"apps/crm/index.html":   "<!doctype html><p>crm</p>\n",
		"apps/other/xbin.json":  `{}`,
		"apps/other/index.html": "<!doctype html><p>other</p>\n",
	} {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	reg, err := registry.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	a, err := auth.Load(root, false)
	if err != nil {
		t.Fatal(err)
	}
	st, err := users.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range []users.User{
		{ID: "alice", Role: users.RoleAdmin},
		{ID: "wanda", Tiles: map[string]string{"apps/crm": users.LevelWrite}},
		{ID: "rita", Tiles: map[string]string{"apps/crm": users.LevelRead}},
		{ID: "nora"},
	} {
		if _, err := st.Upsert(u, "a-long-password"); err != nil {
			t.Fatal(err)
		}
	}
	a.SetUsers(st)
	hub := events.NewHub()
	srv := &server.Server{Reg: reg, Auth: a, Hub: hub}
	srv.InstallPolicy(dlPolicy{})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return &dlEvents{url: ts.URL, a: a, hub: hub}
}

// subscribers connects the fixture's subscribers, each by the credential a
// browser, a backend or a session would hold, and returns them once each
// receives events (a subscription starts after the upgrade answers).
func (d *dlEvents) subscribers(t *testing.T) map[string]*evSock {
	t.Helper()
	bearer := func(tok string) http.Header { return http.Header{"Authorization": {"Bearer " + tok}} }
	session := func(uid string) http.Header {
		return http.Header{"Cookie": {auth.CookieName + "=" + d.a.NewSession(uid, "127.0.0.1")}}
	}
	frame := func(tok string) string { return "frame=" + url.QueryEscape(tok) }
	d.a.RegisterInstance("tok-main", "apps/crm")
	d.a.RegisterInstanceDeployment("tok-dev", "apps/crm", "dev")
	d.a.RegisterInstanceDeployment("tok-staging", "apps/crm", "staging")
	d.a.RegisterInstance("tok-other", "apps/other")
	type sub struct {
		h http.Header
		q string
	}
	subs := map[string]sub{
		"owner": {h: bearer(d.a.OwnerTokenValue())},
		"alice": {h: session("alice")},
		"wanda": {h: session("wanda")},
		"rita":  {h: session("rita")},
		"nora":  {h: session("nora")},
		// the primary's credentials
		"mainInst":   {h: bearer("tok-main")},
		"mainFrameR": {q: frame(d.a.MintFrameToken("apps/crm", "rita", time.Hour))},
		"mainFrameW": {q: frame(d.a.MintFrameToken("apps/crm", "wanda", time.Hour))},
		// dev's own: its backend, a writer's dev frame and its xbin.window document
		"devInst":    {h: bearer("tok-dev")},
		"devFrameW":  {q: frame(d.a.MintFrameTokenDeployment("apps/crm", "wanda", "dev", time.Hour))},
		"devWindowW": {q: frame(d.a.MintFrameTokenDeployment("apps/crm/editor", "wanda", "dev", time.Hour))},
		// bound to dev, but its user only reads the tile
		"devFrameR": {q: frame(d.a.MintFrameTokenDeployment("apps/crm", "rita", "dev", time.Hour))},
		// sessions targeting dev: a writer's (the write audience), a reader's
		"termDevW": {h: bearer(d.a.MintTerminalTarget("apps/crm", "wanda", "dev"))},
		"termDevR": {h: bearer(d.a.MintTerminalTarget("apps/crm", "rita", "dev"))},
		// another deployment, and other tiles
		"stagingInst": {h: bearer("tok-staging")},
		"otherDevW":   {q: frame(d.a.MintFrameTokenDeployment("apps/other", "wanda", "dev", time.Hour))},
		"otherInst":   {h: bearer("tok-other")},
	}
	out := map[string]*evSock{}
	for name, s := range subs {
		out[name] = dialEvents(t, d.url, name, s.h, s.q)
	}
	warm := events.Event{Type: "reload", Component: "apps/zz-warm"}
	for name, s := range out {
		deadline := time.Now().Add(10 * time.Second)
		for got := false; !got; {
			d.hub.Publish(warm)
			select {
			case f := <-s.frames:
				got = f.Component == warm.Component
			case <-time.After(20 * time.Millisecond):
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s never received an event", name)
			}
		}
	}
	for _, s := range out { // drop the rest of the warm-up
		s.quiet(100 * time.Millisecond)
	}
	return out
}

// receivedBy publishes each event of evs, then a sentinel every subscriber
// receives, and returns per subscriber the frames that came before it.
func (d *dlEvents) receivedBy(t *testing.T, subs map[string]*evSock, evs ...events.Event) map[string][]evFrame {
	t.Helper()
	for _, e := range evs {
		d.hub.Publish(e)
	}
	d.hub.Publish(events.Event{Type: "reload", Component: "apps/zz-sentinel"})
	out := map[string][]evFrame{}
	for name, s := range subs {
		fs := s.until(t, "the sentinel", 10*time.Second, sentinelReload("apps/zz-sentinel"))
		out[name] = fs[:len(fs)-1]
	}
	return out
}

func names(m map[string]*evSock) []string {
	out := make([]string, 0, len(m))
	for n := range m {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func setOf(ns ...[]string) []string {
	var out []string
	for _, n := range ns {
		for _, x := range n {
			if !slices.Contains(out, x) {
				out = append(out, x)
			}
		}
	}
	sort.Strings(out)
	return out
}

func without(a, b []string) []string {
	var out []string
	for _, x := range a {
		if !slices.Contains(b, x) {
			out = append(out, x)
		}
	}
	return out
}

// crmRunnerEvent is the data the runner publishes for a non-primary
// deployment's reload or build (internal/runner's deployActivity).
type crmRunnerEvent struct {
	Op         string `json:"op"`
	Deployment string `json:"deployment"`
	Phase      string `json:"phase,omitempty"`
	Text       string `json:"text,omitempty"`
}

// covers T7 D127h SC-EVENTS PO-5 — TestDeploymentEventsFiltered's non-primary
// rows, with the principals auth builds from real credentials (the claims of
// 11-contract §7): over /ws/events, anything naming dev (the runner's reload
// and build, the plane's deploy, status and notify) reaches only the write
// audience (the owner, an admin, a writer, a writer's session targeting dev)
// and dev's own principals (its backend, a writer's dev frame and dev
// xbin.window document). Refused: the primary's frame tokens, a reader's and
// a writer's alike; the primary's backend; staging's; a dev frame whose user
// only reads; a reader's session targeting dev; readers; other tiles' dev.
// A deploy onto the primary from dev reaches dev's own principals too;
// reader forms reach the reader audience the full forms miss; a bus event
// of dev's namespace reaches admins and the principals the Policy binds to
// that namespace, never the primary's.
func TestDeploymentEventsFilteredCredentials(t *testing.T) {
	d := newDLEvents(t)
	subs := d.subscribers(t)
	W := []string{"owner", "alice", "wanda", "termDevW"}
	devOwn := []string{"devInst", "devFrameW", "devWindowW"}
	R := setOf(W, []string{"rita", "mainInst", "mainFrameR", "mainFrameW", "devInst", "devFrameW", "devWindowW",
		"devFrameR", "termDevR", "stagingInst", "otherDevW"})
	crm := func(data any) events.Event {
		return events.Event{Type: "deployments", Component: "apps/crm", Data: data}
	}
	type m = map[string]any
	for _, tc := range []struct {
		name string
		e    events.Event
		want []string
	}{
		{"the runner's reload of dev", crm(crmRunnerEvent{Op: "reload", Deployment: "dev"}), setOf(W, devOwn)},
		{"the runner's build of dev, failing", crm(crmRunnerEvent{Op: "build", Deployment: "dev", Phase: "error",
			Text: "main.go:3: undefined: nope"}), setOf(W, devOwn)},
		{"a deploy onto dev", crm(m{"op": "deploy", "id": 7, "deployment": "dev", "how": "deploy", "checkpoint": "c:1a2b3c4",
			"result": "ok", "phase": "swap", "by": "user:wanda"}), setOf(W, devOwn)},
		{"dev's status", crm(m{"op": "status", "deployment": "dev", "level": "error", "message": "db down",
			"ts": 1790000000, "transient": false}), setOf(W, devOwn)},
		{"a notification dev didn't push", crm(m{"op": "notify", "deployment": "dev", "to": "user:rita", "title": "hi",
			"at": "2026-09-27T10:00:00Z"}), setOf(W, devOwn)},
		{"staging's reload", crm(crmRunnerEvent{Op: "reload", Deployment: "staging"}), setOf(W, []string{"stagingInst"})},
		{"a promotion of dev onto the primary", crm(m{"op": "deploy", "id": 8, "deployment": "main", "how": "promote",
			"from": "dev", "checkpoint": "c:1a2b3c4", "result": "ok", "phase": "swap", "by": "user:wanda"}), setOf(W, devOwn)},
		{"its reader form", crm(m{"op": "deploy", "deployment": "main", "checkpoint": "c:1a2b3c4", "result": "ok",
			"phase": "swap", "by": "user:wanda"}), without(R, W)},
		{"a record change's reader form", crm(m{"op": "record", "what": []string{"liveReload"}}), without(R, W)},
		{"another tile's dev", events.Event{Type: "deployments", Component: "apps/other",
			Data: crmRunnerEvent{Op: "reload", Deployment: "dev"}}, []string{"alice", "owner"}},
		{"a bus event of dev's namespace", events.Event{Type: "bus", Topic: "res:apps/crm/bus/t", Deployment: "dev"},
			setOf([]string{"owner", "alice"}, []string{"devInst", "devFrameW", "devFrameR", "termDevW", "termDevR"})},
	} {
		got := d.receivedBy(t, subs, tc.e)
		var who []string
		for name, fs := range got {
			if len(fs) > 1 {
				t.Errorf("%s: %s received %d frames: %v", tc.name, name, len(fs), raws(fs))
			}
			if len(fs) > 0 {
				who = append(who, name)
			}
		}
		sort.Strings(who)
		if want := setOf(tc.want); !slices.Equal(who, want) {
			t.Errorf("%s:\n  got  %v\n  want %v\n  missing %v, extra %v", tc.name, who, want, without(want, who), without(who, want))
		}
	}
}

// covers T7 D127h SC-EVENTS — a non-primary deployment's failing build is not
// broadcast: the compiler output, in the data the runner publishes for it
// (a deployments event, op build, phase error, the bare tile), reaches the
// write audience and dev's own principals byte for byte, and nothing of it
// reaches the primary's frame and instance tokens, readers, staging, other
// tiles, or anyone as a build-error; the primary's own build-error still
// reaches every subscriber, as today.
func TestNonPrimaryBuildErrorNotBroadcast(t *testing.T) {
	d := newDLEvents(t)
	subs := d.subscribers(t)
	const text = "build failed:\n./main.go:3:2: undefined: nope"
	got := d.receivedBy(t, subs, events.Event{Type: "deployments", Component: "apps/crm",
		Data: crmRunnerEvent{Op: "build", Deployment: "dev", Phase: "error", Text: text}})
	wantRaw := `{"type":"deployments","component":"apps/crm","data":{"op":"build","deployment":"dev","phase":"error","text":` +
		strconv.Quote(text) + `}}` + "\n"
	told := []string{"owner", "alice", "wanda", "termDevW", "devInst", "devFrameW", "devWindowW"}
	for _, name := range names(subs) {
		fs := got[name]
		switch {
		case slices.Contains(told, name):
			if len(fs) != 1 || fs[0].raw != wantRaw {
				t.Errorf("%s: got %q, want %q", name, raws(fs), wantRaw)
			}
		case len(fs) != 0:
			t.Errorf("%s received dev's build output: %v", name, raws(fs))
		}
	}
	// The primary's build-error: today's broadcast, unchanged.
	got = d.receivedBy(t, subs, events.Event{Type: "build-error", Component: "apps/crm", Text: text})
	for _, name := range names(subs) {
		if fs := got[name]; len(fs) != 1 || fs[0].Type != "build-error" || fs[0].Component != "apps/crm" {
			t.Errorf("%s: the primary's build-error = %v", name, raws(fs))
		}
	}
}

// ---- the running daemon ----

// dlCall is one owner or user request to a daemon, its answer decoded.
func dlCall(t *testing.T, addr, bearer, method, path, body string) (int, map[string]any, string) {
	t.Helper()
	req, err := http.NewRequest(method, "http://"+addr+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := (&http.Client{Timeout: 90 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return resp.StatusCode, m, string(b)
}

// dlOp posts one deployments operation as the owner and, when it queued a
// deploy, waits for that deploy to finish; it returns the answer.
func dlOp(t *testing.T, addr, owner, route, body string) map[string]any {
	t.Helper()
	code, m, raw := dlCall(t, addr, owner, "POST", "/api/xbin/deployments/"+route, body)
	if code != http.StatusOK {
		t.Fatalf("POST %s %s: %d %s", route, body, code, raw)
	}
	var req struct{ Tile string }
	_ = json.Unmarshal([]byte(body), &req)
	if dep, _ := m["deploy"].(map[string]any); dep != nil {
		id, _ := dep["id"].(float64)
		for i := 0; i < 8; i++ {
			code, lm, raw := dlCall(t, addr, owner, "GET",
				"/api/xbin/deployments/log?tile="+url.QueryEscape(req.Tile)+"&id="+strconv.Itoa(int(id))+"&wait=20", "")
			if code != http.StatusOK {
				t.Fatalf("the deploy log of %s's entry %v: %d %s", route, id, code, raw)
			}
			e, _ := lm["entry"].(map[string]any)
			switch e["result"] {
			case "queued", "running":
				continue
			case "ok":
				return m
			}
			t.Fatalf("%s's deploy %v ended %v: %s", route, id, e["result"], raw)
		}
		t.Fatalf("%s's deploy %v never finished", route, id)
	}
	return m
}

// dlNeedsStore skips a test the checkpoint store can't run here: the store's
// tools run directly on a daemon without isolation, and need git and GNU
// find on the host.
func dlNeedsStore(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("boots a workspace")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git on this host: the checkpoint store's tools run directly without isolation")
	}
	if out, err := exec.Command("find", "--version").CombinedOutput(); err != nil || !strings.Contains(string(out), "GNU") {
		t.Skip("no GNU find on this host: the checkpoint store's tools run directly without isolation")
	}
}

func dlSave(t *testing.T, ws, rel, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(ws, filepath.FromSlash(rel)), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// oldTypes are the event types a non-primary deployment never rides
// (11-contract §3.2).
var oldTypes = []string{"reload", "build-start", "build-ok", "build-error", "status"}

// covers D127h D127j SC-EVENTS PO-5 — non-primary activity rides only the
// deployments type (rule C2, 11-contract §3.2), on a nested tile
// (apps/shop/admin, under apps/shop) in a running daemon: pausing live
// reload, a save while paused, adding canary with live reload attached, a
// save that reaches canary, a deploy onto canary, promoting canary onto the
// primary, disabling and enabling the tile, and reassigning the primary
// (when this xbind builds it). No event of a type the old clients act on
// (reload, build-*, status) names canary or carries a qualified component,
// and every deployments event names the bare tile: a mounted ancestor
// frame's prefix match never fires on a deployment. A save reaching canary
// is its op reload, never a bare one; a save while paused reloads nothing;
// the promotion's swap on the primary emits exactly one bare reload.
func TestNonPrimaryUsesNewEventTypes(t *testing.T) {
	dlNeedsStore(t)
	const tile, dep = "apps/shop/admin", "canary"
	ws, addr, owner := evDaemonWith(t, map[string]string{
		"xbin.json":                  `{"schema":1}`,
		"apps/shop/xbin.json":        `{}`,
		"apps/shop/index.html":       "<!doctype html><p>shop</p>\n",
		"apps/shop/admin/xbin.json":  `{}`,
		"apps/shop/admin/index.html": "<!doctype html><p>admin 1</p>\n",
		"apps/zz/xbin.json":          `{}`,
		"apps/zz/index.html":         "<!doctype html><p>sentinel</p>\n",
	})
	s := dialEvents(t, "http://"+addr, "owner", http.Header{"Authorization": {"Bearer " + owner}}, "")
	n := 0
	// sync saves the sentinel tile and returns every frame up to its reload.
	sync := func(what string) []evFrame {
		t.Helper()
		n++
		dlSave(t, ws, "apps/zz/index.html", fmt.Sprintf("<!doctype html><p>sentinel %d</p>\n", n))
		fs := s.until(t, what+"'s sentinel", 60*time.Second, sentinelReload("apps/zz"))
		return fs[:len(fs)-1]
	}
	sync("the subscription")
	var all []evFrame
	bare := func(fs []evFrame) (k int) {
		for _, f := range fs {
			if f.Type == "reload" && f.Component == tile {
				k++
			}
		}
		return k
	}
	body := func(extra string) string { return `{"tile":"` + tile + `"` + extra + `}` }

	dlOp(t, addr, owner, "live-reload/pause", body(""))
	all = append(all, sync("the pause")...)

	dlSave(t, ws, "apps/shop/admin/index.html", "<!doctype html><p>admin 2</p>\n")
	time.Sleep(1500 * time.Millisecond) // the watcher's debounce: a paused tile reloads nothing
	fs := sync("a save while paused")
	if k := bare(fs); k != 0 {
		t.Errorf("a save while live reload is paused: %d bare reloads of %s: %v", k, tile, raws(fs))
	}
	all = append(all, fs...)

	dlOp(t, addr, owner, "add", body(`,"deployment":"`+dep+`","attach":true`))
	all = append(all, sync("adding canary")...)

	dlSave(t, ws, "apps/shop/admin/index.html", "<!doctype html><p>admin 3</p>\n")
	fs = s.until(t, "canary's op reload", 60*time.Second, func(f evFrame) bool {
		return f.Type == "deployments" && f.op() == "reload" && f.dep() == dep
	})
	fs = append(fs, sync("a save reaching canary")...)
	if k := bare(fs); k != 0 {
		t.Errorf("a save reaching canary: %d bare reloads of %s: %v", k, tile, raws(fs))
	}
	all = append(all, fs...)

	dlOp(t, addr, owner, "deploy", body(`,"deployment":"`+dep+`"`))
	all = append(all, sync("a deploy onto canary")...)

	dlOp(t, addr, owner, "promote", body(`,"from":"`+dep+`","to":"main"`))
	fs = sync("the promotion")
	if k := bare(fs); k != 1 {
		t.Errorf("promoting canary's new code onto the primary: %d bare reloads of %s, want exactly 1: %v", k, tile, raws(fs))
	}
	all = append(all, fs...)

	for _, state := range []string{"disabled", "enabled"} {
		if code, _, raw := dlCall(t, addr, owner, "POST", "/api/xbin/lifecycle",
			`{"component":"`+tile+`","state":"`+state+`"}`); code != http.StatusOK {
			t.Fatalf("lifecycle %s: %d %s", state, code, raw)
		}
		all = append(all, sync("the tile "+state)...)
	}

	t.Run("reassignment", func(t *testing.T) {
		code, _, raw := dlCall(t, addr, owner, "POST", "/api/xbin/deployments/primary",
			body(`,"deployment":"`+dep+`","confirm":"data-stays"`))
		if code == http.StatusNotImplemented {
			t.Skipf("POST /deployments/primary answers 501 on this xbind (%s): the reassignment rows wait for it", strings.TrimSpace(raw))
		}
		if code != http.StatusOK {
			t.Fatalf("reassigning the primary: %d %s", code, raw)
		}
		all = append(all, sync("the reassignment")...)
	})

	sawDeploy, sawReload := false, false
	for _, f := range all {
		switch {
		case f.Type == "deployments":
			if f.Component != tile && f.Component != "apps/zz" {
				t.Errorf("a deployments event names %q, not the bare tile: %s", f.Component, f.raw)
			}
			sawDeploy = sawDeploy || (f.op() == "deploy" && f.dep() == dep)
			sawReload = sawReload || (f.op() == "reload" && f.dep() == dep)
		case slices.Contains(oldTypes, f.Type):
			if strings.Contains(f.Component, "+") || strings.Contains(f.raw, dep) || f.Deployment != "" {
				t.Errorf("a %s event speaks of a deployment: %s", f.Type, f.raw)
			}
		case f.Type != "bus" && f.Type != "term" && strings.Contains(f.Component, "+"):
			t.Errorf("a %s event carries a qualified component: %s", f.Type, f.raw)
		}
	}
	if !sawDeploy || !sawReload {
		t.Errorf("canary's activity: deploy seen %v, op reload seen %v, in %d frames", sawDeploy, sawReload, len(all))
	}
}

// covers T7 SC-EVENTS — TestPrimaryFrameTokenGetsNoNonPrimaryFacts (09-fabric
// §6.1), in a running daemon: the primary's frame tokens, minted by GET
// /frame-token for a reader (rita) and a writer (wanda), receive no
// deployments event naming dev while dev is added with live reload
// attached, reloads on a save and takes a deploy, nor does rita's own
// session; the owner's does (the control). Rita's GET /deployments is the
// reader view: the primary alone, no non-primary name or count, while
// wanda's names dev.
func TestPrimaryFrameTokenGetsNoNonPrimaryFacts(t *testing.T) {
	dlNeedsStore(t)
	const tile = "apps/crm"
	ws, addr, owner := evDaemonWith(t, map[string]string{
		"xbin.json":           `{"schema":1}`,
		"apps/crm/xbin.json":  `{}`,
		"apps/crm/index.html": "<!doctype html><p>crm 1</p>\n",
		"apps/zz/xbin.json":   `{}`,
		"apps/zz/index.html":  "<!doctype html><p>sentinel</p>\n",
	})
	signIn := map[string]string{}
	for uid, level := range map[string]string{"rita": "read", "wanda": "write"} {
		if code, _, raw := dlCall(t, addr, owner, "POST", "/api/xbin/users",
			`{"id":"`+uid+`","password":"a-long-password","tiles":{"`+tile+`":"`+level+`"}}`); code != http.StatusOK {
			t.Fatalf("create %s: %d %s", uid, code, raw)
		}
		req, _ := http.NewRequest("POST", "http://"+addr+"/api/xbin/login",
			strings.NewReader(`{"username":"`+uid+`","password":"a-long-password"}`))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var sess struct{ Token string }
		_ = json.NewDecoder(resp.Body).Decode(&sess)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || sess.Token == "" {
			t.Fatalf("%s's sign-in: %d", uid, resp.StatusCode)
		}
		signIn[uid] = sess.Token
	}
	frameOf := func(uid string) string {
		code, m, raw := dlCall(t, addr, signIn[uid], "GET", "/api/xbin/frame-token?component="+tile, "")
		tok, _ := m["token"].(string)
		if code != http.StatusOK || tok == "" || strings.Count(tok, "|") != 4 {
			t.Fatalf("%s's frame token for the primary: %d %s", uid, code, raw)
		}
		return tok
	}
	base := "http://" + addr
	socks := map[string]*evSock{
		"owner":      dialEvents(t, base, "owner", http.Header{"Authorization": {"Bearer " + owner}}, ""),
		"rita":       dialEvents(t, base, "rita", http.Header{"Authorization": {"Bearer " + signIn["rita"]}}, ""),
		"ritaFrame":  dialEvents(t, base, "ritaFrame", nil, "frame="+url.QueryEscape(frameOf("rita"))),
		"wandaFrame": dialEvents(t, base, "wandaFrame", nil, "frame="+url.QueryEscape(frameOf("wanda"))),
	}
	n := 0
	sync := func(what string) map[string][]evFrame {
		t.Helper()
		n++
		dlSave(t, ws, "apps/zz/index.html", fmt.Sprintf("<!doctype html><p>sentinel %d</p>\n", n))
		out := map[string][]evFrame{}
		for name, s := range socks {
			fs := s.until(t, what+"'s sentinel", 60*time.Second, sentinelReload("apps/zz"))
			out[name] = fs[:len(fs)-1]
		}
		return out
	}
	sync("the subscriptions")
	got := map[string][]evFrame{}
	collect := func(m map[string][]evFrame) {
		for k, v := range m {
			got[k] = append(got[k], v...)
		}
	}
	dlOp(t, addr, owner, "live-reload/pause", `{"tile":"`+tile+`"}`)
	dlOp(t, addr, owner, "add", `{"tile":"`+tile+`","deployment":"dev","attach":true}`)
	collect(sync("adding dev"))
	dlSave(t, ws, "apps/crm/index.html", "<!doctype html><p>crm 2</p>\n")
	fs := socks["owner"].until(t, "dev's op reload", 60*time.Second, func(f evFrame) bool {
		return f.Type == "deployments" && f.op() == "reload" && f.dep() == "dev"
	})
	got["owner"] = append(got["owner"], fs...)
	dlOp(t, addr, owner, "deploy", `{"tile":"`+tile+`","deployment":"dev"}`)
	collect(sync("a deploy onto dev"))

	aboutDev := func(f evFrame) bool {
		return f.Type == "deployments" && (strings.Contains(f.raw, `"dev"`) || f.dep() != "" && f.dep() != "main")
	}
	for _, who := range []string{"ritaFrame", "wandaFrame", "rita"} {
		for _, f := range got[who] {
			if aboutDev(f) {
				t.Errorf("%s received a fact about dev: %s", who, f.raw)
			}
		}
	}
	control := 0
	for _, f := range got["owner"] {
		if aboutDev(f) {
			control++
		}
	}
	if control == 0 {
		t.Errorf("the owner received no event naming dev (the control): %v", raws(got["owner"]))
	}

	code, st, raw := dlCall(t, addr, signIn["rita"], "GET", "/api/xbin/deployments?tile="+tile, "")
	rows, _ := st["deployments"].([]any)
	if code != http.StatusOK || st["view"] != "reader" || len(rows) != 1 || strings.Contains(raw, `"dev"`) {
		t.Errorf("rita's GET /deployments = %d %s, want the reader view with the primary alone", code, raw)
	}
	if code, _, raw := dlCall(t, addr, signIn["wanda"], "GET", "/api/xbin/deployments?tile="+tile, ""); code != http.StatusOK ||
		!strings.Contains(raw, `"dev"`) {
		t.Errorf("wanda's GET /deployments = %d %s, want dev named (the control)", code, raw)
	}
}

// frameTokenOf is the frame token xbind injects into the document at path,
// fetched with the owner's bearer.
func frameTokenOf(t *testing.T, addr, owner, path string) string {
	t.Helper()
	code, _, raw := dlCall(t, addr, owner, "GET", path, "")
	_, rest, ok := strings.Cut(raw, `<meta name="xbin-frame-token" content="`)
	tok, _, _ := strings.Cut(rest, `"`)
	if code != http.StatusOK || !ok || tok == "" {
		t.Fatalf("GET %s: %d, no frame token: %.300s", path, code, raw)
	}
	return html.UnescapeString(tok)
}

// covers D127r T9 — TestDeploymentRouteClasses in a running daemon, where the
// broker names the deployment a credential binds from the tile's record:
// the frame token injected into dev's document (/c/apps/crm+dev/) is refused
// on primary-only routes, reads included (the user list, tile creation,
// the admin runtime view), with the rule's text, and reaches neutral and
// deployment-scoped ones (whoami, the component list, its own kv); the
// primary's frame token meets the handlers' own answers, as today.
func TestDeploymentRouteClassesLive(t *testing.T) {
	dlNeedsStore(t)
	const tile = "apps/crm"
	ws, addr, owner := evDaemonWith(t, map[string]string{
		"xbin.json":           `{"schema":1}`,
		"apps/crm/xbin.json":  `{}`,
		"apps/crm/index.html": "<!doctype html><html><head></head><body>crm</body></html>\n",
	})
	dlOp(t, addr, owner, "live-reload/pause", `{"tile":"`+tile+`"}`)
	dlOp(t, addr, owner, "add", `{"tile":"`+tile+`","deployment":"dev"}`)
	devTok := frameTokenOf(t, addr, owner, "/c/apps/crm+dev/")
	mainTok := frameTokenOf(t, addr, owner, "/c/apps/crm/")
	if strings.Count(devTok, "|") != 5 || strings.Count(mainTok, "|") != 4 {
		t.Fatalf("dev's token %q, main's %q: want a claim on dev's alone", devTok, mainTok)
	}
	call := func(tok, method, path, body string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(method, "http://"+addr+path, strings.NewReader(body))
		req.Header.Set(auth.FrameTokenHeader, tok)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	const refusal = "this route is the primary's alone: a non-primary deployment's credentials can't use it (dev)"
	for _, c := range []struct{ method, path, body string }{
		{"GET", "/api/xbin/users", ""},
		{"POST", "/api/xbin/create", `{"path":"apps/sneaky"}`},
		{"GET", "/api/xbin/runtime", ""},
	} {
		if code, body := call(devTok, c.method, c.path, c.body); code != http.StatusForbidden || !strings.Contains(body, refusal) {
			t.Errorf("dev's frame %s %s: %d %s, want 403 %q", c.method, c.path, code, body, refusal)
		}
		if _, body := call(mainTok, c.method, c.path, c.body); strings.Contains(body, "non-primary") {
			t.Errorf("the primary's frame %s %s met the class check: %s", c.method, c.path, body)
		}
	}
	for _, path := range []string{"/api/xbin/whoami", "/api/xbin/components"} {
		if code, body := call(devTok, "GET", path, ""); code != http.StatusOK {
			t.Errorf("dev's frame GET %s: %d %s", path, code, body)
		}
	}
	if _, err := os.Stat(filepath.Join(ws, "apps", "sneaky")); err == nil {
		t.Error("a refused create made a tile")
	}
}
