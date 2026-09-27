package main

// fsb_test.go — the sandbox-manager contract's conformance suite
// (docs/sandbox-manager.md, protocol 1), run against the reference manager.
//
// Every Test… below drives a manager over HTTP only, through ctWho (a
// consumer, maybe a person), and is grouped by the contract's sections, so
// the suite can move to an SDK package and take any manager's URL. The two
// exceptions are named TestFake…: the fake's own test hooks, and what only a
// manager whose sandboxes are host directories must refuse.

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// --- the harness ------------------------------------------------------------

type ctEnv struct {
	t   *testing.T
	url string      // …/sbx
	m   *fsbManager // TestFake… only
}

// newCT serves a fresh reference manager; tweak sets its knobs.
func newCT(t *testing.T, tweak ...func(*fsbManager)) *ctEnv {
	t.Helper()
	m := &fsbManager{Root: t.TempDir(), DefaultFrom: "apps/nobody", Grace: 200 * time.Millisecond}
	for _, f := range tweak {
		f(m)
	}
	srv := httptest.NewServer(m)
	t.Cleanup(func() { srv.Close(); m.Close() }) // before TempDir's own cleanup
	return &ctEnv{t: t, url: srv.URL + "/sbx", m: m}
}

// ctWho is a caller: a consumer (X-XBin-From, which xbind sets) and maybe a
// person — verified (X-XBin-User, a page's call) or asserted (Sbx-User, a
// backend naming whom it acts for).
type ctWho struct {
	e              *ctEnv
	from           string
	user, asserted string
}

func (e *ctEnv) as(from string) ctWho       { return ctWho{e: e, from: from} }
func (w ctWho) verified(user string) ctWho  { w.user = user; return w }
func (w ctWho) asserting(user string) ctWho { w.asserted = user; return w }

