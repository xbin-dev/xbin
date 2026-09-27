//go:build integration

package test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/sandbox"
)

// The isolated daemon (15-test-plan §5.1). Pinned and non-primary backends
// need isolation (P18), so every backend property of the dev lifecycle runs
// on an xbind of its own started with --isolate, while the suite's shared
// daemon stays non-isolated.

// isoOpts configures startIsolatedDaemon. The zero value is --no-auth with no
// ingress listener.
type isoOpts struct {
	Auth    bool     // auth on (--insecure-vault); the root token is read into d.Token
	Ingress bool     // --ingress-listen on a free port, in d.Ingress
	Args    []string // more xbind flags, e.g. "--tile-deployments=off"
	Env     []string // more environment (the last value of a key wins), e.g. XBIN_BIN=<dir holding bx>
}

// isoDaemon is one isolated xbind on a workspace of its own. restart keeps the
// workspace, the ports and the token.
type isoDaemon struct {
	URL     string // http://<Addr>
	Addr    string // the console listener
	Ingress string // the ingress listener's host:port; "" without isoOpts.Ingress
	WS      string // the workspace
	Rootfs  string // the base rootfs of the sandboxes
	Token   string // the root token with isoOpts.Auth; "" with --no-auth
	// Bin is the xbind binary each start runs: the suite's build by default.
	// A test may point it at another binary between stop and start.
	Bin string

	opts    isoOpts
	logPath string
	cmd     *exec.Cmd
	done    chan struct{} // closed once cmd has been waited for
}

// isoClient bounds a single request, so a poll never hangs on a wedged
// daemon; it outlasts a cold backend build, which the first request waits for.
var isoClient = &http.Client{Timeout: 2 * time.Minute}

