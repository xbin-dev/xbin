package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// Without a terminal: `ssh api-dev@host cmd` runs as an exec with stdin; the
// output is exact (no terminal in between), the exit status is the command's.
func TestSSHExec(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	sb := r.sandbox("alice", "api-dev", shared("*"))
	key := r.register("alice")
	c := r.mustDial("api-dev", key)

	if out, errOut, code := run(t, c, "echo hi", false, ""); out != "hi\n" || code != 0 {
		t.Fatalf("echo hi: %q %q %d", out, errOut, code)
	}
	if out, _, code := run(t, c, "cat; echo; echo $SANDBOX_ID", false, "piped\x00bytes"); out != "piped\x00bytes\n"+sb.ID+"\n" || code != 0 {
		t.Fatalf("stdin: %q %d", out, code)
	}
	if _, _, code := run(t, c, "exit 3", false, ""); code != 3 {
		t.Fatalf("exit 3: %d", code)
	}
	// `ssh -T host`: the login shell reading its script from stdin
	if out, _, code := run(t, c, "", false, "echo from-a-shell\nexit 4\n"); out != "from-a-shell\n" || code != 4 {
		t.Fatalf("shell: %q %d", out, code)
	}
	// every call to the manager named alice (asserted), from this tile
	for _, call := range r.mgr.Calls() {
		if strings.Contains(call.Path, "/execs") && (call.SbxUser != "alice" || call.From != self) {
			t.Fatalf("a call not as alice from %s: %+v", self, call)
		}
	}
}

