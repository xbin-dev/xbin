//go:build linux && integration

package isolated

// harness_test.go — the agent template's coding agents live (D-harness,
// WP-A11): coding-sandbox on xbind's runtime (setupCS), the agent template
// instantiated beside it with its `sandboxes` slot bound to the manager, a
// sandbox with the manager's internet egress (a coding agent must reach its
// provider), and the agent's API driven as a person, through xbind's
// proxy, as its page drives it:
//
//   - GET /harnesses: the four real coding agents of the rootfs (claude,
//     codex, gemini, opencode) and the test's fake advertised by the image,
//     and a probe that finds each installed in the sandbox;
//   - each real adapter by hand through the contract's exec routes (the
//     baseline transport: stdin POSTs, the base64 output long-poll) —
//     initialize, then session/new, as the agent's client asks them — and
//     then through the agent's engine: a conversation per adapter (POST /ask
//     {harness, sandbox}) that gets through initialize and session/new and,
//     with no credentials in the sandbox, parks on its sign-in
//     (pendingState.kind "login", the login command and methods) or ends its
//     turn with the adapter's own error. What each did is logged ("adapter
//     <id>: …") for the owner;
//   - the fake adapter (hack/fakeacp, copied into the sandbox through the
//     files API and advertised by the image as `fake`) for a whole turn: its
//     sign-in in a terminal relayed by the agent (GET /runs/{id}/harness/
//     terminal?login=1 — a WebSocket through xbind's proxy, the agent's
//     relay, the gateway, the manager and the runtime's pty), Retry, a
//     permission parked and answered, the answer; the stdio socket as the
//     pipe's transport (the runtime offers `stdio`); a shell relayed by the
//     run's and by the sandbox's relay; and the handoff — the agent's backend
//     redeployed mid-turn attaches to the same adapter and finishes with it.
//
// They act as people, so they need owner auth on (as consumers_test.go).
// HARNESS_LIVE_ONLY=<ids> (comma-separated) narrows the real adapters.

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/xbin-dev/xbin/sdk/sandboxcontract"
	"github.com/xbin-dev/xbin/test/xbindtest"
)

// realHarnesses are the coding agents the base rootfs pins (docker/
// rootfs.Dockerfile), with the command the manager advertises for each and
// the environment the agent's engine starts it with (sdk/acp's catalog:
// claude offers its remote sign-in, codex no browser).
var realHarnesses = []struct {
	id   string
	argv []string
	env  map[string]string
}{
	{"claude", []string{"claude-agent-acp"}, map[string]string{"CLAUDE_CODE_REMOTE": "1"}},
	{"codex", []string{"codex-acp"}, map[string]string{"NO_BROWSER": "1"}},
	{"gemini", []string{"gemini", "--acp"}, nil},
	{"opencode", []string{"opencode", "acp"}, nil},
}

// fakeACPPath is where the test puts hack/fakeacp in a sandbox; the image
// advertises it as the coding agent `fake` (--require-login: signed in by
// its terminal sign-in; --steer, --persist as the UI harness runs it).
const fakeACPPath = "/work/.xbin-a11/fakeacp"

func TestHarnessLive(t *testing.T) {
	e, _, fake := setupHarness(t, false)
	runHarness(t, e, fake, "namespace", 1)
}

// TestHarnessLiveVM is the same with VM sandboxes (the manager's `auto`
// mode where the runtime offers VMs).
func TestHarnessLiveVM(t *testing.T) {
	e, accel, fake := setupHarness(t, true)
	slow := time.Duration(1)
	if accel == "emulate" {
		slow = 6
	}
	runHarness(t, e, fake, "vm", slow)
}

// setupHarness is setupCS, plus the fake adapter advertised by the base
// image, a size that holds four adapters at once, and the agent template
// as apps/agent with its sandboxes slot bound to the manager (no model: a
// coding agent's conversation never reaches one). It returns the fake
// adapter's binary.
func setupHarness(t *testing.T, vm bool) (*csEnv, string, []byte) {
	t.Helper()
	e, accel := setupCS(t, vm)
	if !e.people {
		t.Skip("no people here (--no-auth): the agent's coding agents are driven as verified people")
	}
	d := e.d
	agTile = "apps/agent"
	if d.IsRemote() {
		agTile = "apps/agent-" + strings.TrimPrefix(csTile, "apps/cs-")
	}
	fake := buildFakeACP(t, d)

	// the image advertises the fake beside the four (its saved harnesses
	// replace the default list, so the four are kept as they are)
	var st struct {
		Config struct{ Images []map[string]any }
	}
	e.ops(t, "GET", "/state", nil, 200, &st)
	if len(st.Config.Images) == 0 {
		t.Fatalf("the manager's images: %+v", st.Config)
	}
	im := st.Config.Images[0]
	hs, _ := im["harnesses"].([]any)
	var ids []string
	for _, h := range hs {
		if m, ok := h.(map[string]any); ok {
			ids = append(ids, fmt.Sprint(m["id"]))
		}
	}
	if fmt.Sprint(ids) != "[claude codex gemini opencode]" {
		t.Errorf("the base image's default harnesses: %v", hs)
	}
	im["harnesses"] = append(hs, map[string]any{"id": "fake", "title": "Fake agent (tests)",
		"argv": []string{fakeACPPath, "--steer", "--persist", "--require-login"}, "login": fakeACPPath + " login"})
	sizes := []map[string]any{{"id": "tiny", "title": "Tiny", "memMiB": 512, "vcpus": 1, "diskGiB": 2, "default": true},
		{"id": "small", "title": "Small", "memMiB": 1024, "vcpus": 1, "diskGiB": 4},
		{"id": "coder", "title": "Coder", "memMiB": 3072, "vcpus": 2, "diskGiB": 8}}
	e.ops(t, "PUT", "/config", map[string]any{"images": []any{im}, "sizes": sizes}, 200, nil)

	d.Must(t, "POST", "/api/xbin/templates/new", map[string]string{"source": "agent", "path": agTile}, 200)
	d.WaitComponent(t, agTile)
	d.Must(t, "POST", "/api/xbin/bindings", map[string]any{"component": agTile, "slot": "sandboxes", "providers": []string{csTile}}, 200)
	d.Bind(t, agTile, "net", "none")
	xbindtest.Eventually(t, 8*time.Minute, "the agent's backend answers", func() (bool, string) {
		r := d.Call(t, "GET", "/api/"+agTile+"/config", nil)
		return r.Status == 200, fmt.Sprint(r.Status, " ", r)
	})
	return e, accel, fake
}

