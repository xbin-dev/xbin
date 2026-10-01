// Package sandboxcontract is the conformance suite of the sandbox-manager
// contract (docs/sandbox-manager.md, protocol 1): run it against your
// manager to check it speaks the contract the way consumers — the agent
// template, terminal tiles — rely on.
//
//	func TestContract(t *testing.T) {
//		srv := httptest.NewServer(myManager) // X-XBin-From etc. are set by the suite
//		defer srv.Close()
//		sandboxcontract.Run(t, sandboxcontract.Target{URL: srv.URL})
//	}
//
// Every check drives the manager over HTTP (and WebSocket, for terminals
// and stdio sockets) alone, grouped by the contract's sections as subtests — hello, sandboxes,
// partitions, people, lifecycle, run, execs, tty, stdio, files, tar,
// snapshots, ports, caps — so `go test -run 'TestContract/execs'` picks a section. Sections
// of an optional capability hello doesn't offer are skipped; a missing
// one's routes must answer `unsupported` (stdio's may answer `not-found`: a
// manager from before it).
//
// Each check acts as consumers of its own (apps/ct-<section>-<check>-a, …),
// so the checks run in parallel against one manager and see only their own
// sandboxes; each sandbox a check creates is deleted when it ends. The
// manager is called directly: the suite sets the headers xbind would
// (X-XBin-From, X-XBin-User, and on a partitioned consumer's calls
// X-XBin-Partition and X-XBin-Partition-Id) and Sbx-User — Target's hooks
// change how. The user-partitions section runs when hello's caps carry
// "partitions".
//
// Standard library and sdk/ws only.
package sandboxcontract

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/sdk/ws"
)

// Target is the manager under test.
type Target struct {
	// URL is the manager's base: its routes are URL + "/sbx/…".
	URL string
	// Client sends every request and dials the terminals (nil:
	// http.DefaultClient).
	Client *http.Client
	// Consumer, Verified and Asserted make a request come from a consumer
	// (a tile's path), a verified person and an asserted person. Nil: the
	// X-XBin-From and X-XBin-User headers xbind sets, and Sbx-User.
	Consumer func(r *http.Request, consumer string)
	Verified func(r *http.Request, user string)
	Asserted func(r *http.Request, user string)
	// Partition makes a request come from a partition of a partitioned
	// consumer: partition is "user:<id>" or "global", partitionID the user
	// partition's stable id ("" for global). Nil: the X-XBin-Partition and
	// X-XBin-Partition-Id headers xbind sets.
	Partition func(r *http.Request, partition, partitionID string)
	// Caps, when set, are capabilities hello must offer. Either way the
	// suite checks every capability hello offers and skips the sections of
	// the ones it doesn't.
	Caps []string
	// Grace is the manager's TERM → KILL grace on a timeout (0: the
	// contract's 5 s); the timeout checks wait for it.
	Grace time.Duration
	// Create is merged into the body of every sandbox the suite creates
	// (where the body doesn't set the field) — an image or size to use.
	Create map[string]any
	// Skip names checks this manager is known not to pass, with why — a
	// section ("execs") or one check ("execs/stdin"). They are skipped and
	// say so: a declared deviation, never a silent one.
	Skip map[string]string
	// Strict fails what the suite still only warns about. A check that a
	// manager built to an earlier suite may not pass yet, though the
	// contract always said it (today: tty/backend's refusals, from
	// 2026-09-30), skips with a warning for one release and fails in the
	// next; with Strict it fails now. The reference managers set it.
	Strict bool
	// Setup, when set, runs at the start of every check (manager-specific
	// preparation).
	Setup func(t *testing.T)
	// Fresh, when set, serves another manager for one check that wants one
	// of the Knobs (a small ring, a small fileMax, fewer capabilities),
	// torn down with t.Cleanup; hooks it leaves nil are this Target's. Nil:
	// those checks use this manager, with the limits its hello states.
	Fresh func(t *testing.T, k Knobs) Target
}

// Knobs are what a check would like a Fresh manager to have. A manager
// applies what it can; the check reads the result from hello.
type Knobs struct {
	Caps       []string // offer only these capabilities
	OutputRing int      // limits.outputRing, bytes (small: the ring overflows sooner)
	FileMax    int64    // limits.fileMax, bytes
}

