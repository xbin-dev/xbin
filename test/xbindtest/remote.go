//go:build linux

package xbindtest

// remote.go — a daemon the test doesn't run: an xbind elsewhere (the QA
// box's test instance, reached through an ssh tunnel), driven by the same
// tests. The environment names it:
//
//	XBIN_E2E_URL      its base URL (http://127.0.0.1:18650: a tunnel)
//	XBIN_E2E_TOKEN    its owner token; unset, one with owner auth on is
//	                  read from the workspace (.xbin/token, through
//	                  XBIN_E2E_SH), and --no-auth needs none
//	XBIN_E2E_SH       a command that runs the shell script on its stdin on
//	                  xbind's host as the workspace's user (WriteTile,
//	                  CopyTile), e.g. "qa-sbxtest.sh sh 'sudo -u xbin sh -s'"
//	XBIN_E2E_WS       the workspace's path on that host
//	XBIN_E2E_RESTART  a command that restarts it on the same workspace and
//	                  address (Restart)
//	XBIN_E2E_LOGS     a command printing its log's tail (a failed test's)
//	XBIN_E2E_VM_MIB   the tile VMs' memory budget RequireVM sets (2048)
//	XBIN_E2E_VMS      and their number (4)
//	XBIN_E2E_FORWARD  a command forwarding a local port to one on xbind's
//	                  host's loopback until it is killed (Forward), {local}
//	                  and {remote} its addresses, e.g. "ssh -o BatchMode=yes
//	                  -o ControlPath=none -o ExitOnForwardFailure=yes -N -L
//	                  {local}:{remote} ubuntu@84.239.100.188" — ControlPath
//	                  none, or an ssh ControlMaster keeps the forward after
//	                  the command is killed
//
// The commands run under sh -c on this host. Its workspace outlives the
// test: tiles a test writes stay (name them per run), its tile sandboxes
// are deleted when the test ends, and it is never stopped.

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// remote is how a remote daemon is reached beyond its URL.
type remote struct {
	sh, ws, restart, logs string
}

// Remote reports whether XBIN_E2E_URL names a daemon to test instead of
// one of the test's own.
func Remote() bool { return os.Getenv("XBIN_E2E_URL") != "" }

// StartOrConnect is the daemon at XBIN_E2E_URL (Connect) when it is set,
// else an isolated xbind of the test's own (Require, Start with o).
func StartOrConnect(t testing.TB, o Options) *Daemon {
	t.Helper()
	if Remote() {
		return Connect(t)
	}
	return Start(t, Require(t), o)
}

// Connect is the daemon at XBIN_E2E_URL, checked answering. When the test
// ends its tile sandboxes are deleted; it keeps running.
func Connect(t testing.TB) *Daemon {
	t.Helper()
	u, err := url.Parse(os.Getenv("XBIN_E2E_URL"))
	if err != nil || u.Host == "" || u.Scheme != "http" {
		t.Fatalf("XBIN_E2E_URL %q: want http://host:port", os.Getenv("XBIN_E2E_URL"))
	}
	repo, err := repoRoot()
	if err != nil {
		t.Fatal(err)
	}
	d := &Daemon{
		A:   &Assets{Repo: repo}, // the examples to copy; nothing built, no rootfs here
		URL: strings.TrimRight(u.String(), "/"), Addr: u.Host, seen: map[int]string{},
		token: os.Getenv("XBIN_E2E_TOKEN"),
		rem: &remote{sh: os.Getenv("XBIN_E2E_SH"), ws: os.Getenv("XBIN_E2E_WS"),
			restart: os.Getenv("XBIN_E2E_RESTART"), logs: os.Getenv("XBIN_E2E_LOGS")},
	}
	if err := d.waitHealthy(30 * time.Second); err != nil {
		t.Fatalf("XBIN_E2E_URL: %v", err)
	}
	if d.token == "" {
		var m struct{ Auth bool }
		if err := d.get("/api/xbin/login/methods", &m); err != nil {
			t.Fatalf("XBIN_E2E_URL: %v", err)
		}
		if m.Auth {
			if d.rem.ws == "" || strings.ContainsAny(d.rem.ws, "'\n") {
				t.Fatalf("XBIN_E2E_URL has owner auth on: set XBIN_E2E_TOKEN, or XBIN_E2E_SH and XBIN_E2E_WS to read it (XBIN_E2E_WS %q)", d.rem.ws)
			}
			out, err := d.HostSh("cat -- '" + d.rem.ws + "/.xbin/token'\n")
			if err != nil || strings.TrimSpace(out) == "" {
				t.Fatalf("XBIN_E2E_URL has owner auth on: set XBIN_E2E_TOKEN, or XBIN_E2E_SH and XBIN_E2E_WS to read it (%v)", err)
			}
			d.token = strings.TrimSpace(out)
		}
	}
	t.Cleanup(func() { d.cleanup(t) })
	return d
}

// IsRemote reports whether d is a daemon the test doesn't run (Connect).
func (d *Daemon) IsRemote() bool { return d.rem != nil }

// HasPeople reports whether d authenticates people (owner auth on), so a
// test can make accounts and sign them in.
func (d *Daemon) HasPeople() bool { return d.token != "" }