// buildFakeACP builds hack/fakeacp static for linux/amd64 (a sandbox's
// system, a VM's too): its bytes.
func buildFakeACP(t *testing.T, d *xbindtest.Daemon) []byte {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "fakeacp")
	build := exec.Command("go", "build", "-o", bin, "./hack/fakeacp")
	build.Dir = d.A.Repo
	build.Env = append(os.Environ(), "GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building fakeacp: %v\n%s", err, out)
	}
	b, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// hcEntry is one entry of GET /harnesses, as far as the test reads it.
type hcEntry struct {
	ID, Name, Reason, Why string
	Available             bool
	Classes               []string
	Images                []struct {
		Provider, Manager, Image string
		Advertised               bool
		Egress                   []string
	}
	Login     struct{ Command string }
	Sandboxes map[string]struct {
		Installed, SignedIn *bool
		At                  int64
	}
}

// hSummary is a harness run's `harness` (D-harness §4.3.2).
type hSummary struct {
	Provider, Name, State, Error, Title string
	Gen                                 int
	Steering                            bool
	Mode                                struct {
		Current   string
		Available []struct {
			ID, Name string
			Explicit bool
		}
	}
	Options []struct {
		ID, Name, Category string
		CurrentValue       any
	}
	Commands []struct{ Name string }
	Login    *struct {
		Command string
		Methods []struct{ ID, Name, Kind string }
	}
	Pending  *struct{ Park, Kind, Title string }
	Activity *struct{ Kind, Title string }
	Sandbox  struct {
		Ref, Name, Cwd string
		Shared         bool
	}
}

// hView is GET /runs/{id}/view, as far as the test reads it.
type hView struct {
	Run struct {
		ID           int64
		Status       string
		Engine       string
		PendingState *struct {
			Kind, Park string
			Harness    json.RawMessage
		}
		Harness *hSummary
	}
	Messages []struct {
		Role, Content string
		ACP           map[string]any `json:"acp"`
	}
}

func (v hView) summary() hSummary {
	if v.Run.Harness == nil {
		return hSummary{}
	}
	return *v.Run.Harness
}

func (v hView) pendingKind() string {
	if v.Run.PendingState == nil {
		return ""
	}
	return v.Run.PendingState.Kind
}

// lastAssistant is the run's latest assistant text.
func (v hView) lastAssistant() string {
	for i := len(v.Messages) - 1; i >= 0; i-- {
		if m := v.Messages[i]; m.Role == "assistant" && m.Content != "" {
			return m.Content
		}
	}
	return ""
}

func (v hView) brief() string {
	s := v.summary()
	return fmt.Sprintf("status %s, harness %s (gen %d, error %q), pending %s, last %q", v.Run.Status, s.State, s.Gen, s.Error,
		v.pendingKind(), cut(v.lastAssistant(), 300))
}

func (e *csEnv) view(t *testing.T, person string, id int64) hView {
	t.Helper()
	var v hView
	e.ag(t, person, "GET", fmt.Sprintf("/runs/%d/view", id), nil, 200, &v)
	return v
}

// waitView waits for the run's view to satisfy cond: the view.
func (e *csEnv) waitView(t *testing.T, person string, id int64, what string, timeout time.Duration, cond func(v hView) bool) hView {
	t.Helper()
	var v hView
	xbindtest.Eventually(t, timeout, fmt.Sprintf("run %d: %s", id, what), func() (bool, string) {
		v = e.view(t, person, id)
		return cond(v), v.brief()
	})
	return v
}

// harnessExec is the manager's exec of run id's adapter, generation gen
// (clientId harness:<run>:<gen>, D-harness §3.6): nil when there is none.
func harnessExec(t *testing.T, c sandboxcontract.Caller, box string, id int64, gen int) *sandboxcontract.Exec {
	t.Helper()
	var execs struct{ Execs []sandboxcontract.Exec }
	c.Call("GET", "/sandboxes/"+box+"/execs", nil, 200, &execs)
	want := fmt.Sprintf("harness:%d:%d", id, gen)
	for _, x := range execs.Execs {
		if x.ClientID == want {
			return &x
		}
	}
	return nil
}