func (tg *Target) grace() time.Duration {
	if tg.Grace > 0 {
		return tg.Grace
	}
	return 5 * time.Second
}

func (tg *Target) client() *http.Client {
	if tg.Client != nil {
		return tg.Client
	}
	return http.DefaultClient
}

// check is one conformance check.
type check struct {
	name string
	run  func(t *testing.T, e *env)
}

// section is a part of the contract and its checks.
type section struct {
	name   string
	cap    string // an optional capability the section needs ("" for none)
	checks []check
}

// sections, in the contract's order.
func sections() []section {
	return []section{
		{name: "hello", checks: helloChecks},
		{name: "sandboxes", checks: sandboxChecks},
		{name: "partitions", checks: partitionChecks},
		{name: "user-partitions", cap: "partitions", checks: userPartitionChecks},
		{name: "people", checks: peopleChecks},
		{name: "lifecycle", checks: lifecycleChecks},
		{name: "run", checks: runChecks},
		{name: "execs", checks: execChecks},
		{name: "tty", checks: ttyChecks},
		{name: "stdio", cap: "stdio", checks: stdioChecks},
		{name: "files", checks: fileChecks},
		{name: "tar", cap: "tar", checks: tarChecks},
		{name: "snapshots", cap: "snapshots", checks: snapshotChecks},
		{name: "ports", cap: "ports", checks: portChecks},
		{name: "caps", checks: capChecks},
	}
}

// Run runs the suite against tg, every check a parallel subtest
// <section>/<check>.
func Run(t *testing.T, tg Target) {
	t.Helper()
	if tg.URL == "" {
		t.Fatal("sandboxcontract: Target.URL is empty")
	}
	tg.URL = strings.TrimSuffix(tg.URL, "/")
	h := hello(t, tg.As(t, "apps/ct-hello"))
	for _, s := range sections() {
		t.Run(s.name, func(t *testing.T) {
			t.Parallel()
			if why, ok := tg.Skip[s.name]; ok {
				t.Skip("skipped by the target: " + why)
			}
			if s.cap != "" && !slices.Contains(h.Caps, s.cap) {
				t.Skipf("the manager doesn't offer %s (its routes' unsupported refusals: caps/missing)", s.cap)
			}
			for _, c := range s.checks {
				t.Run(c.name, func(t *testing.T) {
					t.Parallel()
					if why, ok := tg.Skip[s.name+"/"+c.name]; ok {
						t.Skip("skipped by the target: " + why)
					}
					if tg.Setup != nil {
						tg.Setup(t)
					}
					c.run(t, &env{t: t, tg: &tg, hello: h, prefix: "apps/ct-" + s.name + "-" + c.name})
				})
			}
		})
	}
}

// env is one check's world: its target, hello, and consumers of its own.
type env struct {
	t      *testing.T
	tg     *Target
	hello  Hello
	prefix string
}

// as is the check's consumer named role ("a", "b").
func (e *env) as(role string) Caller { return e.tg.As(e.t, e.prefix+"-"+role) }

func (e *env) has(capName string) bool { return slices.Contains(e.hello.Caps, capName) }

// fresh is a manager with knobs (Target.Fresh), or this one; its hello too.
func (e *env) fresh(k Knobs) (*env, bool) {
	if e.tg.Fresh == nil {
		return e, false
	}
	f := e.tg.Fresh(e.t, k)
	f.URL = strings.TrimSuffix(f.URL, "/")
	if f.Client == nil {
		f.Client = e.tg.Client
	}
	if f.Consumer == nil {
		f.Consumer = e.tg.Consumer
	}
	if f.Verified == nil {
		f.Verified = e.tg.Verified
	}
	if f.Asserted == nil {
		f.Asserted = e.tg.Asserted
	}
	if f.Partition == nil {
		f.Partition = e.tg.Partition
	}
	if f.Grace == 0 {
		f.Grace = e.tg.Grace
	}
	if f.Create == nil {
		f.Create = e.tg.Create
	}
	n := &env{t: e.t, tg: &f, prefix: e.prefix}
	n.hello = hello(e.t, n.as("hello"))
	return n, true
}

// --- the caller ---------------------------------------------------------------------

// Caller calls a manager as a consumer, maybe for a person — the suite's
// client, exported for a manager's own tests of what the contract leaves
// to it. Its methods fail the test on a transport error or an unexpected
// answer.
type Caller struct {
	t              *testing.T
	tg             *Target
	from           string
	user, asserted string
	part, partID   string // the consumer's partition: "user:<id>" and its id, or "global" and ""
}