// startIsolatedDaemon starts an xbind with --isolate --rootfs on a fresh
// workspace, waits until it is healthy, and stops it (gracefully, then with a
// process-group kill) and removes the workspace when the test ends. It skips
// the test, saying why, without a git-bearing rootfs or user namespaces
// (isolationOrSkip).
//
// It starts the daemon the way startDaemon does (its own process group,
// killed whole), but tracks the current process itself: startDaemon's cleanup
// would kill each earlier generation's group id long after d.restart reaped
// it, and kill without a graceful stop, which leaves gocryptfs mounts behind.
func startIsolatedDaemon(t *testing.T, opts isoOpts) *isoDaemon {
	t.Helper()
	rootfs := isolationOrSkip(t)
	dir := t.TempDir()
	d := &isoDaemon{
		Addr: isoFreeAddr(t), WS: filepath.Join(dir, "ws"), Rootfs: rootfs, Bin: xbindBin,
		opts: opts, logPath: filepath.Join(dir, "xbind.log"),
	}
	d.URL = "http://" + d.Addr
	if opts.Ingress {
		d.Ingress = isoFreeAddr(t)
	}
	t.Cleanup(func() {
		if err := d.halt(); err != nil {
			t.Errorf("stopping the isolated xbind: %v", err)
		}
		if t.Failed() {
			t.Logf("isolated xbind (%s):\n%s", d.WS, d.logTails())
		}
		for i := 0; i < 40; i++ { // outlast a straggler's final flush
			if removeTree(d.WS); !isoExists(d.WS) {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Errorf("the isolated workspace %s could not be removed", d.WS)
	})
	d.start(t)
	return d
}

// isolationOrSkip returns the rootfs for --rootfs (XBIN_TEST_ROOTFS, or the
// repo's .rootfs from `make rootfs`), or skips the test with the reason: not
// Linux, no rootfs with git (confined tool runs need it, D78), unprivileged
// user namespaces switched off, or a host that refuses to create the
// sandbox's namespace set (probed with util-linux's unshare when present).
func isolationOrSkip(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("isolated xbind: namespace sandboxes are Linux-only")
	}
	fs := os.Getenv("XBIN_TEST_ROOTFS")
	if fs == "" {
		fs = filepath.Join(repo, ".rootfs")
	}
	if fi, err := os.Stat(filepath.Join(fs, "usr", "bin", "git")); err != nil || fi.IsDir() {
		t.Skipf("isolated xbind: no rootfs with git at %s (set XBIN_TEST_ROOTFS, or make rootfs)", fs)
	}
	if !sandbox.Available() {
		t.Skip("isolated xbind: unprivileged user namespaces are off (kernel.unprivileged_userns_clone)")
	}
	if unshare, err := exec.LookPath("unshare"); err == nil {
		// the namespace set sandbox.Launch clones, with the single-uid map
		probe := exec.Command(unshare, "--user", "--map-root-user", "--mount", "--pid", "--fork",
			"--ipc", "--uts", "--net", "true")
		if out, err := probe.CombinedOutput(); err != nil {
			t.Skipf("isolated xbind: this host refuses the sandbox's namespaces (unshare: %v %s)", err, bytes.TrimSpace(out))
		}
	}
	return fs
}

// start launches the daemon on d's workspace and ports, waits (a bounded
// minute) until it is healthy, and reads the root token when auth is on.
func (d *isoDaemon) start(t *testing.T) {
	t.Helper()
	if d.cmd != nil {
		t.Fatal("isolated xbind: start while it runs (stop it first)")
	}
	args := []string{"--workspace", d.WS, "--listen", d.Addr, "--isolate", "--rootfs", d.Rootfs}
	if d.opts.Auth {
		args = append(args, "--insecure-vault") // boots without a passphrase
	} else {
		args = append(args, "--no-auth")
	}
	if d.Ingress != "" {
		args = append(args, "--ingress-listen", d.Ingress)
	}
	args = append(args, d.opts.Args...)
	cmd := exec.Command(d.Bin, args...)
	cmd.Env = append(os.Environ(), "XBIN_SDK_PATH="+filepath.Join(repo, "sdk"))
	// A worktree has no bin/: the variables usually come from the environment;
	// else the repo's builds (make fuse-overlayfs, make gocryptfs) are used.
	for _, tool := range []struct{ env, file string }{
		{"XBIN_FUSE_OVERLAYFS", "fuse-overlayfs"}, {"XBIN_GOCRYPTFS", "gocryptfs"},
	} {
		if p := filepath.Join(repo, "bin", tool.file); os.Getenv(tool.env) == "" && isoExists(p) {
			cmd.Env = append(cmd.Env, tool.env+"="+p)
		}
	}
	cmd.Env = append(cmd.Env, d.opts.Env...)
	logf, err := os.OpenFile(d.logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer logf.Close() // the child holds its own copy
	_, _ = io.WriteString(logf, "--- start "+time.Now().Format(time.RFC3339Nano)+": "+strings.Join(args, " ")+"\n")
	// A file, not a pipe: Wait never waits for a straggler holding a pipe open.
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} // group-killable: builds and sandboxes too
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	d.cmd, d.done = cmd, done
	healthy := waitFor(func() bool {
		select {
		case <-done:
			return true // exited: no use waiting
		default:
		}
		c, _ := d.do(t, "GET", "/healthz", "")
		return c == 200
	}, 60*time.Second)
	select {
	case <-done:
		healthy = false
	default:
	}
	if !healthy {
		_ = d.halt()
		t.Fatalf("the isolated xbind never became healthy:\n%s", isoTail(d.logPath, 60))
	}
	if d.opts.Auth {
		b, err := os.ReadFile(filepath.Join(d.WS, ".xbin", "token"))
		if err != nil {
			t.Fatalf("the isolated xbind's root token: %v", err)
		}
		d.Token = strings.TrimSpace(string(b))
	}
}

// stop stops the daemon and everything it started, keeping the workspace.
func (d *isoDaemon) stop(t *testing.T) {
	t.Helper()
	if err := d.halt(); err != nil {
		t.Fatalf("stopping the isolated xbind: %v", err)
	}
}

// restart is an xbind restart: stop, then start on the same workspace.
func (d *isoDaemon) restart(t *testing.T) {
	t.Helper()
	d.stop(t)
	d.start(t)
}

// halt sends SIGTERM (xbind stops its backends and unmounts encrypted
// resources on the way out), waits a bounded time, then kills the process
// group: xbind if it is still there, and every straggler (a build, a
// sandbox) either way.
func (d *isoDaemon) halt() error {
	if d.cmd == nil {
		return nil
	}
	cmd, done := d.cmd, d.done
	d.cmd, d.done = nil, nil
	var err error
	select {
	case <-done: // it had exited already
	default:
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(20 * time.Second):
			err = errors.New("no exit 20 s after SIGTERM; killed")
		}
	}
	// The group id stays reserved while any member lives, so this reaches
	// only the daemon's own processes.
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	<-done
	return err
}