// do sends a request under /sbx; a []byte body goes raw, anything else as JSON.
func (w ctWho) do(ctx context.Context, method, path string, body any) (*http.Response, []byte, error) {
	var rd io.Reader
	ctype := ""
	switch b := body.(type) {
	case nil:
	case []byte:
		rd, ctype = bytes.NewReader(b), "application/octet-stream"
	default:
		j, err := json.Marshal(b)
		if err != nil {
			return nil, nil, err
		}
		rd, ctype = bytes.NewReader(j), "application/json"
	}
	req, err := http.NewRequestWithContext(ctx, method, w.e.url+path, rd)
	if err != nil {
		return nil, nil, err
	}
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	req.Header.Set("X-XBin-From", w.from)
	if w.user != "" {
		req.Header.Set("X-XBin-User", w.user)
	}
	if w.asserted != "" {
		req.Header.Set("Sbx-User", w.asserted)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return resp, b, err
}

// call wants status and decodes the answer into out (when non-nil).
func (w ctWho) call(method, path string, body any, want int, out any) http.Header {
	w.e.t.Helper()
	resp, b, err := w.do(context.Background(), method, path, body)
	if err != nil {
		w.e.t.Fatalf("%s %s: %v", method, path, err)
	}
	if resp.StatusCode != want {
		w.e.t.Fatalf("%s %s as %s/%s: status %d, want %d: %s", method, path, w.from, w.user+w.asserted, resp.StatusCode, want, b)
	}
	if out != nil {
		if p, ok := out.(*[]byte); ok {
			*p = b
		} else if err := json.Unmarshal(b, out); err != nil {
			w.e.t.Fatalf("%s %s: %v in %s", method, path, err, b)
		}
	}
	return resp.Header
}

type ctErr struct {
	Error, Refusal, State, ETag string
	RetryAfterMs                int
	Protocols                   []int
}

// refused wants an error answer — JSON {error, refusal} with this status.
func (w ctWho) refused(method, path string, body any, status int, refusal string) ctErr {
	w.e.t.Helper()
	resp, b, err := w.do(context.Background(), method, path, body)
	if err != nil {
		w.e.t.Fatalf("%s %s: %v", method, path, err)
	}
	var e ctErr
	if resp.StatusCode != status || json.Unmarshal(b, &e) != nil || e.Refusal != refusal || e.Error == "" {
		w.e.t.Fatalf("%s %s as %s/%s: %d %s, want %d refusal %q", method, path, w.from, w.user+w.asserted, resp.StatusCode, b, status, refusal)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		w.e.t.Fatalf("%s %s: an error as %q, want JSON", method, path, ct)
	}
	return e
}

type ctSandbox struct {
	ID, Name, State, StateDetail, Isolation, Egress, Visibility string
	Workdir, Home, User, Shell                                  string
	Image                                                       struct{ ID, Title string }
	Owner                                                       struct {
		User, Via string
		Asserted  bool
	}
	Members             []string
	Shares              []json.RawMessage
	Shared              bool
	Labels              map[string]string
	Caps                []string
	Created, LastActive int64
	AutoStopMin         int
	Version             int
	RestartNeeded       bool
}

type ctOut struct {
	Head, Tail    string
	Elided, Bytes int64
}

type ctRun struct {
	ExitCode               *int // nil: a signal ended it
	Signal                 string
	TimedOut               bool
	Ms                     int64
	Stdout, Stderr, Output *ctOut
}

type ctExec struct {
	ID, Label, Cmd, Cwd, State, Signal, ClientID string
	Argv                                         []string
	TTY                                          bool
	ExitCode                                     *int
	Started, Ended, Total                        int64
}

type ctChunk struct {
	Start, End, Total, RingStart  int64
	Data, Encoding, State, Signal string
	ExitCode                      *int
}

type ctStat struct {
	Path, Type, Mode, ETag, Target string
	Size, MtimeMs                  int64
}

type ctSnap struct {
	ID, Name       string
	Created, Bytes int64
}

func q(p string) string { return url.QueryEscape(p) }

func (w ctWho) mk(body map[string]any) ctSandbox {
	w.e.t.Helper()
	var sb ctSandbox
	w.call("POST", "/sandboxes", body, http.StatusCreated, &sb)
	return sb
}

func (w ctWho) get(id string) ctSandbox {
	w.e.t.Helper()
	var sb ctSandbox
	w.call("GET", "/sandboxes/"+id, nil, http.StatusOK, &sb)
	return sb
}

func (w ctWho) list() map[string]ctSandbox {
	w.e.t.Helper()
	var l struct{ Sandboxes []ctSandbox }
	w.call("GET", "/sandboxes", nil, http.StatusOK, &l)
	out := map[string]ctSandbox{}
	for _, s := range l.Sandboxes {
		out[s.ID] = s
	}
	return out
}

func (w ctWho) run(id string, body map[string]any) ctRun {
	w.e.t.Helper()
	var r ctRun
	w.call("POST", "/sandboxes/"+id+"/run", body, http.StatusOK, &r)
	return r
}

// sh runs cmd, wants exit 0 and returns its stdout.
func (w ctWho) sh(id, cmd string) string {
	w.e.t.Helper()
	r := w.run(id, map[string]any{"cmd": cmd})
	if r.ExitCode == nil || *r.ExitCode != 0 || r.Stdout == nil {
		w.e.t.Fatalf("run %q: exit %v (signal %q), stderr %+v", cmd, r.ExitCode, r.Signal, r.Stderr)
	}
	return r.Stdout.Head + r.Stdout.Tail
}

func (w ctWho) put(id, path, content string, query string) ctStat {
	w.e.t.Helper()
	var st ctStat
	w.call("PUT", "/sandboxes/"+id+"/files/content?path="+q(path)+query, []byte(content), http.StatusOK, &st)
	return st
}

func (w ctWho) read(id, path string) string {
	w.e.t.Helper()
	var b []byte
	w.call("GET", "/sandboxes/"+id+"/files/content?path="+q(path), nil, http.StatusOK, &b)
	return string(b)
}

func (w ctWho) stat(id, path string) ctStat {
	w.e.t.Helper()
	var st ctStat
	w.call("GET", "/sandboxes/"+id+"/files/stat?path="+q(path), nil, http.StatusOK, &st)
	return st
}

func (w ctWho) exec(id string, body map[string]any) ctExec {
	w.e.t.Helper()
	var x ctExec
	w.call("POST", "/sandboxes/"+id+"/execs", body, http.StatusCreated, &x)
	return x
}

func (w ctWho) chunk(id, eid, query string) ctChunk {
	w.e.t.Helper()
	var c ctChunk
	w.call("GET", "/sandboxes/"+id+"/execs/"+eid+"/output?"+query, nil, http.StatusOK, &c)
	return c
}

// drain reads an exec's output from 0 until it has ended and all is read.
func (w ctWho) drain(id, eid string) (string, ctChunk) {
	w.e.t.Helper()
	var buf []byte
	since := int64(0)
	for deadline := time.Now().Add(10 * time.Second); ; {
		c := w.chunk(id, eid, fmt.Sprintf("since=%d&waitMs=2000&encoding=base64", since))
		d, err := base64.StdEncoding.DecodeString(c.Data)
		if err != nil || c.Encoding != "base64" {
			w.e.t.Fatalf("output: %v (encoding %q)", err, c.Encoding)
		}
		buf, since = append(buf, d...), c.End
		if c.State != "running" && c.End >= c.Total {
			return string(buf), c
		}
		if time.Now().After(deadline) {
			w.e.t.Fatalf("exec %s still %s after 10 s: %q", eid, c.State, buf)
		}
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

// gone: the process whose pid the sandbox wrote at path has ended — asked
// inside the sandbox, as any manager's sandbox can answer it.
func (w ctWho) gone(id, path string) bool {
	w.e.t.Helper()
	out := w.sh(id, `p=$(cat `+path+` 2>/dev/null); [ -n "$p" ] && ! kill -0 "$p" 2>/dev/null && echo gone || echo alive`)
	return strings.TrimSpace(out) == "gone"
}

// --- hello and errors ---------------------------------------------------------

func TestHello(t *testing.T) {
	t.Parallel()
	a := newCT(t).as("apps/a")
	var h struct {
		Protocol  int
		Protocols []int
		Manager   struct{ Name, Title, Version string }
		Caps      []string
		Egress    []string
		Images    []struct {
			ID      string
			Default bool
		}
		Sizes []struct {
			ID      string
			Default bool
		}
		Limits map[string]int64
	}
	a.call("GET", "/hello?protocol=1", nil, 200, &h)
	if h.Protocol != 1 || !slices.Contains(h.Protocols, 1) || h.Manager.Name == "" || h.Manager.Version == "" {
		t.Fatalf("hello: %+v", h)
	}
	for _, c := range []string{"exec", "files"} { // required in protocol 1
		if !slices.Contains(h.Caps, c) {
			t.Errorf("caps %v lack %s", h.Caps, c)
		}
	}
	for _, c := range h.Caps {
		if !slices.Contains([]string{"exec", "files", "tar", "tty", "snapshots", "clone", "archive"}, c) {
			t.Errorf("caps: unknown %q", c)
		}
	}
	for _, x := range h.Egress {
		if !slices.Contains([]string{"none", "internet", "open"}, x) {
			t.Errorf("egress: unknown %q", x)
		}
	}
	defaults := 0
	for _, im := range h.Images {
		if im.Default {
			defaults++
		}
	}
	if defaults != 1 {
		t.Errorf("images %+v: want one default", h.Images)
	}
	for _, k := range []string{"sandboxes", "runTimeoutMaxMs", "runOutputMax", "execsRunning", "outputRing", "stdinMax", "fileMax", "tarMax", "waitMaxSec"} {
		if _, ok := h.Limits[k]; !ok {
			t.Errorf("limits lack %s: %v", k, h.Limits)
		}
	}
	a.call("GET", "/hello", nil, 200, nil)
	if e := a.refused("GET", "/hello?protocol=2", nil, 400, "protocol"); !slices.Contains(e.Protocols, 1) {
		t.Errorf("protocol refusal lists %v", e.Protocols)
	}
	a.refused("GET", "/no-such-route", nil, 404, "not-found") // errors are JSON everywhere
}

// --- sandboxes -----------------------------------------------------------------

var ctID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

func TestSandboxCreateGetList(t *testing.T) {
	t.Parallel()
	a := newCT(t).as("apps/a")
	sb := a.mk(map[string]any{"name": " api-dev ", "labels": map[string]string{"xbin.agent/conversation": "42"}})
	switch {
	case !ctID.MatchString(sb.ID), sb.Name != "api-dev", sb.State != "running", sb.Owner.Via != "apps/a",
		sb.Owner.User != "", sb.Owner.Asserted, sb.Visibility != "private", sb.Members == nil, len(sb.Members) != 0,
		sb.Shares == nil, sb.Shared, sb.Labels["xbin.agent/conversation"] != "42", !strings.HasPrefix(sb.Workdir, "/"),
		!strings.HasPrefix(sb.Home, "/"), sb.Created == 0, sb.Version < 1, sb.Image.ID == "", sb.Egress == "",
		!slices.Contains(sb.Caps, "exec"), !slices.Contains(sb.Caps, "files"):
		t.Fatalf("created: %+v", sb)
	}
	if got := a.get(sb.ID); got.ID != sb.ID || got.Name != "api-dev" || got.Version != sb.Version {
		t.Fatalf("get: %+v", got)
	}
	stopped := a.mk(map[string]any{"name": "cold", "start": false})
	if stopped.State != "stopped" {
		t.Fatalf("start:false: %s", stopped.State)
	}
	if l := a.list(); len(l) != 2 || l[sb.ID].Name != "api-dev" || l[stopped.ID].State != "stopped" {
		t.Fatalf("list: %+v", l)
	}
	a.refused("GET", "/sandboxes/nope", nil, 404, "not-found")
	for _, bad := range []map[string]any{{"name": ""}, {"name": strings.Repeat("n", 65)}, {"name": "x", "image": "no-such"},
		{"name": "x", "size": "no-such"}, {"name": "x", "egress": "everything"}, {"name": "x", "visibility": "world"}} {
		a.refused("POST", "/sandboxes", bad, 400, "invalid")
	}
	a.refused("POST", "/sandboxes", map[string]any{"name": "x", "labels": map[string]string{"k": strings.Repeat("v", 1100)}}, 413, "too-large")
}

func TestSandboxCreateIdempotent(t *testing.T) {
	t.Parallel()
	e := newCT(t)
	a, b := e.as("apps/a"), e.as("apps/b")
	first := a.mk(map[string]any{"name": "one", "clientId": "c1"})
	var again ctSandbox
	a.call("POST", "/sandboxes", map[string]any{"name": "one", "clientId": "c1"}, http.StatusOK, &again)
	if again.ID != first.ID {
		t.Fatalf("a repeated clientId made %s, want %s", again.ID, first.ID)
	}
	a.refused("POST", "/sandboxes", map[string]any{"name": "two", "clientId": "c1"}, 409, "exists")
	if other := b.mk(map[string]any{"name": "one", "clientId": "c1"}); other.ID == first.ID { // unique per consumer
		t.Fatalf("another consumer's clientId returned %s", other.ID)
	}
	if n := len(a.list()); n != 1 {
		t.Fatalf("a has %d sandboxes, want 1", n)
	}
}

func TestSandboxPatchDelete(t *testing.T) {
	t.Parallel()
	a := newCT(t).as("apps/a")
	sb := a.mk(map[string]any{"name": "p"})
	var p ctSandbox
	a.call("PATCH", "/sandboxes/"+sb.ID, map[string]any{"name": "renamed", "labels": map[string]string{"k": "v"}, "autoStopMin": 5}, 200, &p)
	if p.Name != "renamed" || p.Labels["k"] != "v" || p.AutoStopMin != 5 || p.Version <= sb.Version {
		t.Fatalf("patched: %+v", p)
	}
	// version: a lost update is refused
	a.refused("PATCH", "/sandboxes/"+sb.ID, map[string]any{"name": "stale", "version": sb.Version}, 412, "precondition")
	a.call("PATCH", "/sandboxes/"+sb.ID, map[string]any{"name": "fresh", "version": p.Version}, 200, &p)
	if p.Name != "fresh" {
		t.Fatalf("a PATCH with the current version: %+v", p)
	}
	// a refused PATCH changes nothing
	a.refused("PATCH", "/sandboxes/"+sb.ID, map[string]any{"name": "half", "visibility": "world"}, 400, "invalid")
	a.refused("PATCH", "/sandboxes/"+sb.ID, map[string]any{"name": "half", "size": "no-such"}, 400, "invalid")
	a.refused("PATCH", "/sandboxes/"+sb.ID, map[string]any{"name": "half", "labels": map[string]string{"k": strings.Repeat("v", 1100)}}, 413, "too-large")
	if got := a.get(sb.ID); got.Name != "fresh" || got.Version != p.Version {
		t.Fatalf("after refused PATCHes: %+v", got)
	}
	// egress applies at the next start
	other := "internet"
	if p.Egress == "internet" {
		other = "none"
	}
	a.call("PATCH", "/sandboxes/"+sb.ID, map[string]any{"egress": other}, 200, &p)
	if p.Egress != other || !p.RestartNeeded {
		t.Fatalf("egress change on a running sandbox: %+v", p)
	}
	a.call("DELETE", "/sandboxes/"+sb.ID, nil, http.StatusNoContent, nil)
	a.refused("GET", "/sandboxes/"+sb.ID, nil, 404, "not-found")
	a.refused("DELETE", "/sandboxes/"+sb.ID, nil, 404, "not-found")
	if len(a.list()) != 0 {
		t.Fatal("a deleted sandbox is still listed")
	}
}

// --- partitions and sharing ---------------------------------------------------------

func TestPartitions(t *testing.T) {
	t.Parallel()
	e := newCT(t)
	a, b := e.as("apps/a"), e.as("apps/b")
	sb := a.mk(map[string]any{"name": "mine", "visibility": "team"})
	// another consumer: it doesn't exist
	b.refused("GET", "/sandboxes/"+sb.ID, nil, 404, "not-found")
	b.refused("PATCH", "/sandboxes/"+sb.ID, map[string]any{"name": "x"}, 404, "not-found")
	b.refused("DELETE", "/sandboxes/"+sb.ID, nil, 404, "not-found")
	b.refused("POST", "/sandboxes/"+sb.ID+"/run", map[string]any{"cmd": "true"}, 404, "not-found")
	b.refused("GET", "/sandboxes/"+sb.ID+"/files/stat?path="+q(sb.Workdir), nil, 404, "not-found")
	if _, ok := b.list()[sb.ID]; ok {
		t.Fatal("b lists a's sandbox")
	}
	// shared with everyone b serves
	a.call("PATCH", "/sandboxes/"+sb.ID, map[string]any{"shares": []map[string]any{{"consumer": "apps/b", "users": "*"}}}, 200, nil)
	if got := b.get(sb.ID); !got.Shared || got.ID != sb.ID {
		t.Fatalf("b's view of a shared sandbox: %+v", got)
	}
	if got := a.get(sb.ID); got.Shared {
		t.Fatal("the home consumer sees its own sandbox as shared")
	}
	if l := b.list(); !l[sb.ID].Shared {
		t.Fatalf("b's list: %+v", l)
	}
	if out := b.sh(sb.ID, "echo via-b"); out != "via-b\n" {
		t.Fatalf("b runs: %q", out)
	}
	if got := b.verified("zoe").get(sb.ID); got.ID != sb.ID { // users "*", a team sandbox
		t.Fatalf("zoe via b: %+v", got)
	}
	// only the home consumer changes shares (or deletes)
	b.refused("PATCH", "/sandboxes/"+sb.ID, map[string]any{"shares": []map[string]any{}}, 403, "not-allowed")
	b.refused("DELETE", "/sandboxes/"+sb.ID, nil, 403, "not-allowed")
	// shared with some of b's people
	a.call("PATCH", "/sandboxes/"+sb.ID, map[string]any{"shares": []map[string]any{{"consumer": "apps/b", "users": []string{"carol"}}}}, 200, nil)
	b.verified("carol").get(sb.ID)
	b.verified("bob").refused("GET", "/sandboxes/"+sb.ID, nil, 403, "not-allowed")
	b.verified("bob").refused("POST", "/sandboxes/"+sb.ID+"/run", map[string]any{"cmd": "true"}, 403, "not-allowed")
	if _, ok := b.verified("bob").list()[sb.ID]; ok {
		t.Fatal("bob lists a sandbox not shared with him")
	}
	if _, ok := b.verified("carol").list()[sb.ID]; !ok {
		t.Fatal("carol doesn't list a sandbox shared with her")
	}
	b.get(sb.ID) // b's backend: the consumer polices its own people
	// unshared: gone again
	a.call("PATCH", "/sandboxes/"+sb.ID, map[string]any{"shares": []map[string]any{}}, 200, nil)
	b.refused("GET", "/sandboxes/"+sb.ID, nil, 404, "not-found")
}

// --- people -----------------------------------------------------------------------------

func TestPeopleOwners(t *testing.T) {
	t.Parallel()
	a := newCT(t).as("apps/a")
	asserted := a.asserting("alice").mk(map[string]any{"name": "asserted"})
	if asserted.Owner.User != "alice" || !asserted.Owner.Asserted || asserted.Owner.Via != "apps/a" {
		t.Fatalf("asserted owner: %+v", asserted.Owner)
	}
	verified := a.verified("alice").asserting("mallory").mk(map[string]any{"name": "verified"})
	if verified.Owner.User != "alice" || verified.Owner.Asserted {
		t.Fatalf("a verified person wins over an asserted one: %+v", verified.Owner)
	}
	if own := a.mk(map[string]any{"name": "the consumer's"}); own.Owner.User != "" || own.Owner.Asserted {
		t.Fatalf("a backend call with no person: %+v", own.Owner)
	}
	// an assertion is recorded, not enforced
	a.asserting("bob").get(asserted.ID)
	a.asserting("bob").run(asserted.ID, map[string]any{"cmd": "true"})
}

func TestPeopleVisibility(t *testing.T) {
	t.Parallel()
	a := newCT(t).as("apps/a")
	alice, bob, carol := a.verified("alice"), a.verified("bob"), a.verified("carol")
	sb := alice.mk(map[string]any{"name": "private"})
	// private: the owner and members only, on a verified call
	alice.get(sb.ID)
	bob.refused("GET", "/sandboxes/"+sb.ID, nil, 403, "not-allowed")
	bob.refused("POST", "/sandboxes/"+sb.ID+"/execs", map[string]any{"cmd": "true"}, 403, "not-allowed")
	bob.refused("GET", "/sandboxes/"+sb.ID+"/files/list?path="+q(sb.Workdir), nil, 403, "not-allowed")
	if _, ok := bob.list()[sb.ID]; ok {
		t.Fatal("bob lists alice's private sandbox")
	}
	a.get(sb.ID)                  // the consumer's backend
	a.asserting("bob").get(sb.ID) // … acting for bob: its call, not verified
	// members
	bob.refused("PATCH", "/sandboxes/"+sb.ID, map[string]any{"members": []string{"bob"}}, 403, "not-allowed")
	alice.call("PATCH", "/sandboxes/"+sb.ID, map[string]any{"members": []string{"bob"}}, 200, nil)
	bob.get(sb.ID)
	if _, ok := bob.list()[sb.ID]; !ok {
		t.Fatal("a member doesn't list the sandbox")
	}
	// only the home consumer's backend or the owner changes who may use it
	bob.refused("PATCH", "/sandboxes/"+sb.ID, map[string]any{"visibility": "team"}, 403, "not-allowed")
	bob.refused("PATCH", "/sandboxes/"+sb.ID, map[string]any{"shares": []map[string]any{{"consumer": "apps/x", "users": "*"}}}, 403, "not-allowed")
	carol.refused("GET", "/sandboxes/"+sb.ID, nil, 403, "not-allowed")
	// team: anyone the consumer serves
	alice.call("PATCH", "/sandboxes/"+sb.ID, map[string]any{"visibility": "team"}, 200, nil)
	carol.get(sb.ID)
	carol.run(sb.ID, map[string]any{"cmd": "true"})
	a.asserting("bob").call("PATCH", "/sandboxes/"+sb.ID, map[string]any{"visibility": "private", "members": []string{}}, 200, nil)
	carol.refused("GET", "/sandboxes/"+sb.ID, nil, 403, "not-allowed")
	bob.refused("GET", "/sandboxes/"+sb.ID, nil, 403, "not-allowed")
	// a sandbox of the consumer itself has no owner: a verified person needs it team
	own := a.mk(map[string]any{"name": "the consumer's"})
	carol.refused("GET", "/sandboxes/"+own.ID, nil, 403, "not-allowed")
}

// --- lifecycle --------------------------------------------------------------------------

func TestLifecycle(t *testing.T) {
	t.Parallel()
	a := newCT(t).as("apps/a")
	sb := a.mk(map[string]any{"name": "life"})
	act := func(action string, body any) ctSandbox {
		t.Helper()
		var s ctSandbox
		a.call("POST", "/sandboxes/"+sb.ID+"/"+action, body, 200, &s)
		return s
	}
	v := sb.Version
	if s := act("stop", nil); s.State != "stopped" || s.Version <= v {
		t.Fatalf("stop: %+v", s)
	}
	if s := act("start?wait=5", nil); s.State != "running" {
		t.Fatalf("start: %s", s.State)
	}
	// a stopped sandbox starts on an exec or a file operation
	act("stop", nil)
	a.sh(sb.ID, "true")
	if s := a.get(sb.ID); s.State != "running" {
		t.Fatalf("after a run on a stopped sandbox: %s", s.State)
	}
	act("stop", nil)
	a.put(sb.ID, sb.Workdir+"/f", "x", "")
	if s := a.get(sb.ID); s.State != "running" {
		t.Fatalf("after a write on a stopped sandbox: %s", s.State)
	}
	act("stop", nil)
	x := a.exec(sb.ID, map[string]any{"cmd": "true"})
	a.drain(sb.ID, x.ID)
	// archived: thawing is explicit
	if s := act("archive", nil); s.State != "archived" {
		t.Fatalf("archive: %s", s.State)
	}
	for _, c := range []struct {
		method, path string
		body         any
	}{
		{"POST", "/run", map[string]any{"cmd": "true"}},
		{"POST", "/execs", map[string]any{"cmd": "true"}},
		{"GET", "/files/stat?path=" + q(sb.Workdir), nil},
		{"PUT", "/files/content?path=" + q(sb.Workdir+"/g"), []byte("x")},
		{"POST", "/start", nil},
	} {
		if e := a.refused(c.method, "/sandboxes/"+sb.ID+c.path, c.body, 409, "state"); e.State != "archived" {
			t.Fatalf("%s %s on an archived sandbox: state %q", c.method, c.path, e.State)
		}
	}
	if s := act("thaw", nil); s.State != "stopped" {
		t.Fatalf("thaw: %s", s.State)
	}
	if e := a.refused("POST", "/sandboxes/"+sb.ID+"/thaw", nil, 409, "state"); e.State != "stopped" {
		t.Fatalf("thaw of a stopped sandbox: %+v", e)
	}
	act("archive", nil)
	if s := act("thaw", map[string]any{"start": true}); s.State != "running" {
		t.Fatalf("thaw {start}: %s", s.State)
	}
	if got := a.read(sb.ID, sb.Workdir+"/f"); got != "x" {
		t.Fatalf("an archive keeps the contents: %q", got)
	}
	a.refused("POST", "/sandboxes/"+sb.ID+"/no-such-action", nil, 404, "not-found")
	// a stop ends the running commands
	x = a.exec(sb.ID, map[string]any{"cmd": "sleep 30"})
	act("stop", nil)
	eventually(t, 3*time.Second, "the exec ends with its sandbox", func() bool {
		var g ctExec
		a.call("GET", "/sandboxes/"+sb.ID+"/execs/"+x.ID, nil, 200, &g)
		return g.State == "killed"
	})
}

// --- run --------------------------------------------------------------------------------

func TestRunResults(t *testing.T) {
	t.Parallel()
	a := newCT(t).as("apps/a")
	sb := a.mk(map[string]any{"name": "runner"})
	id := sb.ID
	if r := a.run(id, map[string]any{"cmd": "exit 3"}); r.ExitCode == nil || *r.ExitCode != 3 || r.TimedOut || r.Signal != "" {
		t.Fatalf("exit 3: %+v", r)
	}
	r := a.run(id, map[string]any{"cmd": "echo out; echo err >&2"})
	if r.Stdout.Head != "out\n" || r.Stderr.Head != "err\n" || r.Stdout.Bytes != 4 || r.Stdout.Elided != 0 || r.Output != nil {
		t.Fatalf("split streams: %+v %+v", r.Stdout, r.Stderr)
	}
	r = a.run(id, map[string]any{"cmd": "echo a; echo b >&2; echo c", "merge": true})
	if r.Output == nil || r.Output.Head != "a\nb\nc\n" || r.Stdout != nil || r.Stderr != nil {
		t.Fatalf("merge: %+v", r)
	}
	env := a.sh(id, `echo "$IN_SANDBOX|$SANDBOX_ID|$SANDBOX_NAME|$CI"`)
	if r := a.run(id, map[string]any{"cmd": `echo "$IN_SANDBOX|$SANDBOX_ID|$SANDBOX_NAME|$CI"`, "env": map[string]string{"CI": "1"}}); r.Stdout.Head != "1|"+id+"|runner|1\n" {
		t.Fatalf("env: %q (without env: %q)", r.Stdout.Head, env)
	}
	if pwd := a.sh(id, "pwd"); pwd != sb.Workdir+"\n" {
		t.Fatalf("cwd defaults to workdir %s: %q", sb.Workdir, pwd)
	}
	a.sh(id, "mkdir -p sub/dir")
	if r := a.run(id, map[string]any{"cmd": "pwd", "cwd": sb.Workdir + "/sub/dir"}); r.Stdout.Head != sb.Workdir+"/sub/dir\n" {
		t.Fatalf("cwd: %q", r.Stdout.Head)
	}
	a.refused("POST", "/sandboxes/"+id+"/run", map[string]any{"cmd": "pwd", "cwd": sb.Workdir + "/no-such"}, 400, "invalid")
	a.refused("POST", "/sandboxes/"+id+"/run", map[string]any{"cmd": "pwd", "cwd": "sub"}, 400, "invalid")
	a.refused("POST", "/sandboxes/"+id+"/run", map[string]any{}, 400, "invalid")
	if r := a.run(id, map[string]any{"cmd": "cat", "stdin": "fed in"}); r.Stdout.Head != "fed in" {
		t.Fatalf("stdin: %q", r.Stdout.Head)
	}
	if r := a.run(id, map[string]any{"argv": []string{"printf", "%s|", "a b", "$HOME"}}); r.Stdout.Head != "a b|$HOME|" {
		t.Fatalf("argv: %q", r.Stdout.Head)
	}
	if r := a.run(id, map[string]any{"cmd": `printf 'ok\377'`}); r.Stdout.Head != "ok�" {
		t.Fatalf("output is UTF-8, invalid bytes replaced: %q", r.Stdout.Head)
	}
}

func TestRunShaping(t *testing.T) {
	t.Parallel()
	a := newCT(t).as("apps/a")
	id := a.mk(map[string]any{"name": "shape"}).ID
	const s = "0123456789abcdefghijklmnopqrstuvwxyz" // 36 bytes
	r := a.run(id, map[string]any{"cmd": "printf " + s + "; printf " + s + " >&2", "maxOutput": 16})
	for _, o := range []*ctOut{r.Stdout, r.Stderr} { // per stream: a quarter of head, three of tail
		if o.Head != "0123" || o.Tail != "opqrstuvwxyz" || o.Elided != 20 || o.Bytes != 36 {
			t.Fatalf("maxOutput 16: %+v", o)
		}
	}
	r = a.run(id, map[string]any{"cmd": "printf " + s, "maxOutput": 36})
	if r.Stdout.Head != s || r.Stdout.Tail != "" || r.Stdout.Elided != 0 {
		t.Fatalf("output that fits is all in head: %+v", r.Stdout)
	}
	r = a.run(id, map[string]any{"cmd": "printf " + s + "; printf " + s + " >&2", "maxOutput": 40, "merge": true})
	if o := r.Output; o.Head != "0123456789" || o.Tail != s[6:] || o.Elided != 32 || o.Bytes != 72 {
		t.Fatalf("merged, maxOutput 40: %+v", o)
	}
}

func TestRunTimeout(t *testing.T) {
	t.Parallel()
	e := newCT(t) // Grace 200 ms
	a := e.as("apps/a")
	sb := a.mk(map[string]any{"name": "slow"})
	wd := sb.Workdir
	// TERM ends it
	start := time.Now()
	r := a.run(sb.ID, map[string]any{"cmd": "sleep 30", "timeoutMs": 200})
	if !r.TimedOut || r.Signal != "TERM" || r.ExitCode != nil || time.Since(start) > 5*time.Second {
		t.Fatalf("a timeout: %+v", r)
	}
	// TERM is ignored: KILL after the grace, the whole group — a child too
	r = a.run(sb.ID, map[string]any{"cmd": "trap '' TERM; sleep 30 & echo $! > " + wd + "/child; wait", "timeoutMs": 200})
	if !r.TimedOut || r.Signal != "KILL" || r.Ms < 350 {
		t.Fatalf("TERM ignored: %+v", r)
	}
	eventually(t, 3*time.Second, "the child that ignored TERM is killed", func() bool { return a.gone(sb.ID, wd+"/child") })
	// a member that outlives its leader (and ignores TERM) is still killed
	r = a.run(sb.ID, map[string]any{"cmd": "(trap '' TERM; exec sleep 30) >/dev/null 2>&1 & echo $! > " + wd + "/orphan; sleep 30", "timeoutMs": 200})
	if !r.TimedOut {
		t.Fatalf("timedOut: %+v", r)
	}
	eventually(t, 3*time.Second, "the detached member is killed after the grace", func() bool { return a.gone(sb.ID, wd+"/orphan") })
}

func TestRunHangup(t *testing.T) {
	t.Parallel()
	a := newCT(t).as("apps/a")
	sb := a.mk(map[string]any{"name": "hangup"})
	pidf := sb.Workdir + "/pid"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	answered := make(chan error, 1)
	go func() {
		_, _, err := a.do(ctx, "POST", "/sandboxes/"+sb.ID+"/run", map[string]any{"cmd": "echo $$ > " + pidf + "; exec sleep 30"})
		answered <- err
	}()
	eventually(t, 5*time.Second, "the run starts", func() bool {
		resp, _, err := a.do(context.Background(), "GET", "/sandboxes/"+sb.ID+"/files/stat?path="+q(pidf), nil)
		return err == nil && resp.StatusCode == 200
	})
	cancel()
	if err := <-answered; err == nil {
		t.Fatal("the run answered before the hang-up")
	}
	eventually(t, 3*time.Second, "a run whose caller hung up is killed", func() bool { return a.gone(sb.ID, pidf) })
}

// --- background execs -------------------------------------------------------------------

func TestExecBasics(t *testing.T) {
	t.Parallel()
	a := newCT(t).as("apps/a")
	sb := a.mk(map[string]any{"name": "ex"})
	x := a.exec(sb.ID, map[string]any{"cmd": "echo hi; echo err >&2; exit 4", "label": "build"})
	if x.ID == "" || x.Label != "build" || x.Cwd != sb.Workdir || x.Started == 0 || (x.State != "running" && x.State != "exited") {
		t.Fatalf("exec: %+v", x)
	}
	out, c := a.drain(sb.ID, x.ID)
	if out != "hi\nerr\n" || c.State != "exited" || c.ExitCode == nil || *c.ExitCode != 4 { // one combined stream
		t.Fatalf("output %q, chunk %+v", out, c)
	}
	var g ctExec
	a.call("GET", "/sandboxes/"+sb.ID+"/execs/"+x.ID, nil, 200, &g)
	if g.State != "exited" || *g.ExitCode != 4 || g.Total != 7 || g.Ended == 0 {
		t.Fatalf("exec after: %+v", g)
	}
	y := a.exec(sb.ID, map[string]any{"argv": []string{"printf", "%s", "a b"}, "cwd": sb.Home})
	if out, _ := a.drain(sb.ID, y.ID); out != "a b" || y.Cwd != sb.Home {
		t.Fatalf("argv exec: %q in %s", out, y.Cwd)
	}
	var l struct{ Execs []ctExec }
	a.call("GET", "/sandboxes/"+sb.ID+"/execs", nil, 200, &l)
	if len(l.Execs) != 2 || l.Execs[0].ID != x.ID || l.Execs[1].ID != y.ID {
		t.Fatalf("execs: %+v", l.Execs)
	}
	a.refused("GET", "/sandboxes/"+sb.ID+"/execs/nope", nil, 404, "not-found")
	a.refused("GET", "/sandboxes/"+sb.ID+"/execs/nope/output", nil, 404, "not-found")
	a.refused("POST", "/sandboxes/"+sb.ID+"/execs", map[string]any{"cmd": "true", "cwd": sb.Workdir + "/no-such"}, 400, "invalid")
	a.refused("POST", "/sandboxes/"+sb.ID+"/execs", map[string]any{"label": "nothing"}, 400, "invalid")
	a.refused("POST", "/sandboxes/nope/execs", map[string]any{"cmd": "true"}, 404, "not-found")
}

func TestExecOutputPolling(t *testing.T) {
	t.Parallel()
	a := newCT(t).as("apps/a")
	id := a.mk(map[string]any{"name": "poll"}).ID
	x := a.exec(id, map[string]any{"cmd": "sleep 1; printf late"})
	if c := a.chunk(id, x.ID, "since=0&waitMs=0"); c.Data != "" || c.State != "running" || c.Start != 0 || c.End != 0 {
		t.Fatalf("waitMs=0 with nothing yet: %+v", c)
	}
	if c := a.chunk(id, x.ID, "since=0&waitMs=5000"); c.Data != "late" || c.End != 4 { // it waited for the bytes
		t.Fatalf("a long poll: %+v", c)
	}
	a.drain(id, x.ID)
	y := a.exec(id, map[string]any{"cmd": "printf 0123456789"})
	a.drain(id, y.ID)
	if c := a.chunk(id, y.ID, "since=3&max=4"); c.Data != "3456" || c.Start != 3 || c.End != 7 || c.Total != 10 || c.Encoding != "text" {
		t.Fatalf("since=3&max=4: %+v", c)
	}
	if c := a.chunk(id, y.ID, "since=10&waitMs=5000"); c.Data != "" || c.Start != 10 || c.End != 10 || c.State != "exited" { // ended: no wait
		t.Fatalf("at the end: %+v", c)
	}
	z := a.exec(id, map[string]any{"cmd": `printf '\377\000x'`})
	a.drain(id, z.ID)
	c := a.chunk(id, z.ID, "since=0&encoding=base64")
	if b, _ := base64.StdEncoding.DecodeString(c.Data); c.Encoding != "base64" || string(b) != "\xff\x00x" {
		t.Fatalf("base64 is exact: %+v", c)
	}
	if c := a.chunk(id, z.ID, "since=0&encoding=text"); c.Data != "�\x00x" {
		t.Fatalf("text replaces invalid bytes: %q", c.Data)
	}
	a.refused("GET", "/sandboxes/"+id+"/execs/"+z.ID+"/output?encoding=hex", nil, 400, "invalid")
}

func TestExecRing(t *testing.T) {
	t.Parallel()
	e := newCT(t, func(m *fsbManager) { m.Ring = 64 })
	a := e.as("apps/a")
	var h struct{ Limits map[string]int64 }
	a.call("GET", "/hello", nil, 200, &h)
	ring := h.Limits["outputRing"]
	id := a.mk(map[string]any{"name": "ring"}).ID
	var want strings.Builder
	for i := range 100 {
		fmt.Fprintf(&want, "%05d\n", i)
	}
	x := a.exec(id, map[string]any{"cmd": `i=0; while [ $i -lt 100 ]; do printf '%05d\n' $i; i=$((i+1)); done`})
	eventually(t, 5*time.Second, "the exec ends", func() bool {
		var g ctExec
		a.call("GET", "/sandboxes/"+id+"/execs/"+x.ID, nil, 200, &g)
		return g.State == "exited"
	})
	c := a.chunk(id, x.ID, "since=0&max=100000")
	full := want.String()
	if c.Total != int64(len(full)) || c.End != c.Total || c.RingStart <= 0 || c.Start != c.RingStart || c.Start <= 0 {
		t.Fatalf("after the ring overflowed: %+v", c)
	}
	if c.Total-c.RingStart < ring { // a ring of at least limits.outputRing
		t.Fatalf("ring keeps %d bytes, less than outputRing %d", c.Total-c.RingStart, ring)
	}
	if c.Data != full[c.Start:c.End] {
		t.Fatalf("data %q, want %q", c.Data, full[c.Start:c.End])
	}
}

func TestExecStdin(t *testing.T) {
	t.Parallel()
	a := newCT(t).as("apps/a")
	id := a.mk(map[string]any{"name": "stdin"}).ID
	x := a.exec(id, map[string]any{"cmd": "cat; echo done", "stdin": true})
	a.call("POST", "/sandboxes/"+id+"/execs/"+x.ID+"/stdin", []byte("abc"), http.StatusNoContent, nil)
	a.call("POST", "/sandboxes/"+id+"/execs/"+x.ID+"/stdin?eof=1", []byte("def\n"), http.StatusNoContent, nil)
	if out, c := a.drain(id, x.ID); out != "abcdef\ndone\n" || *c.ExitCode != 0 {
		t.Fatalf("stdin: %q %+v", out, c)
	}
	if resp, _, _ := a.do(context.Background(), "POST", "/sandboxes/"+id+"/execs/"+x.ID+"/stdin", []byte("late")); resp.StatusCode < 400 {
		t.Fatalf("stdin after the end: %d", resp.StatusCode)
	}
	y := a.exec(id, map[string]any{"cmd": "sleep 5"})
	if resp, _, _ := a.do(context.Background(), "POST", "/sandboxes/"+id+"/execs/"+y.ID+"/stdin", []byte("x")); resp.StatusCode < 400 {
		t.Fatalf("stdin to an exec without stdin: %d", resp.StatusCode)
	}
}

func TestExecSignalDelete(t *testing.T) {
	t.Parallel()
	a := newCT(t).as("apps/a")
	sb := a.mk(map[string]any{"name": "sig"})
	id, wd := sb.ID, sb.Workdir
	// the group (default): the leader and its child
	x := a.exec(id, map[string]any{"cmd": "sleep 30 & echo $! > " + wd + "/child; wait"})
	eventually(t, 3*time.Second, "the child's pid", func() bool { return strings.TrimSpace(a.sh(id, "cat child 2>/dev/null")) != "" })
	a.call("POST", "/sandboxes/"+id+"/execs/"+x.ID+"/signal", map[string]any{"signal": "TERM"}, http.StatusNoContent, nil)
	if _, c := a.drain(id, x.ID); c.State != "killed" || c.Signal != "TERM" || c.ExitCode != nil {
		t.Fatalf("after TERM: %+v", c)
	}
	eventually(t, 3*time.Second, "the group's child ends", func() bool { return a.gone(id, wd+"/child") })
	// the leader alone: it handles TERM and exits by itself
	y := a.exec(id, map[string]any{"cmd": "trap 'echo caught; exit 7' TERM; echo ready; while :; do sleep 0.05; done"})
	eventually(t, 3*time.Second, "the trap is set", func() bool { return a.chunk(id, y.ID, "since=0&waitMs=500").Data == "ready\n" })
	a.call("POST", "/sandboxes/"+id+"/execs/"+y.ID+"/signal", map[string]any{"signal": "TERM", "group": false}, http.StatusNoContent, nil)
	if out, c := a.drain(id, y.ID); out != "ready\ncaught\n" || c.State != "exited" || *c.ExitCode != 7 {
		t.Fatalf("a handled TERM: %q %+v", out, c)
	}
	a.refused("POST", "/sandboxes/"+id+"/execs/"+y.ID+"/signal", map[string]any{"signal": "STOP"}, 400, "invalid")
	// DELETE kills the group and forgets the exec
	z := a.exec(id, map[string]any{"cmd": "echo $$ > " + wd + "/z; exec sleep 30"})
	eventually(t, 3*time.Second, "its pid", func() bool { return strings.TrimSpace(a.sh(id, "cat z 2>/dev/null")) != "" })
	a.call("DELETE", "/sandboxes/"+id+"/execs/"+z.ID, nil, http.StatusNoContent, nil)
	a.refused("GET", "/sandboxes/"+id+"/execs/"+z.ID, nil, 404, "not-found")
	a.refused("DELETE", "/sandboxes/"+id+"/execs/"+z.ID, nil, 404, "not-found")
	eventually(t, 3*time.Second, "a deleted exec's process ends", func() bool { return a.gone(id, wd+"/z") })
}

func TestExecIdempotentTimeout(t *testing.T) {
	t.Parallel()
	e := newCT(t)
	a := e.as("apps/a")
	id := a.mk(map[string]any{"name": "idem"}).ID
	x := a.exec(id, map[string]any{"cmd": "sleep 5", "clientId": "k"})
	var again ctExec
	a.call("POST", "/sandboxes/"+id+"/execs", map[string]any{"cmd": "sleep 5", "clientId": "k"}, http.StatusOK, &again)
	if again.ID != x.ID || again.ClientID != "k" {
		t.Fatalf("a repeated clientId: %+v, want %s", again, x.ID)
	}
	a.refused("POST", "/sandboxes/"+id+"/execs", map[string]any{"cmd": "sleep 6", "clientId": "k"}, 409, "exists")
	a.call("DELETE", "/sandboxes/"+id+"/execs/"+x.ID, nil, http.StatusNoContent, nil)
	// timeoutMs: TERM at the timeout
	y := a.exec(id, map[string]any{"cmd": "sleep 30", "timeoutMs": 200})
	if _, c := a.drain(id, y.ID); c.State != "killed" || c.Signal != "TERM" {
		t.Fatalf("an exec's timeout: %+v", c)
	}
	// and KILL after the grace when TERM is ignored
	z := a.exec(id, map[string]any{"cmd": "trap '' TERM; sleep 30", "timeoutMs": 200})
	if _, c := a.drain(id, z.ID); c.State != "killed" || c.Signal != "KILL" {
		t.Fatalf("an exec's timeout, TERM ignored: %+v", c)
	}
}

func TestNoTTY(t *testing.T) { // a manager without the tty cap
	t.Parallel()
	a := newCT(t).as("apps/a")
	id := a.mk(map[string]any{"name": "tty"}).ID
	a.refused("POST", "/sandboxes/"+id+"/execs", map[string]any{"cmd": "sh", "tty": true}, 501, "unsupported")
	a.refused("GET", "/sandboxes/"+id+"/tty", nil, 501, "unsupported")
	x := a.exec(id, map[string]any{"cmd": "true"})
	a.refused("GET", "/sandboxes/"+id+"/execs/"+x.ID+"/tty", nil, 501, "unsupported")
	a.refused("POST", "/sandboxes/"+id+"/execs/"+x.ID+"/resize", map[string]any{"rows": 10, "cols": 10}, 501, "unsupported")
}

// --- files ------------------------------------------------------------------------------

func TestFilesContent(t *testing.T) {
	t.Parallel()
	a := newCT(t).as("apps/a")
	sb := a.mk(map[string]any{"name": "files"})
	id, wd := sb.ID, sb.Workdir
	if st := a.stat(id, wd); st.Type != "dir" || st.Path != wd {
		t.Fatalf("stat workdir: %+v", st)
	}
	a.refused("GET", "/sandboxes/"+id+"/files/stat?path="+q(wd+"/none"), nil, 404, "not-found")
	st1 := a.put(id, wd+"/a.txt", "one", "")
	if st1.Type != "file" || st1.Size != 3 || st1.ETag == "" || st1.Path != wd+"/a.txt" {
		t.Fatalf("PUT: %+v", st1)
	}
	var body []byte
	hdr := a.call("GET", "/sandboxes/"+id+"/files/content?path="+q(wd+"/a.txt"), nil, 200, &body)
	if string(body) != "one" || strings.Trim(hdr.Get("ETag"), `"`) != st1.ETag {
		t.Fatalf("GET: %q etag %s, want %s", body, hdr.Get("ETag"), st1.ETag)
	}
	if st := a.stat(id, wd+"/a.txt"); st.ETag != st1.ETag {
		t.Fatalf("stat etag %s, PUT's %s", st.ETag, st1.ETag)
	}
	if st2 := a.put(id, wd+"/a.txt", "two", ""); st2.ETag == st1.ETag {
		t.Fatal("the etag didn't change with the content")
	}
	// ranged reads
	a.put(id, wd+"/digits", "0123456789", "")
	for rng, want := range map[string]string{"offset=2&length=3": "234", "offset=8": "89", "length=2": "01", "offset=20": ""} {
		var b []byte
		a.call("GET", "/sandboxes/"+id+"/files/content?path="+q(wd+"/digits")+"&"+rng, nil, 200, &b)
		if string(b) != want {
			t.Errorf("%s: %q, want %q", rng, b, want)
		}
	}
	// mkdirs, mode
	if st := a.put(id, wd+"/d1/d2/f", "deep", "&mkdirs=1&mode=755"); st.Size != 4 {
		t.Fatalf("PUT mkdirs: %+v", st)
	}
	if m, err := strconv.ParseUint(a.stat(id, wd+"/d1/d2/f").Mode, 8, 32); err != nil || m != 0o755 {
		t.Fatalf("mode: %o %v", m, err)
	}
	a.refused("PUT", "/sandboxes/"+id+"/files/content?path="+q(wd+"/x/y/f"), []byte("x"), 404, "not-found")
	a.refused("GET", "/sandboxes/"+id+"/files/content?path="+q(wd+"/none"), nil, 404, "not-found")
	// symlinks resolve inside the sandbox
	a.sh(id, "ln -s a.txt link")
	if st := a.stat(id, wd+"/link"); st.Type != "symlink" || st.Target != "a.txt" {
		t.Fatalf("stat of a symlink: %+v", st)
	}
	if got := a.read(id, wd+"/link"); got != "two" {
		t.Fatalf("read through a symlink: %q", got)
	}
}

func TestFilesConditional(t *testing.T) {
	t.Parallel()
	a := newCT(t).as("apps/a")
	sb := a.mk(map[string]any{"name": "cond"})
	id, p := sb.ID, sb.Workdir+"/a.txt"
	st1 := a.put(id, p, "v1", "")
	st2 := a.put(id, p, "v2", "&ifMatch="+st1.ETag)
	e := a.refused("PUT", "/sandboxes/"+id+"/files/content?path="+q(p)+"&ifMatch="+st1.ETag, []byte("v3"), 412, "precondition")
	if e.ETag != st2.ETag {
		t.Fatalf("412 carries the current etag %s: %+v", st2.ETag, e)
	}
	a.put(id, p, "v3", "&ifMatch="+q(`"`+st2.ETag+`"`)) // as the ETag header quotes it
	if e := a.refused("PUT", "/sandboxes/"+id+"/files/content?path="+q(p)+"&ifNoneMatch=*", []byte("v4"), 412, "precondition"); e.ETag == "" {
		t.Fatalf("ifNoneMatch=* on a file: %+v", e)
	}
	a.put(id, sb.Workdir+"/new.txt", "fresh", "&ifNoneMatch=*")
	if got := a.read(id, p); got != "v3" {
		t.Fatalf("content: %q", got)
	}
}

func TestFilesTree(t *testing.T) {
	t.Parallel()
	a := newCT(t).as("apps/a")
	sb := a.mk(map[string]any{"name": "tree"})
	id, wd := sb.ID, sb.Workdir
	post := func(op string, body map[string]any, want int) {
		t.Helper()
		a.call("POST", "/sandboxes/"+id+"/files/"+op, body, want, nil)
	}
	a.put(id, wd+"/a", "A", "")
	a.put(id, wd+"/b", "B", "")
	post("mkdir", map[string]any{"path": wd + "/sub"}, http.StatusNoContent)
	post("mkdir", map[string]any{"path": wd + "/p/q/r", "parents": true}, http.StatusNoContent)
	a.refused("POST", "/sandboxes/"+id+"/files/mkdir", map[string]any{"path": wd + "/x/y"}, 404, "not-found")
	var l struct {
		Path    string
		Entries []struct {
			Name, Type, Mode string
			Size, MtimeMs    int64
		}
		Truncated bool
	}
	a.call("GET", "/sandboxes/"+id+"/files/list?path="+q(wd), nil, 200, &l)
	got := map[string]string{}
	for _, e := range l.Entries {
		got[e.Name] = e.Type
	}
	if l.Path != wd || l.Truncated || len(got) != 4 || got["a"] != "file" || got["b"] != "file" || got["sub"] != "dir" || got["p"] != "dir" {
		t.Fatalf("list: %+v", l)
	}
	a.call("GET", "/sandboxes/"+id+"/files/list?path="+q(wd)+"&limit=1", nil, 200, &l)
	if len(l.Entries) != 1 || !l.Truncated {
		t.Fatalf("list limit=1: %+v", l)
	}
	// remove
	post("remove", map[string]any{"path": wd + "/a"}, http.StatusNoContent)
	a.refused("GET", "/sandboxes/"+id+"/files/stat?path="+q(wd+"/a"), nil, 404, "not-found")
	if resp, _, _ := a.do(context.Background(), "POST", "/sandboxes/"+id+"/files/remove", map[string]any{"path": wd + "/p"}); resp.StatusCode < 400 {
		t.Fatalf("removing a non-empty directory without recursive: %d", resp.StatusCode)
	}
	post("remove", map[string]any{"path": wd + "/p", "recursive": true}, http.StatusNoContent)
	a.refused("POST", "/sandboxes/"+id+"/files/remove", map[string]any{"path": wd + "/p"}, 404, "not-found")
	// move
	post("move", map[string]any{"from": wd + "/b", "to": wd + "/sub/c"}, http.StatusNoContent)
	if got := a.read(id, wd+"/sub/c"); got != "B" {
		t.Fatalf("moved: %q", got)
	}
	a.refused("GET", "/sandboxes/"+id+"/files/stat?path="+q(wd+"/b"), nil, 404, "not-found")
	a.put(id, wd+"/d", "D", "")
	if resp, _, _ := a.do(context.Background(), "POST", "/sandboxes/"+id+"/files/move", map[string]any{"from": wd + "/sub/c", "to": wd + "/d"}); resp.StatusCode < 400 {
		t.Fatalf("a move onto a file without overwrite: %d", resp.StatusCode)
	}
	if got := a.read(id, wd+"/d"); got != "D" {
		t.Fatalf("a refused move changed the target: %q", got)
	}
	post("move", map[string]any{"from": wd + "/sub/c", "to": wd + "/d", "overwrite": true}, http.StatusNoContent)
	if got := a.read(id, wd+"/d"); got != "B" {
		t.Fatalf("overwritten: %q", got)
	}
	a.refused("POST", "/sandboxes/"+id+"/files/move", map[string]any{"from": wd + "/none", "to": wd + "/e"}, 404, "not-found")
}

func TestFilesPaths(t *testing.T) {
	t.Parallel()
	a := newCT(t).as("apps/a")
	sb := a.mk(map[string]any{"name": "paths"})
	id, wd := sb.ID, sb.Workdir
	outside := "/" + strings.Repeat("../", 20) + "etc"
	for _, p := range []string{"", "work/x", "./x", wd + "/" + strings.Repeat("../", 20) + "etc", outside} {
		if p == outside && strings.HasPrefix(wd, "/etc") {
			continue
		}
		a.refused("GET", "/sandboxes/"+id+"/files/stat?path="+q(p), nil, 400, "invalid")
		a.refused("GET", "/sandboxes/"+id+"/files/list?path="+q(p), nil, 400, "invalid")
		a.refused("PUT", "/sandboxes/"+id+"/files/content?path="+q(p+"/f"), []byte("x"), 400, "invalid")
		a.refused("POST", "/sandboxes/"+id+"/files/mkdir", map[string]any{"path": p + "/d"}, 400, "invalid")
	}
	a.put(id, wd+"/f", "x", "")
	a.refused("POST", "/sandboxes/"+id+"/files/move", map[string]any{"from": wd + "/f", "to": "f2"}, 400, "invalid")
}

func TestFilesTooLarge(t *testing.T) {
	t.Parallel()
	a := newCT(t, func(m *fsbManager) { m.FileMax = 1024 }).as("apps/a")
	var h struct{ Limits map[string]int64 }
	a.call("GET", "/hello", nil, 200, &h)
	fmax := h.Limits["fileMax"]
	sb := a.mk(map[string]any{"name": "big"})
	id, wd := sb.ID, sb.Workdir
	a.refused("PUT", "/sandboxes/"+id+"/files/content?path="+q(wd+"/f"), bytes.Repeat([]byte("x"), int(fmax)+1), 413, "too-large")
	a.put(id, wd+"/f", strings.Repeat("x", int(fmax)), "")
	a.sh(id, fmt.Sprintf("head -c %d /dev/zero > big", 2*fmax))
	a.refused("GET", "/sandboxes/"+id+"/files/content?path="+q(wd+"/big"), nil, 413, "too-large")
	var b []byte
	a.call("GET", "/sandboxes/"+id+"/files/content?path="+q(wd+"/big")+fmt.Sprintf("&offset=%d&length=%d", fmax, fmax/2), nil, 200, &b)
	if int64(len(b)) != fmax/2 {
		t.Fatalf("a ranged read of a big file: %d bytes", len(b))
	}
}

// --- trees (tar) -------------------------------------------------------------------------

type ctEntry struct {
	typ  byte
	body string
	link string
}

func ctUntar(t *testing.T, b []byte) map[string]ctEntry {
	t.Helper()
	out := map[string]ctEntry{}
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
		out[h.Name] = ctEntry{h.Typeflag, string(body), h.Linkname}
	}
}

func TestTar(t *testing.T) {
	t.Parallel()
	a := newCT(t).as("apps/a")
	sb := a.mk(map[string]any{"name": "tar"})
	id, wd := sb.ID, sb.Workdir
	a.put(id, wd+"/t/a.txt", "A", "&mkdirs=1")
	a.put(id, wd+"/t/sub/b.txt", "B", "&mkdirs=1")
	a.put(id, wd+"/t/node_modules/x", "X", "&mkdirs=1")
	a.sh(id, "ln -s a.txt t/l")
	var tb []byte
	hdr := a.call("GET", "/sandboxes/"+id+"/tar?path="+q(wd+"/t")+"&exclude=node_modules", nil, 200, &tb)
	if hdr.Get("Content-Type") != "application/x-tar" {
		t.Fatalf("content type %q", hdr.Get("Content-Type"))
	}
	ents := ctUntar(t, tb)
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
	// PUT: extracted under path; entries that climb out are skipped
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
	a.call("PUT", "/sandboxes/"+id+"/tar?path="+q(wd+"/u")+"&mkdirs=1", buf.Bytes(), http.StatusNoContent, nil)
	if got := a.read(id, wd+"/u/sub/b.txt"); got != "B" {
		t.Fatalf("extracted: %q", got)
	}
	if st := a.stat(id, wd+"/u/l"); st.Type != "symlink" || st.Target != "a.txt" {
		t.Fatalf("an extracted symlink: %+v", st)
	}
	for _, p := range []string{wd + "/evil", wd + "/evil2", wd + "/u/abs"} {
		a.refused("GET", "/sandboxes/"+id+"/files/stat?path="+q(p), nil, 404, "not-found")
	}
	a.refused("PUT", "/sandboxes/"+id+"/tar?path="+q(wd+"/missing"), buf.Bytes(), 404, "not-found")
	a.refused("GET", "/sandboxes/"+id+"/tar?path="+q(wd+"/none"), nil, 404, "not-found")
	a.refused("GET", "/sandboxes/"+id+"/tar?path="+q("/etc"), nil, 400, "invalid")
}

// --- snapshots and clones ------------------------------------------------------------------

func TestSnapshotsClones(t *testing.T) {
	t.Parallel()
	e := newCT(t)
	a, b := e.as("apps/a"), e.as("apps/b")
	sb := a.mk(map[string]any{"name": "snap"})
	id, wd := sb.ID, sb.Workdir
	a.put(id, wd+"/f", "v1", "")
	var s1, again ctSnap
	a.call("POST", "/sandboxes/"+id+"/snapshots", map[string]any{"name": "one", "clientId": "c"}, http.StatusCreated, &s1)
	if s1.ID == "" || s1.Name != "one" || s1.Created == 0 {
		t.Fatalf("snapshot: %+v", s1)
	}
	a.call("POST", "/sandboxes/"+id+"/snapshots", map[string]any{"name": "one", "clientId": "c"}, http.StatusOK, &again)
	if again.ID != s1.ID {
		t.Fatalf("a repeated clientId: %+v", again)
	}
	a.refused("POST", "/sandboxes/"+id+"/snapshots", map[string]any{"name": "two", "clientId": "c"}, 409, "exists")
	var l struct{ Snapshots []ctSnap }
	a.call("GET", "/sandboxes/"+id+"/snapshots", nil, 200, &l)
	if len(l.Snapshots) != 1 || l.Snapshots[0].ID != s1.ID {
		t.Fatalf("snapshots: %+v", l)
	}
	// restore: the files roll back and the execs are killed
	a.put(id, wd+"/f", "v2", "")
	a.put(id, wd+"/g", "new", "")
	x := a.exec(id, map[string]any{"cmd": "sleep 30"})
	var r ctSandbox
	a.call("POST", "/sandboxes/"+id+"/snapshots/"+s1.ID+"/restore", nil, 200, &r)
	if r.ID != id {
		t.Fatalf("restore answers the sandbox: %+v", r)
	}
	if got := a.read(id, wd+"/f"); got != "v1" {
		t.Fatalf("restored: %q", got)
	}
	a.refused("GET", "/sandboxes/"+id+"/files/stat?path="+q(wd+"/g"), nil, 404, "not-found")
	eventually(t, 3*time.Second, "a restore kills the execs", func() bool {
		var g ctExec
		a.call("GET", "/sandboxes/"+id+"/execs/"+x.ID, nil, 200, &g)
		return g.State == "killed"
	})
	// clones: of the sandbox now, of a snapshot
	a.put(id, wd+"/f", "v3", "")
	c1 := a.mk(map[string]any{"name": "clone", "from": map[string]any{"sandbox": id}})
	if c1.ID == id || a.read(c1.ID, c1.Workdir+"/f") != "v3" {
		t.Fatalf("a clone of the sandbox: %+v", c1)
	}
	c2 := a.mk(map[string]any{"name": "from-snap", "from": map[string]any{"sandbox": id, "snapshot": s1.ID}})
	if got := a.read(c2.ID, c2.Workdir+"/f"); got != "v1" {
		t.Fatalf("a clone of the snapshot: %q", got)
	}
	a.put(id, wd+"/f", "v4", "")
	if got := a.read(c1.ID, c1.Workdir+"/f"); got != "v3" {
		t.Fatalf("a clone is a copy: %q", got)
	}
	b.refused("POST", "/sandboxes", map[string]any{"name": "steal", "from": map[string]any{"sandbox": id}}, 404, "not-found")
	a.refused("POST", "/sandboxes", map[string]any{"name": "x", "from": map[string]any{"sandbox": id, "snapshot": "nope"}}, 404, "not-found")
	// delete
	a.call("DELETE", "/sandboxes/"+id+"/snapshots/"+s1.ID, nil, http.StatusNoContent, nil)
	a.call("GET", "/sandboxes/"+id+"/snapshots", nil, 200, &l)
	if len(l.Snapshots) != 0 {
		t.Fatalf("after delete: %+v", l)
	}
	a.refused("POST", "/sandboxes/"+id+"/snapshots/"+s1.ID+"/restore", nil, 404, "not-found")
	a.refused("DELETE", "/sandboxes/"+id+"/snapshots/"+s1.ID, nil, 404, "not-found")
}

// --- capabilities ----------------------------------------------------------------------------

func TestCapsMissing(t *testing.T) {
	t.Parallel()
	e := newCT(t, func(m *fsbManager) { m.Caps = []string{"exec", "files"} })
	a := e.as("apps/a")
	var h struct{ Caps []string }
	a.call("GET", "/hello", nil, 200, &h)
	if !slices.Equal(h.Caps, []string{"exec", "files"}) {
		t.Fatalf("caps: %v", h.Caps)
	}
	sb := a.mk(map[string]any{"name": "plain"})
	if slices.Contains(sb.Caps, "tar") || slices.Contains(sb.Caps, "snapshots") {
		t.Fatalf("a sandbox's caps %v", sb.Caps)
	}
	p := "/sandboxes/" + sb.ID
	for _, c := range []struct {
		method, path string
		body         any
	}{
		{"GET", p + "/tar?path=" + q(sb.Workdir), nil},
		{"PUT", p + "/tar?path=" + q(sb.Workdir), []byte{}},
		{"GET", p + "/snapshots", nil},
		{"POST", p + "/snapshots", map[string]any{"name": "s"}},
		{"POST", p + "/snapshots/s1/restore", nil},
		{"DELETE", p + "/snapshots/s1", nil},
		{"POST", "/sandboxes", map[string]any{"name": "c", "from": map[string]any{"sandbox": sb.ID}}},
		{"POST", p + "/archive", nil},
		{"POST", p + "/thaw", nil},
	} {
		a.refused(c.method, c.path, c.body, 501, "unsupported")
	}
	a.sh(sb.ID, "true") // exec and files still work
	a.put(sb.ID, sb.Workdir+"/f", "x", "")
}

// --- the reference manager itself -------------------------------------------------------------

func TestFakeHooks(t *testing.T) {
	t.Parallel()
	e := newCT(t)
	a := e.as("apps/a")
	// FailNext: the next call of an op answers a refusal, once
	e.m.FailNext("create", 503, "unavailable", "the substrate is down")
	if er := a.refused("POST", "/sandboxes", map[string]any{"name": "x"}, 503, "unavailable"); er.Error != "the substrate is down" {
		t.Fatalf("FailNext: %+v", er)
	}
	sb := a.asserting("alice").mk(map[string]any{"name": "hooks"})
	e.m.FailNext("output", 429, "limit", "busy")
	x := a.exec(sb.ID, map[string]any{"cmd": "true"})
	a.refused("GET", "/sandboxes/"+sb.ID+"/execs/"+x.ID+"/output", nil, 429, "limit")
	a.drain(sb.ID, x.ID)
	// GateExecs: exec starts wait for the release
	release := e.m.GateExecs()
	started := make(chan ctExec, 1)
	go func() {
		_, b, _ := a.do(context.Background(), "POST", "/sandboxes/"+sb.ID+"/execs", map[string]any{"cmd": "echo gated"})
		var g ctExec
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
	if out, _ := a.drain(sb.ID, g.ID); out != "gated\n" {
		t.Fatalf("the gated exec: %q", out)
	}
	// a gated exec killed before it starts never runs
	release = e.m.GateExecs()
	go func() {
		_, b, _ := a.do(context.Background(), "POST", "/sandboxes/"+sb.ID+"/execs", map[string]any{"cmd": "echo never > never"})
		var g ctExec
		_ = json.Unmarshal(b, &g)
		started <- g
	}()
	eventually(t, 5*time.Second, "the gated exec is held", func() bool {
		var l struct{ Execs []ctExec }
		a.call("GET", "/sandboxes/"+sb.ID+"/execs", nil, 200, &l)
		return len(l.Execs) == 3
	})
	a.call("POST", "/sandboxes/"+sb.ID+"/stop", nil, 200, nil)
	release()
	if g := <-started; g.State != "killed" {
		t.Fatalf("a gated exec stopped before it started: %+v", g)
	}
	a.refused("GET", "/sandboxes/"+sb.ID+"/files/stat?path="+q(sb.Workdir+"/never"), nil, 404, "not-found")
	// Fail412: the next conditional writes fail; unconditional ones don't count
	p := sb.Workdir + "/f"
	st := a.put(sb.ID, p, "v1", "")
	e.m.Fail412(1)
	a.put(sb.ID, p, "v1", "")
	a.refused("PUT", "/sandboxes/"+sb.ID+"/files/content?path="+q(p)+"&ifMatch="+st.ETag, []byte("v2"), 412, "precondition")
	a.put(sb.ID, p, "v2", "&ifMatch="+st.ETag)
	// Box, Calls
	if bx, ok := e.m.Box(sb.ID); !ok || bx.Owner.User != "alice" {
		t.Fatalf("Box: %+v %v", bx, ok)
	}
	var seen bool
	for _, c := range e.m.Calls() {
		if c.Method == "POST" && c.Path == "/sbx/sandboxes" && c.From == "apps/a" && c.SbxUser == "alice" && strings.Contains(c.Body, `"hooks"`) {
			seen = true
		}
	}
	if !seen {
		t.Fatalf("Calls lacks the create: %+v", e.m.Calls())
	}
}

// A sandbox of the fake is a host directory: whatever would leave it — a
// symlink to the host, a cwd through one — is refused (a real manager's
// symlinks resolve inside its own filesystem instead).
func TestFakeSymlinkEscape(t *testing.T) {
	t.Parallel()
	a := newCT(t).as("apps/a")
	sb := a.mk(map[string]any{"name": "escape"})
	id, wd := sb.ID, sb.Workdir
	a.sh(id, "ln -s / root")
	if st := a.stat(id, wd+"/root"); st.Type != "symlink" { // the link itself is the sandbox's
		t.Fatalf("stat: %+v", st)
	}
	a.refused("GET", "/sandboxes/"+id+"/files/content?path="+q(wd+"/root/etc/hostname"), nil, 400, "invalid")
	a.refused("GET", "/sandboxes/"+id+"/files/list?path="+q(wd+"/root"), nil, 400, "invalid")
	a.refused("PUT", "/sandboxes/"+id+"/files/content?path="+q(wd+"/root/tmp/fsb-escape"), []byte("x"), 400, "invalid")
	a.refused("POST", "/sandboxes/"+id+"/run", map[string]any{"cmd": "pwd", "cwd": wd + "/root"}, 400, "invalid")
	a.refused("GET", "/sandboxes/"+id+"/tar?path="+q(wd+"/root/etc"), nil, 400, "invalid")
	// a tar can't write through it either
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	_ = tw.WriteHeader(&tar.Header{Name: "root/tmp/fsb-escape-tar", Mode: 0o644, Size: 1, Typeflag: tar.TypeReg})
	_, _ = tw.Write([]byte("!"))
	_ = tw.Close()
	a.call("PUT", "/sandboxes/"+id+"/tar?path="+q(wd), buf.Bytes(), http.StatusNoContent, nil)
	if a.sh(id, "test -e /tmp/fsb-escape-tar && echo there || echo absent") != "absent\n" {
		t.Fatal("a tar entry went through a symlink out of the sandbox")
	}
	a.call("POST", "/sandboxes/"+id+"/files/remove", map[string]any{"path": wd + "/root"}, http.StatusNoContent, nil) // the link, not /
}