// agentPath is a route of the agent's dialled as its page does (xbin.ws:
// the frame token person's session minted, in ?frame=).
func (e *csEnv) agentPath(t *testing.T, sub, person string) string {
	sep := "?"
	if strings.Contains(sub, "?") {
		sep = "&"
	}
	return "/api/" + agTile + sub + sep + "frame=" + url.QueryEscape(e.pageTok(t, agTile, person))
}

func runHarness(t *testing.T, e *csEnv, fake []byte, mode string, slow time.Duration) {
	d := e.d
	turn := 90 * time.Second * slow

	// alice's sandbox, made in the agent: the manager's internet egress, room
	// for four adapters at once
	var box agSandbox
	e.ag(t, "alice", "POST", "/sandboxes", map[string]any{"name": "coder", "egress": "internet", "size": "coder"}, 201, &box)
	asAlice := e.target().As(t, agTile).Verified("alice")
	if got := asAlice.Get(box.ID); got.Isolation != mode || got.Egress != "internet" || got.State != "running" {
		t.Fatalf("the sandbox at the manager: %+v (want %s, internet, running)", got, mode)
	}
	asAlice.Put(box.ID, fakeACPPath, string(fake), "&mkdirs=1&mode=755")

	var hello struct{ Caps []string }
	asAlice.Call("GET", "/hello?protocol=1", nil, 200, &hello)
	stdio := slices.Contains(hello.Caps, "stdio")
	if !d.IsRemote() && !stdio {
		t.Errorf("the manager's caps: %v (this tree's runtime offers stdio)", hello.Caps)
	}

	t.Run("catalog", func(t *testing.T) { testHarnessCatalog(t, e, box) })

	only := os.Getenv("HARNESS_LIVE_ONLY")
	t.Run("adapters", func(t *testing.T) {
		for _, h := range realHarnesses {
			if only != "" && !slices.Contains(strings.Split(only, ","), h.id) {
				continue
			}
			t.Run(h.id+"/handshake", func(t *testing.T) {
				p := acpHandshake(t, e.target().As(t, agTile).Verified("alice"), box.ID, h.argv, h.env, stdio, turn)
				t.Logf("adapter %s: %s", h.id, p)
				if p.Init == nil {
					t.Errorf("%s didn't answer initialize: %s", h.id, p)
				}
			})
		}
		var mu sync.Mutex
		report := map[string]string{}
		t.Run("engine", func(t *testing.T) { // the four at once
			for _, h := range realHarnesses {
				if only != "" && !slices.Contains(strings.Split(only, ","), h.id) {
					continue
				}
				t.Run(h.id, func(t *testing.T) {
					t.Parallel()
					r := testHarnessAdapter(t, e, box, h.id, stdio, turn)
					mu.Lock()
					report[h.id] = r
					mu.Unlock()
				})
			}
		})
		for _, h := range realHarnesses {
			if r, ok := report[h.id]; ok {
				t.Logf("adapter %s through the agent: %s", h.id, r)
			}
		}
	})

	t.Run("fake", func(t *testing.T) { testHarnessFake(t, e, box, stdio, slow) })
}

// testHarnessCatalog: GET /harnesses?probe=<ref> as alice — the four and
// the fake available, advertised by the manager's image, each found
// installed in the sandbox.
func testHarnessCatalog(t *testing.T, e *csEnv, box agSandbox) {
	var cat struct {
		Harnesses []hcEntry
		Probe     struct {
			Ref         string
			Ran, Cached bool
			Error       string
		}
	}
	e.ag(t, "alice", "GET", "/harnesses?probe="+url.QueryEscape(box.Ref), nil, 200, &cat)
	if cat.Probe.Ref != box.Ref || !cat.Probe.Ran || cat.Probe.Error != "" {
		t.Errorf("the probe: %+v", cat.Probe)
	}
	seen := map[string]hcEntry{}
	for _, h := range cat.Harnesses {
		seen[h.ID] = h
	}
	for _, id := range []string{"claude", "codex", "gemini", "opencode", "fake"} {
		h, ok := seen[id]
		if !ok {
			t.Errorf("GET /harnesses lists no %s: %+v", id, cat.Harnesses)
			continue
		}
		adv := len(h.Images) > 0 && h.Images[0].Provider == csTile && h.Images[0].Advertised && slices.Contains(h.Images[0].Egress, "internet")
		inst := h.Sandboxes[box.Ref].Installed
		if !h.Available || !adv || inst == nil || !*inst {
			t.Errorf("%s: available %v (%s: %s), images %+v, in the sandbox %+v (want available, advertised with internet, installed)",
				id, h.Available, h.Reason, h.Why, h.Images, h.Sandboxes[box.Ref])
		}
		t.Logf("catalog %s: %q, classes %v, login %q", id, h.Name, h.Classes, h.Login.Command)
	}
}

