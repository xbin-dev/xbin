//go:build linux && integration

package isolated

// consumers_test.go — coding-sandbox's consumers, live (WP-21 part C): the
// agent template (D115, D116) with hack/fakeopenai as its model through
// llm-gw, and sandbox-terminal (D121), both bound to the builtin manager
// (setupCS) on xbind's runtime. Everything goes through xbind's proxy as
// their pages call it: a person's calls carry the page's frame token their
// session minted (the verified person), and the terminals are gorilla
// clients dialling the manager's tty route as <bx-terminal src> does.
// sandbox-terminal's SSH is a stream expose bound on 127.0.0.1 only, reached
// with golang.org/x/crypto/ssh's client.
//
// They act as people, so they need owner auth on (a --no-auth xbind has
// none: they skip). Against a remote xbind (XBIN_E2E_URL: the QA box's test
// instance deployed with SBXTEST_AUTH=1) the tiles are named per run,
// fakeopenai runs on that host's loopback (uploaded through XBIN_E2E_SH),
// and the SSH expose binds a loopback port there, reached through
// XBIN_E2E_FORWARD.

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/xbin-dev/xbin/sdk/sandboxcontract"
	"github.com/xbin-dev/xbin/test/xbindtest"
)

// The consumers: the agent, its model gateway and the people's terminals
// (named per run on a remote xbind: setupConsumers).
var (
	agTile = "apps/agent"
	gwTile = "apps/llm-gw"
	stTile = "apps/sandbox-terminal"
)

// TestCodingSandboxConsumers drives the agent and sandbox-terminal against
// coding-sandbox with namespace sandboxes (range mode where the host
// delegates a sub-uid range).
func TestCodingSandboxConsumers(t *testing.T) {
	e, _ := setupConsumers(t, false)
	runConsumers(t, e, "namespace", 1)
}

// TestCodingSandboxConsumersVM is the same with VM sandboxes (the manager's
// `auto` mode where the runtime offers VMs).
func TestCodingSandboxConsumersVM(t *testing.T) {
	e, accel := setupConsumers(t, true)
	slow := time.Duration(1)
	if accel == "emulate" {
		slow = 6
	}
	runConsumers(t, e, "vm", slow)
}

// setupConsumers is setupCS, plus fakeopenai on the host's loopback, llm-gw
// reaching it (its net: host) with one backend "fake", and the agent
// template instantiated as apps/agent: its `llm` slot bound to llm-gw, its
// `sandboxes` slot to the manager (the binding is the grant), no web, and
// fake/fake-chat as its model.
func setupConsumers(t *testing.T, vm bool) (*csEnv, string) {
	t.Helper()
	e, accel := setupCS(t, vm)
	if !e.people {
		t.Skip("no people here (--no-auth): the consumers act as verified people (the QA box's test instance: deploy it with SBXTEST_AUTH=1)")
	}
	d := e.d
	agTile, gwTile, stTile = "apps/agent", "apps/llm-gw", "apps/sandbox-terminal"
	if d.IsRemote() {
		run := strings.TrimPrefix(csTile, "apps/cs-")
		agTile, gwTile, stTile = "apps/agent-"+run, "apps/llm-gw-"+run, "apps/sandbox-terminal-"+run
	}
	fake := startFakeOpenAI(t, d)

	ok := func(method, path string, body any) xbindtest.Resp {
		t.Helper()
		r := d.Call(t, method, path, body)
		if r.Status/100 != 2 {
			t.Fatalf("%s %s: %d %s", method, path, r.Status, r)
		}
		return r
	}
	ok("POST", "/api/xbin/builtins/import", map[string]string{"name": "llm-gw", "path": gwTile})
	d.WaitComponent(t, gwTile)
	d.Bind(t, gwTile, "net", "host")
	ok("PUT", "/api/xbin/vault/"+gwTile+"/api-token-fake", map[string]string{"value": "sk-fake"})
	ok("POST", "/api/xbin/templates/new", map[string]string{"source": "agent", "path": agTile})
	d.WaitComponent(t, agTile)
	ok("POST", "/api/xbin/bindings", map[string]any{"component": agTile, "slot": "llm", "providers": []string{gwTile}})
	ok("POST", "/api/xbin/bindings", map[string]any{"component": agTile, "slot": "sandboxes", "providers": []string{csTile}})
	d.Bind(t, agTile, "net", "none")

	xbindtest.Eventually(t, 5*time.Minute, "llm-gw's backend answers", func() (bool, string) {
		r := d.Call(t, "GET", "/api/"+gwTile+"/config", nil)
		return r.Status == 200 && strings.Contains(string(r.Body), "backends"), fmt.Sprint(r.Status, " ", r)
	})
	ok("PUT", "/api/"+gwTile+"/config/backend", map[string]string{"name": "fake", "baseURL": "http://" + fake})
	var cfg map[string]any
	xbindtest.Eventually(t, 8*time.Minute, "the agent's backend answers", func() (bool, string) {
		r := d.Call(t, "GET", "/api/"+agTile+"/config", nil)
		cfg = nil
		if r.Status == 200 {
			_ = json.Unmarshal(r.Body, &cfg)
		}
		return cfg["system"] != nil, fmt.Sprint(r.Status, " ", r)
	})
	cfg["model"] = "fake/fake-chat"
	ok("PUT", "/api/"+agTile+"/config", cfg)
	return e, accel
}