func (d *Daemon) waitHealthy(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		r, err := http.Get(d.URL + "/healthz")
		if err == nil {
			r.Body.Close()
			if r.StatusCode == 200 {
				return nil
			}
			err = fmt.Errorf("healthz %d", r.StatusCode)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s doesn't answer /healthz within %s: %v", d.URL, timeout, err)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// runLocal runs a remote's command (sh -c) with stdin, returning its
// output.
func runLocal(command string, stdin []byte) (string, error) {
	cmd := exec.Command("sh", "-c", command) // exec-ok: the test's own configured command (XBIN_E2E_*), on the test's host
	cmd.Stdin = bytes.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s: %w: %s", command, err, cut(out))
	}
	return string(out), nil
}

// restartRemote restarts a remote daemon (XBIN_E2E_RESTART) and waits for
// it to answer again.
func (d *Daemon) restartRemote(t testing.TB) {
	t.Helper()
	if d.rem.restart == "" {
		t.Fatal("restarting a remote xbind needs XBIN_E2E_RESTART")
	}
	if _, err := runLocal(d.rem.restart, nil); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second) // its old self may still answer
	if err := d.waitHealthy(3 * time.Minute); err != nil {
		t.Fatal(err)
	}
}

// writeRemote writes files (a tile-relative path → its content, or a
// directory's files) under the remote workspace's tile, as its user.
func (d *Daemon) writeRemote(tile string, files map[string][]byte) error {
	if d.rem.sh == "" || d.rem.ws == "" {
		return fmt.Errorf("writing a tile on a remote xbind needs XBIN_E2E_SH and XBIN_E2E_WS")
	}
	if strings.Contains(tile, "..") || strings.ContainsAny(tile, "'\n") {
		return fmt.Errorf("tile %q", tile)
	}
	var script strings.Builder
	script.WriteString("set -e\numask 022\n")
	for rel, content := range files {
		if strings.Contains(rel, "..") || strings.ContainsAny(rel, "'\n") {
			return fmt.Errorf("file %q", rel)
		}
		p := d.rem.ws + "/" + tile + "/" + rel
		fmt.Fprintf(&script, "mkdir -p '%s'\nbase64 -d > '%s.xbindtest' <<'EOF'\n%s\nEOF\nmv '%s.xbindtest' '%s'\n",
			filepath.Dir(p), p, base64.StdEncoding.EncodeToString(content), p, p)
	}
	_, err := runLocal(d.rem.sh, []byte(script.String()))
	return err
}

// dirFiles reads every regular file under dir (a tile to copy).
func dirFiles(dir string) (map[string][]byte, error) {
	out := map[string][]byte{}
	err := filepath.WalkDir(dir, func(p string, e fs.DirEntry, err error) error {
		if err != nil || !e.Type().IsRegular() {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		b, err := os.ReadFile(p)
		out[filepath.ToSlash(rel)] = b
		return err
	})
	return out, err
}

// remoteLogs is the remote daemon's log tail (XBIN_E2E_LOGS), or why not.
func (d *Daemon) remoteLogs() string {
	if d.rem.logs == "" {
		return "(no XBIN_E2E_LOGS)"
	}
	out, err := runLocal(d.rem.logs, nil)
	if err != nil {
		return err.Error()
	}
	return out
}

// vmBudget is the tile-VM budget RequireVM sets on a remote daemon.
func (d *Daemon) vmBudget() (mib, vms int) {
	mib, vms = 2048, 4
	if n, err := strconv.Atoi(os.Getenv("XBIN_E2E_VM_MIB")); err == nil && n > 0 {
		mib = n
	}
	if n, err := strconv.Atoi(os.Getenv("XBIN_E2E_VMS")); err == nil && n > 0 {
		vms = n
	}
	return mib, vms
}

// HostSh runs script on xbind's host as the workspace's user — here, as
// this process; on a remote daemon, through XBIN_E2E_SH — and returns its
// output: what a test checks of the host's files (owners, modes, what's
// left).
func (d *Daemon) HostSh(script string) (string, error) {
	if d.rem != nil {
		if d.rem.sh == "" {
			return "", fmt.Errorf("a remote xbind's host needs XBIN_E2E_SH")
		}
		return runLocal(d.rem.sh, []byte(script))
	}
	return runLocal("sh -s", []byte(script))
}

// Workspace is the workspace's path on xbind's host.
func (d *Daemon) Workspace() string {
	if d.rem != nil {
		return d.rem.ws
	}
	return d.WS
}

// Forward is a local address reaching addr, a loopback address on xbind's
// host (a stream expose bound there): addr itself here; on a remote daemon
// a forward XBIN_E2E_FORWARD starts, stopped when the test ends.
func (d *Daemon) Forward(t testing.TB, addr string) string {
	t.Helper()
	if d.rem == nil {
		return addr
	}
	tmpl := os.Getenv("XBIN_E2E_FORWARD")
	if tmpl == "" || !strings.Contains(tmpl, "{local}") || !strings.Contains(tmpl, "{remote}") {
		t.Skip("reaching a port on a remote xbind's host needs XBIN_E2E_FORWARD (with {local} and {remote})")
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	local := l.Addr().String()
	l.Close()
	command := strings.NewReplacer("{local}", local, "{remote}", addr).Replace(tmpl)
	var out bytes.Buffer
	cmd := exec.Command("sh", "-c", "exec "+command) // exec-ok: the test's own configured forward (XBIN_E2E_FORWARD), on the test's host
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatalf("XBIN_E2E_FORWARD: %v", err)
	}
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-exited
	})
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		select {
		case <-exited:
			t.Fatalf("XBIN_E2E_FORWARD %q ended: %s", command, out.String())
		default:
		}
		if c, err := net.DialTimeout("tcp", local, time.Second); err == nil {
			c.Close()
			return local
		}
		if time.Now().After(deadline) {
			t.Fatalf("XBIN_E2E_FORWARD %q: %s isn't listening after 30 s", command, local)
		}
	}
}