// testHarnessAdapter drives real adapter id through the agent: a
// conversation whose first prompt it answers — or, signed out, parks on its
// sign-in (its login terminal read) — then deletes it. It returns what happened,
// for the report.
func testHarnessAdapter(t *testing.T, e *csEnv, box agSandbox, id string, stdio bool, turn time.Duration) string {
	var run struct {
		ID     int64
		Engine string
	}
	e.ag(t, "alice", "POST", "/ask", map[string]any{"text": "Reply with the single word: pong", "harness": map[string]string{"provider": id},
		"sandbox": map[string]string{"ref": box.Ref}}, 200, &run)
	if run.ID == 0 || run.Engine != "harness" {
		t.Fatalf("POST /ask with %s: %+v", id, run)
	}
	v := e.waitView(t, "alice", run.ID, id+" settles", 3*turn, func(v hView) bool {
		s := v.summary()
		switch {
		case s.State == "login" && v.pendingKind() == "login":
			return true
		case s.State == "failed" || s.State == "lost":
			return true
		}
		return v.Run.Status != "running" && s.State != "starting" && s.State != "working"
	})
	s := v.summary()
	var b strings.Builder
	var modes []string
	for _, m := range s.Mode.Available {
		modes = append(modes, m.ID)
	}
	fmt.Fprintf(&b, "status %s, state %s, gen %d, steering %v, mode %q of %v", v.Run.Status, s.State, s.Gen, s.Steering, s.Mode.Current, modes)
	if len(s.Options) > 0 {
		var ids []string
		for _, o := range s.Options {
			ids = append(ids, fmt.Sprintf("%s=%v", o.ID, o.CurrentValue))
		}
		fmt.Fprintf(&b, ", options %v", ids)
	}
	if len(s.Commands) > 0 {
		fmt.Fprintf(&b, ", %d commands", len(s.Commands))
	}
	if s.Login != nil {
		fmt.Fprintf(&b, ", login command %q, methods %+v", s.Login.Command, s.Login.Methods)
	}
	if s.Error != "" {
		fmt.Fprintf(&b, ", error %q", s.Error)
	}
	if a := v.lastAssistant(); a != "" {
		fmt.Fprintf(&b, ", said %q", cut(a, 400))
	}
	c := e.target().As(t, agTile).Verified("alice")
	if x := harnessExec(t, c, box.ID, run.ID, s.Gen); x == nil {
		t.Errorf("no exec harness:%d:%d at the manager", run.ID, s.Gen)
	} else {
		fmt.Fprintf(&b, ", exec %s split %v", x.State, x.Split)
		if stdio && !x.Split {
			t.Errorf("%s's exec isn't split: the pipe didn't take the stdio socket the manager offers (%+v)", id, *x)
		}
	}
	if r := e.ag(t, "alice", "GET", fmt.Sprintf("/runs/%d/harness/log?max=2000", run.ID), nil, 0, nil); r.Status == 200 {
		fmt.Fprintf(&b, "; stderr tail: %q", cut(strings.TrimSpace(string(r.Body)), 600))
	}

	// its sign-in command in a terminal the agent relays (login=1): what it
	// shows, for the report (the relay ends it when the socket goes)
	if s.State == "login" {
		fmt.Fprintf(&b, "; its login terminal shows %q", loginScreen(t, e, run.ID, turn))
	}

	// deleting the conversation ends its adapter (eof, TERM, DELETE)
	e.ag(t, "alice", "DELETE", fmt.Sprintf("/runs/%d", run.ID), nil, 200, nil)
	xbindtest.Eventually(t, 30*time.Second, id+"'s adapter ended with its conversation", func() (bool, string) {
		x := harnessExec(t, c, box.ID, run.ID, s.Gen)
		return x == nil || x.State != "running", fmt.Sprintf("%+v", x)
	})

	// through initialize and session/new: signed out it parks on its sign-in,
	// signed in (or free) it answers, or its prompt ends with its own error
	switch {
	case s.State == "login":
		if v.pendingKind() != "login" || v.Run.Status != "waiting_input" || s.Login == nil || len(s.Login.Methods) == 0 || s.Login.Command == "" {
			t.Errorf("%s's sign-in park: %s, login %+v", id, v.brief(), s.Login)
		}
	case s.State == "failed" || s.State == "lost":
		t.Errorf("%s didn't start: %s", id, b.String())
	case v.Run.Status == "error":
		t.Logf("%s's turn ended with its error (a clean ACP error counts): %s", id, b.String())
	case v.lastAssistant() == "":
		t.Errorf("%s: no sign-in park and no answer: %s", id, b.String())
	}
	return b.String()
}

// loginScreen opens run id's sign-in terminal (GET /runs/{id}/harness/
// terminal?login=1, as alice) and reads what it prints until it has been
// quiet for 3 s (at most wait): the text, escapes stripped.
func loginScreen(t *testing.T, e *csEnv, id int64, wait time.Duration) string {
	t.Helper()
	conn, r, err := e.d.Dial(t, e.agentPath(t, fmt.Sprintf("/runs/%d/harness/terminal?login=1&rows=30&cols=120", id), "alice"))
	if err != nil {
		t.Errorf("run %d's sign-in terminal: %v (%d %s)", id, err, r.Status, r)
		return ""
	}
	defer conn.Close()
	readSession(t, conn)
	var out strings.Builder
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		kind, b, err := conn.ReadMessage()
		if err != nil {
			break // quiet for 3 s, or it ended
		}
		if kind == websocket.BinaryMessage {
			out.Write(b)
		}
	}
	text := ansiRE.ReplaceAllString(out.String(), " ")
	return cut(strings.Join(strings.Fields(text), " "), 700)
}