// As is a caller from consumer (a tile's path).
func (tg Target) As(t *testing.T, consumer string) Caller {
	tg.URL = strings.TrimSuffix(tg.URL, "/")
	return Caller{t: t, tg: &tg, from: consumer}
}

// Verified is c for a verified person (a page's call).
func (c Caller) Verified(user string) Caller { c.user = user; return c }

// Asserting is c naming a person it acts for (a backend's call).
func (c Caller) Asserting(user string) Caller { c.asserted = user; return c }

// InPartition is c from user's partition of a partitioned consumer, whose
// stable id is id (X-XBin-Partition: user:<user>, X-XBin-Partition-Id: id):
// the partition's backend — add Verified(user) for its page.
func (c Caller) InPartition(user, id string) Caller { c.part, c.partID = "user:"+user, id; return c }

// Global is c from a partitioned consumer's global instance
// (X-XBin-Partition: global): the same consumer as c without a partition.
func (c Caller) Global() Caller { c.part, c.partID = "global", ""; return c }

// Consumer is c's consumer.
func (c Caller) Consumer() string { return c.from }

func (c Caller) who() string {
	w := c.from
	if c.part != "" {
		w += "[" + c.part + "]"
	}
	return w + "/" + c.user + c.asserted
}

// decorate sets who is asking on r.
func (c Caller) decorate(r *http.Request) {
	if c.tg.Consumer != nil {
		c.tg.Consumer(r, c.from)
	} else {
		r.Header.Set("X-XBin-From", c.from)
	}
	if c.user != "" {
		if c.tg.Verified != nil {
			c.tg.Verified(r, c.user)
		} else {
			r.Header.Set("X-XBin-User", c.user)
		}
	}
	if c.asserted != "" {
		if c.tg.Asserted != nil {
			c.tg.Asserted(r, c.asserted)
		} else {
			r.Header.Set("Sbx-User", c.asserted)
		}
	}
	if c.part != "" {
		if c.tg.Partition != nil {
			c.tg.Partition(r, c.part, c.partID)
		} else {
			r.Header.Set("X-XBin-Partition", c.part)
			if c.partID != "" {
				r.Header.Set("X-XBin-Partition-Id", c.partID)
			}
		}
	}
}

// Do sends a request to URL/sbx + path; a []byte body goes raw, anything
// else as JSON. The whole answer is read.
func (c Caller) Do(ctx context.Context, method, path string, body any) (*http.Response, []byte, error) {
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
	req, err := http.NewRequestWithContext(ctx, method, c.tg.URL+"/sbx"+path, rd)
	if err != nil {
		return nil, nil, err
	}
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	c.decorate(req)
	resp, err := c.tg.client().Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return resp, b, err
}

// Call wants status and decodes the answer into out (JSON; a *[]byte takes
// it raw) when out isn't nil.
func (c Caller) Call(method, path string, body any, want int, out any) http.Header {
	c.t.Helper()
	resp, b, err := c.Do(context.Background(), method, path, body)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	if resp.StatusCode != want {
		c.t.Fatalf("%s %s as %s: status %d, want %d: %s", method, path, c.who(), resp.StatusCode, want, b)
	}
	if out != nil {
		if p, ok := out.(*[]byte); ok {
			*p = b
		} else if err := json.Unmarshal(b, out); err != nil {
			c.t.Fatalf("%s %s: %v in %s", method, path, err, b)
		}
	}
	return resp.Header
}

// Refusal is an error answer: {error, refusal, state?, etag?, retryAfterMs?, protocols?}.
type Refusal struct {
	Error        string `json:"error"`
	Refusal      string `json:"refusal"`
	State        string `json:"state"`
	ETag         string `json:"etag"`
	RetryAfterMs int    `json:"retryAfterMs"`
	Protocols    []int  `json:"protocols"`
}

// Refused wants an error answer: JSON {error, refusal} with this status.
func (c Caller) Refused(method, path string, body any, status int, refusal string) Refusal {
	c.t.Helper()
	resp, b, err := c.Do(context.Background(), method, path, body)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	return c.refusal(method+" "+path, resp, b, status, refusal)
}