// startFakeOpenAI builds hack/fakeopenai and runs it on a free loopback
// port of xbind's host for the test: its address there. It is killed when
// the test ends.
func startFakeOpenAI(t *testing.T, d *xbindtest.Daemon) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "fakeopenai")
	build := exec.Command("go", "build", "-o", bin, "./hack/fakeopenai")
	build.Dir = d.A.Repo
	if d.IsRemote() {
		build.Env = append(os.Environ(), "GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0")
	}
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building fakeopenai: %v\n%s", err, out)
	}
	if d.IsRemote() {
		return startRemoteFakeOpenAI(t, d, bin)
	}
	addr := freeLoopback(t)
	logf, err := os.Create(filepath.Join(dir, "fakeopenai.log"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "-addr", addr)
	cmd.Stdout, cmd.Stderr = logf, logf
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		logf.Close()
		if t.Failed() {
			b, _ := os.ReadFile(logf.Name())
			t.Logf("fakeopenai's log:\n%s", cut(string(b), 6000))
		}
	})
	xbindtest.Eventually(t, 20*time.Second, "fakeopenai answers", func() (bool, string) {
		r, err := http.Get("http://" + addr + "/debug/requests")
		if err != nil {
			return false, err.Error()
		}
		r.Body.Close()
		return r.StatusCode == 200, r.Status
	})
	return addr
}

// startRemoteFakeOpenAI uploads bin to a temporary directory on a remote
// xbind's host and starts it there on a free 127.0.0.1 port (never a
// public one): its address there. When the test ends it is killed by its
// PID and the directory removed.
func startRemoteFakeOpenAI(t *testing.T, d *xbindtest.Daemon, bin string) string {
	t.Helper()
	b, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	out, err := d.HostSh("set -e\nd=$(mktemp -d /tmp/xbindtest-fakeopenai.XXXXXX)\nbase64 -d > \"$d/fakeopenai\" <<'EOF'\n" +
		base64.StdEncoding.EncodeToString(b) + "\nEOF\nchmod 0700 \"$d/fakeopenai\"\n" + `p=
for c in $(shuf -i 20000-29999 -n 50); do
	ss -Hltn "sport = :$c" | grep -q . || { p=$c; break; }
done
[ -n "$p" ]
setsid "$d/fakeopenai" -addr "127.0.0.1:$p" > "$d/log" 2>&1 < /dev/null &
echo "$d $! 127.0.0.1:$p"
`)
	f := strings.Fields(out)
	if err != nil || len(f) != 3 {
		t.Fatalf("starting fakeopenai on xbind's host: %q %v", out, err)
	}
	dir, pid, addr := f[0], f[1], f[2]
	if _, err := strconv.Atoi(pid); err != nil || !strings.HasPrefix(dir, "/tmp/xbindtest-fakeopenai.") {
		t.Fatalf("starting fakeopenai on xbind's host: %q", out)
	}
	t.Cleanup(func() {
		if t.Failed() {
			log, _ := d.HostSh("tail -c 6000 '" + dir + "/log'\n")
			t.Logf("fakeopenai's log (on xbind's host):\n%s", log)
		}
		if out, err := d.HostSh("kill " + pid + "; rm -rf '" + dir + "'\n"); err != nil {
			t.Errorf("stopping fakeopenai (pid %s on xbind's host): %v %s", pid, err, out)
		}
	})
	xbindtest.Eventually(t, 30*time.Second, "fakeopenai answers on xbind's host", func() (bool, string) {
		out, err := d.HostSh("curl -fsS -o /dev/null -w '%{http_code}' http://" + addr + "/debug/requests\n")
		return err == nil && out == "200", fmt.Sprint(out, err)
	})
	return addr
}