// ansiRE matches terminal escape sequences (CSI, OSC, single-character).
var ansiRE = regexp.MustCompile(`\x1b\[[0-9;?<>=]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(\x07|\x1b\\)|\x1b[@-Z\\-_]|[\x00-\x08\x0b-\x1f\x7f]`)

// testHarnessFake: the fake adapter for a whole turn — signed in through
// the run's terminal relay, a permission parked and answered, a shell on
// both relays, and the handoff: the agent's backend redeployed mid-turn.
func testHarnessFake(t *testing.T, e *csEnv, box agSandbox, stdio bool, slow time.Duration) {
	d := e.d
	turn := 60 * time.Second * slow
	c := e.target().As(t, agTile).Verified("alice")
	var run struct{ ID int64 }
	e.ag(t, "alice", "POST", "/ask", map[string]any{"text": "perm", "harness": map[string]string{"provider": "fake"},
		"sandbox": map[string]string{"ref": box.Ref}}, 200, &run)
	id := run.ID

	// signed out: the prompt parks on the sign-in, the prompt held
	v := e.waitView(t, "alice", id, "the sign-in park", turn, func(v hView) bool {
		return v.pendingKind() == "login" || v.summary().State == "failed"
	})
	s := v.summary()
	if s.State != "login" || s.Login == nil || s.Login.Command == "" {
		t.Fatalf("the fake's sign-in: %s, login %+v", v.brief(), s.Login)
	}
	kinds := map[string]string{}
	for _, m := range s.Login.Methods {
		kinds[m.ID] = m.Kind
	}
	if kinds["fake-login"] != "terminal" || kinds["fake-api-key"] != "api-key" || kinds["fake-device"] != "device-code" {
		t.Errorf("the fake's sign-in methods: %+v", s.Login.Methods)
	}
	x := harnessExec(t, c, box.ID, id, s.Gen)
	if x == nil || x.State != "running" {
		t.Fatalf("the fake's exec at the manager: %+v", x)
	}
	if stdio && !x.Split {
		t.Errorf("the fake's exec isn't split: the pipe didn't take the stdio socket (%+v)", *x)
	}
	gen1 := s.Gen

	// its terminal sign-in, relayed by the agent: page → xbind's proxy → the
	// agent's backend → the gateway → coding-sandbox → the runtime's pty
	t.Run("login terminal", func(t *testing.T) {
		conn, r, err := d.Dial(t, e.agentPath(t, fmt.Sprintf("/runs/%d/harness/terminal?login=1&rows=24&cols=80", id), "alice"))
		if err != nil {
			t.Fatalf("the sign-in terminal: %v (%d %s)", err, r.Status, r)
		}
		defer conn.Close()
		if sess := readSession(t, conn); sess.ID == "" || sess.Sandbox != box.ID {
			t.Errorf("the relayed session frame: %+v", sess)
		}
		readUntil(t, conn, "paste the code", 30*time.Second*slow)
		send(t, conn, "fake-code\r")
		readUntil(t, conn, "Signed in.", 30*time.Second*slow)
		if code := readExit(t, conn, 30*time.Second*slow); code == nil || *code != 0 {
			t.Errorf("the sign-in's exit: %v", code)
		}
	})

	// Signed in? Retry: a fresh adapter (the next generation: it reads the
	// new credentials) takes the held prompt and asks to run its tool
	e.ag(t, "alice", "POST", fmt.Sprintf("/runs/%d/resume", id), map[string]any{}, 200, nil)
	v = e.waitView(t, "alice", id, "the permission park", turn, func(v hView) bool {
		return v.pendingKind() == "approval" || v.summary().State == "failed"
	})
	var perm struct {
		Options []struct{ OptionID, Kind string }
		Tool    struct{ Title, Kind string }
	}
	if v.Run.PendingState == nil || json.Unmarshal(v.Run.PendingState.Harness, &perm) != nil || len(perm.Options) != 3 || perm.Tool.Title != "run ls" {
		t.Fatalf("the permission park: %s %s", v.brief(), v.Run.PendingState.Harness)
	}
	gen := v.summary().Gen
	if gen != gen1+1 {
		t.Errorf("after Retry: generation %d (want %d, a fresh adapter)", gen, gen1+1)
	}
	if x := harnessExec(t, c, box.ID, id, gen1); x != nil && x.State == "running" {
		t.Errorf("the signed-out adapter still runs after Retry: %+v", *x)
	}
	x = harnessExec(t, c, box.ID, id, gen)
	if x == nil || x.State != "running" || (stdio && !x.Split) {
		t.Fatalf("the fresh adapter's exec: %+v", x)
	}
	execID := x.ID
	e.ag(t, "alice", "POST", fmt.Sprintf("/runs/%d/approve", id), map[string]any{"approve": true, "park": v.Run.PendingState.Park}, 200, nil)
	v = e.waitView(t, "alice", id, "the answer", turn, func(v hView) bool {
		return v.Run.Status != "running" && v.pendingKind() == "" && strings.Contains(v.lastAssistant(), "listed")
	})
	if s := v.summary(); v.Run.Status != "idle" || s.State != "ready" || s.Gen != gen {
		t.Errorf("after the turn: %s (want idle, ready, generation %d)", v.brief(), gen)
	}
	var tool map[string]any
	for _, m := range v.Messages {
		if m.Role == "tool" && m.ACP != nil {
			tool = m.ACP
		}
	}
	if tool == nil || tool["kind"] != "execute" || tool["status"] != "completed" {
		t.Errorf("the tool row's acp: %v", tool)
	}

	// a shell on the run's relay and on the sandbox's, at the sandbox's cwd
	t.Run("shell relays", func(t *testing.T) {
		for _, sub := range []string{fmt.Sprintf("/runs/%d/harness/terminal?rows=24&cols=80", id),
			"/sandboxes/" + url.PathEscape(box.Ref) + "/terminal?cwd=%2Fwork&rows=24&cols=80"} {
			conn, r, err := d.Dial(t, e.agentPath(t, sub, "alice"))
			if err != nil {
				t.Fatalf("%s: %v (%d %s)", sub, err, r.Status, r)
			}
			readSession(t, conn)
			// a tty exec's defaults (the runtime's TERM, COLORTERM, LANG;
			// coding-sandbox's IS_SANDBOX, D-harness §5.1)
			send(t, conn, `echo "relayed-$((6*7)):$PWD:$TERM:$COLORTERM:$LANG:$IS_SANDBOX"`+"\r")
			readUntil(t, conn, "relayed-42:/work:xterm-256color:truecolor:C.UTF-8:1", 30*time.Second*slow)
			send(t, conn, "exit\r")
			if code := readExit(t, conn, 30*time.Second*slow); code == nil || *code != 0 {
				t.Errorf("%s: the shell's exit: %v", sub, code)
			}
			conn.Close()
		}
		// bob may not use alice's private sandbox: refused before the upgrade
		if conn, r, err := d.Dial(t, e.agentPath(t, fmt.Sprintf("/runs/%d/harness/terminal", id), "bob")); err == nil {
			conn.Close()
			t.Error("bob opened a terminal on alice's coding agent")
		} else if r.Status != 403 && r.Status != 404 {
			t.Errorf("bob's terminal: %d %s", r.Status, r)
		}
	})

	t.Run("handoff", func(t *testing.T) { testHarnessHandoff(t, e, box, id, gen, execID, slow) })
}