func (c Caller) refusal(what string, resp *http.Response, b []byte, status int, refusal string) Refusal {
	c.t.Helper()
	var e Refusal
	if resp.StatusCode != status || json.Unmarshal(b, &e) != nil || e.Refusal != refusal || e.Error == "" {
		c.t.Fatalf("%s as %s: %d %s, want %d refusal %q", what, c.who(), resp.StatusCode, b, status, refusal)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		c.t.Fatalf("%s: an error as %q, want JSON", what, ct)
	}
	return e
}

// Dial opens a WebSocket to URL/sbx + path (a terminal) as c, through the
// target's client. The response comes back too — on a refusal, with its
// status and body.
func (c Caller) Dial(ctx context.Context, path string) (*ws.Conn, *http.Response, error) {
	req, err := http.NewRequest(http.MethodGet, c.tg.URL+"/sbx"+path, nil)
	if err != nil {
		return nil, nil, err
	}
	c.decorate(req)
	return ws.Dial(ctx, req.URL.String(), req.Header, &ws.DialOptions{Client: c.tg.client()})
}

// --- the resources, as the suite reads them ------------------------------------------

// Hello is GET /sbx/hello.
type Hello struct {
	Protocol  int
	Protocols []int
	Manager   struct{ Name, Title, Version string }
	Caps      []string
	Egress    []string
	Images    []struct {
		ID        string
		Default   bool
		Harnesses []Harness
	}
	Sizes []struct {
		ID      string
		Default bool
	}
	Limits map[string]int64
}

// Harness is one of an image's hello.images[].harnesses: a coding agent
// installed in it that speaks ACP (docs/sandbox-manager.md §hello).
type Harness struct {
	ID, Title string
	Argv      []string
	Login     string
}

func hello(t *testing.T, c Caller) Hello {
	t.Helper()
	var h Hello
	c.Call("GET", "/hello?protocol=1", nil, http.StatusOK, &h)
	return h
}