// With a terminal: the manager's tty route — exec and shell, a resize, the
// exit status from the wire's exit frame.
func TestSSHTerminal(t *testing.T) {
	t.Parallel()
	if !fsbHasPTY() {
		t.Skip("no pseudo-terminals on this host")
	}
	r := newRig(t)
	r.sandbox("alice", "api-dev", shared([]string{"alice"}))
	key := r.register("alice")
	c := r.mustDial("api-dev", key)

	out, _, code := run(t, c, "echo hi; stty size", true, "")
	if !strings.Contains(out, "hi\r\n") || !strings.Contains(out, "24 80") || code != 0 {
		t.Fatalf("echo hi in a pty: %q %d", out, code)
	}
	if _, _, code := run(t, c, "exit 5", true, ""); code != 5 {
		t.Fatalf("exit 5 in a pty: %d", code)
	}

	// an interactive shell: resize, then read the size back
	s, err := c.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.RequestPty("xterm", 24, 80, ssh.TerminalModes{}); err != nil {
		t.Fatal(err)
	}
	in, _ := s.StdinPipe()
	var mu sync.Mutex
	var buf strings.Builder
	stdout, _ := s.StdoutPipe()
	go func() {
		b := make([]byte, 4096)
		for {
			n, err := stdout.Read(b)
			mu.Lock()
			buf.Write(b[:n])
			mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	if err := s.Shell(); err != nil {
		t.Fatal(err)
	}
	if err := s.WindowChange(40, 100); err != nil {
		t.Fatal(err)
	}
	// a resize and keystrokes travel apart over SSH: ask until it shows
	eventually(t, 10*time.Second, "the shell sees 40x100", func() bool {
		_, _ = io.WriteString(in, "stty size\r")
		time.Sleep(100 * time.Millisecond)
		mu.Lock()
		defer mu.Unlock()
		return strings.Contains(buf.String(), "40 100")
	})
	_, _ = io.WriteString(in, "exit 7\r")
	if code := exitCode(t, s.Wait()); code != 7 {
		t.Fatalf("the shell's exit: %d (%q)", code, buf.String())
	}
}

// deafCmd turns a deaf ear to HUP, then says so: only a DELETE ends it.
const deafCmd = "trap '' HUP; echo deaf; exec sleep 600"

// A client that leaves while its command runs ends it (HUP, then DELETE).
func TestSSHClientLeaves(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	sb := r.sandbox("alice", "api-dev", shared("*"))
	key := r.register("alice")
	deleted := r.deletes(sb.ID)
	c := r.mustDial("api-dev", key)
	s, err := c.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	stdout, _ := s.StdoutPipe()
	if err := s.Start(deafCmd); err != nil {
		t.Fatal(err)
	}
	// its output reaching the client: it runs, deaf to HUP, and the tile
	// bridges it
	line := make(chan string, 1)
	go func() {
		l, err := bufio.NewReader(stdout).ReadString('\n')
		line <- fmt.Sprintf("%q %v", l, err)
	}()
	if got := recv(t, line, "the command's output"); got != `"deaf\n" <nil>` {
		t.Fatalf("the command's output: %s", got)
	}
	c.Close()
	r.endedByDelete("alice", sb.ID, recv(t, deleted, "the tile ends the command"))
}

// One that leaves while the manager is still starting it, too: the tile
// waits for the command's id, then ends it — the manager runs it either way.
func TestSSHClientLeavesWhileStarting(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	sb := r.sandbox("alice", "api-dev", shared("*"))
	key := r.register("alice")
	deleted := r.deletes(sb.ID)
	started, answer := r.holdStarts(sb.ID)
	c := r.mustDial("api-dev", key)
	s, err := c.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start(deafCmd); err != nil {
		t.Fatal(err)
	}
	id := recv(t, started, "the manager starts the command")
	r.awaitOutput("alice", sb.ID, id, "deaf\n") // deaf to HUP from here on
	c.Close()
	// the tile has seen the client go before the start is answered
	eventually(t, hangGuard, "the tile sees the client gone", func() bool { return len(r.tile.sessions("")) == 0 })
	answer()
	if got := recv(t, deleted, "the tile ends the command"); got != id {
		t.Fatalf("the tile ended %s, not the command it started (%s)", got, id)
	}
	r.endedByDelete("alice", sb.ID, id)
}

// An unregistered key logs nobody in.
func TestSSHRefusedKey(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.sandbox("alice", "api-dev", shared("*"))
	r.register("alice")
	if c, err := r.dial("api-dev", newKey(t)); err == nil || !strings.Contains(err.Error(), "unable to authenticate") {
		if c != nil {
			c.Close()
		}
		t.Fatalf("a stranger's key: %v", err)
	}
}

// The user name picks the sandbox: unknown names list the person's; one
// name on two sandboxes asks for <name>~<n> or an id — never a login another
// sandbox's own name gives (a sandbox named "web.1" is web.1).
func TestSSHPickSandbox(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.sandbox("alice", "api-dev", shared("*"))
	w1 := r.sandbox("alice", "Web", shared("*"))
	w2 := r.sandbox("alice", "web", shared("*"))
	dot := r.sandbox("alice", "web.1", shared("*"))
	key := r.register("alice")

	c := r.mustDial("nope", key)
	_, errOut, code := run(t, c, "true", false, "")
	if code != 1 || !strings.Contains(errOut, `no sandbox "nope"`) || !strings.Contains(errOut, "api-dev") || !strings.Contains(errOut, "web~1") {
		t.Fatalf("an unknown name: %d %q", code, errOut)
	}
	c = r.mustDial("web", key)
	_, errOut, code = run(t, c, "true", false, "")
	if code != 1 || !strings.Contains(errOut, "names 2 sandboxes") || !strings.Contains(errOut, "web~1") || !strings.Contains(errOut, w2.ID) {
		t.Fatalf("an ambiguous name: %d %q", code, errOut)
	}
	for login, want := range map[string]string{"web~1": w1.ID, "web~2": w2.ID, "web.1": dot.ID, w2.ID: w2.ID, "API-Dev": ""} {
		c := r.mustDial(login, key)
		out, errOut, code := run(t, c, "echo $SANDBOX_ID", false, "")
		if code != 0 || (want != "" && out != want+"\n") {
			t.Fatalf("login %s: %q %q %d", login, out, errOut, code)
		}
	}

	// the page's list carries the logins, each one once
	st, b := r.do("GET", "/sandboxes", "alice", "read", nil)
	var l struct {
		Sandboxes []sandboxView `json:"sandboxes"`
	}
	if st != 200 || json.Unmarshal(b, &l) != nil || len(l.Sandboxes) != 4 {
		t.Fatalf("GET /sandboxes: %d %s", st, b)
	}
	logins := map[string]string{}
	for _, s := range l.Sandboxes {
		if logins[s.Login] != "" {
			t.Fatalf("two sandboxes log in as %s: %v", s.Login, l.Sandboxes)
		}
		logins[s.Login] = s.ID
	}
	if logins["web~1"] != w1.ID || logins["web~2"] != w2.ID || logins["web.1"] != dot.ID || logins["api-dev"] == "" {
		t.Fatalf("logins: %v", logins)
	}
}

// Generated logins never collide with a name's own, whatever the names.
func TestAssignLoginsUnique(t *testing.T) {
	t.Parallel()
	names := []string{"web", "web", "web.1", "web~1", "web-1", "Web 1", "", "", "sb-x", "a", "A", "a~2", "a.2"}
	es := make([]entry, len(names))
	for i, n := range names {
		es[i].SB = sandbox{ID: fmt.Sprintf("sb-%d", i), Name: n}
	}
	es[8].SB.ID = "sb-y"
	assignLogins(es)
	seen := map[string]int{}
	for i, e := range es {
		if j, dup := seen[e.Login]; dup {
			t.Fatalf("%q (%q) and %q (%q) both log in as %q", names[j], es[j].SB.ID, names[i], e.SB.ID, e.Login)
		}
		seen[e.Login] = i
	}
	if es[0].Login != "web~1" || es[1].Login != "web~2" || es[2].Login != "web.1" || es[6].Login != "sb-6" {
		t.Fatalf("%+v", es)
	}
}

// Every login the list gives picks its own sandbox — a generated `web~1`
// too, beside a sandbox whose own name is `web-1` (what `web~1` spells
// loosely).
func TestPickEveryLogin(t *testing.T) {
	t.Parallel()
	for _, names := range [][]string{
		{"web", "web", "web-1"},
		{"web", "web", "Web 1", "web.1", "api"},
		{"a", "A", "a-2", "a.2", "", ""},
	} {
		es := make([]entry, len(names))
		for i, n := range names {
			es[i].SB = sandbox{ID: fmt.Sprintf("sb-%d", i), Name: n}
		}
		assignLogins(es)
		for i, e := range es {
			got, err := pick(es, nil, e.Login)
			if err != nil || got.SB.ID != e.SB.ID {
				t.Errorf("%v: logging in as %q (sandbox %d, %q): %v %v", names, e.Login, i, names[i], got, err)
			}
		}
	}
}

// Only sandboxes shared with this tile for the person — then their own, a
// member's, or a team one — are theirs to log into.
func TestSSHAccess(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.sandbox("alice", "unshared", nil)
	r.sandbox("alice", "bobs-share", shared([]string{"bob"}))
	r.sandbox("bob", "bobs-private", shared("*"))
	r.sandbox("bob", "bobs-team", map[string]any{"visibility": "team", "shares": []map[string]any{{"consumer": self, "users": "*"}}})
	r.sandbox("bob", "with-alice", map[string]any{"members": []string{"alice"}, "shares": []map[string]any{{"consumer": self, "users": []string{"alice"}}}})
	key := r.register("alice")
	for login, ok := range map[string]bool{"unshared": false, "bobs-share": false, "bobs-private": false, "bobs-team": true, "with-alice": true} {
		c := r.mustDial(login, key)
		out, errOut, code := run(t, c, "echo in", false, "")
		if ok != (code == 0 && out == "in\n") {
			t.Errorf("%s: want ok=%v, got %q %q %d", login, ok, out, errOut, code)
		}
		if !ok && !strings.Contains(errOut, "no sandbox") {
			t.Errorf("%s: %q", login, errOut)
		}
	}
	// bob's key gets bob's; a share naming him doesn't make alice's private
	// sandbox his
	bob := r.register("bob")
	for login, ok := range map[string]bool{"bobs-private": true, "bobs-share": false} {
		c := r.mustDial(login, bob)
		if out, _, code := run(t, c, "echo in", false, ""); ok != (code == 0 && out == "in\n") {
			t.Errorf("bob in %s: want ok=%v, got %q %d", login, ok, out, code)
		}
	}
}

// A stopped sandbox starts on the login's exec; an archived one says to
// thaw it.
func TestSSHStates(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	sb := r.sandbox("alice", "api-dev", shared("*"))
	key := r.register("alice")
	a := r.agent("alice")
	a.Call("POST", "/sandboxes/"+sb.ID+"/stop", nil, 200, nil)
	c := r.mustDial("api-dev", key)
	if out, _, code := run(t, c, "echo up", false, ""); code != 0 || out != "up\n" {
		t.Fatalf("a stopped sandbox: %q %d", out, code)
	}
	a.Call("POST", "/sandboxes/"+sb.ID+"/archive?wait=10", nil, 200, nil)
	if _, errOut, code := run(t, c, "echo up", false, ""); code != 1 || !strings.Contains(errOut, "thaw") {
		t.Fatalf("an archived sandbox: %q %d", errOut, code)
	}
}

// No port forwarding, no sftp.
func TestSSHNoForwarding(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.sandbox("alice", "api-dev", shared("*"))
	c := r.mustDial("api-dev", r.register("alice"))
	if _, err := c.Dial("tcp", "127.0.0.1:22"); err == nil {
		t.Fatal("a direct-tcpip channel opened")
	}
	if _, err := c.Listen("tcp", "127.0.0.1:0"); err == nil {
		t.Fatal("a remote forward was accepted")
	}
	s, _ := c.NewSession()
	defer s.Close()
	if err := s.RequestSubsystem("sftp"); err == nil {
		t.Fatal("the sftp subsystem was accepted")
	}
}