// testHarnessHandoff: a turn that waits (the fake's `stall`) while the
// agent's backend is redeployed (a changed file under _backend: xbind's
// live reload builds it and swaps the processes); the new process attaches
// to the same adapter (no new generation, the same exec), and its
// interrupt ends the turn the old one started; the next turn is answered by
// the same adapter.
func testHarnessHandoff(t *testing.T, e *csEnv, box agSandbox, id int64, gen int, execID string, slow time.Duration) {
	d := e.d
	turn := 60 * time.Second * slow
	c := e.target().As(t, agTile).Verified("alice")
	pid0 := backendPID(t, d, agTile)
	e.ag(t, "alice", "POST", fmt.Sprintf("/runs/%d/message", id), map[string]string{"text": "stall"}, 200, nil)
	e.waitView(t, "alice", id, "the stalling turn", turn, func(v hView) bool {
		return v.Run.Status == "running" && v.summary().State == "working"
	})
	time.Sleep(time.Second) // the chunk "stalling" read (a draft)

	marker := fmt.Sprintf("package main\n\n// redeployed by test/isolated TestHarnessLive at %d\n", time.Now().UnixNano())
	if err := d.WriteFiles(agTile, map[string]string{"_backend/zz_a11_redeploy.go": marker}); err != nil {
		t.Fatal(err)
	}
	xbindtest.Eventually(t, 8*time.Minute, "the agent's backend redeployed", func() (bool, string) {
		p := backendPID(t, d, agTile)
		return p != 0 && p != pid0, fmt.Sprintf("pid %d (was %d)", p, pid0)
	})
	t.Logf("the agent's backend: pid %d → %d", pid0, backendPID(t, d, agTile))

	// the successor holds the turn the predecessor started, on the same adapter
	v := e.waitView(t, "alice", id, "the successor holds the turn", turn, func(v hView) bool {
		s := v.summary()
		return v.Run.Status == "running" && s.State == "working" && s.Activity != nil
	})
	if s := v.summary(); s.Gen != gen {
		t.Errorf("after the handoff: generation %d (want %d: the same adapter, attached)", s.Gen, gen)
	}
	if x := harnessExec(t, c, box.ID, id, gen); x == nil || x.ID != execID || x.State != "running" {
		t.Errorf("the adapter's exec after the handoff: %+v (want %s running)", x, execID)
	}
	if x := harnessExec(t, c, box.ID, id, gen+1); x != nil {
		t.Errorf("the successor started another adapter: %+v", *x)
	}

	// its interrupt reaches the adapter (session/cancel) and ends the turn
	e.ag(t, "alice", "POST", fmt.Sprintf("/runs/%d/interrupt", id), map[string]any{}, 200, nil)
	e.waitView(t, "alice", id, "the interrupted turn", turn, func(v hView) bool {
		return v.Run.Status != "running" && v.summary().State == "ready"
	})
	// and the next turn is the same adapter's
	e.ag(t, "alice", "POST", fmt.Sprintf("/runs/%d/message", id), map[string]string{"text": "slow"}, 200, nil)
	v = e.waitView(t, "alice", id, "the next turn's answer", turn, func(v hView) bool {
		return v.Run.Status != "running" && strings.Contains(v.lastAssistant(), "tick 9")
	})
	if s := v.summary(); v.Run.Status != "idle" || s.Gen != gen {
		t.Errorf("the turn after the handoff: %s (want idle on generation %d)", v.brief(), gen)
	}
}