// do sends a request to the daemon with the root token when auth is on. A
// transport error is code 0 with the error as the body, so polls go on across
// a restart.
func (d *isoDaemon) do(t *testing.T, method, path, body string) (int, string) {
	t.Helper()
	return isoHTTP(d.URL, d.Token, method, path, body)
}

// isoHTTP sends one request to base (a bearer token when token isn't "");
// a transport error is code 0 with the error as the body.
func isoHTTP(base, token, method, path, body string) (int, string) {
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	rq, err := http.NewRequest(method, base+path, rd)
	if err != nil {
		return 0, err.Error()
	}
	if body != "" {
		rq.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		rq.Header.Set("Authorization", "Bearer "+token)
	}
	r, err := isoClient.Do(rq)
	if err != nil {
		return 0, err.Error()
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	return r.StatusCode, string(b)
}

// logTails is the end of the daemon's log and of every backend log, for a
// failed test's report.
func (d *isoDaemon) logTails() string {
	var b strings.Builder
	b.WriteString("== xbind.log\n" + isoTail(d.logPath, 80))
	logs, _ := filepath.Glob(filepath.Join(d.WS, ".xbin", "log", "*.log"))
	for _, p := range logs {
		b.WriteString("== " + filepath.Base(p) + "\n" + isoTail(p, 20))
	}
	return b.String()
}

// isoTail is the last n lines of the file at p.
func isoTail(p string, n int) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return err.Error() + "\n"
	}
	lines := strings.SplitAfter(string(b), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "")
}

// isoFreeAddr is a loopback address with a port free a moment ago.
func isoFreeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