// freeLoopback is a free 127.0.0.1 port, as host:port.
func freeLoopback(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

func cut(s string, n int) string {
	if len(s) > n {
		return "…" + s[len(s)-n:]
	}
	return s
}

// agSandbox is a sandbox as the agent's routes answer it.
type agSandbox struct {
	sandboxcontract.Sandbox
	Ref      string
	Provider string
	Mine     bool
	CanUse   bool
	BoundTo  []int64
}

// agRun is GET /runs/{id}, as far as the tests read it.
type agRun struct {
	Run struct {
		ID      int64
		Status  string
		Pending string
	}
	Messages []struct{ Role, Content string }
	Config   struct {
		Sandbox  *struct{ Ref, Cwd, By string }
		Attached []struct{ Ref string }
	}
}

// pending is the run's parked ask, if any.
func (r agRun) pending() (p struct {
	Kind, Grant, GrantAsk, Park string
}) {
	_ = json.Unmarshal([]byte(r.Run.Pending), &p)
	return p
}

// answers counts the run's assistant messages holding want; last is the
// latest of them.
func (r agRun) answers(want string) (n int, last string) {
	for _, m := range r.Messages {
		if m.Role == "assistant" && strings.Contains(m.Content, want) {
			n, last = n+1, m.Content
		}
	}
	return n, last
}

// call sends one request to a tile's routes from its page as person (their
// session minted the frame token): the answer, failing the test unless
// its status is want (0: any).
func (e *csEnv) call(t *testing.T, tile, person, method, path string, body any, want int, out any) xbindtest.Resp {
	t.Helper()
	r := e.d.Call(t, method, "/api/"+tile+path, body, xbindtest.FrameHeader(e.pageTok(t, tile, person)))
	if want != 0 && r.Status != want {
		t.Fatalf("%s as %s: %s %s: %d %s (want %d)", tile, person, method, path, r.Status, r, want)
	}
	if out != nil {
		r.Decode(t, out)
	}
	return r
}

func (e *csEnv) ag(t *testing.T, person, method, path string, body any, want int, out any) xbindtest.Resp {
	t.Helper()
	return e.call(t, agTile, person, method, path, body, want, out)
}

func (e *csEnv) run(t *testing.T, person string, id int64) agRun {
	t.Helper()
	var r agRun
	e.ag(t, person, "GET", fmt.Sprintf("/runs/%d", id), nil, 200, &r)
	return r
}

// waitAnswer waits for the run to hold more than n0 answers with want and
// to settle: the latest such answer.
func (e *csEnv) waitAnswer(t *testing.T, person string, id int64, n0 int, want string, timeout time.Duration) string {
	t.Helper()
	var last string
	xbindtest.Eventually(t, timeout, fmt.Sprintf("run %d answers %q", id, want), func() (bool, string) {
		r := e.run(t, person, id)
		n, l := r.answers(want)
		last = l
		msgs := r.Messages
		if len(msgs) > 4 {
			msgs = msgs[len(msgs)-4:]
		}
		return n > n0 && r.Run.Status != "running", fmt.Sprintf("status %s, pending %s, latest %+v", r.Run.Status, r.Run.Pending, msgs)
	})
	return last
}

// ask starts a conversation as person (POST /ask): its id and the answer
// holding want.
func (e *csEnv) ask(t *testing.T, person string, body map[string]any, want string, timeout time.Duration) (int64, string) {
	t.Helper()
	var run struct{ ID int64 }
	e.ag(t, person, "POST", "/ask", body, 200, &run)
	if run.ID == 0 {
		t.Fatalf("POST /ask %v: no run", body)
	}
	return run.ID, e.waitAnswer(t, person, run.ID, 0, want, timeout)
}

// say sends a message into a conversation as person: the new answer
// holding want.
func (e *csEnv) say(t *testing.T, person string, id int64, text, want string, timeout time.Duration) string {
	t.Helper()
	n0, _ := e.run(t, person, id).answers(want)
	e.ag(t, person, "POST", fmt.Sprintf("/runs/%d/message", id), map[string]string{"text": text}, 200, nil)
	return e.waitAnswer(t, person, id, n0, want, timeout)
}

func runConsumers(t *testing.T, e *csEnv, mode string, slow time.Duration) {
	var box, team agSandbox
	t.Run("agent", func(t *testing.T) { box, team = testAgent(t, e, mode, slow) })
	if box.ID == "" {
		t.Fatal("the agent part made no sandbox to share")
	}
	t.Run("sandbox-terminal", func(t *testing.T) { testSandboxTerminal(t, e, box, team, slow) })
}

// testAgent: a coding-class conversation in a sandbox the agent made at the
// manager — bash, write, edit, a command that goes on as a job; its
// page's Open terminal; a team conversation's sandbox another person works
// in; sandbox_create through the conversation owner's grant. It returns
// alice's private sandbox and the team's.
func testAgent(t *testing.T, e *csEnv, mode string, slow time.Duration) (box, team agSandbox) {
	turn := 60 * time.Second * slow
	// the agent lists the bound manager and what it offers
	var list struct {
		Sandboxes []agSandbox
		Managers  []struct {
			Provider, Title, Error string
			OK                     bool
			Caps, Egress           []string
		}
	}
	e.ag(t, "alice", "GET", "/sandboxes?fresh=1", nil, 200, &list)
	if len(list.Managers) != 1 || list.Managers[0].Provider != csTile || !list.Managers[0].OK || !strings.Contains(strings.Join(list.Managers[0].Caps, " "), "tty") {
		t.Fatalf("the agent's managers: %+v", list.Managers)
	}

	// a private sandbox made in the agent (its Sandboxes dialog's New
	// sandbox): owned by alice at the manager, in the runtime's mode
	e.ag(t, "alice", "POST", "/sandboxes", map[string]any{"name": "box-a", "egress": "none"}, 201, &box)
	if box.Ref != csTile+"|"+box.ID || box.Name != "box-a" || !box.Mine {
		t.Fatalf("the new sandbox: %+v", box)
	}
	asAlice := e.target().As(t, agTile).Verified("alice")
	if got := asAlice.Get(box.ID); got.Isolation != mode || got.Owner.User != "alice" || got.Visibility != "private" {
		t.Errorf("box-a at the manager: %+v", got)
	}

	// a coding conversation that starts in it
	id, ran := e.ask(t, "alice", map[string]any{"text": "sandbox pwd", "class": "coding", "sandbox": map[string]string{"ref": box.Ref}}, "Ran: ", turn)
	if ran != "Ran: /work" {
		t.Errorf(`"sandbox pwd": %q (want the sandbox's workdir)`, ran)
	}
	if r := e.run(t, "alice", id); r.Config.Sandbox == nil || r.Config.Sandbox.Ref != box.Ref || r.Config.Sandbox.By != "alice" {
		t.Errorf("the conversation's binding: %+v", r.Config)
	}
	var execs struct{ Execs []sandboxcontract.Exec }
	asAlice.Call("GET", "/sandboxes/"+box.ID+"/execs", nil, 200, &execs)
	if len(execs.Execs) == 0 || !strings.HasPrefix(execs.Execs[0].ClientID, fmt.Sprintf("agent:%d:", id)) {
		t.Errorf("the manager's execs of box-a: %+v (want the agent's bash, its client id agent:<conversation>:<call>)", execs.Execs)
	}

	// write, edit, and what the file says now — in the sandbox itself
	if a := e.say(t, "alice", id, "sandbox write", "Wrote it.", turn); a != "Wrote it." {
		t.Errorf(`"sandbox write": %q`, a)
	}
	if got := asAlice.Read(box.ID, "/work/hello.txt"); got != "hi from the agent\n" {
		t.Errorf("hello.txt after write: %q", got)
	}
	if a := e.say(t, "alice", id, "sandbox edit", "Edited it.", turn); a != "Edited it." {
		t.Errorf(`"sandbox edit": %q`, a)
	}
	if a := e.say(t, "alice", id, "sandbox cat", "Ran: ", turn); a != "Ran: hello from the agent" {
		t.Errorf(`"sandbox cat": %q`, a)
	}

	// a command that outlives its timeout goes on as a job, followed to its end
	if a := e.say(t, "alice", id, "sandbox long", "Job: ", 2*turn); a != "Job: slow done" {
		t.Errorf(`"sandbox long": %q`, a)
	}

	t.Run("open terminal", func(t *testing.T) { testAgentTerminal(t, e, box, slow) })

	// a team conversation: its sandbox, made for it, is the team's; bob
	// (a participant) works in it; alice's private one stays hers
	teamID, q := e.ask(t, "alice", map[string]any{"text": "quick, for the team", "class": "coding"}, "Quick answer.", turn)
	if q != "Quick answer." {
		t.Errorf("the team conversation's first answer: %q", q)
	}
	e.ag(t, "alice", "PATCH", fmt.Sprintf("/runs/%d", teamID), map[string]string{"visibility": "team", "teamRole": "participant"}, 200, nil)
	e.ag(t, "alice", "POST", "/sandboxes", map[string]any{"name": "team-box", "conversation": teamID}, 201, &team)
	if team.Visibility != "team" || len(team.BoundTo) != 1 || team.BoundTo[0] != teamID {
		t.Errorf("the team conversation's sandbox: %+v", team)
	}
	e.ag(t, "bob", "GET", "/sandboxes?fresh=1", nil, 200, &list)
	var bobSees []string
	for _, s := range list.Sandboxes {
		bobSees = append(bobSees, s.Name)
	}
	if !strings.Contains(" "+strings.Join(bobSees, " ")+" ", " team-box ") || strings.Contains(" "+strings.Join(bobSees, " ")+" ", " box-a ") {
		t.Errorf("bob's sandboxes in the agent: %v (want team-box, not alice's private box-a)", bobSees)
	}
	if a := e.say(t, "bob", teamID, "sandbox pwd, bob here", "Ran: ", turn); a != "Ran: /work" {
		t.Errorf("bob in the team sandbox: %q", a)
	}
	asAlice.Call("GET", "/sandboxes/"+team.ID+"/execs", nil, 200, &execs)
	if len(execs.Execs) == 0 {
		t.Errorf("bob's command didn't run in team-box: %+v", execs)
	}
	e.ag(t, "bob", "PATCH", fmt.Sprintf("/runs/%d", teamID), map[string]any{"sandbox": map[string]string{"ref": box.Ref}}, 0, nil)
	if r := e.run(t, "alice", teamID); r.Config.Sandbox == nil || r.Config.Sandbox.Ref != team.Ref {
		t.Errorf("bob bound alice's private sandbox to the team conversation: %+v", r.Config.Sandbox)
	}

	// sandbox_create: bob asks; it parks for the owner's grant — bob may
	// only deny, alice allows it once, and the agent makes it
	n0, _ := e.run(t, "alice", teamID).answers("Created: ")
	e.ag(t, "bob", "POST", fmt.Sprintf("/runs/%d/message", teamID), map[string]string{"text": "new sandbox please"}, 200, nil)
	var park struct{ Kind, Grant, GrantAsk, Park string }
	xbindtest.Eventually(t, turn, "sandbox_create parks for the owner's grant", func() (bool, string) {
		r := e.run(t, "alice", teamID)
		park = r.pending()
		return r.Run.Status == "waiting_input" && park.Grant == "sandboxes", fmt.Sprintf("%s %s", r.Run.Status, r.Run.Pending)
	})
	if !strings.Contains(park.GrantAsk, `"scratch"`) || !strings.Contains(park.GrantAsk, "team") {
		t.Errorf("the grant card: %q (want the sandbox named, and that it will be the team's)", park.GrantAsk)
	}
	e.ag(t, "bob", "POST", fmt.Sprintf("/runs/%d/approve", teamID), map[string]any{"approve": true, "park": park.Park}, 403, nil)
	e.ag(t, "alice", "POST", fmt.Sprintf("/runs/%d/approve", teamID), map[string]any{"approve": true, "grant": "once", "park": park.Park}, 200, nil)
	made := e.waitAnswer(t, "alice", teamID, n0, "Created: ", turn)
	if !strings.HasPrefix(made, `Created: Created the sandbox "scratch"`) {
		t.Errorf("sandbox_create: %q", made)
	}
	var scratch *csSandboxRow
	for _, s := range e.state(t).Sandboxes {
		if s.Name == "scratch" {
			scratch = &csSandboxRow{s.ID, s.Owner.User, s.Consumer}
		}
	}
	if scratch == nil || scratch.owner != "alice" || scratch.consumer != agTile {
		t.Errorf("scratch at the manager: %+v (want alice's, made by the agent)", scratch)
	} else if got := asAlice.Get(scratch.id); got.Visibility != "team" || got.Isolation != mode {
		t.Errorf("scratch: %+v (want the team's)", got)
	}
	if r := e.run(t, "alice", teamID); r.Config.Sandbox == nil || r.Config.Sandbox.Ref != team.Ref || len(r.Config.Attached) != 2 {
		t.Errorf("after sandbox_create: active %+v, attached %+v (want team-box active, scratch beside it)", r.Config.Sandbox, r.Config.Attached)
	}
	return box, team
}

type csSandboxRow struct{ id, owner, consumer string }

// testAgentTerminal: the agent page's Open terminal — <bx-terminal src> on
// the manager's …/sbx/sandboxes/{id}/tty?cwd=, dialled with the page's frame
// token (xbin.iface('sandboxes')): the manager sees alice, verified. End
// kills the shell at the manager and forgets it (DELETE …/execs/{id} from
// the page), which ends the terminal; bob's page may not open alice's
// private sandbox.
func testAgentTerminal(t *testing.T, e *csEnv, box agSandbox, slow time.Duration) {
	d := e.d
	conn, r, err := d.Dial(t, e.ttyPath(t, "/sandboxes/"+box.ID+"/tty?cwd=%2Fwork&rows=24&cols=80", agTile, "alice"))
	if err != nil {
		t.Fatalf("Open terminal: %v (%d %s)", err, r.Status, r)
	}
	defer conn.Close()
	sess := readSession(t, conn)
	send(t, conn, `echo "hi-from-tty:$PWD:$(id -un)"`+"\r")
	readUntil(t, conn, "hi-from-tty:/work:dev", 30*time.Second*slow) // the layout's user, by name
	asAlice := e.target().As(t, agTile).Verified("alice")
	var got sandboxcontract.Exec
	asAlice.Call("GET", "/sandboxes/"+box.ID+"/execs/"+sess.ID, nil, 200, &got)
	if !got.TTY || got.State != "running" {
		t.Errorf("the terminal's exec at the manager: %+v", got)
	}
	// End: the manager kills the shell and forgets it (the contract), and
	// the page's terminal ends with it
	asAlice.Call("DELETE", "/sandboxes/"+box.ID+"/execs/"+sess.ID, nil, 204, nil)
	asAlice.Refused("GET", "/sandboxes/"+box.ID+"/execs/"+sess.ID, nil, 404, "not-found")
	_ = conn.SetReadDeadline(time.Now().Add(15 * time.Second * slow))
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				t.Errorf("the terminal outlived its exec's end by 15 s")
			}
			break
		}
	}
	if c, r, err := d.Dial(t, e.ttyPath(t, "/sandboxes/"+box.ID+"/tty", agTile, "bob")); err == nil {
		c.Close()
		t.Error("bob's agent page opened a terminal onto alice's private sandbox")
	} else if r.Status != 403 && r.Status != 404 {
		t.Errorf("bob's terminal onto alice's sandbox: %d %s", r.Status, r)
	}
}