// backendPID is the pid of tile's running backend (GET /api/xbin/runtime,
// the owner's): 0 when none runs.
func backendPID(t *testing.T, d *xbindtest.Daemon, tile string) int {
	t.Helper()
	var rt struct {
		Backends []struct {
			Path, State string
			PID         int
		}
	}
	d.Must(t, "GET", "/api/xbin/runtime", nil, 200).Decode(t, &rt)
	for _, b := range rt.Backends {
		if b.Path == tile && b.PID != 0 {
			return b.PID
		}
	}
	return 0
}

// --- an adapter by hand ------------------------------------------------------

// envOf is maps merged, later ones winning.
func envOf(ms ...map[string]string) map[string]string {
	out := map[string]string{}
	for _, m := range ms {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}

// acpReport is what an adapter answered to initialize and session/new.
type acpReport struct {
	Init, New       json.RawMessage
	InitErr, NewErr string
	SetMode         string   // session/set_mode's answer, asked of an agent whose modes are a config option
	Stray           []string // lines on stdout that aren't JSON-RPC
	Stderr          string
	took            time.Duration
}

func (p acpReport) String() string {
	var b strings.Builder
	if p.Init == nil {
		fmt.Fprintf(&b, "initialize FAILED: %s", p.InitErr)
	} else {
		var in struct {
			ProtocolVersion   int
			AgentInfo         map[string]any
			AgentCapabilities map[string]any
			AuthMethods       []map[string]any
		}
		_ = json.Unmarshal(p.Init, &in)
		var ams []string
		for _, m := range in.AuthMethods {
			s := fmt.Sprintf("%v (%v)", m["id"], m["name"])
			if ty, ok := m["type"]; ok {
				s += fmt.Sprintf(" type %v", ty)
			}
			if meta, ok := m["_meta"].(map[string]any); ok {
				var ks []string
				for k, v := range meta {
					vb, _ := json.Marshal(v)
					ks = append(ks, k+"="+cut(string(vb), 160))
				}
				slices.Sort(ks)
				s += " _meta{" + strings.Join(ks, "; ") + "}"
			}
			if args, ok := m["args"]; ok {
				s += fmt.Sprintf(" args %v", args)
			}
			ams = append(ams, s)
		}
		fmt.Fprintf(&b, "initialize ok (protocol %d, agent %v, loadSession %v, auth methods: [%s])", in.ProtocolVersion, in.AgentInfo,
			in.AgentCapabilities["loadSession"], strings.Join(ams, ", "))
		if meta, ok := in.AgentCapabilities["_meta"]; ok {
			mb, _ := json.Marshal(meta)
			fmt.Fprintf(&b, " _meta %s", cut(string(mb), 300))
		}
		switch {
		case p.New != nil:
			var ns struct {
				SessionID string
				Modes     struct {
					CurrentModeID  string
					AvailableModes []struct{ ID string }
				}
				ConfigOptions []struct {
					ID, Category, CurrentValue string
					Options                    []struct{ Value string }
				}
				Models struct {
					CurrentModelID string
				}
			}
			_ = json.Unmarshal(p.New, &ns)
			var modes, opts []string
			for _, m := range ns.Modes.AvailableModes {
				modes = append(modes, m.ID)
			}
			for _, o := range ns.ConfigOptions {
				s := fmt.Sprintf("%s(%s)=%s", o.ID, o.Category, o.CurrentValue)
				if o.Category == "mode" {
					var vs []string
					for _, v := range o.Options {
						vs = append(vs, v.Value)
					}
					s += fmt.Sprint(vs)
				}
				opts = append(opts, s)
			}
			fmt.Fprintf(&b, "; session/new ok (session %q, mode %q of %v, options %v, model %q)", ns.SessionID, ns.Modes.CurrentModeID, modes, opts, ns.Models.CurrentModelID)
			if p.SetMode != "" {
				fmt.Fprintf(&b, "; session/set_mode (its modes are a config option): %s", p.SetMode)
			}
		case p.NewErr != "":
			fmt.Fprintf(&b, "; session/new FAILED: %s", p.NewErr)
		}
	}
	if len(p.Stray) > 0 {
		fmt.Fprintf(&b, "; other stdout %q", p.Stray)
	}
	if p.Stderr != "" {
		fmt.Fprintf(&b, "; stderr %q", cut(p.Stderr, 400))
	}
	fmt.Fprintf(&b, " (%s)", p.took.Round(100*time.Millisecond))
	return b.String()
}

// acpHandshake starts argv in sandbox box through the contract's exec
// routes (stdin: true; split when the manager has stdio), writes
// initialize — the capabilities the agent's client advertises — then
// session/new, each as a stdin POST, and reads the answers by the output
// long-poll (base64). The exec is deleted after.
func acpHandshake(t *testing.T, c sandboxcontract.Caller, box string, argv []string, env map[string]string, split bool, wait time.Duration) acpReport {
	t.Helper()
	t0 := time.Now()
	var x sandboxcontract.Exec
	c.Call("POST", "/sandboxes/"+box+"/execs", map[string]any{"argv": argv, "stdin": true, "split": split, "cwd": "/work",
		"env": envOf(env, map[string]string{"IS_SANDBOX": "1", "NO_COLOR": "1"}), "label": "a11 handshake " + argv[0]}, 201, &x)
	defer c.Call("DELETE", "/sandboxes/"+box+"/execs/"+x.ID, nil, 204, nil)
	var p acpReport
	var since int64
	var buf []byte
	exited := false
	frame := func(id int, method string, params any) {
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
		c.Call("POST", "/sandboxes/"+box+"/execs/"+x.ID+"/stdin", append(b, '\n'), 204, nil)
	}
	// answer is the response to request id: its result, or its error's words
	answer := func(id int) (json.RawMessage, string) {
		deadline := time.Now().Add(wait)
		for {
			for {
				i := strings.IndexByte(string(buf), '\n')
				if i < 0 {
					break
				}
				line := strings.TrimSpace(string(buf[:i]))
				buf = buf[i+1:]
				var m struct {
					ID     *int
					Method string
					Result json.RawMessage
					Error  *struct {
						Code    int
						Message string
						Data    json.RawMessage
					}
				}
				if line == "" {
					continue
				}
				if json.Unmarshal([]byte(line), &m) != nil {
					if len(p.Stray) < 5 {
						p.Stray = append(p.Stray, cut(line, 200))
					}
					continue
				}
				if m.ID == nil || *m.ID != id || m.Method != "" {
					continue // a notification, or a request of the agent's (none before a prompt)
				}
				if m.Error != nil {
					return nil, fmt.Sprintf("%d %s %s", m.Error.Code, m.Error.Message, cut(string(m.Error.Data), 300))
				}
				return m.Result, ""
			}
			if exited {
				return nil, "the adapter exited"
			}
			if time.Now().After(deadline) {
				return nil, fmt.Sprintf("no answer in %s", wait)
			}
			var ch sandboxcontract.Chunk
			c.Call("GET", fmt.Sprintf("/sandboxes/%s/execs/%s/output?since=%d&waitMs=5000&encoding=base64", box, x.ID, since), nil, 200, &ch)
			data, err := base64.StdEncoding.DecodeString(ch.Data)
			if err != nil {
				t.Fatalf("the output's base64: %v", err)
			}
			buf, since = append(buf, data...), ch.End
			exited = ch.State != "running" && ch.End >= ch.Total
		}
	}
	frame(1, "initialize", map[string]any{"protocolVersion": 1, "clientInfo": map[string]string{"name": "xbin-a11", "version": "0"},
		"clientCapabilities": map[string]any{"fs": map[string]bool{"readTextFile": false, "writeTextFile": false}, "terminal": false,
			"elicitation": map[string]any{"form": map[string]any{}, "url": map[string]any{}},
			"_meta":       map[string]bool{"terminal_output": true, "terminal_output_delta": true, "subagent-transcript": true, "terminal-auth": true}}})
	p.Init, p.InitErr = answer(1)
	if p.Init != nil {
		frame(2, "session/new", map[string]any{"cwd": "/work", "mcpServers": []any{}})
		p.New, p.NewErr = answer(2)
	}
	// an agent whose modes are only a config option (category mode): does
	// it take session/set_mode too?
	var ns struct {
		SessionID     string
		Modes         *struct{ AvailableModes []any }
		ConfigOptions []struct {
			Category, CurrentValue string
			Options                []struct{ Value string }
		}
	}
	if p.New != nil && json.Unmarshal(p.New, &ns) == nil && (ns.Modes == nil || len(ns.Modes.AvailableModes) == 0) {
		for _, o := range ns.ConfigOptions {
			if o.Category != "mode" {
				continue
			}
			for _, v := range o.Options {
				if v.Value != o.CurrentValue {
					frame(3, "session/set_mode", map[string]any{"sessionId": ns.SessionID, "modeId": v.Value})
					res, err := answer(3)
					p.SetMode = fmt.Sprintf("%s → %s%s", v.Value, cut(string(res), 200), err)
					break
				}
			}
			break
		}
	}
	p.took = time.Since(t0)
	if split {
		var ch sandboxcontract.Chunk
		c.Call("GET", fmt.Sprintf("/sandboxes/%s/execs/%s/output?since=0&stream=stderr&encoding=base64", box, x.ID), nil, 200, &ch)
		b, _ := base64.StdEncoding.DecodeString(ch.Data)
		p.Stderr = strings.TrimSpace(string(b))
	}
	return p
}