// isoExists says whether anything is at p (a dangling link counts).
func isoExists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// covers NP-15-2 — the isolated daemon runs the probe (15-test-plan §5.2) in
// a namespace sandbox: every endpoint answers, a save swaps the code (a tile
// with no record follows its saves), and d.restart brings the same code and
// data back.
func TestIsolatedProbeSmoke(t *testing.T) {
	d := startIsolatedDaemon(t, isoOpts{})
	const tile = "apps/probe"
	api := "/api/" + tile
	writeProbe(t, d.WS, tile, "m1")
	waitProbe(t, d, tile, "m1")
	must200 := func(method, path, body string) string {
		t.Helper()
		c, b := d.do(t, method, path, body)
		if c != 200 {
			t.Fatalf("%s %s: %d %s", method, path, c, b)
		}
		return b
	}
	decode := func(body string, v any) {
		t.Helper()
		if err := json.Unmarshal([]byte(body), v); err != nil {
			t.Fatalf("%v: %s", err, body)
		}
	}

	var sbs struct{ Sandboxes []struct{ Kind, Mode string } }
	if decode(must200("GET", "/api/xbin/sandboxes?tile="+tile, ""), &sbs); len(sbs.Sandboxes) != 1 ||
		sbs.Sandboxes[0].Kind != "backend" || sbs.Sandboxes[0].Mode != "namespace" {
		t.Fatalf("the probe doesn't run in a namespace sandbox: %+v", sbs)
	}
	if b := must200("GET", api+"/file", ""); b != "m1" {
		t.Fatalf("/file: %q, want m1", b)
	}
	var env map[string]string
	decode(must200("GET", api+"/env", ""), &env)
	for _, name := range []string{"kv", "bus", "cron"} {
		if k := "XBIN_RES_" + strings.ToUpper(name); env[k] != "res:"+tile+"/"+name {
			t.Errorf("/env %s = %q, want res:%s/%s", k, env[k], tile, name)
		}
	}
	if v, ok := env["XBIN_DEPLOYMENT"]; ok {
		t.Errorf("/env: XBIN_DEPLOYMENT=%q on a tile with no record", v)
	}
	must200("PUT", api+"/kv/greeting", "hello")
	if b := must200("GET", api+"/kv/greeting", ""); b != "hello" {
		t.Fatalf("/kv/greeting: %q", b)
	}
	if c, b := d.do(t, "GET", api+"/kv/absent", ""); c != 404 {
		t.Fatalf("/kv/absent: %d %s, want 404", c, b)
	}
	var caller map[string]any
	if decode(must200("GET", api+"/caller", ""), &caller); caller["from"] != "owner" || caller["deployment"] != "" {
		t.Fatalf("/caller: %v", caller)
	}
	var self struct { // its +main answer isn't asserted: the qualifier comes with M2
		Self     int
		SelfBody string
	}
	if decode(must200("GET", api+"/self", ""), &self); self.Self != 200 || self.SelfBody != "m1" {
		t.Fatalf("/self: %+v", self)
	}

	// A cron tick and a bus event, each recorded with who delivered it.
	must200("PUT", "/api/xbin/cron/jobs", `{"name":"probe-tick","resource":"res:`+tile+`/cron",`+
		`"schedule":"@every 1s","path":"/tick","role":"writer","component":"`+tile+`"}`)
	must200("PUT", "/api/xbin/bus/subscriptions", `{"name":"probe-on","resource":"res:`+tile+`/bus",`+
		`"prefix":"ev/","path":"/on","component":"`+tile+`"}`)
	must200("POST", "/api/xbin/bus/publish", `{"resource":"res:`+tile+`/bus","topic":"ev/1","data":{"n":1}}`)
	type delivery struct{ Path, From, Subscription, Topic, Data string }
	var seen []delivery
	has := func(want delivery) bool {
		for _, s := range seen {
			if s == want {
				return true
			}
		}
		return false
	}
	tick := delivery{Path: "/tick", From: "xbin/cron"}
	event := delivery{Path: "/on", From: "xbin/bus", Subscription: "probe-on", Topic: "ev/1", Data: `{"n":1}`}
	if !waitFor(func() bool {
		_, b := d.do(t, "GET", api+"/seen", "")
		return json.Unmarshal([]byte(b), &seen) == nil && has(tick) && has(event)
	}, 30*time.Second) {
		t.Fatalf("/seen never showed a cron tick and the bus event: %+v", seen)
	}
	must200("DELETE", "/api/xbin/cron/jobs/probe-tick?component="+tile, "")

	// A save: the new code and the new tree.
	writeProbe(t, d.WS, tile, "m2")
	waitProbe(t, d, tile, "m2")
	if b := must200("GET", api+"/file", ""); b != "m2" {
		t.Fatalf("/file after the save: %q, want m2", b)
	}

	d.restart(t)
	waitProbe(t, d, tile, "m2")
	if b := must200("GET", api+"/kv/greeting", ""); b != "hello" {
		t.Fatalf("/kv/greeting after the restart: %q", b)
	}
}

// covers NP-15-2 — the isolated daemon's auth-on flavour and its ingress
// listener: the root token is read, requests need it, it still works after
// d.restart, and the ingress listener answers (no host is published: 404).
func TestIsolatedDaemonAuthIngress(t *testing.T) {
	d := startIsolatedDaemon(t, isoOpts{Auth: true, Ingress: true})
	if d.Token == "" || d.Ingress == "" {
		t.Fatalf("token %q, ingress %q", d.Token, d.Ingress)
	}
	check := func() {
		t.Helper()
		if c, b := d.do(t, "GET", "/api/xbin/users", ""); c != 200 {
			t.Fatalf("with the root token: %d %s", c, b)
		}
		if c, b := isoHTTP(d.URL, "", "GET", "/api/xbin/users", ""); c != 401 {
			t.Fatalf("without a token: %d %s, want 401", c, b)
		}
		if c, b := isoHTTP("http://"+d.Ingress, "", "GET", "/", ""); c != 404 {
			t.Fatalf("the ingress listener: %d %s, want 404", c, b)
		}
	}
	check()
	d.restart(t)
	check()
}