// stSandbox is one row of sandbox-terminal's GET /sandboxes.
type stSandbox struct {
	Provider, ID, Name, Login, State, Visibility string
	Shared, TTY                                  bool
}

func (e *csEnv) st(t *testing.T, person, method, path string, body any, want int, out any) xbindtest.Resp {
	t.Helper()
	return e.call(t, stTile, person, method, path, body, want, out)
}

// testSandboxTerminal: the builtin people's-terminals tile bound to the
// manager. The agent shares alice's sandbox with it; alice's page lists it
// and opens a browser terminal onto it (the manager's tty, the page's frame
// token); bob's lists nothing of hers. SSH on a stream expose bound to
// 127.0.0.1 only: a key alice registered logs her in — a pty session,
// exec mode with its output and exit code, stdin — and her access to the
// tile taken away cuts a live connection and refuses the next login.
func testSandboxTerminal(t *testing.T, e *csEnv, box, team agSandbox, slow time.Duration) {
	d := e.d
	d.Must(t, "POST", "/api/xbin/builtins/import", map[string]string{"name": "sandbox-terminal", "path": stTile}, 200)
	d.WaitComponent(t, stTile)
	d.Must(t, "POST", "/api/xbin/bindings", map[string]any{"component": stTile, "slot": "sandboxes", "providers": []string{csTile}}, 200)
	sshAddr := hostLoopback(t, d) // xbind's host's: the expose listens there
	d.Must(t, "POST", "/api/xbin/bindings", map[string]any{"component": stTile, "slot": "ssh", "provider": "runtime", "listen": sshAddr}, 200)
	var me struct {
		SSH struct {
			Listening bool
			Error     string
			HostKey   struct{ PublicKey string }
		}
	}
	xbindtest.Eventually(t, 8*time.Minute, "sandbox-terminal's SSH server listens", func() (bool, string) {
		r := d.Call(t, "GET", "/api/"+stTile+"/me", nil)
		if r.Status == 200 {
			_ = json.Unmarshal(r.Body, &me)
		}
		return me.SSH.Listening, fmt.Sprint(r.Status, " ", r)
	})
	d.Must(t, "PUT", "/api/"+stTile+"/settings", map[string]string{"sshAddress": sshAddr}, 200)
	_, port, _ := net.SplitHostPort(sshAddr)
	if pub := publicListeners(t, d, port); len(pub) > 0 {
		t.Errorf("the SSH expose listens beyond loopback: %v", pub)
	}
	sshDial := d.Forward(t, sshAddr) // here: the expose itself; remotely an ssh -L to it

	// the agent shares alice's sandbox with the tile, for alice
	var cur agSandbox
	e.ag(t, "alice", "GET", "/sandboxes/"+url.PathEscape(box.Ref), nil, 200, &cur)
	e.ag(t, "alice", "PATCH", "/sandboxes/"+url.PathEscape(box.Ref), map[string]any{
		"shares": []map[string]any{{"consumer": stTile, "users": []string{"alice"}}}, "version": cur.Version}, 200, nil)
	var list struct {
		Managers []struct {
			Provider, Title, Error string
			TTY                    bool
		}
		Sandboxes []stSandbox
	}
	e.st(t, "alice", "GET", "/sandboxes", nil, 200, &list)
	if len(list.Managers) != 1 || list.Managers[0].Provider != csTile || list.Managers[0].Error != "" || !list.Managers[0].TTY {
		t.Errorf("sandbox-terminal's managers: %+v", list.Managers)
	}
	if len(list.Sandboxes) != 1 || list.Sandboxes[0].ID != box.ID || list.Sandboxes[0].Login != "box-a" || !list.Sandboxes[0].TTY {
		t.Fatalf("alice's sandboxes in sandbox-terminal: %+v (want box-a alone: team-box isn't shared with it)", list.Sandboxes)
	}
	e.st(t, "bob", "GET", "/sandboxes", nil, 200, &list)
	if len(list.Sandboxes) != 0 {
		t.Errorf("bob's sandboxes in sandbox-terminal: %+v (the share names alice)", list.Sandboxes)
	}
	_ = team

	// a browser terminal: the page dials the manager's tty itself
	conn, r, err := d.Dial(t, e.ttyPath(t, "/sandboxes/"+box.ID+"/tty?cwd=%2Fwork", stTile, "alice"))
	if err != nil {
		t.Fatalf("the page's terminal: %v (%d %s)", err, r.Status, r)
	}
	readSession(t, conn)
	send(t, conn, "echo hi-term-$((3*3))\r")
	readUntil(t, conn, "hi-term-9", 30*time.Second*slow)
	send(t, conn, "exit\r")
	if code := readExit(t, conn, 30*time.Second*slow); code == nil || *code != 0 {
		t.Errorf("the page terminal's exit: %v", code)
	}
	conn.Close()
	if c, r, err := d.Dial(t, e.ttyPath(t, "/sandboxes/"+box.ID+"/tty", stTile, "bob")); err == nil {
		c.Close()
		t.Error("bob's sandbox-terminal page opened alice's sandbox")
	} else if r.Status != 403 && r.Status != 404 {
		t.Errorf("bob's terminal onto alice's sandbox: %d %s", r.Status, r)
	}

	// SSH: alice registers a key on the page
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	spub, _ := ssh.NewPublicKey(pub)
	authorized := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(spub))) + " alice@consumers-test"
	e.st(t, "alice", "POST", "/keys", map[string]string{"publicKey": authorized}, 201, nil)
	hostKey, _, _, _, err := ssh.ParseAuthorizedKey([]byte(me.SSH.HostKey.PublicKey))
	if err != nil {
		t.Fatalf("the host key /me shows: %q: %v", me.SSH.HostKey.PublicKey, err)
	}
	dial := func(login string) (*ssh.Client, error) {
		return ssh.Dial("tcp", sshDial, &ssh.ClientConfig{
			User: login, Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)},
			HostKeyCallback: ssh.FixedHostKey(hostKey), Timeout: 20 * time.Second,
		})
	}
	cli, err := dial("box-a")
	if err != nil {
		t.Fatalf("ssh box-a@%s (%s): %v", sshAddr, sshDial, err)
	}
	defer cli.Close()

	// a login shell in a pseudo-terminal
	t.Run("ssh pty", func(t *testing.T) {
		s, err := cli.NewSession()
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		var out syncBuf
		s.Stdout, s.Stderr = &out, &out
		in, _ := s.StdinPipe()
		if err := s.RequestPty("xterm", 30, 100, ssh.TerminalModes{ssh.ECHO: 1}); err != nil {
			t.Fatal(err)
		}
		if err := s.Shell(); err != nil {
			t.Fatal(err)
		}
		fmt.Fprint(in, "echo hi-$((40+2)) $PWD; stty size\n")
		xbindtest.Eventually(t, 30*time.Second*slow, "the shell answers", func() (bool, string) {
			return strings.Contains(out.String(), "hi-42 /work") && strings.Contains(out.String(), "30 100"), out.String()
		})
		fmt.Fprint(in, "exit 7\n")
		var ee *ssh.ExitError
		if err := s.Wait(); !errors.As(err, &ee) || ee.ExitStatus() != 7 {
			t.Errorf("the shell's exit: %v (want status 7)", err)
		}
	})

	// exec mode: no terminal, stdout and stderr together, the exit code, stdin
	t.Run("ssh exec", func(t *testing.T) {
		s, err := cli.NewSession()
		if err != nil {
			t.Fatal(err)
		}
		var out, errOut bytes.Buffer
		s.Stdout, s.Stderr = &out, &errOut
		var ee *ssh.ExitError
		if err := s.Run(`echo out; echo err >&2; test -t 0 || echo no-tty; exit 3`); !errors.As(err, &ee) || ee.ExitStatus() != 3 {
			t.Errorf("exec's exit: %v (want status 3)", err)
		}
		if got := out.String(); got != "out\nerr\nno-tty\n" {
			t.Errorf("exec's output: %q (stderr %q)", got, errOut.String())
		}
		s.Close()
		s, err = cli.NewSession()
		if err != nil {
			t.Fatal(err)
		}
		s.Stdin = strings.NewReader("one\ntwo\n")
		b, err := s.Output("wc -l")
		if err != nil || strings.TrimSpace(string(b)) != "2" {
			t.Errorf("exec with stdin: %q %v", b, err)
		}
		s.Close()
	})

	// access removal: a live connection is cut, the next login refused, the key kept
	t.Run("access removed", func(t *testing.T) {
		live, err := cli.NewSession()
		if err != nil {
			t.Fatal(err)
		}
		ended := make(chan error, 1)
		go func() { ended <- live.Run("sleep 600") }()
		time.Sleep(time.Second)
		d.Must(t, "PUT", "/api/xbin/access", map[string]string{"tile": stTile, "kind": "user", "id": "alice", "level": "none"}, 200)
		select {
		case err := <-ended:
			t.Logf("the live session ended: %v", err)
		case <-time.After(100 * time.Second):
			t.Error("a live SSH connection outlived its person's access by 100 s (checks: every 30 s, answers kept 30 s)")
		}
		c2, err := dial("box-a")
		if err == nil {
			defer c2.Close()
			s, err := c2.NewSession()
			if err == nil {
				b, err := s.CombinedOutput("echo should-not-run")
				var ee *ssh.ExitError
				if strings.Contains(string(b), "should-not-run") || !strings.Contains(string(b), "access revoked") || !errors.As(err, &ee) || ee.ExitStatus() != 1 {
					t.Errorf("a login after access was removed: %q %v (want access revoked, exit 1)", b, err)
				}
				s.Close()
			}
		} else {
			t.Logf("a login after access was removed: %v", err)
		}
		var keys struct {
			Keys []struct {
				User     string
				Inactive int64
			}
		}
		d.Must(t, "GET", "/api/"+stTile+"/keys?all=1", nil, 200).Decode(t, &keys)
		if len(keys.Keys) != 1 || keys.Keys[0].User != "alice" || keys.Keys[0].Inactive == 0 {
			t.Errorf("everyone's keys: %+v (want alice's kept, inactive)", keys.Keys)
		}
	})
}