// Sandbox is the sandbox resource.
type Sandbox struct {
	ID, Name, State, StateDetail, Isolation, Egress, Visibility string
	EgressNext                                                  string
	Workdir, Home, User, Shell                                  string
	Image                                                       struct{ ID, Title string }
	Owner                                                       struct {
		User, Via string
		Asserted  bool
		// a sandbox homed in a partitioned consumer's user partition: that
		// partition's id, and "user:<id>" for display (absent otherwise)
		PartitionID, Partition string
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

// Output is one stream of a run: head, tail, what was elided between.
type Output struct {
	Head, Tail    string
	Elided, Bytes int64
}

// RunResult is a run's answer.
type RunResult struct {
	ExitCode               *int // nil: a signal ended it
	Signal                 string
	TimedOut               bool
	Ms                     int64
	Stdout, Stderr, Output *Output
}

// Exec is a background exec (Split and ErrTotal: the stdio capability's
// split exec, its stderr apart).
type Exec struct {
	ID, Label, Cmd, Cwd, State, Signal, ClientID string
	Argv                                         []string
	TTY, Split                                   bool
	ExitCode                                     *int
	Started, Ended, Total, ErrTotal              int64
}

// Chunk is a read of an exec's output.
type Chunk struct {
	Start, End, Total, RingStart  int64
	Data, Encoding, State, Signal string
	ExitCode                      *int
}

// Stat is a file's stat.
type Stat struct {
	Path, Type, Mode, ETag, Target string
	Size, MtimeMs                  int64
}

// Snapshot is a sandbox's snapshot.
type Snapshot struct {
	ID, Name       string
	Created, Bytes int64
}

// q escapes a query value.
func q(p string) string { return url.QueryEscape(p) }

// Create makes a sandbox (201) with the target's Create defaults merged in,
// deleted again when the test ends.
func (c Caller) Create(body map[string]any) Sandbox {
	c.t.Helper()
	full := map[string]any{}
	for k, v := range c.tg.Create {
		full[k] = v
	}
	for k, v := range body {
		full[k] = v
	}
	var sb Sandbox
	c.Call("POST", "/sandboxes", full, http.StatusCreated, &sb)
	home := Caller{t: c.t, tg: c.tg, from: c.from, part: c.part, partID: c.partID}
	c.t.Cleanup(func() { _, _, _ = home.Do(context.Background(), "DELETE", "/sandboxes/"+sb.ID, nil) })
	return sb
}

// Get reads a sandbox (200).
func (c Caller) Get(id string) Sandbox {
	c.t.Helper()
	var sb Sandbox
	c.Call("GET", "/sandboxes/"+id, nil, http.StatusOK, &sb)
	return sb
}

// List is the caller's sandboxes by id.
func (c Caller) List() map[string]Sandbox {
	c.t.Helper()
	var l struct{ Sandboxes []Sandbox }
	c.Call("GET", "/sandboxes", nil, http.StatusOK, &l)
	out := map[string]Sandbox{}
	for _, s := range l.Sandboxes {
		out[s.ID] = s
	}
	return out
}

// Run runs a command and waits (200).
func (c Caller) Run(id string, body map[string]any) RunResult {
	c.t.Helper()
	var r RunResult
	c.Call("POST", "/sandboxes/"+id+"/run", body, http.StatusOK, &r)
	return r
}

// Sh runs cmd, wants exit 0 and returns its stdout.
func (c Caller) Sh(id, cmd string) string {
	c.t.Helper()
	r := c.Run(id, map[string]any{"cmd": cmd})
	if r.ExitCode == nil || *r.ExitCode != 0 || r.Stdout == nil {
		exit := "none"
		if r.ExitCode != nil {
			exit = fmt.Sprint(*r.ExitCode)
		}
		c.t.Fatalf("run %q: exit %s (signal %q), stderr %+v", cmd, exit, r.Signal, r.Stderr)
	}
	return r.Stdout.Head + r.Stdout.Tail
}

// Put writes a file (200); query adds parameters ("&mkdirs=1").
func (c Caller) Put(id, path, content, query string) Stat {
	c.t.Helper()
	var st Stat
	c.Call("PUT", "/sandboxes/"+id+"/files/content?path="+q(path)+query, []byte(content), http.StatusOK, &st)
	return st
}

// Read reads a file (200).
func (c Caller) Read(id, path string) string {
	c.t.Helper()
	var b []byte
	c.Call("GET", "/sandboxes/"+id+"/files/content?path="+q(path), nil, http.StatusOK, &b)
	return string(b)
}

// Stat stats a path (200).
func (c Caller) Stat(id, path string) Stat {
	c.t.Helper()
	var st Stat
	c.Call("GET", "/sandboxes/"+id+"/files/stat?path="+q(path), nil, http.StatusOK, &st)
	return st
}

// Exec starts a background exec (201).
func (c Caller) Exec(id string, body map[string]any) Exec {
	c.t.Helper()
	var x Exec
	c.Call("POST", "/sandboxes/"+id+"/execs", body, http.StatusCreated, &x)
	return x
}

// Chunk reads an exec's output (query: since, max, waitMs, encoding).
func (c Caller) Chunk(id, eid, query string) Chunk {
	c.t.Helper()
	var ch Chunk
	c.Call("GET", "/sandboxes/"+id+"/execs/"+eid+"/output?"+query, nil, http.StatusOK, &ch)
	return ch
}

// Drain reads an exec's output from 0 until it has ended and all is read.
func (c Caller) Drain(id, eid string) (string, Chunk) {
	c.t.Helper()
	var buf []byte
	since := int64(0)
	for deadline := time.Now().Add(10*time.Second + c.tg.grace()); ; {
		ch := c.Chunk(id, eid, fmt.Sprintf("since=%d&waitMs=2000&encoding=base64", since))
		d, err := base64.StdEncoding.DecodeString(ch.Data)
		if err != nil || ch.Encoding != "base64" {
			c.t.Fatalf("output: %v (encoding %q)", err, ch.Encoding)
		}
		buf, since = append(buf, d...), ch.End
		if ch.State != "running" && ch.End >= ch.Total {
			return string(buf), ch
		}
		if time.Now().After(deadline) {
			c.t.Fatalf("exec %s still %s: %q", eid, ch.State, buf)
		}
	}
}

// Gone: the process whose pid the sandbox wrote at path has ended — asked
// inside the sandbox.
func (c Caller) Gone(id, path string) bool {
	c.t.Helper()
	out := c.Sh(id, `p=$(cat `+path+` 2>/dev/null); [ -n "$p" ] && ! kill -0 "$p" 2>/dev/null && echo gone || echo alive`)
	return strings.TrimSpace(out) == "gone"
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
