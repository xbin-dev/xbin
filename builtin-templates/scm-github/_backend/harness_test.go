// harness_test.go — the tests' workspace: a fake GitHub, this tile's global
// instance and people's partitions in memory (their own state and vault,
// one shared conf), a partition's relay wired to global as xbind would
// attribute it, and callers with the headers xbind would verify.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const tilePath = "apps/scm-github"

// caller is one request's verified headers.
type caller struct {
	from, role, user, level, viewedBy, partition, pid, deployment string
}

func (c caller) set(r *http.Request) {
	for k, v := range map[string]string{"X-XBin-From": c.from, "X-XBin-Role": c.role, "X-XBin-User": c.user, "X-XBin-User-Level": c.level,
		"X-XBin-Viewed-By": c.viewedBy, "X-XBin-Partition": c.partition, "X-XBin-Partition-Id": c.pid, "X-XBin-Deployment": c.deployment} {
		if v != "" {
			r.Header.Set(k, v)
		}
	}
}

var (
	ownerC  = caller{from: "owner", role: "admin", partition: "global"}
	agentC  = caller{from: "apps/agent", role: "consumer"}                          // a tile at global (or an unpartitioned one)
	ingress = caller{from: "ingress"}                                               // public traffic
	cronC   = caller{from: "xbin/cron", role: "admin"}                              // xbind's cron
	selfC   = caller{from: tilePath, role: "admin"}                                 // the tile itself
	agent2C = caller{from: "apps/other-agent", role: "consumer"}                    // another consumer
	nobodyC = caller{from: "apps/stranger", role: "reader"}                         // no consumer role
	adminAt = caller{from: tilePath, role: "admin", user: "alice", level: "write"}  // a person in the frame (legacy)
	viewAs  = caller{from: tilePath, role: "admin", user: "alice", viewedBy: "bob"} // an admin viewing as alice
)

// personC is alice's partition of the agent calling alice's partition here.
func personC(user string) caller {
	return caller{from: "apps/agent", role: "consumer", partition: "user:" + user, pid: "pid-" + user}
}

// pageC is the person in this tile's frame in their own partition.
func pageC(user string) caller {
	return caller{from: tilePath, role: "admin", user: user, level: "write", partition: "user:" + user, pid: "pid-" + user}
}

// relayC is a person's partition (backend, frame or terminal) calling global.
func relayC(user, level string) caller {
	role := "writer"
	if level == "read" {
		role = "reader"
	}
	return caller{from: tilePath, role: role, user: user, level: level, partition: "user:" + user, pid: "pid-" + user}
}

type env struct {
	t      *testing.T
	clock  *clock
	gh     *fakeGH
	conf   *memKV
	global *srv
	gH     http.Handler
	users  map[string]*srv
	pids   map[string]string // a person's partition id (a test may change it)
	seen   []seenResp        // every answer the tests got (TestNoSecretsInResponsesOrLogs)
}

type seenResp struct {
	method, path string
	code         int
	body         string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{t: t, clock: newClock(), conf: newMemKV(), users: map[string]*srv{}, pids: map[string]string{}}
	e.gh = newFakeGH(t, e.clock.now)
	e.global = e.newSrv("global", e.conf)
	e.gH = e.global.routes()
	return e
}

func (e *env) newSrv(partition string, conf kvStore) *srv {
	s := newSrv(partition, tilePath, newMemKV(), conf, newMemVault(), e.gh.srv.Client(), e.clock.now)
	s.defaultAPI, s.defaultWeb = e.gh.srv.URL, e.gh.srv.URL
	return s
}

// setup pastes the fake's App at global as the owner.
func (e *env) setup() {
	e.t.Helper()
	r := e.call(e.gH, ownerC, "POST", "/setup/app", map[string]any{"appId": e.gh.appID, "clientId": e.gh.clientID,
		"clientSecret": e.gh.clientSecret, "privateKey": e.gh.keyPEM, "hookUrl": "https://scm.example.com/hook/github"})
	if r.Code != 200 {
		e.t.Fatalf("setup: %d %s", r.Code, r.Body)
	}
}

// user is alice's partition (made on first use), relaying to global as her.
func (e *env) user(name string) *srv {
	if s, ok := e.users[name]; ok {
		return s
	}
	s := e.newSrv("user:"+name, e.conf)
	if e.pids[name] == "" {
		e.pids[name] = "pid-" + name
	}
	s.relayCall = func(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
		r := httptest.NewRequest(method, "/"+strings.TrimPrefix(path, "/"), bytes.NewReader(body)).WithContext(ctx)
		c := relayC(name, "write")
		c.pid = e.pids[name]
		c.set(r)
		w := httptest.NewRecorder()
		e.gH.ServeHTTP(w, r)
		return w.Result(), nil
	}
	e.users[name] = s
	return s
}

// signIn runs alice's device flow to the end against the fake.
func (e *env) signIn(name, login string) *srv {
	e.t.Helper()
	s := e.user(name)
	h := s.routes()
	r := e.call(h, pageC(name), "POST", "/scm/signin", nil)
	var st signinState
	decode(e.t, r, &st)
	if st.State == "done" {
		return s
	}
	if st.State != "pending" {
		e.t.Fatalf("signin: %d %s", r.Code, r.Body)
	}
	e.gh.approve(login, "approved")
	e.clock.advance(6e9)
	r = e.call(h, pageC(name), "GET", "/scm/signin/"+st.Signin.PollID, nil)
	decode(e.t, r, &st)
	if st.State != "done" {
		e.t.Fatalf("signin poll: %d %s", r.Code, r.Body)
	}
	return s
}

func (e *env) call(h http.Handler, c caller, method, path string, body any) *httptest.ResponseRecorder {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			e.t.Fatal(err)
		}
		rd = bytes.NewReader(b)
	}
	r := httptest.NewRequest(method, path, rd)
	c.set(r)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	e.seen = append(e.seen, seenResp{method, path, w.Code, w.Body.String()})
	return w
}

func decode(t *testing.T, r *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(r.Body.Bytes(), v); err != nil {
		t.Fatalf("decode %d %q: %v", r.Code, r.Body.String(), err)
	}
}

// refusal reads a refusal body and checks its status and word.
func refusal(t *testing.T, r *httptest.ResponseRecorder, status int, word string) scmErr {
	t.Helper()
	var e scmErr
	_ = json.Unmarshal(r.Body.Bytes(), &e)
	if r.Code != status || e.Refusal != word {
		t.Fatalf("want %d %s, got %d %s", status, word, r.Code, r.Body)
	}
	return e
}

func ok(t *testing.T, r *httptest.ResponseRecorder, status int) {
	t.Helper()
	if r.Code != status {
		t.Fatalf("want %d, got %d %s", status, r.Code, r.Body)
	}
}

// legacy is an unpartitioned copy with the fake's App pasted.
func (e *env) legacy() (*srv, http.Handler) {
	e.t.Helper()
	s := e.newSrv("", newMemKV())
	h := s.routes()
	r := e.call(h, caller{from: "owner", role: "admin"}, "POST", "/setup/app", map[string]any{"appId": e.gh.appID, "clientId": e.gh.clientID,
		"clientSecret": e.gh.clientSecret, "privateKey": e.gh.keyPEM})
	ok(e.t, r, 200)
	return s, h
}

// setPolicy replaces the policy as the owner.
func (e *env) setPolicy(p policy) {
	e.t.Helper()
	ok(e.t, e.call(e.gH, ownerC, "PUT", "/api/policy", p), 200)
}

func basePolicy() policy {
	p := defaultPolicy()
	p.AllowedAccounts = []string{"acme"}
	return p
}