// syncBuf is a buffer an SSH session's output goroutines write while the
// test reads it.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// hostLoopback is a free 127.0.0.1 port on xbind's host, as host:port.
func hostLoopback(t *testing.T, d *xbindtest.Daemon) string {
	t.Helper()
	if !d.IsRemote() {
		return freeLoopback(t)
	}
	out, err := d.HostSh(`for c in $(shuf -i 30000-39999 -n 50); do ss -Hltn "sport = :$c" | grep -q . || { echo "127.0.0.1:$c"; exit 0; }; done; exit 1` + "\n")
	if err != nil || !strings.HasPrefix(out, "127.0.0.1:") {
		t.Fatalf("a free loopback port on xbind's host: %q %v", out, err)
	}
	return strings.TrimSpace(out)
}

// publicListeners is every listening TCP socket on port (decimal) of
// xbind's host whose address isn't loopback, from its /proc/net/tcp{,6}:
// xbind's port relay for a stream expose bound on 127.0.0.1 must add none.
func publicListeners(t *testing.T, d *xbindtest.Daemon, port string) []string {
	t.Helper()
	p, _ := strconv.Atoi(port)
	var out []string
	for _, f := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		b, err := d.HostSh("cat " + f + "\n")
		if err != nil {
			continue
		}
		for _, line := range strings.Split(b, "\n")[1:] {
			fs := strings.Fields(line)
			if len(fs) < 4 || fs[3] != "0A" { // LISTEN
				continue
			}
			host, hexPort, ok := strings.Cut(fs[1], ":")
			if n, _ := strconv.ParseUint(hexPort, 16, 16); !ok || int(n) != p {
				continue
			}
			if ip := procIP(host); ip == nil || !ip.IsLoopback() {
				out = append(out, f+" "+fs[1])
			}
		}
	}
	return out
}

// procIP decodes /proc/net/tcp's address: 32-bit words in host (little
// endian) order.
func procIP(h string) net.IP {
	b, err := hex.DecodeString(h)
	if err != nil || (len(b) != 4 && len(b) != 16) {
		return nil
	}
	ip := make(net.IP, len(b))
	for i := 0; i < len(b); i += 4 {
		ip[i], ip[i+1], ip[i+2], ip[i+3] = b[i+3], b[i+2], b[i+1], b[i]
	}
	return ip
}
